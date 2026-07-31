import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { UsageSummaryEnvelope } from '../api/types';
import { AccountUsagePanel } from './AccountUsagePanel';

const response: UsageSummaryEnvelope = {
  summary: {
    availability: 'available',
    requests: 2,
    capture: { reported: 2, partial: 0, unavailable: 0, parse_error: 0 },
    tokens: { input: 800, output: 120, cache_read: null, cache_write: null },
    costs: { reported: [], estimated: [], uncosted: [] },
  },
  groups: [{
    kind: 'device',
    id: 'device-1',
    name: 'Jack’s Mac',
    summary: {
      availability: 'available',
      requests: 2,
      capture: { reported: 2, partial: 0, unavailable: 0, parse_error: 0 },
      tokens: { input: 800, output: 120, cache_read: null, cache_write: null },
      costs: { reported: [], estimated: [], uncosted: [] },
    },
  }],
};

describe('AccountUsagePanel', () => {
  it('shows Device Cloud Proxy usage and refetches by grant without mixing Project usage', async () => {
    const getAccountUsage = vi.fn(async (groupBy: 'device' | 'model' | 'grant') => ({
      ...response,
      groups: response.groups.map((group) => ({ ...group, kind: groupBy })),
    }));
    render(
      <MemoryRouter>
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <ApiProvider client={{ getAccountUsage } as unknown as ApiClient}>
            <AccountUsagePanel />
          </ApiProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );

    expect(await screen.findByTestId('account-usage')).toBeTruthy();
    expect(screen.getByText('Only requests sent through Cloud Model Proxy are included.')).toBeTruthy();
    expect((await screen.findByRole('link', { name: 'Jack’s Mac' })).getAttribute('href')).toBe('/devices/device-1');
    fireEvent.click(screen.getByRole('button', { name: 'Grant' }));
    await waitFor(() => expect(getAccountUsage.mock.calls.at(-1)?.[0]).toBe('grant'));
  });
});
