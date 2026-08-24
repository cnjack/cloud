import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { Service } from '../api/types';
import { RepositoriesPage } from './RepositoriesPage';

function renderPage(repositories: Service[]) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = { listRepositories: async () => repositories } as ApiClient;
  return render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <MemoryRouter><RepositoriesPage /></MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('RepositoriesPage', () => {
  it('lists Repository targets and filters without exposing Project or Service', async () => {
    renderPage([
      { id: 'repo-1', project_id: 'hidden', name: 'payments', repo_owner_name: 'acme/payments', repo_kind: 'provider', provider: 'github', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
      { id: 'repo-2', project_id: 'hidden', name: 'docs', repo_owner_name: 'acme/docs', repo_kind: 'provider', provider: 'github', default_branch: 'main', git_mode: 'readonly', created_at: '' },
    ]);

    const payment = await screen.findByRole('link', { name: /acme\/payments/i });
    expect(payment.getAttribute('href')).toBe('/repositories/repo-1');
    expect(screen.queryByText(/Project|Service/)).toBeNull();

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search repositories' }), { target: { value: 'docs' } });
    expect(screen.queryByText('acme/payments')).toBeNull();
    expect(screen.getAllByText('acme/docs')).toHaveLength(2);
  });
});
