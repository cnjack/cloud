/*
 * encryptedDevices.ts — transparent E2EE over the device relay DeviceApi
 * (docs/17 §6, M5).
 *
 * The wrapper sits UNDER the react-query/hook layer, so the rendering layer
 * never sees ciphertext: session meta, durable-event payloads and SSE frame
 * payloads that arrive as envelopes (object + string `enc`) are opened with
 * the device's CEK before they reach the UI; anything without the marker is
 * gray-rollout plaintext and passes through untouched. Outgoing messages /
 * approval responses are sealed as envelopes when (and only when) this client
 * holds the device's CEK.
 *
 * Fail-visibly policy: a decrypt failure with a CEK present is a real error
 * (wrong key, tampered ciphertext) and propagates; ciphertext WITHOUT a local
 * CEK passes through as-is — the pairing card (useDevicePairing) is the
 * visible state for that, and the envelope shape simply renders as an
 * untitled/empty payload until pairing completes.
 */
import { decryptJson, encryptJson, isEnvelope } from '../devicecrypto/envelope';
import type { DeviceCrypto } from '../devicecrypto/provider';
import type { BrowseFoldersResult, Device, DeviceApi, DeviceSession, DeviceSessionEvent, DeviceStreamFrame } from './devices';

export function withDeviceCrypto(api: DeviceApi, crypto: DeviceCrypto): DeviceApi {
  /** Open an envelope when we hold the key; pass everything else through. */
  async function open<T>(deviceId: string, value: T): Promise<T> {
    if (!isEnvelope(value)) return value;
    const key = await crypto.getKey(deviceId);
    if (!key) return value;
    return (await decryptJson(key, value)) as T;
  }

  return {
    ...api,

    listDevices: async () => {
      const devices = await api.listDevices();
      // capabilities is reported as a sealed envelope on E2EE devices (M12) —
      // without opening it here the composer pickers (models/projects/slash)
      // silently render empty even when the device advertised them.
      return Promise.all(
        devices.map(async (d) => {
          if (!isEnvelope(d.capabilities)) return d;
          return { ...d, capabilities: await open<Device['capabilities']>(d.id, d.capabilities) };
        }),
      );
    },

    listSessions: async (deviceId) => {
      const sessions = await api.listSessions(deviceId);
      return Promise.all(
        sessions.map(async (s): Promise<DeviceSession> => {
          if (!isEnvelope(s.meta)) return s;
          return { ...s, meta: await open<DeviceSession['meta']>(deviceId, s.meta) };
        }),
      );
    },

    listSessionEvents: async (deviceId, sessionId, afterSeq, limit) => {
      const events = await api.listSessionEvents(deviceId, sessionId, afterSeq, limit);
      return Promise.all(
        events.map(async (e): Promise<DeviceSessionEvent> => {
          if (!isEnvelope(e.payload)) return e;
          return { ...e, payload: await open<DeviceSessionEvent['payload']>(deviceId, e.payload) };
        }),
      );
    },

    sendMessage: async (deviceId, sessionId, text, mode, extras) => {
      const key = await crypto.getKey(deviceId);
      // No CEK: the plaintext gray-rollout path cannot carry the M12 compose
      // extras (the orchestrator validates the plaintext body strictly), so
      // they are dropped here — see createDeviceApi.sendMessage.
      if (!key) return api.sendMessage(deviceId, sessionId, text, mode);
      // The plaintext payload the server would have built itself (channel is
      // pinned to console — the server no longer sees the body to pin it),
      // extended with the M12 compose fields (project/model/effort/goal/
      // attachments). The orchestrator stores the envelope verbatim; the
      // connector reads these under the encryption layer.
      const keyGen = (await crypto.getKeyGen(deviceId)) ?? 1;
      const payload: Record<string, unknown> = { text, channel: 'console', ...extras };
      if (mode) payload.mode = mode;
      const envelope = await encryptJson(key, keyGen, payload);
      return api.sendEnvelope(deviceId, sessionId, envelope);
    },

    respondApproval: async (deviceId, sessionId, approvalId, decision) => {
      const key = await crypto.getKey(deviceId);
      if (!key) return api.respondApproval(deviceId, sessionId, approvalId, decision);
      const keyGen = (await crypto.getKeyGen(deviceId)) ?? 1;
      const envelope = await encryptJson(key, keyGen, { approval_id: approvalId, decision });
      return api.respondApprovalEnvelope(deviceId, sessionId, envelope);
    },

    deleteSession: async (deviceId, sessionId) => {
      const key = await crypto.getKey(deviceId);
      if (!key) return api.deleteSession(deviceId, sessionId);
      const keyGen = (await crypto.getKeyGen(deviceId)) ?? 1;
      const envelope = await encryptJson(key, keyGen, {});
      return api.deleteSessionEnvelope(deviceId, sessionId, envelope);
    },

    browseFolders: async (deviceId, path) => {
      const key = await crypto.getKey(deviceId);
      if (!key) return api.browseFolders(deviceId, path);
      const keyGen = (await crypto.getKeyGen(deviceId)) ?? 1;
      const envelope = await encryptJson(key, keyGen, { path: path ?? '' });
      const state = await api.browseFoldersEnvelope(deviceId, envelope);
      const result = await open<unknown>(deviceId, state.result);
      if (state.status === 'failed') {
        const failure = result as { error?: string } | undefined;
        throw new Error(failure?.error ?? 'Could not browse folders on the desktop device');
      }
      return result as BrowseFoldersResult;
    },

    streamDevice: (deviceId, cb) =>
      api.streamDevice(deviceId, {
        ...cb,
        onFrame: (frame: DeviceStreamFrame) => {
          if (frame.event === 'device.status' || !isEnvelope(frame.data.payload)) {
            cb.onFrame(frame);
            return;
          }
          open(deviceId, frame.data.payload)
            .then((payload) => {
              if (payload === frame.data.payload) return; // no CEK — pass through
              cb.onFrame({ ...frame, data: { ...frame.data, payload } } as DeviceStreamFrame);
            })
            .catch((err) => cb.onError?.(err));
        },
      }),
  };
}
