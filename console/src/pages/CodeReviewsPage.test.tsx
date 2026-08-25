import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import { ToastProvider } from '../components/Toast';
import { CodeReviewsPage } from './CodeReviewsPage';

function renderPage(client: ApiClient, initialEntry = '/code-reviews') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={[initialEntry]}>
            <Routes>
              <Route path="/code-reviews" element={<CodeReviewsPage />} />
              <Route path="/runs/:runId" element={<div>Review run opened</div>} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('CodeReviewsPage', () => {
  it('aggregates only Review Runs across the hidden workspace containers', async () => {
    const client = {
      listProjects: async () => [{ id: 'hidden', name: 'Hidden', role: 'owner', services: [], created_at: '' }],
      listRuns: async () => [
        { id: 'review-1', project_id: 'hidden', kind: 'review', prompt: 'Review PR 42', status: 'succeeded', created_at: '2026-08-25T02:00:00Z' },
        { id: 'agent-1', project_id: 'hidden', kind: 'agent', prompt: 'Implement feature', status: 'succeeded', created_at: '2026-08-25T01:00:00Z' },
      ],
    } as unknown as ApiClient;
    renderPage(client);

    expect(await screen.findByText('Review PR 42')).toBeTruthy();
    expect(screen.queryByText('Implement feature')).toBeNull();
  });

  it('uses an aggregate empty state instead of Repository-scoped copy', async () => {
    const client = {
      listProjects: async () => [],
    } as unknown as ApiClient;
    renderPage(client);

    expect(await screen.findByText('No code reviews yet')).toBeTruthy();
    expect(screen.queryByText(/service|this Repository/i)).toBeNull();
    expect(screen.getByText('No Cloud-created pull request is ready for review.')).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Review Git account access' })).toBeNull();
  });

  it('preselects the Repository from Work Home and starts a real review from an eligible PR Run', async () => {
    const requestReview = vi.fn(async () => ({
      id: 'review-new', project_id: 'hidden', service_id: 'repo-1', kind: 'review',
      prompt: 'Review PR 42', status: 'queued', created_at: '2026-08-26T03:00:00Z',
    }));
    const client = {
      listProjects: async () => [{
        id: 'hidden', name: 'Hidden', role: 'owner', created_at: '', services: [
          { id: 'repo-1', project_id: 'hidden', name: 'api', repo_kind: 'provider', provider: 'github', repo_owner_name: 'acme/api', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
          { id: 'repo-2', project_id: 'hidden', name: 'web', repo_kind: 'provider', provider: 'github', repo_owner_name: 'acme/web', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
        ],
      }],
      listRuns: async () => [
        { id: 'agent-api', project_id: 'hidden', service_id: 'repo-1', kind: 'agent', prompt: 'Fix callback', status: 'succeeded', pr_url: 'https://github.test/acme/api/pull/42', pr_number: 42, pr_title: 'Fix callback', created_at: '2026-08-25T01:00:00Z' },
        { id: 'agent-web', project_id: 'hidden', service_id: 'repo-2', kind: 'agent', prompt: 'Fix layout', status: 'succeeded', pr_url: 'https://github.test/acme/web/pull/8', pr_number: 8, created_at: '2026-08-25T01:00:00Z' },
      ],
      listProjectModels: async () => ({ models: [{ id: 'model-1' }], env_fallback: false }),
      requestReview,
    } as unknown as ApiClient;

    renderPage(client, '/code-reviews?repository=repo-1');

    expect(await screen.findByRole('heading', { name: 'Start a code review' })).toBeTruthy();
    expect((await screen.findByLabelText('Repository') as HTMLSelectElement).value).toBe('repo-1');
    expect(await screen.findByText('#42 · Fix callback')).toBeTruthy();
    expect(screen.queryByText('#8 · Fix layout')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Create code review' }));
    await waitFor(() => expect(requestReview).toHaveBeenCalledWith('agent-api'));
    expect(await screen.findByText('Review run opened')).toBeTruthy();
  });

  it('explains how to create a reviewable PR instead of leaving a dead create action', async () => {
    const client = {
      listProjects: async () => [{
        id: 'hidden', name: 'Hidden', role: 'owner', created_at: '', services: [
          { id: 'repo-1', project_id: 'hidden', name: 'api', repo_kind: 'provider', provider: 'github', repo_owner_name: 'acme/api', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
        ],
      }],
      listRuns: async () => [
        { id: 'agent-running', project_id: 'hidden', service_id: 'repo-1', kind: 'agent', prompt: 'Still working', status: 'running', created_at: '2026-08-25T01:00:00Z' },
      ],
      listProjectModels: async () => ({ models: [{ id: 'model-1' }], env_fallback: false }),
    } as unknown as ApiClient;

    renderPage(client, '/code-reviews?repository=repo-1');

    expect(await screen.findByText('No Cloud-created pull request is ready for review.')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Open Work Home' }).getAttribute('href')).toBe('/?repository=repo-1');
    expect(screen.queryByRole('button', { name: 'Create code review' })).toBeNull();
  });
});
