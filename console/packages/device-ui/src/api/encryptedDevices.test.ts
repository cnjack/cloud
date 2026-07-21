/*
 * encryptedDevices.test.ts — the transparent E2EE layer over DeviceApi:
 * sessions meta / events payloads / SSE frames are opened when an envelope
 * arrives and the CEK is held; gray plaintext (no `enc` marker) passes
 * through untouched; outgoing bodies switch to the envelope form only with a
 * CEK in hand.
 */
import { describe, expect, it } from 'vitest';
import { decryptJson, encryptJson, importCek, isEnvelope } from '../devicecrypto/envelope';
import { createDeviceCrypto } from '../devicecrypto/provider';
import { createMemoryCekStore } from '../devicecrypto/storage';
import { withDeviceCrypto } from './encryptedDevices';
import type {
  DeviceApi,
  DeviceSession,
  DeviceSessionEvent,
  DeviceStreamCallbacks,
  DeviceStreamFrame,
} from './devices';

const DEVICE = 'dev-1';
const CEK_RAW = crypto.getRandomValues(new Uint8Array(32));

async function sealedMeta(meta: unknown, keyGen = 1) {
  return encryptJson(await importCek(CEK_RAW), keyGen, meta);
}

interface FakeInner {
  api: DeviceApi;
  sentEnvelopeBodies: unknown[];
  approvalEnvelopeBodies: unknown[];
  frames: DeviceStreamFrame[];
}

function fakeInner(overrides: Partial<DeviceApi> = {}): FakeInner {
  const sentEnvelopeBodies: unknown[] = [];
  const approvalEnvelopeBodies: unknown[] = [];
  const frames: DeviceStreamFrame[] = [];
  const api: DeviceApi = {
    listDevices: async () => [],
    listSessions: async () => [],
    listSessionEvents: async () => [],
    sendMessage: async () => ({ command_id: 'c1', session_id: 's1' }),
    sendEnvelope: async (_d, _s, envelope) => {
      sentEnvelopeBodies.push(envelope);
      return { command_id: 'c2', session_id: 's1' };
    },
    stopSession: async () => {},
    respondApproval: async () => {},
    respondApprovalEnvelope: async (_d, _s, envelope) => {
      approvalEnvelopeBodies.push(envelope);
    },
    createPairing: async () => ({ pairing_id: 'p1', status: 'pending' }),
    getPairing: async () => ({ status: 'pending' }),
    streamDevice: (_d, cb: DeviceStreamCallbacks) => {
      for (const f of frames) cb.onFrame(f);
      return { close: () => {} };
    },
    ...overrides,
  };
  return { api, sentEnvelopeBodies, approvalEnvelopeBodies, frames };
}

function cryptoWithCek() {
  const store = createMemoryCekStore();
  return { store, crypto: createDeviceCrypto(store) };
}

describe('withDeviceCrypto reads', () => {
  it('passes gray plaintext meta through untouched', async () => {
    const inner = fakeInner({
      listSessions: async () => [
        { session_id: 's1', status: 'idle', meta: { title: 'plain' }, updated_at: 't' },
      ],
    });
    const { crypto } = cryptoWithCek();
    const api = withDeviceCrypto(inner.api, crypto);
    const sessions = await api.listSessions(DEVICE);
    expect(sessions[0]!.meta).toEqual({ title: 'plain' });
  });

  it('opens envelope meta when the CEK is held', async () => {
    const envelope = await sealedMeta({ title: 'secret session' });
    const inner = fakeInner({
      listSessions: async () => [
        { session_id: 's1', status: 'idle', meta: envelope as never, updated_at: 't' },
      ],
    });
    const { store, crypto } = cryptoWithCek();
    await store.put(DEVICE, { cek: CEK_RAW, keyGen: 1 });
    const api = withDeviceCrypto(inner.api, crypto);
    const sessions = await api.listSessions(DEVICE);
    expect(sessions[0]!.meta).toEqual({ title: 'secret session' });
  });

  it('passes ciphertext through when unpaired (the pairing card covers the UX)', async () => {
    const envelope = await sealedMeta({ title: 'secret' });
    const inner = fakeInner({
      listSessions: async () => [
        { session_id: 's1', status: 'idle', meta: envelope as never, updated_at: 't' },
      ],
    });
    const { crypto } = cryptoWithCek(); // no CEK stored
    const api = withDeviceCrypto(inner.api, crypto);
    const sessions = await api.listSessions(DEVICE);
    expect(isEnvelope(sessions[0]!.meta)).toBe(true);
  });

  it('opens envelope event payloads', async () => {
    const envelope = await sealedMeta({ type: 'user_message', data: { text: 'hi' } });
    const inner = fakeInner({
      listSessionEvents: async (): Promise<DeviceSessionEvent[]> => [
        { seq: 1, kind: 'user', payload: envelope as never, ts: 't' },
      ],
    });
    const { store, crypto } = cryptoWithCek();
    await store.put(DEVICE, { cek: CEK_RAW, keyGen: 1 });
    const api = withDeviceCrypto(inner.api, crypto);
    const events = await api.listSessionEvents(DEVICE, 's1');
    expect(events[0]!.payload).toEqual({ type: 'user_message', data: { text: 'hi' } });
  });

  it('decrypts SSE frame payloads but never touches device.status', async () => {
    const envelope = await sealedMeta({ delta: 'hel' });
    const inner = fakeInner();
    inner.frames.push(
      { event: 'device.status', data: { online: true } },
      { event: 'session.delta', data: { session_id: 's1', kind: 'agent_text', payload: envelope as never } },
    );
    const { store, crypto } = cryptoWithCek();
    await store.put(DEVICE, { cek: CEK_RAW, keyGen: 1 });
    const api = withDeviceCrypto(inner.api, crypto);

    const got: DeviceStreamFrame[] = [];
    const errors: unknown[] = [];
    api.streamDevice(DEVICE, { onFrame: (f) => got.push(f), onError: (e) => errors.push(e) });
    // The decrypt hop is async; flush microtasks.
    await new Promise((r) => setTimeout(r, 0));

    expect(errors).toEqual([]);
    expect(got[0]).toEqual({ event: 'device.status', data: { online: true } });
    expect(got[1]).toEqual({
      event: 'session.delta',
      data: { session_id: 's1', kind: 'agent_text', payload: { delta: 'hel' } },
    });
  });
});

