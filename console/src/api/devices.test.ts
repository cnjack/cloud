/*
 * devices.test.ts — device relay API request shaping: paths, envelopes,
 * message/approval bodies, and the typed 409 device_offline error.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createDeviceApi } from './devices';
import { apiErrorCode } from './client';

interface FetchCall {
  url: string;
  init: RequestInit | undefined;
}

function mockFetch(handler: (call: FetchCall) => { status?: number; body?: unknown }) {
  const calls: FetchCall[] = [];
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const { status = 200, body } = handler({ url, init });
    const payload = body === undefined ? '' : JSON.stringify(body);
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: `S${status}`,
      json: async () => JSON.parse(payload),
      text: async () => payload,
    } as unknown as Response;
  });
  vi.stubGlobal('fetch', fn);
  return { calls };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('deviceApi — request shaping', () => {
  it('lists devices from the envelope with the bearer token', async () => {
    const { calls } = mockFetch(() => ({
      body: { devices: [{ id: 'd1', name: 'mbp', online: true }] },
    }));
    const api = createDeviceApi('tok');
    const devices = await api.listDevices();
    expect(calls[0]!.url).toBe('/api/v1/devices');
    expect((calls[0]!.init!.headers as Record<string, string>).Authorization).toBe('Bearer tok');
    expect(devices[0]).toMatchObject({ id: 'd1', online: true });
  });

  it('passes after_seq/limit on the events replay', async () => {
    const { calls } = mockFetch(() => ({ body: { events: [] } }));
    const api = createDeviceApi(undefined);
    await api.listSessionEvents('d1', 's1', 42, 500);
    expect(calls[0]!.url).toBe('/api/v1/devices/d1/sessions/s1/events?after_seq=42&limit=500');
  });

  it('sends a message with text (+mode when given) to the session path', async () => {
    const { calls } = mockFetch(() => ({ status: 202, body: { command_id: 'c1', session_id: 's1' } }));
    const api = createDeviceApi(undefined);
    const res = await api.sendMessage('d1', 's1', 'hello', 'plan');
    expect(calls[0]!.url).toBe('/api/v1/devices/d1/sessions/s1/messages');
    expect(calls[0]!.init!.method).toBe('POST');
    expect(JSON.parse(calls[0]!.init!.body as string)).toEqual({ text: 'hello', mode: 'plan' });
    expect(res).toEqual({ command_id: 'c1', session_id: 's1' });
  });

  it('omits the mode field entirely when not given', async () => {
    const { calls } = mockFetch(() => ({ status: 202, body: { command_id: 'c1', session_id: null } }));
    const api = createDeviceApi(undefined);
    const res = await api.sendMessage('d1', 'new', 'hello');
    expect(calls[0]!.url).toBe('/api/v1/devices/d1/sessions/new/messages');
    expect(JSON.parse(calls[0]!.init!.body as string)).toEqual({ text: 'hello' });
    expect(res.session_id).toBeNull();
  });

  it('posts approval decisions as {approval_id, decision}', async () => {
    const { calls } = mockFetch(() => ({ status: 202, body: { command_id: 'c2', session_id: 's1' } }));
    const api = createDeviceApi(undefined);
    await api.respondApproval('d1', 's1', 'approval_1', 'approve_all');
    expect(calls[0]!.url).toBe('/api/v1/devices/d1/sessions/s1/approval');
    expect(JSON.parse(calls[0]!.init!.body as string)).toEqual({ approval_id: 'approval_1', decision: 'approve_all' });
  });

  it('posts stop with an empty body', async () => {
    const { calls } = mockFetch(() => ({ status: 202, body: { command_id: 'c3', session_id: 's1' } }));
    const api = createDeviceApi(undefined);
    await api.stopSession('d1', 's1');
    expect(calls[0]!.url).toBe('/api/v1/devices/d1/sessions/s1/stop');
    expect(calls[0]!.init!.method).toBe('POST');
  });

  it('409 device_offline surfaces through apiErrorCode', async () => {
    mockFetch(() => ({
      status: 409,
      body: { error: { code: 'device_offline', message: 'device is offline' } },
    }));
    const api = createDeviceApi(undefined);
    const err = await api.sendMessage('d1', 's1', 'hi').catch((e: unknown) => e);
    expect(apiErrorCode(err)).toBe('device_offline');
    expect((err as Error).message).toBe('device is offline');
  });
});
