/*
 * useDeviceSessionStream.test.tsx — the session stream lifecycle against a
 * fake DeviceApi: backlog replay, live frames routed by session_id, seq-gap
 * refill, device.status online edges, and reconnect after a fatal error.
 */
import { describe, expect, it } from 'vitest';
import type { ReactNode } from 'react';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { DeviceApiProvider } from '../api/DeviceApiProvider';
import type {
  DeviceApi,
  DeviceSessionEvent,
  DeviceStreamCallbacks,
  DeviceStreamHandle,
} from '../api/devices';
import { useDeviceSessionStream } from './useDeviceSessionStream';

function ev(seq: number, kind: string, data?: unknown): DeviceSessionEvent {
  return { seq, ts: '2026-07-20T10:00:00Z', kind, payload: { type: kind, data } };
}

interface StreamCall {
  cb: DeviceStreamCallbacks;
  handle: DeviceStreamHandle & { closed: boolean };
}

function makeFakeApi(opts: {
  backlog?: DeviceSessionEvent[];
  /** Extra events returned on gap refills (after_seq > 0). */
  refill?: DeviceSessionEvent[];
}) {
  const streamCalls: StreamCall[] = [];
  const eventCalls: number[] = [];
  const api: Partial<DeviceApi> = {
    listSessionEvents: async (_d, _s, afterSeq = 0) => {
      eventCalls.push(afterSeq);
      return afterSeq === 0 ? (opts.backlog ?? []) : (opts.refill ?? []);
    },
    streamDevice: (_deviceId, cb) => {
      const handle = {
        closed: false,
        close() {
          this.closed = true;
        },
      };
      streamCalls.push({ cb, handle });
      return handle;
    },
  };
  return { api: api as DeviceApi, streamCalls, eventCalls };
}

function wrapper(api: DeviceApi) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <DeviceApiProvider api={api}>{children}</DeviceApiProvider>
      </QueryClientProvider>
    );
  };
}

describe('useDeviceSessionStream', () => {
  it('replays the backlog, then follows live frames for the session', async () => {
    const { api, streamCalls } = makeFakeApi({ backlog: [ev(1, 'agent_start')] });
    const { result } = renderHook(() => useDeviceSessionStream('d1', 's1'), { wrapper: wrapper(api) });

    await waitFor(() => expect(streamCalls.length).toBe(1));
    expect(result.current.state.lastSeq).toBe(1);

    act(() => {
      streamCalls[0]!.cb.onOpen?.();
      streamCalls[0]!.cb.onFrame({
        event: 'session.delta',
        data: { session_id: 's1', kind: 'agent_text', payload: { type: 'agent_text', data: { text: 'Hi' } } },
      });
      streamCalls[0]!.cb.onFrame({
        event: 'session.event',
        data: { session_id: 's1', seq: 2, kind: 'agent_done', payload: { type: 'agent_done', data: {} } },
      });
    });
    expect(result.current.state.finalizedText).toEqual([{ id: -1, text: 'Hi' }]);
    expect(result.current.state.lastSeq).toBe(2);
    expect(result.current.phase).toBe('live');
  });

  it('ignores frames for other sessions on the device-wide stream', async () => {
    const { api, streamCalls } = makeFakeApi({});
    const { result } = renderHook(() => useDeviceSessionStream('d1', 's1'), { wrapper: wrapper(api) });
    await waitFor(() => expect(streamCalls.length).toBe(1));

    act(() => {
      streamCalls[0]!.cb.onFrame({
        event: 'session.event',
        data: { session_id: 'other', seq: 9, kind: 'agent_done', payload: {} },
      });
      streamCalls[0]!.cb.onFrame({
        event: 'session.delta',
        data: { session_id: 'other', kind: 'agent_text', payload: { data: { text: 'noise' } } },
      });
    });
    expect(result.current.state.events).toHaveLength(0);
    expect(result.current.state.streamingText).toBe('');
  });

  it('refetches after_seq=lastSeq when a live frame skips seqs (gap fill)', async () => {
    const { api, streamCalls, eventCalls } = makeFakeApi({
      backlog: [ev(1, 'agent_start')],
      refill: [ev(2, 'user_message', { content: 'missed', source: 'console' }), ev(3, 'agent_done', {})],
    });
    const { result } = renderHook(() => useDeviceSessionStream('d1', 's1'), { wrapper: wrapper(api) });
    await waitFor(() => expect(streamCalls.length).toBe(1));
    act(() => streamCalls[0]!.cb.onOpen?.());

    // A frame arrives with seq 3 while lastSeq is 1 → seq 2 was missed.
    act(() => {
      streamCalls[0]!.cb.onFrame({
        event: 'session.event',
        data: { session_id: 's1', seq: 3, kind: 'agent_done', payload: { type: 'agent_done', data: {} } },
      });
    });
    // Gap refill re-fetches from the cursor; dedupe keeps the list clean.
    await waitFor(() => expect(eventCalls).toContain(1));
    await waitFor(() =>
      expect(result.current.state.events.map((e) => e.seq)).toEqual([1, 2, 3]),
    );
  });

  it('tracks device.status online edges', async () => {
    const { api, streamCalls } = makeFakeApi({});
    const { result } = renderHook(() => useDeviceSessionStream('d1', 's1'), { wrapper: wrapper(api) });
    await waitFor(() => expect(streamCalls.length).toBe(1));

    act(() => {
      streamCalls[0]!.cb.onFrame({ event: 'device.status', data: { online: false } });
    });
    expect(result.current.online).toBe(false);
    act(() => {
      streamCalls[0]!.cb.onFrame({ event: 'device.status', data: { online: true } });
    });
    expect(result.current.online).toBe(true);
  });

  it('surfaces phase "error" and reconnect() re-subscribes + resets', async () => {
    const { api, streamCalls } = makeFakeApi({ backlog: [ev(1, 'agent_start')] });
    const { result } = renderHook(() => useDeviceSessionStream('d1', 's1'), { wrapper: wrapper(api) });
    await waitFor(() => expect(streamCalls.length).toBe(1));

    act(() => streamCalls[0]!.cb.onError?.(new Error('boom')));
    await waitFor(() => expect(result.current.phase).toBe('error'));

    act(() => result.current.reconnect());
    await waitFor(() => expect(streamCalls.length).toBe(2));
    expect(streamCalls[0]!.handle.closed).toBe(true);
    await waitFor(() => expect(result.current.state.lastSeq).toBe(1));
  });
});
