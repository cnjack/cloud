import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import { RepositoryUsagePanel } from './RepositoryUsagePanel';

describe('RepositoryUsagePanel', () => {
  it('loads only the selected Repository usage and updates the time range', async () => {
    const getServiceUsage = vi.fn(async (_repositoryId: string, _from?: string, _to?: string) => ({
      availability: 'available' as const,
      requests: 2,
      capture: { reported: 2, partial: 0, unavailable: 0, parse_error: 0 },
      tokens: { input: 200, output: 50, cache_read: 0, cache_write: 0 },
      costs: { reported: [], estimated: [], uncosted: [] },
    }));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ApiProvider client={{ getServiceUsage } as unknown as ApiClient}>
          <RepositoryUsagePanel repositoryId="repo-1" />
        </ApiProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId('repository-usage')).toBeTruthy();
    expect(screen.getByText('2 / 2 captured')).toBeTruthy();
    expect(getServiceUsage.mock.calls[0]?.[0]).toBe('repo-1');
    fireEvent.click(screen.getByRole('button', { name: '24 hours' }));
    await waitFor(() => expect(getServiceUsage).toHaveBeenCalledTimes(2));
  });
});
