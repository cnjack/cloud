/*
 * usePendingNewSession — pending card + found matching (UX review fix).
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { DeviceApiProvider } from '../api/DeviceApiProvider';
import type { DeviceApi, DeviceSession } from '../api/devices';
import { usePendingNewSession } from './usePendingNewSession';

const DEVICE = 'dev-1';

function sessionsApi(sessions: DeviceSession[]): DeviceApi {
  return { listSessions: async () => sessions } as unknown as DeviceApi;
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
    act(() => result.current.markSent({ text: 'hello world', at: Date.now() }));
    expect(result.current.pending?.text).toBe('hello world');
  });

  it('matches the session mirrored after the send, ignoring older ones', async () => {
    const at = Date.now();
    const api = sessionsApi([
      row('s-old', new Date(at - 60_000).toISOString()),
      row('s-new', new Date(at + 3_000).toISOString()),
    ]);
    const { result } = renderHook(() => usePendingNewSession(DEVICE), { wrapper: wrapper(api) });
    act(() => result.current.markSent({ text: 'hi', at }));
    await waitFor(() => expect(result.current.found?.session_id).toBe('s-new'));
  });
});
