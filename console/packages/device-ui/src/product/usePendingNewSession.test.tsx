/*
 * usePendingNewSession — pending card + found matching (UX review fix).
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
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
});