describe('withDeviceCrypto writes', () => {
  it('sends the envelope form (with channel pinned) when the CEK is held', async () => {
    const inner = fakeInner();
    const { store, crypto } = cryptoWithCek();
    await store.put(DEVICE, { cek: CEK_RAW, keyGen: 2 });
    const api = withDeviceCrypto(inner.api, crypto);

    const res = await api.sendMessage(DEVICE, 's1', 'hello', 'plan');
    expect(res.command_id).toBe('c2');
    expect(inner.sentEnvelopeBodies).toHaveLength(1);
    const env = inner.sentEnvelopeBodies[0];
    expect(isEnvelope(env)).toBe(true);
    const key = await importCek(CEK_RAW);
    expect(await decryptJson(key, env as never)).toEqual({
      text: 'hello',
      channel: 'console',
      mode: 'plan',
    });
    expect((env as { key_gen: number }).key_gen).toBe(2);
  });

  it('falls back to plaintext when unpaired', async () => {
    let plaintextBody: unknown = null;
    const inner = fakeInner({
      sendMessage: async (_d, _s, text, mode) => {
        plaintextBody = { text, mode };
        return { command_id: 'c1', session_id: 's1' };
      },
    });
    const { crypto } = cryptoWithCek();
    const api = withDeviceCrypto(inner.api, crypto);
    const res = await api.sendMessage(DEVICE, 's1', 'hello');
    expect(res.command_id).toBe('c1');
    expect(plaintextBody).toEqual({ text: 'hello', mode: undefined });
    expect(inner.sentEnvelopeBodies).toHaveLength(0);
  });

  it('merges the M12 compose extras into the envelope plaintext', async () => {
    const inner = fakeInner();
    const { store, crypto } = cryptoWithCek();
    await store.put(DEVICE, { cek: CEK_RAW, keyGen: 1 });
    const api = withDeviceCrypto(inner.api, crypto);

    await api.sendMessage(DEVICE, 's1', 'hello', undefined, {
      project_path: '/repo/a',
      model: { provider: 'anthropic', id: 'claude-opus-4-1' },
      effort: 'high',
      goal: 'ship M12',
      attachments: [{ name: 'spec.txt', mime: 'text/plain', data_b64: 'aGk=' }],
    });
    expect(inner.sentEnvelopeBodies).toHaveLength(1);
    const key = await importCek(CEK_RAW);
    expect(await decryptJson(key, inner.sentEnvelopeBodies[0] as never)).toEqual({
      text: 'hello',
      channel: 'console',
      project_path: '/repo/a',
      model: { provider: 'anthropic', id: 'claude-opus-4-1' },
      effort: 'high',
      goal: 'ship M12',
      attachments: [{ name: 'spec.txt', mime: 'text/plain', data_b64: 'aGk=' }],
    });
  });

  it('drops the compose extras on the unpaired plaintext fallback', async () => {
    let sawExtras = false;
    const inner = fakeInner({
      sendMessage: async (_d, _s, _text, _mode, extras) => {
        sawExtras = extras !== undefined;
        return { command_id: 'c1', session_id: 's1' };
      },
    });
    const { crypto } = cryptoWithCek();
    const api = withDeviceCrypto(inner.api, crypto);
    await api.sendMessage(DEVICE, 's1', 'hello', undefined, { effort: 'high' });
    expect(sawExtras).toBe(false);
    expect(inner.sentEnvelopeBodies).toHaveLength(0);
  });

  it('seals approval responses when the CEK is held', async () => {
    const inner = fakeInner();
    const { store, crypto } = cryptoWithCek();
    await store.put(DEVICE, { cek: CEK_RAW, keyGen: 1 });
    const api = withDeviceCrypto(inner.api, crypto);

    await api.respondApproval(DEVICE, 's1', 'a1', 'approve');
    expect(inner.approvalEnvelopeBodies).toHaveLength(1);
    const key = await importCek(CEK_RAW);
    expect(await decryptJson(key, inner.approvalEnvelopeBodies[0] as never)).toEqual({
      approval_id: 'a1',
      decision: 'approve',
    });
  });

  it('picks up a CEK that arrives mid-session (cache invalidation)', async () => {
    const { store, crypto } = cryptoWithCek();
    expect(await crypto.getKey(DEVICE)).toBeNull();
    await store.put(DEVICE, { cek: CEK_RAW, keyGen: 1 });
    expect(await crypto.getKey(DEVICE)).not.toBeNull();
    expect(await crypto.getKeyGen(DEVICE)).toBe(1);
    await store.delete(DEVICE);
    expect(await crypto.getKey(DEVICE)).toBeNull();
  });
});

// Type-level guard: sessions from the fake stay structurally compatible.
const _session: DeviceSession = { session_id: 's', status: 'idle', meta: null, updated_at: '' };
void _session;
