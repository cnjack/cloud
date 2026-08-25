import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import { CodeReviewsPage } from './CodeReviewsPage';

describe('CodeReviewsPage', () => {
  it('aggregates only Review Runs across the hidden workspace containers', async () => {
    const client = {
      listProjects: async () => [{ id: 'hidden', name: 'Hidden', role: 'owner', services: [], created_at: '' }],
      listRuns: async () => [
        { id: 'review-1', project_id: 'hidden', kind: 'review', prompt: 'Review PR 42', status: 'succeeded', created_at: '2026-08-25T02:00:00Z' },
        { id: 'agent-1', project_id: 'hidden', kind: 'agent', prompt: 'Implement feature', status: 'succeeded', created_at: '2026-08-25T01:00:00Z' },
      ],
    } as unknown as ApiClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client}>
          <MemoryRouter><CodeReviewsPage /></MemoryRouter>
        </ApiProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByText('Review PR 42')).toBeTruthy();
    expect(screen.queryByText('Implement feature')).toBeNull();
  });

  it('uses an aggregate empty state instead of Repository-scoped copy', async () => {
    const client = {
      listProjects: async () => [],
    } as unknown as ApiClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client}>
          <MemoryRouter><CodeReviewsPage /></MemoryRouter>
        </ApiProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByText('No code reviews yet')).toBeTruthy();
    expect(screen.queryByText(/service|this Repository/i)).toBeNull();
  });
});
