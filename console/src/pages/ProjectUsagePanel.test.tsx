import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { UsageSummaryEnvelope } from '../api/types';
import { ProjectUsagePanel } from './ProjectUsagePanel';

const response: UsageSummaryEnvelope = {
  summary: {
    availability: 'available',
    requests: 3,
    capture: { reported: 2, partial: 1, unavailable: 0, parse_error: 0 },
    tokens: { input: 3000, output: 900, cache_read: 200, cache_write: null },
    costs: { reported: [], estimated: [], uncosted: [] },
  },
  groups: [{
    kind: 'service',
    id: 'svc-1',
    name: 'API',
    summary: {
      availability: 'available',
      requests: 3,
      capture: { reported: 2, partial: 1, unavailable: 0, parse_error: 0 },
      tokens: { input: 3000, output: 900, cache_read: 200, cache_write: null },
      costs: { reported: [], estimated: [], uncosted: [] },
    },
  }],
};

describe('ProjectUsagePanel', () => {
  it('shows the project total and refetches with an explicit grouping', async () => {
    const getProjectUsage = vi.fn(async (
      _projectId: string,
      groupBy: 'service' | 'automation' | 'model',
    ) => ({
      ...response,
      groups: response.groups.map((group) => ({ ...group, kind: groupBy })),
    }));
    render(
      <MemoryRouter>
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <ApiProvider client={{ getProjectUsage } as unknown as ApiClient}>
            <ProjectUsagePanel projectId="p1" />
          </ApiProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );

    expect(await screen.findByTestId('project-usage')).toBeTruthy();
    expect(screen.getAllByText('3 / 3 captured').length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: '24 hours' })).toBeTruthy();
    expect(screen.getByTestId('project-cost-sources')).toBeTruthy();
    expect(screen.getByTestId('project-capture-health')).toBeTruthy();
    expect(screen.getByText(/UTC hourly buckets/)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'API' }).getAttribute('href')).toContain('service=svc-1');
    fireEvent.click(screen.getByRole('button', { name: 'Automation' }));
    await waitFor(() => expect(getProjectUsage.mock.calls.at(-1)?.[1]).toBe('automation'));
  });
});
