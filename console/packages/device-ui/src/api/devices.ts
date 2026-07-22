/*
 * devices.ts — the jcode device relay API (M4 device view).
 *
 * Deliberately a SEPARATE module from client.ts's ApiClient: the device relay
 * surface is session-cookie authenticated like the rest of the console, but it
 * is not part of the project/run domain the mock client demos. Keeping it
 * standalone avoids forcing mockClient.ts to fake devices. Tests inject a fake
 * DeviceApi; the app builds the real one from the auth token getter.
 */
import { ApiError, type TokenSource } from './errors';
import type { DeviceEnvelope } from '../devicecrypto/envelope';
import type { DeviceWrap } from '../devicecrypto/pairing';

/** One connected jcode device (GET /devices). */
export interface Device {
  id: string;
  name: string;
  hostname?: string;
  jcode_version?: string;
  /** Device kind reported at registration: "desktop" | "cli" (anything else: hide). */
  platform?: string;
  /** The device's E2EE identity public key (base64), set once registered. */
  pubkey?: string;
  /** The device's current CEK generation. */
  key_gen?: number;
  /**
   * E2EE enforcement flag reported at registration (M13). When true the
   * orchestrator rejects plaintext control with 409 pairing_required, so
   * clients gate the session surfaces behind pairing (DevicePairingGate).
   * Absent/false: gray rollout — no gate.
   */
  e2ee?: boolean;
  /**
   * The connector-reported compose capabilities (M12). Absent/null on devices
   * running a connector that predates the field — clients hide the compose
   * panel entirely then.
   */
  capabilities?: DeviceCapabilities | null;
  online: boolean;
  last_seen_at?: string;
  created_at?: string;
}

/** The device-side compose surface mirrored by the connector (M12). */
export interface DeviceCapabilities {
  projects?: DeviceCapabilityProject[];
  models?: DeviceCapabilityModel[];
  /** Model selected in desktop settings when capabilities were mirrored. */
  current_model?: DeviceCapabilityModel | null;
  efforts?: string[];
  /** Slash commands advertised by the device (M14; absent on pre-M14 connectors). */
  slash_commands?: DeviceCapabilitySlashCommand[];
}

export interface DeviceCapabilityProject {
  path: string;
  name: string;
}

export interface DeviceCapabilityModel {
  provider: string;
  id: string;
  label: string;
}

/** One slash command the device relayed from its local jcode (M14). */
export interface DeviceCapabilitySlashCommand {
  slash: string;
  description: string;
  /** The connector only reports 'skill' | 'flow' today (jcode /api/slash-commands). */
  type: string;
}

/** One compose attachment: a non-image file read into base64 (M12). */
export interface ComposeAttachment {
  name: string;
  mime: string;
  data_b64: string;
}

/** A base64 image for the vision path (M14; mirrors jcode's chatImage). */
export interface ComposeImage {
  data: string;
  media_type: string;
  name?: string;
}

/**
 * The optional chat.send extension fields the compose panel produces (M12,
 * extended in M14 with goal_armed + images). They ride the envelope plaintext
 * (under the E2EE layer when paired) or the plaintext body; the orchestrator
 * passes them through untouched.
 */
export interface SendMessageExtras {
  project_path?: string;
  model?: { provider: string; id: string };
  effort?: string;
  goal?: string;
  attachments?: ComposeAttachment[];
  /**
   * M14: when true the payload text IS the goal objective — the connector
   * POSTs /api/goal {objective, start:true} and ignores every other compose
   * field (goal takes priority over mode/model/images/session options).
   */
  goal_armed?: boolean;
  /** M14: vision images attached to the message ({data, media_type, name}). */
  images?: ComposeImage[];
}

