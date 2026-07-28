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
import { describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { renderHook, act, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient, StreamCallbacks, StreamHandle } from '../api/client';
import { ApiError } from '../api/client';
import type { Run, RunEvent, RunStatus } from '../api/types';
import { qk } from '../api/queries';
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
  it('never regresses a terminal run cache from stale backlog or full-run frames', async () => {
    const { client, streamCalls } = makeFakeClient([statusEvent(1, 'running')]);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const succeeded: Run = {
      id: 'run1',
      project_id: 'proj1',
      prompt: 'finished task',
      status: 'succeeded',
      created_at: '2026-07-28T08:00:00Z',
      finished_at: '2026-07-28T08:05:00Z',
    };
    qc.setQueryData(qk.run('run1'), succeeded);
    const Wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>
        <ApiProvider client={client}>{children}</ApiProvider>
      </QueryClientProvider>
    );

    renderHook(() => useRunStream('run1'), { wrapper: Wrapper });
    await waitFor(() => expect(streamCalls.length).toBe(1));
    await waitFor(() => expect(qc.getQueryData<Run>(qk.run('run1'))?.status).toBe('succeeded'));

    act(() => {
      streamCalls[0]!.cb.onFrame({
        event: 'run.status',
        data: {
          ...statusEvent(2, 'running'),
          run: { ...succeeded, status: 'running', finished_at: null },
        } as RunEvent & { run: Run },
      });
    });

    expect(qc.getQueryData<Run>(qk.run('run1'))).toEqual(succeeded);
  });

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

  it('loads large history in REST pages before opening one live stream', async () => {
    const firstPage = Array.from({ length: 1000 }, (_, index) =>
      textEvent(index + 1, `chunk-${index + 1}`),
    );
    const finalPage = [
      textEvent(1001, 'tail'),
      statusEvent(1002, 'running'),
    ];
    const listEvents = vi.fn(async (_runId: string, afterSeq = 0, limit?: number) => {
      expect(limit).toBe(1000);
      if (afterSeq === 0) return firstPage;
      if (afterSeq === 1000) return finalPage;
      return [];
    });
    const { client, streamCalls } = makeFakeClient([]);
    client.listEvents = listEvents;

    const { result } = renderHook(() => useRunStream('run1'), {
      wrapper: wrapper(client),
    });

    await waitFor(() => expect(streamCalls.length).toBe(1));
    expect(listEvents.mock.calls.map((call) => call[1])).toEqual([0, 1000]);
    expect(streamCalls[0]!.afterSeq).toBe(1002);
    expect(result.current.events).toHaveLength(1002);
  });

  it('does not reopen SSE after a fully restored terminal history', async () => {
    const backlog = [
      statusEvent(1, 'queued'),
      textEvent(2, 'done'),
      statusEvent(3, 'succeeded'),
    ];
    const { client, streamCalls } = makeFakeClient(backlog);
    const { result } = renderHook(() => useRunStream('run1'), {
      wrapper: wrapper(client),
    });

    await waitFor(() => expect(result.current.phase).toBe('closed'));
    expect(result.current.events).toHaveLength(3);
    expect(result.current.terminal).toBe(true);
    expect(streamCalls).toHaveLength(0);
  });

  it('keeps completed pages when a later history page fails', async () => {
    const firstPage = Array.from({ length: 1000 }, (_, index) =>
      textEvent(index + 1, `chunk-${index + 1}`),
    );
    const listEvents = vi.fn(async (_runId: string, afterSeq = 0) => {
      if (afterSeq === 0) return firstPage;
      throw new ApiError(503, 'history temporarily unavailable');
    });
    const { client, streamCalls } = makeFakeClient([]);
    client.listEvents = listEvents;

    const { result } = renderHook(() => useRunStream('run1'), {
      wrapper: wrapper(client),
    });

    await waitFor(() => expect(streamCalls.length).toBe(1));
    expect(result.current.events).toHaveLength(1000);
    expect(streamCalls[0]!.afterSeq).toBe(1000);
  });

  it('F11: keeps polling the run after terminal so a late pr_url lands without reload', async () => {
    vi.useFakeTimers();
    try {
      const backlog = [statusEvent(1, 'queued'), statusEvent(2, 'running')];
      const { client, streamCalls } = makeFakeClient(backlog);
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const invalidateSpy = vi.spyOn(qc, 'invalidateQueries');
      const Wrapper = ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={qc}>
          <ApiProvider client={client}>{children}</ApiProvider>
        </QueryClientProvider>
      );
      renderHook(() => useRunStream('run1'), { wrapper: Wrapper });
      await vi.waitFor(() => expect(streamCalls.length).toBe(1));

      // Terminal succeeded arrives; the draft PR (pr_url) is NOT here yet.
      act(() => {
        streamCalls[0]!.cb.onOpen?.();
        streamCalls[0]!.cb.onFrame({ event: 'run.status', data: statusEvent(3, 'succeeded') });
      });
      invalidateSpy.mockClear();

      // Advance through the bounded poll window (1s,2s,4s,8s). Each tick re-fetches
      // the authoritative run until pr_url appears — here it never does, so it
      // polls the full bounded set and stops (readonly runs are the same shape).
      await act(async () => {
        await vi.advanceTimersByTimeAsync(16000);
      });
      const runInvalidations = invalidateSpy.mock.calls.filter(
        (c) => JSON.stringify(c[0]).includes('run1'),
      ).length;
      expect(runInvalidations).toBeGreaterThanOrEqual(4);
    } finally {
      vi.useRealTimers();
    }
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

  // D22: awaiting_input is NON-terminal — the stream must stay open so the next
  // turn's events (user.message, agent.text, run.status) keep flowing without a
  // reconnect. Only succeeded/failed/canceled close the stream.
  it('keeps the SSE handle open on an awaiting_input status (D22 session)', async () => {
    const backlog = [statusEvent(1, 'queued'), statusEvent(2, 'running')];
    const { client, streamCalls } = makeFakeClient(backlog);
    const { result } = renderHook(() => useRunStream('run1'), {
      wrapper: wrapper(client),
    });

    await waitFor(() => expect(streamCalls.length).toBe(1));
    const call = streamCalls[0]!;

    act(() => {
      call.cb.onOpen?.();
      call.cb.onFrame({ event: 'run.status', data: statusEvent(3, 'awaiting_input') });
    });

    await waitFor(() => expect(result.current.derivedStatus).toBe('awaiting_input'));
    expect(result.current.terminal).toBe(false);
    expect(call.handle.closed).toBe(false); // stream stays live for the next turn
    expect(result.current.phase).toBe('live');

    // The session later finishes → NOW the stream closes.
    act(() => {
      call.cb.onFrame({ event: 'run.status', data: statusEvent(4, 'succeeded') });
    });
    await waitFor(() => expect(call.handle.closed).toBe(true));
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
