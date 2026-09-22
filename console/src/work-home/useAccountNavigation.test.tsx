import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor, act } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import type { ReactNode } from 'react';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import { useAccountNavigation } from './useAccountNavigation';

function setup(client: Partial<ApiClient>) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderHook(() => useAccountNavigation(), { wrapper: ({ children }: {children: ReactNode}) =>
    <QueryClientProvider client={qc}><ApiProvider client={client as ApiClient}>{children}</ApiProvider></QueryClientProvider> });
}
describe('Account navigation', () => {
  it('loads each authorized project once and combines its conversations across page routes', async () => {
    const listRuns = vi.fn(async (id: string) => [{ id: `${id}-task` }]) as unknown as ApiClient['listRuns'];
    const { result } = setup({ listRepositories: async () => [
      { id: 'r1', project_id: 'p1' }, { id: 'r2', project_id: 'p1' }, { id: 'r3', project_id: 'p2' },
    ] as Awaited<ReturnType<ApiClient['listRepositories']>>, listRuns });
    await waitFor(() => expect(result.current.runs.map(run => run.id)).toEqual(['p1-task','p2-task']));
    expect(listRuns).toHaveBeenCalledTimes(2);
  });
  it('surfaces an unavailable task list and retries without replacing other repositories with fake empty state', async () => {
    let unavailable = true;
    const { result } = setup({ listRepositories: async () => [{ id: 'r1', project_id: 'p1' }] as Awaited<ReturnType<ApiClient['listRepositories']>>,
      listRuns: async () => { if (unavailable) throw new Error('Task service unavailable'); return []; },
    });
    await waitFor(() => expect(result.current.error?.message).toBe('Task service unavailable'));
    unavailable = false;
    act(() => result.current.onRetry());
    await waitFor(() => expect(result.current.error).toBeUndefined());
  });
});
