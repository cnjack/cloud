import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router-dom';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { AccountRepositoryCatalog, AccountRepositoryTarget, ProjectModel, Run, Service } from '../api/types';
import { ToastProvider } from '../components/Toast';
import { setLocale } from '../i18n';
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
  runs = [],
  startAccountTask = vi.fn(),
  catalog = { repositories: targets, sources: [] },
  listAccountRepositories,
  initialEntry = '/repositories',
  historyControls = false,
}: {
  initialEntry?: string;
  historyControls?: boolean;
  repositories?: Service[];
  runs?: Run[];
  startAccountTask?: ReturnType<typeof vi.fn>;
  catalog?: AccountRepositoryCatalog;
  listAccountRepositories?: (q?: string, limit?: number) => Promise<AccountRepositoryCatalog>;
} = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = {
    listRepositories: async () => repositories,
    listRuns: async () => runs,
    listProjectBoardLinks: async () => [],
    listProjectPlugins: async () => [],
    getProject: async (id: string) => ({ id, name: 'Personal workspace', created_at: '', services: repositories }),
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
            <MemoryRouter initialEntries={[initialEntry]}>
              {historyControls && <NavigationHistory />}
              <Routes>
                <Route path="/repositories" element={<WorkHomePage />} />
                <Route path="/devices/:deviceId" element={<div data-testid="device-workspace-page" />} />
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

function NavigationHistory() {
  const navigate = useNavigate();
  const location = useLocation();
  return <div><button onClick={() => navigate(-1)}>History back</button><button onClick={() => navigate(1)}>History forward</button><span data-testid="navigation-location">{location.pathname}{location.search}</span></div>;
}

const navigationRepositories = [
  { id: 'repo-payments', project_id: 'personal', name: 'payments', repo_kind: 'provider', provider: 'github', provider_repo_id: 42, repo_owner_name: 'acme/payments', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
  { id: 'repo-docs', project_id: 'personal', name: 'docs', repo_kind: 'provider', provider: 'gitea', provider_repo_id: 43, repo_owner_name: 'acme/docs', default_branch: 'trunk', git_mode: 'draft_pr', created_at: '' },
] as Service[];
const navigationRuns = navigationRepositories.map((repo) => ({ id: `run-${repo.id}`, project_id: 'personal', service_id: repo.id, prompt: `Task for ${repo.name}`, status: 'succeeded', created_at: '2026-09-12T00:00:00Z' })) as Run[];

describe('WorkHomePage', () => {
  beforeEach(async () => {
    window.localStorage.clear();
    await setLocale('en');
    window.localStorage.clear();
  });

  it('combines the account composer and Repository workspace without Project or Service navigation', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { name: 'What would you like to work on?' })).toBeTruthy();
    expect(screen.queryByText(/^Projects$/)).toBeNull();
    expect(screen.queryByText(/^Services$/)).toBeNull();
    expect(await screen.findByRole('button', { name: /acme\/payments/ })).toBeTruthy();
    for (const name of ['Tasks', 'Board', 'Reviews', 'Automations', 'Usage', 'Settings']) {
      expect(screen.getByRole('tab', { name })).toBeTruthy();
    }
    expect(screen.getAllByText('acme/payments').length).toBeGreaterThanOrEqual(2);
  });

  it('shows recent repositories and account conversations without mixing in code reviews', async () => {
    const materializedRepositories: Service[] = [
      { id: 'repo-payments', project_id: 'personal', name: 'payments', repo_kind: 'provider', provider: 'github', provider_repo_id: 42, repo_owner_name: 'acme/payments', default_branch: 'main', git_mode: 'draft_pr', created_at: '' },
      { id: 'repo-docs', project_id: 'personal', name: 'docs', repo_kind: 'provider', provider: 'gitea', provider_repo_id: 43, repo_owner_name: 'acme/docs', default_branch: 'trunk', git_mode: 'draft_pr', created_at: '' },
    ];
    const conversationRuns: Run[] = [
      { id: 'run-payments', project_id: 'personal', service_id: 'repo-payments', prompt: 'Fix checkout callback', status: 'running', created_at: '2026-08-27T08:00:00Z' },
      { id: 'run-docs', project_id: 'personal', service_id: 'repo-docs', prompt: 'Refresh onboarding guide', status: 'succeeded', created_at: '2026-08-26T08:00:00Z' },
      { id: 'review-payments', project_id: 'personal', service_id: 'repo-payments', kind: 'review', prompt: 'Review checkout PR', status: 'succeeded', created_at: '2026-08-27T09:00:00Z' },
    ];

    renderPage({ repositories: materializedRepositories, runs: conversationRuns });

    const rail = within(await screen.findByTestId('conversation-rail'));
    expect(await rail.findByText('acme/payments')).toBeTruthy();
    expect(rail.getByText('acme/docs')).toBeTruthy();
    expect(rail.getByRole('link', { name: /Fix checkout callback/ })).toBeTruthy();
    expect(rail.getByRole('link', { name: /Refresh onboarding guide/ })).toBeTruthy();
    expect(rail.queryByText('Review checkout PR')).toBeNull();

    fireEvent.change(rail.getByRole('searchbox', { name: 'Search conversations' }), { target: { value: 'onboarding' } });
    expect(rail.queryByText('Fix checkout callback')).toBeNull();
    fireEvent.click(rail.getByRole('button', { name: 'Collapse conversations' }));
    expect(screen.getByTestId('work-home').getAttribute('data-rail-collapsed')).toBe('true');
    expect(rail.getByRole('button', { name: 'Expand conversations' })).toBeTruthy();
    fireEvent.click(rail.getByRole('link', { name: /Refresh onboarding guide/ }));
    expect(await screen.findByTestId('run-page')).toBeTruthy();
  });

  it('keeps the header, rail selection and browser history in the same Repository section', async () => {
    renderPage({ repositories: navigationRepositories, runs: navigationRuns, initialEntry: '/repositories?repository=repo-payments&tab=board', historyControls: true });
    const header = within(await screen.findByRole('navigation', { name: 'Workspace location' }));
    await waitFor(() => expect(header.getByText('acme/payments')).toBeTruthy());
    expect(header.getByText('Board')).toBeTruthy();
    const rail = within(screen.getByTestId('conversation-rail'));
    const selected = rail.getByRole('link', { name: 'Open repository acme/payments' });
    expect(selected.getAttribute('aria-current')).toBe('location');
    expect(rail.getByRole('link', { name: 'Task for payments' })).toBeTruthy();
    expect(header.getByText('Board')).toBeTruthy();
    fireEvent.click(rail.getByRole('link', { name: 'Open repository acme/docs' }));
    await waitFor(() => expect(header.getByText('acme/docs')).toBeTruthy());
    expect(header.queryByText('acme/payments')).toBeNull();
    expect(header.getByText('Board')).toBeTruthy();
    expect(rail.getByRole('link', { name: 'Open repository acme/docs' }).getAttribute('aria-current')).toBe('location');
    fireEvent.click(screen.getByRole('button', { name: 'History back' }));
    await waitFor(() => expect(header.getByText('acme/payments')).toBeTruthy());
    expect(header.getByText('Board')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'History forward' }));
    await waitFor(() => expect(header.getByText('acme/docs')).toBeTruthy());
    fireEvent.click(screen.getByRole('tab', { name: 'Tasks' }));
    expect(header.getByText('Tasks')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'History back' }));
    await waitFor(() => expect(header.getByText('Board')).toBeTruthy());
  });

  it('resolves a linked Repository outside the first catalog page instead of showing another Repository', async () => {
    const listAccountRepositories = vi.fn(async (q = '') => ({ repositories: q === 'acme/docs' ? [targets[1]!] : [targets[0]!], sources: [] }));
    renderPage({ repositories: navigationRepositories, runs: navigationRuns, initialEntry: '/repositories?repository=repo-docs&tab=tasks', listAccountRepositories });
    const header = within(await screen.findByRole('navigation', { name: 'Workspace location' }));
    await waitFor(() => expect(listAccountRepositories).toHaveBeenCalledWith('acme/docs', 20));
    expect(header.getByText('acme/docs')).toBeTruthy();
    expect(header.queryByText('acme/payments')).toBeNull();
    expect(await screen.findByRole('button', { name: /Repository or Remote context acme\/docs/ })).toBeTruthy();
  });

  it('restores an unmaterialized repository from its stable provider identity after reload', async () => {
    renderPage({ initialEntry: '/repositories?target=gitea%3A43&repositoryName=acme%2Fdocs&tab=board' });
    expect(await screen.findByRole('button', { name: /Repository or Remote context acme\/docs/ })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Board' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.queryByRole('button', { name: /Repository or Remote context acme\/payments/ })).toBeNull();
  });

  it('shows an unavailable target instead of silently selecting the first Repository', async () => {
    renderPage({ initialEntry: '/repositories?repository=missing&tab=board' });
    expect(await screen.findByTestId('workspace-target-unavailable')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Repository or Remote context acme\/payments/ })).toBeNull();
  });

  it('does not retain a selected target after its provider access becomes unavailable', async () => {
    let unavailable = false;
    const listAccountRepositories = async (): Promise<AccountRepositoryCatalog> => unavailable
      ? { repositories: [], sources: [{ provider: 'gitea', account: 'test', status: 'unavailable', message: 'Gitea session expired' }] }
      : { repositories: targets, sources: [] };
    const { queryClient } = renderPage({ listAccountRepositories });
    fireEvent.click(await screen.findByRole('button', { name: /Repository or Remote context acme\/payments/ }));
    fireEvent.click(await screen.findByRole('button', { name: /acme\/docs/ }));
    expect(screen.getByRole('button', { name: /Repository or Remote context acme\/docs/ })).toBeTruthy();
    unavailable = true;
    await act(async () => { await queryClient.invalidateQueries(); });
    expect(await screen.findByText('Gitea session expired')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Repository or Remote context acme\/docs/ })).toBeNull();
  });

  it('stacks the open Repository picker above the shared composer toolbar', async () => {
    renderPage();
    const contextButton = await screen.findByRole('button', { name: /Repository or Remote context acme\/payments/ });
    fireEvent.click(contextButton);

    expect(screen.getByRole('searchbox', { name: 'Search repositories' })).toBeTruthy();
    expect(window.getComputedStyle(contextButton.parentElement!).zIndex)
      .toBe('calc(var(--z-dropdown, 900) + 1)');
  });

  it('renders every Repository workspace structure before the first task materializes storage', async () => {
    renderPage();
    await screen.findByRole('button', { name: /acme\/payments/ });

    expect(screen.queryByText(/materialize|first use/i)).toBeNull();
    expect(screen.getByText('No tasks in this Repository')).toBeTruthy();

    fireEvent.click(screen.getByRole('tab', { name: 'Board' }));
    expect(screen.getByRole('heading', { name: 'Board' })).toBeTruthy();
    expect(screen.getByText('No Board connected')).toBeTruthy();
    expect(screen.getByText('Agent queue')).toBeTruthy();
    expect(screen.getByText('Done (optional)')).toBeTruthy();

    fireEvent.click(screen.getByRole('tab', { name: 'Reviews' }));
    expect(screen.getByText('No code reviews yet')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Create code review' }).getAttribute('href')).toBe('/code-reviews');

    fireEvent.click(screen.getByRole('tab', { name: 'Automations' }));
    expect(screen.getByRole('heading', { name: 'Automations' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'All' })).toBeTruthy();
    expect(screen.getByText('No Automations match this view.')).toBeTruthy();

    fireEvent.click(screen.getByRole('tab', { name: 'Usage' }));
    expect(screen.getByTestId('repository-usage')).toBeTruthy();
    expect(screen.getByTestId('repository-usage-default')).toBeTruthy();
    expect(screen.getAllByText('0').length).toBeGreaterThanOrEqual(4);

    fireEvent.click(screen.getByRole('tab', { name: 'Settings' }));
    expect(screen.getByTestId('repository-default-settings')).toBeTruthy();
    expect(screen.getAllByText('acme/payments').length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText('No Repository override')).toBeTruthy();
    expect(screen.getByText('Lifecycle aware')).toBeTruthy();
    expect(screen.getByText('default')).toBeTruthy();
  });

  it('renders the Work Home shell and skeleton before Repository data resolves', async () => {
    let resolveCatalog!: (catalog: AccountRepositoryCatalog) => void;
    const pendingCatalog = new Promise<AccountRepositoryCatalog>((resolve) => { resolveCatalog = resolve; });
    renderPage({ listAccountRepositories: () => pendingCatalog });

    expect(screen.getByRole('heading', { name: 'What would you like to work on?' })).toBeTruthy();
    expect(screen.getByTestId('work-home-skeleton')).toBeTruthy();
    expect(screen.queryByLabelText('Describe a task')).toBeNull();

    resolveCatalog({ repositories: targets, sources: [] });
    expect(await screen.findByLabelText('Describe a task')).toBeTruthy();
    expect(screen.queryByTestId('work-home-skeleton')).toBeNull();
  });

  it('opens the selected device workspace and keeps browser history', async () => {
    renderPage({ historyControls: true });
    fireEvent.click(await screen.findByRole('button', { name: /acme\/payments/ }));
    fireEvent.click(screen.getByRole('button', { name: /Remote connection/ }));
    fireEvent.click(screen.getByRole('button', { name: /dev-mbp-01/ }));
    expect(await screen.findByTestId('device-workspace-page')).toBeTruthy();
    expect(screen.getByTestId('navigation-location').textContent).toBe('/devices/device-1');
    fireEvent.click(screen.getByRole('button', { name: 'History back' }));
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Board' })).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: 'History forward' }));
    expect(await screen.findByTestId('device-workspace-page')).toBeTruthy();
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

  it('merges Personal and usage settings into one Account settings entry', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /Account menu/ }));
    expect(screen.getByRole('menuitem', { name: 'Account settings' })).toBeTruthy();
    expect(screen.queryByRole('menuitem', { name: 'Personal settings' })).toBeNull();
    expect(screen.queryByRole('menuitem', { name: 'Account usage' })).toBeNull();
    expect(screen.getByRole('menuitem', { name: /Cluster settings/ })).toBeTruthy();
    expect(screen.queryByRole('menuitem', { name: 'Code reviews' })).toBeNull();
  });

  it('keeps an open shared composer menu inside the toolbar stacking context', async () => {
    renderPage();
    const modeButton = await screen.findByRole('button', { name: 'Ask for approval' });
    fireEvent.click(modeButton);

    const toolbar = modeButton.closest('.jcode-product-composer-toolbar');
    expect(toolbar).toBeTruthy();
    const menu = document.querySelector('.jcode-product-composer-mode-menu');
    expect(menu).toBeTruthy();
    expect(menu?.closest('.jcode-product-composer-toolbar')).toBe(toolbar);
  });

  it('places a persistent language switch to the right of the account control', async () => {
    renderPage();
    const account = await screen.findByRole('button', { name: 'Account menu' });
    const language = screen.getByRole('button', { name: 'Language' });

    expect(account.compareDocumentPosition(language) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    fireEvent.click(language);
    fireEvent.click(await screen.findByRole('menuitem', { name: '简体中文' }));

    expect(await screen.findByRole('heading', { name: '下一步，想做什么？' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: '任务' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: '看板' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: '代码审查' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: '自动化' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: '用量' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: '设置' })).toBeTruthy();
    expect(screen.getByText('云端 Runner')).toBeTruthy();
    expect(screen.getByText('此 Repository 暂无任务')).toBeTruthy();
    expect(window.localStorage.getItem('jcloud_locale')).toBe('zh-Hans');
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
