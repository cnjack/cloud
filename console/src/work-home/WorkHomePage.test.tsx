import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { AccountRepositoryCatalog, AccountRepositoryTarget, ProjectModel, Service } from '../api/types';
import { ToastProvider } from '../components/Toast';
import { WorkHomePage } from './WorkHomePage';

const devices = [
  { id: 'device-1', name: 'dev-mbp-01', online: true, platform: 'darwin', capabilities: { projects: [] } },
];

vi.mock('@jcloud/device-ui', async (importOriginal) => {
  const original = await importOriginal<typeof import('@jcloud/device-ui')>();
  return { ...original, useDevices: () => ({ data: devices, isLoading: false, isError: false }) };
});

vi.mock('./RemoteComposer', () => ({
  RemoteComposer: ({ device, contextHeader }: { device: { id: string }; contextHeader?: ReactNode }) => <div data-testid="remote-composer">{contextHeader}Remote composer {device.id}</div>,
}));

const models: ProjectModel[] = [
  { id: 'model-1', name: 'Codex', model_name: 'gpt-test', capabilities: { reasoning: true, tools: true, image: false } },
];
const targets: AccountRepositoryTarget[] = [
  { provider: 'github', provider_repo_id: '42', full_name: 'acme/payments', default_branch: 'main', private: true },
  { provider: 'gitea', provider_repo_id: '43', full_name: 'acme/docs', default_branch: 'trunk', private: false },
];

function renderPage({
  repositories = [],
  startAccountTask = vi.fn(),
  catalog = { repositories: targets, sources: [] },
  listAccountRepositories,
}: {
  repositories?: Service[];
  startAccountTask?: ReturnType<typeof vi.fn>;
  catalog?: AccountRepositoryCatalog;
  listAccountRepositories?: (q?: string, limit?: number) => Promise<AccountRepositoryCatalog>;
} = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = {
    listRepositories: async () => repositories,
    listAccountRepositories: listAccountRepositories ?? (async () => catalog),
    listAccountRepositoryBranches: async () => [
      { name: 'main', default: true, protected: true },
      { name: 'develop', default: false },
    ],
    listAccountModels: async () => models,
    startAccountTask,
  } as unknown as ApiClient;
  return {
    startAccountTask,
    queryClient,
    ...render(
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client} role="cluster-admin">
          <ToastProvider>
            <MemoryRouter initialEntries={['/repositories']}>
              <Routes>
                <Route path="/repositories" element={<WorkHomePage />} />
                <Route path="/runs/:runId" element={<div data-testid="run-page" />} />
                <Route path="/account/settings" element={<div data-testid="git-accounts-page" />} />
              </Routes>
            </MemoryRouter>
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>,
    ),
  };
}

