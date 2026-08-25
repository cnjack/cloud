import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { AccountRepositoryTarget, ProjectModel, Service } from '../api/types';
import { ToastProvider } from '../components/Toast';
import { RepositoriesPage } from './RepositoriesPage';

function renderPage({
  repositories = [],
  targets = [],
  models = [{ id: 'model-1', name: 'Codex', model_name: 'gpt-test', capabilities: { reasoning: true, tools: true, image: false } }],
  startAccountTask = vi.fn(),
}: {
  repositories?: Service[];
  targets?: AccountRepositoryTarget[];
  models?: ProjectModel[];
  startAccountTask?: ReturnType<typeof vi.fn>;
}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = {
    listRepositories: async () => repositories,
    listAccountRepositories: async () => ({ repositories: targets, sources: [] }),
    listAccountModels: async () => models,
    startAccountTask,
  } as unknown as ApiClient;
  return {
    startAccountTask,
    ...render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/repositories']}>
            <Routes>
              <Route path="/repositories" element={<RepositoriesPage />} />
              <Route path="/runs/:runId" element={<div data-testid="run-page" />} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
    ),
  };
}

describe('RepositoriesPage', () => {
  it('makes the account task composer the entry and offers every account repository', async () => {
    const startAccountTask = vi.fn(async () => ({
      run: { id: 'run-1', project_id: 'hidden', service_id: 'repo-1', prompt: 'Fix checkout', status: 'queued', kind: 'agent', phase: 'Queued', attempt: 1, created_at: '' },
      repository: { id: 'repo-1', project_id: 'hidden', name: 'payments', repo_owner_name: 'acme/payments', repo_kind: 'provider', provider: 'gitea', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
    }));
    renderPage({
      targets: [
        { provider: 'gitea', provider_repo_id: '42', full_name: 'acme/payments', default_branch: 'main', private: true },
        { provider: 'gitea', provider_repo_id: '43', full_name: 'acme/docs', default_branch: 'trunk', private: false },
      ],
      startAccountTask,
    });

    expect(await screen.findByRole('heading', { name: 'What should we code next?' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: /Connect repository/i })).toBeNull();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Repository' }).textContent).toContain('acme/payments'));
    fireEvent.change(screen.getByLabelText('Describe a task'), { target: { value: 'Fix checkout' } });
    fireEvent.click(screen.getByRole('button', { name: 'Start task' }));

    await waitFor(() => expect(startAccountTask).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'gitea',
      provider_repo_id: '42',
      prompt: 'Fix checkout',
      base_branch: 'main',
      model_id: 'model-1',
      session: true,
    })));
    expect(await screen.findByTestId('run-page')).toBeTruthy();
  });

  it('keeps materialized repositories as optional detail links, not prerequisites', async () => {
    renderPage({ repositories: [
      { id: 'repo-1', project_id: 'hidden', name: 'payments', repo_owner_name: 'acme/payments', repo_kind: 'provider', provider: 'github', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
      { id: 'repo-2', project_id: 'hidden', name: 'docs', repo_owner_name: 'acme/docs', repo_kind: 'provider', provider: 'github', default_branch: 'main', git_mode: 'readonly', created_at: '' },
    ], targets: [
      { provider: 'github', provider_repo_id: '1', repository_id: 'repo-1', full_name: 'acme/payments', default_branch: 'main', private: true },
    ] });

    const payment = await screen.findByRole('link', { name: /acme\/payments/i });
    expect(payment.getAttribute('href')).toBe('/repositories/repo-1');
    expect(screen.queryByText(/Project|Service/)).toBeNull();

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search repositories' }), { target: { value: 'docs' } });
    expect(screen.queryByRole('link', { name: /acme\/payments/i })).toBeNull();
    expect(screen.getAllByText('acme/docs')).toHaveLength(2);
  });

  it('disables task start with a visible account authorization state', async () => {
    renderPage({ targets: [], models: [] });
    expect(await screen.findByText('Link a Git provider account to choose a repository.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Start task' }).hasAttribute('disabled')).toBe(true);
  });
});
