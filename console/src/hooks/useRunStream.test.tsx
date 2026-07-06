/*
 * useRunStream.test.tsx — behavioral coverage for the run stream lifecycle:
 *   - terminal-close: the SSE StreamHandle is closed once a terminal run.status
 *     event arrives (no infinite reconnect/replay loop).
 *   - after_seq cursor: the live stream opens from the backlog's last seq, not 0
 *     (no full re-replay on every open).
 *   - fatal error: phase becomes 'error' and reconnect() re-subscribes.
 *
 * These assert against a fake ApiClient that records streamRun() args and lets
 * the test drive onOpen/onFrame/onError by hand.
 */
import { describe, expect, it } from 'vitest';
import type { ReactNode } from 'react';
import { renderHook, act, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient, StreamCallbacks, StreamHandle } from '../api/client';
import { ApiError } from '../api/client';
import type { RunEvent, RunStatus } from '../api/types';
import { useRunStream } from './useRunStream';

function statusEvent(seq: number, status: RunStatus): RunEvent {
  return { seq, ts: '', type: 'run.status', payload: { status } };
}
function textEvent(seq: number, text: string): RunEvent {
  return { seq, ts: '', type: 'agent.text', payload: { text } };
}

interface StreamCall {
  afterSeq: number;
  cb: StreamCallbacks;
  handle: StreamHandle & { closed: boolean };
}

/**
 * A fake client that returns `backlog` from listEvents and records every
 * streamRun subscription so the test can emit frames / errors and assert on
 * close() and the after_seq cursor.
 */
function makeFakeClient(backlog: RunEvent[]) {
  const streamCalls: StreamCall[] = [];
  const client: Partial<ApiClient> = {
    listEvents: async () => backlog,
    streamRun: (_runId: string, afterSeq: number, cb: StreamCallbacks) => {
      const handle = {
        closed: false,
        close() {
          this.closed = true;
        },
      };
      streamCalls.push({ afterSeq, cb, handle });
      return handle;
    },
  };
  return { client: client as ApiClient, streamCalls };
}

function wrapper(client: ApiClient) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <ApiProvider client={client}>{children}</ApiProvider>
      </QueryClientProvider>
    );
  };
}

describe('useRunStream — cursor + lifecycle', () => {
  it('opens the live stream from the backlog last seq (after_seq), not 0', async () => {
    // Backlog already has events up to seq 4 — the stream must resume from 4.
    const backlog = [
      statusEvent(1, 'queued'),
      textEvent(2, 'a'),
      textEvent(3, 'b'),
      statusEvent(4, 'running'),
    ];
    const { client, streamCalls } = makeFakeClient(backlog);
    renderHook(() => useRunStream('run1'), { wrapper: wrapper(client) });

    await waitFor(() => expect(streamCalls.length).toBe(1));
    // The bug was after_seq always 0; the fix resumes from the backlog tail.
    expect(streamCalls[0]!.afterSeq).toBe(4);
  });

  it('closes the SSE handle once a terminal status is observed', async () => {
    const backlog = [statusEvent(1, 'queued'), statusEvent(2, 'running')];
    const { client, streamCalls } = makeFakeClient(backlog);
    const { result } = renderHook(() => useRunStream('run1'), {
      wrapper: wrapper(client),
    });

    await waitFor(() => expect(streamCalls.length).toBe(1));
    const call = streamCalls[0]!;
    expect(call.handle.closed).toBe(false);

    // A terminal run.status frame arrives live.
    act(() => {
      call.cb.onOpen?.();
      call.cb.onFrame({ event: 'run.status', data: statusEvent(3, 'succeeded') });
    });

    // The stream must be closed so EventSource stops auto-reconnecting/replaying.
    await waitFor(() => expect(call.handle.closed).toBe(true));
    expect(result.current.terminal).toBe(true);
    expect(result.current.phase).toBe('closed');
  });

  it('surfaces phase "error" on a fatal stream error and reconnect() re-subscribes', async () => {
    const backlog = [statusEvent(1, 'queued'), statusEvent(2, 'running')];
    const { client, streamCalls } = makeFakeClient(backlog);
    const { result } = renderHook(() => useRunStream('run1'), {
      wrapper: wrapper(client),
    });

    await waitFor(() => expect(streamCalls.length).toBe(1));

    // Fatal SSE error (401/404/hostile proxy): EventSource permanently closed.
    act(() => {
      streamCalls[0]!.cb.onError?.(new ApiError(401, 'unauthorized'));
    });
    await waitFor(() => expect(result.current.phase).toBe('error'));
    // Not terminal (run was still running) → the page can offer a Reconnect.
    expect(result.current.terminal).toBe(false);

    // reconnect() opens a fresh subscription (from the current cursor).
    act(() => result.current.reconnect());
    await waitFor(() => expect(streamCalls.length).toBe(2));
  });
});