/** jcode SessionMeta as relayed by the device (passthrough JSON). */
export interface DeviceSessionMeta {
  title?: string;
  /** Project working directory (jcode SessionMeta.project). */
  project?: string;
  model?: string;
  provider?: string;
  /** Originating channel ("console" | "mobile" | …) when jcode relays it in meta. */
  source?: string;
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

export interface BrowseFolder {
  name: string;
  path: string;
}

export interface BrowseFoldersResult {
  current: string;
  folders: BrowseFolder[];
}

export interface DeviceCommandState {
  status: 'pending' | 'delivered' | 'acked' | 'failed';
  result?: unknown;
}

/** Pairing creation response (POST /devices/{id}/pairings). */
export interface CreatePairingResult {
  pairing_id: string;
  status: string;
}

/** Pairing state (GET /devices/{id}/pairings/{pid}); wrap rides along once approved. */
export interface PairingState {
  status: 'pending' | 'approved' | 'denied' | 'expired' | 'revoked';
  key_gen: number;
  wrap?: DeviceWrap;
}

export interface DevicePairingRecord {
  id: string;
  label: string;
  pubkey: string;
  status: PairingState['status'];
  created_at: string;
  resolved_at?: string;
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
  sendMessage(deviceId: string, sessionId: string, text: string, mode?: string, extras?: SendMessageExtras): Promise<SendMessageResult>;
  /** POST an E2EE envelope body (docs/17 §6.2) instead of plaintext text. */
  sendEnvelope(deviceId: string, sessionId: string, envelope: DeviceEnvelope): Promise<SendMessageResult>;
  /** Stop a session; E2EE clients pass the encrypted empty command payload. */
  stopSession(deviceId: string, sessionId: string, envelope?: DeviceEnvelope): Promise<void>;
  /** List folders on the desktop device, starting at its home when path is omitted. */
  browseFolders(deviceId: string, path?: string): Promise<BrowseFoldersResult>;
  /** E2EE command/result form used by withDeviceCrypto. */
  browseFoldersEnvelope(deviceId: string, envelope: DeviceEnvelope): Promise<DeviceCommandState>;
  /** decision: approve | approve_all | deny (jcode approval vocabulary). */
  respondApproval(deviceId: string, sessionId: string, approvalId: string, decision: string): Promise<void>;
  /** POST an E2EE envelope body to the approval endpoint. */
  respondApprovalEnvelope(deviceId: string, sessionId: string, envelope: DeviceEnvelope): Promise<void>;
  /** Start a CEK pairing (docs/17 §6.3): the device is asked to approve. */
  createPairing(deviceId: string, req: { label: string; kty: string; pubkey: string }): Promise<CreatePairingResult>;
  /** Poll a pairing's state; wrap arrives once approved. */
  getPairing(deviceId: string, pairingId: string): Promise<PairingState>;
  /** Approved clients can review pairing requests from other clients. */
  listPairings(deviceId: string, approverId: string, status?: PairingState['status']): Promise<DevicePairingRecord[]>;
  /** Resolve another client's request using this approved client's identity. */
  respondPairing(deviceId: string, pairingId: string, req: { approver_id: string; approve: boolean; key_gen?: number; wrap?: DeviceWrap }): Promise<void>;
  /** Soft-delete the device (M16): revokes it + its tokens; history is retained server-side. */
  deleteDevice(deviceId: string): Promise<void>;
  /** Subscribe to the device-wide SSE stream. */
  streamDevice(deviceId: string, cb: DeviceStreamCallbacks): DeviceStreamHandle;
}

const BASE = '/api/v1';

export interface DeviceApiOptions {
  /**
   * URL prefix the device endpoints hang off. Defaults to the same-origin
   * `/api/v1` (console); cross-origin hosts (the mobile app's Tauri webview)
   * pass an absolute `https://host/api/v1`.
   */
  baseUrl?: string;
  /**
   * fetch/EventSource credential mode. `same-origin` (default) carries the
   * console's session cookie; Bearer-token hosts pass `omit`.
   */
  credentials?: RequestCredentials;
}

export function createDeviceApi(token: TokenSource, options: DeviceApiOptions = {}): DeviceApi {
  const getToken = typeof token === 'function' ? token : () => token;
  const base = options.baseUrl ?? BASE;
  const credentials = options.credentials ?? 'same-origin';

  async function req<T>(path: string, init?: RequestInit): Promise<T> {
    const tok = getToken();
    const res = await fetch(`${base}${path}`, {
      ...init,
      credentials,
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

  const commandState = (deviceId: string, commandId: string) =>
    req<DeviceCommandState>(`${dev(deviceId)}/commands/${encodeURIComponent(commandId)}`);

  async function waitForCommand(deviceId: string, commandId: string): Promise<DeviceCommandState> {
    const deadline = Date.now() + 15_000;
    for (;;) {
      const state = await commandState(deviceId, commandId);
      if (state.status === 'acked' || state.status === 'failed') return state;
      if (Date.now() >= deadline) throw new Error('Timed out waiting for the desktop device');
      await new Promise((resolve) => setTimeout(resolve, 150));
    }
  }

  async function startBrowse(deviceId: string, body: unknown): Promise<DeviceCommandState> {
    const accepted = await req<SendMessageResult>(`${dev(deviceId)}/workspace/browse`, {
      method: 'POST',
      body: JSON.stringify(body),
    });
    return waitForCommand(deviceId, accepted.command_id);
  }

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

    sendMessage: (deviceId, sessionId, text, mode, _extras) =>
      req<SendMessageResult>(
        `${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/messages`,
        {
          method: 'POST',
          // The M12 compose extras (project/model/effort/goal/attachments)
          // ride the E2EE envelope plaintext only — the orchestrator passes
          // them through under the encryption layer unchanged, while the
          // plaintext gray-rollout body is strictly validated (unknown fields
          // → 400), so extras are DELIBERATELY not sent here. Callers holding
          // no CEK simply lose the compose options (an unpaired device is
          // almost always an old connector without capabilities anyway).
          body: JSON.stringify(mode ? { text, mode } : { text }),
        },
      ),

    sendEnvelope: (deviceId, sessionId, envelope) =>
      req<SendMessageResult>(
        `${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/messages`,
        { method: 'POST', body: JSON.stringify({ envelope }) },
      ),

    stopSession: async (deviceId, sessionId, envelope) => {
      await req<void>(`${dev(deviceId)}/sessions/${encodeURIComponent(sessionId)}/stop`, {
        method: 'POST',
        ...(envelope ? { body: JSON.stringify({ envelope }) } : {}),
      });
    },

    browseFolders: async (deviceId, path) => {
      const state = await startBrowse(deviceId, { path: path ?? '' });
      if (state.status === 'failed') {
        const result = state.result as { error?: string } | undefined;
        throw new Error(result?.error ?? 'Could not browse folders on the desktop device');
      }
      return state.result as BrowseFoldersResult;
    },

    browseFoldersEnvelope: (deviceId, envelope) => startBrowse(deviceId, { envelope }),

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

    listPairings: async (deviceId, approverId, status = 'pending') => {
      const params = new URLSearchParams({ approver_id: approverId, status });
      return (await req<{ pairings: DevicePairingRecord[] }>(`${dev(deviceId)}/pairings?${params}`)).pairings ?? [];
    },

    respondPairing: async (deviceId, pairingId, body) => {
      await req<void>(`${dev(deviceId)}/pairings/${encodeURIComponent(pairingId)}/respond`, {
        method: 'POST',
        body: JSON.stringify(body),
      });
    },

    deleteDevice: async (deviceId) => {
      await req<void>(dev(deviceId), { method: 'DELETE' });
    },

    streamDevice: (deviceId, cb) => {
      // Native EventSource cannot set Authorization headers; the stream route
      // accepts ?access_token= (same pattern as streamRun in client.ts).
      const params = new URLSearchParams();
      const tok = getToken();
      if (tok) params.set('access_token', tok);
      const qs = params.toString();
      const es = new EventSource(`${base}${dev(deviceId)}/stream${qs ? `?${qs}` : ''}`);

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