describe('WorkHomePage', () => {
  beforeEach(() => window.localStorage.clear());

  it('combines the account composer and Repository workspace without Project or Service navigation', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { name: 'What should we code next?' })).toBeTruthy();
    expect(screen.queryByText(/^Projects$/)).toBeNull();
    expect(screen.queryByText(/^Services$/)).toBeNull();
    expect(await screen.findByRole('button', { name: /acme\/payments/ })).toBeTruthy();
    for (const name of ['Tasks', 'Board', 'Reviews', 'Automations', 'Usage', 'Settings']) {
      expect(screen.getByRole('tab', { name })).toBeTruthy();
    }
    expect(screen.getAllByText('acme/payments').length).toBeGreaterThanOrEqual(2);
  });

  it('renders the Work Home shell and skeleton before Repository data resolves', async () => {
    let resolveCatalog!: (catalog: AccountRepositoryCatalog) => void;
    const pendingCatalog = new Promise<AccountRepositoryCatalog>((resolve) => { resolveCatalog = resolve; });
    renderPage({ listAccountRepositories: () => pendingCatalog });

    expect(screen.getByRole('heading', { name: 'What should we code next?' })).toBeTruthy();
    expect(screen.getByTestId('work-home-skeleton')).toBeTruthy();
    expect(screen.queryByLabelText('Describe a task')).toBeNull();

    resolveCatalog({ repositories: targets, sources: [] });
    expect(await screen.findByLabelText('Describe a task')).toBeTruthy();
    expect(screen.queryByTestId('work-home-skeleton')).toBeNull();
  });

  it('switches the upper-left context to Remote and removes Repository workspace content', async () => {
    renderPage();
    const context = await screen.findByRole('button', { name: /acme\/payments/ });
    fireEvent.click(context);
    fireEvent.click(screen.getByRole('button', { name: /Remote connection/ }));
    fireEvent.click(screen.getByRole('button', { name: /dev-mbp-01/ }));
    expect(await screen.findByTestId('remote-composer')).toBeTruthy();
    expect(screen.queryByRole('tab', { name: 'Board' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /Repository or Remote context dev-mbp-01/ }));
    fireEvent.click(screen.getByRole('button', { name: /Remote connection/ }));
    expect(screen.getByRole('link', { name: /Connect new device/ }).getAttribute('href')).toBe('/devices/guide');
  });

  it('searches Repositories through the API and reuses the fresh SWR cache', async () => {
    const listAccountRepositories = vi.fn(async (q = '', limit = 12) => ({
      repositories: q === 'docs' ? [targets[1]!] : [targets[0]!],
      sources: [],
      limit,
    }));
    renderPage({ listAccountRepositories });
    fireEvent.click(await screen.findByRole('button', { name: /acme\/payments/ }));
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search repositories' }), { target: { value: 'docs' } });
    expect(await screen.findByTestId('repository-search-skeleton')).toBeTruthy();
    expect(await screen.findByRole('button', { name: /acme\/docs/ })).toBeTruthy();
    await waitFor(() => expect(listAccountRepositories).toHaveBeenCalledWith('docs', 20));

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search repositories' }), { target: { value: '' } });
    expect(await screen.findByRole('button', { name: /acme\/payments/ })).toBeTruthy();
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search repositories' }), { target: { value: 'docs' } });
    expect(await screen.findByRole('button', { name: /acme\/docs/ })).toBeTruthy();
    await act(() => new Promise((resolve) => setTimeout(resolve, 300)));
    expect(listAccountRepositories.mock.calls.filter(([q]) => q === 'docs')).toHaveLength(1);
  });

  it('remembers the selected model and starts the chosen account Repository task', async () => {
    const startAccountTask = vi.fn(async () => ({
      run: { id: 'run-1', project_id: 'hidden', service_id: 'repo-1', prompt: 'Fix checkout', status: 'queued', kind: 'agent', phase: 'Queued', attempt: 1, created_at: '' },
      repository: { id: 'repo-1', project_id: 'hidden', name: 'payments', repo_owner_name: 'acme/payments', repo_kind: 'provider', provider: 'github', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
    }));
    renderPage({ startAccountTask });
    fireEvent.change(await screen.findByLabelText('Describe a task'), { target: { value: 'Fix checkout' } });
    fireEvent.click(screen.getByRole('button', { name: 'Start task' }));
    await waitFor(() => expect(startAccountTask).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'github',
      provider_repo_id: '42',
      prompt: 'Fix checkout',
      base_branch: 'main',
      model_id: 'model-1',
      session: true,
    })));
    expect(window.localStorage.getItem('jcloud.last-model.v1:session')).toBe('model-1');
    expect(await screen.findByTestId('run-page')).toBeTruthy();
  });

  it('uses the shared jcode controls for branch, mode, effort, and Goal', async () => {
    const startAccountTask = vi.fn(async () => ({
      run: { id: 'run-2', project_id: 'hidden', service_id: 'repo-1', prompt: 'Plan checkout', status: 'queued', kind: 'agent', phase: 'Queued', attempt: 1, created_at: '' },
      repository: { id: 'repo-1', project_id: 'hidden', name: 'payments', repo_owner_name: 'acme/payments', repo_kind: 'provider', provider: 'github', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
    }));
    renderPage({ startAccountTask });

    const branchButton = await screen.findByRole('button', { name: 'Base branch main' });
    await waitFor(() => expect((branchButton as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(branchButton);
    fireEvent.click(screen.getByRole('option', { name: 'develop' }));
    fireEvent.click(screen.getByRole('button', { name: 'Ask for approval' }));
    fireEvent.click(screen.getByText('Plan', { selector: 'span' }));
    fireEvent.click(screen.getByRole('button', { name: 'Effort: Default' }));
    fireEvent.click(screen.getByText('high', { selector: 'span' }));
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    fireEvent.click(screen.getByText('Goal'));
    fireEvent.change(screen.getByRole('textbox', { name: 'Describe a task' }), { target: { value: 'Plan checkout' } });
    fireEvent.click(screen.getByRole('button', { name: 'Start task' }));

    await waitFor(() => expect(startAccountTask).toHaveBeenCalledWith(expect.objectContaining({
      base_branch: 'develop',
      permission_mode: 'plan',
      model_effort: 'high',
      goal_mode: true,
    })));
  });

  it('puts Personal, Account usage, and admin-only Cluster settings in the avatar menu', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Account menu/ }));
    expect(screen.getByRole('menuitem', { name: 'Personal settings' })).toBeTruthy();
    expect(screen.getByRole('menuitem', { name: 'Account usage' })).toBeTruthy();
    expect(screen.getByRole('menuitem', { name: /Cluster settings/ })).toBeTruthy();
    expect(screen.getByRole('menuitem', { name: 'Code reviews' })).toBeTruthy();
  });

  it('routes an unavailable Repository source to the exact Git accounts recovery section', async () => {
    renderPage({
      catalog: {
        repositories: [],
        sources: [{
          provider: 'github',
          account: 'cnjack',
          status: 'unavailable',
          message: 'Repository access is unavailable; reconnect this provider account',
        }],
      },
    });

    const recovery = await screen.findByRole('link', { name: 'Review Git account access' });
    expect(recovery.getAttribute('href')).toBe('/account/settings?section=connections');
    fireEvent.click(recovery);
    expect(await screen.findByTestId('git-accounts-page')).toBeTruthy();
  });

  it('does not block a ready Repository because a different provider needs reauthorization', async () => {
    renderPage({
      catalog: {
        repositories: targets,
        sources: [
          { provider: 'github', account: 'cnjack', status: 'ready' },
          { provider: 'gitea', account: 'legacy', status: 'unavailable', message: 'Reconnect Gitea' },
        ],
      },
    });

    expect(await screen.findByRole('button', { name: /acme\/payments/ })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Review Git account access' })).toBeNull();
    expect(screen.queryByText('Reconnect Gitea')).toBeNull();
  });
});
