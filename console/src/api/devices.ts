/*
 * devices.ts — the jcode device relay API (M4 device view).
 *
 * Deliberately a SEPARATE module from client.ts's ApiClient: the device relay
 * surface is session-cookie authenticated like the rest of the console, but it
 * is not part of the project/run domain the mock client demos. Keeping it
 * standalone avoids forcing mockClient.ts to fake devices. Tests inject a fake
 * DeviceApi; the app builds the real one from the auth token getter.
 */
import { ApiError, type TokenSource } from './client';
import type { DeviceEnvelope } from '../devicecrypto/envelope';
import type { DeviceWrap } from '../devicecrypto/pairing';

/** One connected jcode device (GET /devices). */
export interface Device {
  id: string;
  name: string;
  hostname?: string;
  jcode_version?: string;
  /** The device's E2EE identity public key (base64), set once registered. */
  pubkey?: string;
  /** The device's current CEK generation. */
  key_gen?: number;
  online: boolean;
  last_seen_at?: string;
  created_at?: string;
}

/** jcode SessionMeta as relayed by the device (passthrough JSON). */
export interface DeviceSessionMeta {
  title?: string;
  /** Project working directory (jcode SessionMeta.project). */
  project?: string;
  model?: string;
  provider?: string;
  [key: string]: unknown;
}

export interface DeviceSession {
  session_id: string;
  /** jcode session status: "idle" | "running". */
  status: string;
  meta: DeviceSessionMeta | null;
  updated_at: string;
}

/**
 * One durable session event. `payload` is the raw jcode WS message JSON
 * ({ type, data, task_id? }) — the mapping layer in deviceview/ narrows it.
 */
export interface DeviceSessionEvent {
  seq: number;
  kind: string;
  payload: { type?: string; data?: unknown; task_id?: string; [key: string]: unknown };
  /** Event timestamp (RFC3339). The wire field is `ts`, not `created_at`. */
  ts: string;
}

export interface SendMessageResult {
  command_id: string;
  /** null when the path sid was "new" (the device assigns it locally). */
  session_id: string | null;
}

/** Pairing creation response (POST /devices/{id}/pairings). */
export interface CreatePairingResult {
  pairing_id: string;
  status: string;
}

/** Pairing state (GET /devices/{id}/pairings/{pid}); wrap rides along once approved. */
export interface PairingState {
  status: 'pending' | 'approved' | 'denied' | 'expired';
  wrap?: DeviceWrap;
}

/** SSE frame from GET /devices/{id}/stream. */
export type DeviceStreamFrame =
  | { event: 'device.status'; data: { online: boolean } }
  | { event: 'session.event'; data: { session_id: string; seq: number; kind: string; payload: DeviceSessionEvent['payload'] } }
  | { event: 'session.delta'; data: { session_id: string; kind: string; payload: DeviceSessionEvent['payload'] } };

export interface DeviceStreamCallbacks {
  onFrame: (frame: DeviceStreamFrame) => void;
  onError?: (err: unknown) => void;
  onOpen?: () => void;
}

export interface DeviceStreamHandle {
  close: () => void;
}

export interface DeviceApi {
  listDevices(): Promise<Device[]>;
  listSessions(deviceId: string): Promise<DeviceSession[]>;
  /** Replay durable events with seq > afterSeq (0 = from start). */
  listSessionEvents(deviceId: string, sessionId: string, afterSeq?: number, limit?: number): Promise<DeviceSessionEvent[]>;
  /** POST messages; sid "new" starts a fresh session. 409 device_offline when offline. */
  sendMessage(deviceId: string, sessionId: string, text: string, mode?: string): Promise<SendMessageResult>;
  /** POST an E2EE envelope body (docs/17 §6.2) instead of plaintext text. */
  sendEnvelope(deviceId: string, sessionId: string, envelope: DeviceEnvelope): Promise<SendMessageResult>;
  stopSession(deviceId: string, sessionId: string): Promise<void>;
  /** decision: approve | approve_all | deny (jcode approval vocabulary). */
  respondApproval(deviceId: string, sessionId: string, approvalId: string, decision: string): Promise<void>;
  /** POST an E2EE envelope body to the approval endpoint. */
  respondApprovalEnvelope(deviceId: string, sessionId: string, envelope: DeviceEnvelope): Promise<void>;
  /** Start a CEK pairing (docs/17 §6.3): the device is asked to approve. */
  createPairing(deviceId: string, req: { label: string; kty: string; pubkey: string }): Promise<CreatePairingResult>;
  /** Poll a pairing's state; wrap arrives once approved. */
  getPairing(deviceId: string, pairingId: string): Promise<PairingState>;
  /** Subscribe to the device-wide SSE stream. */
  streamDevice(deviceId: string, cb: DeviceStreamCallbacks): DeviceStreamHandle;
}

