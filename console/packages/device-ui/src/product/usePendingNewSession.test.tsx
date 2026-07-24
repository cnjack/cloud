/*
 * usePendingNewSession — pending card + found matching (UX review fix).
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, renderHook, waitFor } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { DeviceApiProvider } from '../api/DeviceApiProvider';
import type { DeviceApi, DeviceSession } from '../api/devices';
import { PENDING_SESSION_TIMEOUT_MS, usePendingNewSession } from './usePendingNewSession';

const DEVICE = 'dev-1';

function sessionsApi(sessions: DeviceSession[], result: unknown = { session_id: 's-new' }): DeviceApi {
  return {
    listSessions: async () => sessions,
    getCommandState: async () => ({ status: 'acked', result }),
  } as unknown as DeviceApi;
}

function wrapper(api: DeviceApi) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>
      <DeviceApiProvider api={api}>{children}</DeviceApiProvider>
    </QueryClientProvider>
  );
}

const row = (id: string, updatedAt: string): DeviceSession =>
  ({ session_id: id, status: 'running', meta: { title: id }, updated_at: updatedAt }) as DeviceSession;

type PendingState = ReturnType<typeof usePendingNewSession>;

function KeyedPendingHarness({
  deviceId,
  report,
}: {
  deviceId: string;
  report: (state: PendingState) => void;
}) {
  const state = usePendingNewSession(deviceId);
  useEffect(() => report(state), [report, state]);
  return null;
}

describe('usePendingNewSession', () => {
  it('surfaces a pending row after markSent', async () => {
    const { result } = renderHook(() => usePendingNewSession(DEVICE), { wrapper: wrapper(sessionsApi([])) });
    expect(result.current.pending).toBeNull();
    act(() => result.current.markSent({ commandId: 'command-1', text: 'hello world', at: Date.now() }));
    expect(result.current.pending?.text).toBe('hello world');
  });

  it('navigates only to the exact ACK session id, never a newer-looking old row', async () => {
    const api = sessionsApi([
      row('s-old', '2026-03-01T10:00:00Z'),
      row('s-new', '2026-03-01T10:00:00Z'),
    ]);
    const { result } = renderHook(() => usePendingNewSession(DEVICE), { wrapper: wrapper(api) });
    act(() => result.current.markSent({ commandId: 'command-1', text: 'hi', at: Date.now() }));
    await waitFor(() => expect(result.current.found?.session_id).toBe('s-new'));
  });

  it('retries a transient command-state failure and still finds the exact ACK session', async () => {
    let reads = 0;
    const api = {
      listSessions: async () => [
        row('s-old', '2026-03-01T10:00:00Z'),
        row('s-new', '2026-03-01T10:00:00Z'),
      ],
      getCommandState: async () => {
        reads += 1;
        if (reads === 1) throw new Error('temporary network failure');
        return { status: 'acked' as const, result: { session_id: 's-new' } };
      },
    } as unknown as DeviceApi;
    const { result } = renderHook(() => usePendingNewSession(DEVICE), { wrapper: wrapper(api) });
    act(() => result.current.markSent({ commandId: 'command-1', text: 'hi', at: Date.now() }));

    await waitFor(() => expect(result.current.found?.session_id).toBe('s-new'), { timeout: 2_000 });
    expect(reads).toBe(2);
    expect(result.current.issue).toBeNull();
  });

  it('keeps a known placeholder pending until the matching metadata is mirrored', async () => {
    const api = sessionsApi([{ ...row('s-new', '2026-03-01T10:00:00Z'), meta: null }]);
    const { result } = renderHook(() => usePendingNewSession(DEVICE), { wrapper: wrapper(api) });
    act(() => result.current.markSent({ commandId: 'command-1', text: 'hi', at: Date.now() }));
    await waitFor(() => expect(result.current.pending?.commandId).toBe('command-1'));
    expect(result.current.found).toBeNull();
  });

  it('keeps the request pending and reports an ACK without a session id', async () => {
    const api = sessionsApi([], {});
    const { result } = renderHook(() => usePendingNewSession(DEVICE), { wrapper: wrapper(api) });
    act(() => result.current.markSent({ commandId: 'command-1', text: 'hi', at: Date.now() }));
    await waitFor(() => expect(result.current.issue).toBe('missing_session_id'));
    expect(result.current.pending?.commandId).toBe('command-1');
  });

  it('times out an ACKed session that never receives metadata', async () => {
    const api = sessionsApi([{ ...row('s-new', '2026-03-01T10:00:00Z'), meta: null }]);
    const { result } = renderHook(() => usePendingNewSession(DEVICE), { wrapper: wrapper(api) });
    act(() => result.current.markSent({
      commandId: 'command-1',
      text: 'hi',
      at: Date.now() - PENDING_SESSION_TIMEOUT_MS,
    }));
    await waitFor(() => expect(result.current.issue).toBe('timed_out'));
    expect(result.current.found).toBeNull();
  });

  it('times out a command-state request that never settles', async () => {
    const api = {
      listSessions: async () => [],
      getCommandState: async () => new Promise<never>(() => {}),
    } as unknown as DeviceApi;
    const { result } = renderHook(() => usePendingNewSession(DEVICE), { wrapper: wrapper(api) });
    act(() => result.current.markSent({
      commandId: 'command-1',
      text: 'hi',
      at: Date.now() - PENDING_SESSION_TIMEOUT_MS,
    }));

    await waitFor(() => expect(result.current.issue).toBe('timed_out'));
    expect(result.current.found).toBeNull();
  });

  it('isolates pending correlation when the welcome route switches devices', async () => {
    let latest: PendingState | null = null;
    let resolveA: ((state: { status: 'acked'; result: { session_id: string } }) => void) | null = null;
    const reads: { deviceId: string; commandId: string }[] = [];
    const api = {
      listSessions: async (deviceId: string) =>
        deviceId === 'device-b' ? [row('session-b', '2026-03-01T10:00:00Z')] : [],
      getCommandState: async (deviceId: string, commandId: string) => {
        reads.push({ deviceId, commandId });
        if (deviceId === 'device-a') {
          return new Promise<{ status: 'acked'; result: { session_id: string } }>((resolve) => {
            resolveA = resolve;
          });
        }
        return { status: 'acked' as const, result: { session_id: 'session-b' } };
      },
    } as unknown as DeviceApi;
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const report = (state: PendingState) => { latest = state; };
    const renderTree = (deviceId: string) => (
      <QueryClientProvider client={qc}>
        <DeviceApiProvider api={api}>
          <KeyedPendingHarness key={deviceId} deviceId={deviceId} report={report} />
        </DeviceApiProvider>
      </QueryClientProvider>
    );
    const screen = render(renderTree('device-a'));
    await waitFor(() => expect(latest).not.toBeNull());
    act(() => latest!.markSent({ commandId: 'command-a', text: 'A', at: Date.now() }));
    await waitFor(() => expect(reads).toEqual([{ deviceId: 'device-a', commandId: 'command-a' }]));

    screen.rerender(renderTree('device-b'));
    await waitFor(() => expect(latest!.pending).toBeNull());
    act(() => latest!.markSent({ commandId: 'command-b', text: 'B', at: Date.now() }));
    await waitFor(() => expect(latest!.found?.session_id).toBe('session-b'));
    expect(reads).toContainEqual({ deviceId: 'device-b', commandId: 'command-b' });
    expect(reads).not.toContainEqual({ deviceId: 'device-b', commandId: 'command-a' });

    await act(async () => {
      resolveA?.({ status: 'acked', result: { session_id: 'session-a' } });
      await Promise.resolve();
    });
    expect(latest!.found?.session_id).toBe('session-b');
    expect(latest!.pending?.commandId).toBe('command-b');
  });
});