const BASE = '/api/v1';

export function createDeviceApi(token: TokenSource): DeviceApi {
  const getToken = typeof token === 'function' ? token : () => token;

  async function req<T>(path: string, init?: RequestInit): Promise<T> {
    const tok = getToken();
    const res = await fetch(`${BASE}${path}`, {
      ...init,
      credentials: 'same-origin',
      headers: {
        Accept: 'application/json',
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...(tok ? { Authorization: `Bearer ${tok}` } : {}),
        ...init?.headers,
      },
    });
    if (!res.ok) {
      let body: unknown;
      let message = `${res.status} ${res.statusText}`;
      try {
        body = await res.json();
        const e = (body as { error?: { message?: string } | string })?.error;
        if (e && typeof e === 'object' && e.message) message = e.message;
        else if (typeof e === 'string') message = e;
      } catch {
        /* bodyless */
      }
      // Reuse the console's typed error so apiErrorCode() works on device calls.
      throw new ApiError(res.status, message, body);
    }
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }

  const dev = (id: string) => `/devices/${encodeURIComponent(id)}`;

  return {
    listDevices: async () => (await req<{ devices: Device[] }>('/devices')).devices ?? [],

    listSessions: async (deviceId) =>
      (await req<{ sessions: DeviceSession[] }>(`${dev(deviceId)}/sessions`)).sessions ?? [],

    listSessionEvents: async (deviceId, sessionId, afterSeq = 0, limit) => {
      const params = new URLSearchParams({ after_seq: String(afterSeq) });
      if (limit) params.set('limit', String(limit));
      return (
        await req<{ events: DeviceSessionEvent[] }>(
          `${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/events?${params}`,
        )
      ).events ?? [];
    },

    sendMessage: (deviceId, sessionId, text, mode) =>
      req<SendMessageResult>(
        `${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/messages`,
        { method: 'POST', body: JSON.stringify(mode ? { text, mode } : { text }) },
      ),

    sendEnvelope: (deviceId, sessionId, envelope) =>
      req<SendMessageResult>(
        `${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/messages`,
        { method: 'POST', body: JSON.stringify({ envelope }) },
      ),

    stopSession: async (deviceId, sessionId) => {
      await req<void>(`${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/stop`, { method: 'POST' });
    },

    respondApproval: async (deviceId, sessionId, approvalId, decision) => {
      await req<void>(`${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/approval`, {
        method: 'POST',
        body: JSON.stringify({ approval_id: approvalId, decision }),
      });
    },

    respondApprovalEnvelope: async (deviceId, sessionId, envelope) => {
      await req<void>(`${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/approval`, {
        method: 'POST',
        body: JSON.stringify({ envelope }),
      });
    },

    createPairing: (deviceId, body) =>
      req<CreatePairingResult>(`${dev(deviceId)}/pairings`, {
        method: 'POST',
        body: JSON.stringify(body),
      }),

    getPairing: (deviceId, pairingId) =>
      req<PairingState>(`${dev(deviceId)}/pairings/${encodeURIComponent(pairingId)}`),

    streamDevice: (deviceId, cb) => {
      // Native EventSource cannot set Authorization headers; the stream route
      // accepts ?access_token= (same pattern as streamRun in client.ts).
      const params = new URLSearchParams();
      const tok = getToken();
      if (tok) params.set('access_token', tok);
      const qs = params.toString();
      const es = new EventSource(`${BASE}${dev(deviceId)}/stream${qs ? `?${qs}` : ''}`);

      const handle = (e: MessageEvent) => {
        try {
          const data = JSON.parse(e.data);
          cb.onFrame({ event: e.type, data } as DeviceStreamFrame);
        } catch (err) {
          cb.onError?.(err);
        }
      };
      es.onopen = () => cb.onOpen?.();
      for (const t of ['device.status', 'session.event', 'session.delta']) {
        es.addEventListener(t, handle as EventListener);
      }
      es.onerror = (err) => cb.onError?.(err);
      return { close: () => es.close() };
    },
  };
}
