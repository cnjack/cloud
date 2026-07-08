/*
 * ProjectDetailPage — M4 composer + role gating (blueprint §5):
 *  - single-repo project: composer, no repository selector; runs dispatch
 *    against the sole service (createServiceRun — the project shim is gone)
 *  - multi-repo project: composer shows a repository selector; runs dispatch
 *    against the selected service
 *  - zero-repo project: an empty state replaces the composer
 *  - viewer: no composer, no Settings, no "+ Add repository"
 *  - owner: "+ Add repository" opens a form that creates a service, with the
 *    draft_pr-needs-a-provider-repo validation inline
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type {
  CreateRunInput,
  CreateServiceInput,
  MemberRole,
  Project,
  ProjectModel,
  Run,
  Service,
  UpdateServiceInput,
} from '../api/types';
import { ProjectDetailPage } from './ProjectDetailPage';

function svc(id: string, name: string): Service {
  return {
    id,
    project_id: 'p1',
    name,
    repo_kind: 'provider',
    provider: 'gitea',
    repo_owner_name: `acme/${name}`,
    default_branch: 'main',
    git_mode: 'readonly',
    created_at: '',
  };
}

function project(role: MemberRole, services: Service[]): Project {
  return {
    id: 'p1',
    name: 'demo',
    created_at: '',
    role,
    services,
  };
}

interface Calls {
  serviceRuns: { sid: string; input: CreateRunInput }[];
  services: { pid: string; input: CreateServiceInput }[];
  serviceUpdates: { sid: string; input: UpdateServiceInput }[];
}

function makeClient(
  p: Project,
  opts: { modelConfigured?: boolean; models?: ProjectModel[] } = {},
): { client: ApiClient; calls: Calls } {
  const calls: Calls = { serviceRuns: [], services: [], serviceUpdates: [] };
  const client: Partial<ApiClient> = {
    getProject: async () => p,
    listRuns: async () => [] as Run[],
    // D21: the composer keys enable/disable off the project's models AND populates
    // its model select. Default configured via the env fallback (empty catalog).
    listProjectModels: async () => ({
      models: opts.models ?? [],
      env_fallback: opts.models ? false : (opts.modelConfigured ?? true),
    }),
    createServiceRun: async (sid, input) => {
      calls.serviceRuns.push({ sid, input });
      return { id: 'r2', project_id: 'p1', service_id: sid, prompt: input.prompt, status: 'queued', created_at: '' } as Run;
    },
    createService: async (pid, input) => {
      calls.services.push({ pid, input });
      return svc('svc_new', input.name ?? 'default');
    },
    updateService: async (sid, input) => {
      calls.serviceUpdates.push({ sid, input });
      return { ...svc(sid, 'default'), default_model_id: input.default_model_id ?? null };
    },
  };
  return { client: client as ApiClient, calls };
}

function renderPage(client: ApiClient, role?: 'cluster-admin' | 'project-admin') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client} role={role}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/projects/p1']}>
            <Routes>
              <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
              <Route path="/runs/:id" element={<div data-testid="run-page" />} />
              <Route path="/" element={<div data-testid="home" />} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('ProjectDetailPage — single-repo composer', () => {
  it('has no repository selector and dispatches against the sole service', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    expect(screen.queryByTestId('composer-service-select')).toBeNull();
    expect(screen.getByTestId('project-settings-btn')).toBeTruthy();
    // The header shows the sole repo's identity (label + git-mode badge).
    expect(screen.getByText('acme/default')).toBeTruthy();
    expect(screen.getByTestId('git-mode-badge')).toBeTruthy();

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'do a thing' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]).toMatchObject({ sid: 'svc_default', input: { prompt: 'do a thing' } });
  });
});

describe('ProjectDetailPage — session toggle (D22)', () => {
  it('defaults OFF: the create body carries NO session field (wire unchanged)', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    const toggle = screen.getByTestId('composer-session-toggle') as HTMLInputElement;
    expect(toggle.checked).toBe(false);

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'one shot' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect('session' in calls.serviceRuns[0]!.input).toBe(false);
  });

  it('sends session:true when checked and re-labels the submit button', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    fireEvent.click(screen.getByTestId('composer-session-toggle'));
    expect(screen.getByTestId('run-submit').textContent).toContain('Start session');

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'chat with me' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]!.input).toMatchObject({ prompt: 'chat with me', session: true });
  });
});

describe('ProjectDetailPage — model selection (D21)', () => {
  const grantedModels: ProjectModel[] = [
    { id: 'm_gpt', name: 'GPT-4o', model_name: 'openai/gpt-4o' },
    { id: 'm_claude', name: 'Claude', model_name: 'anthropic/claude' },
  ];

  it('renders a model select from granted models and dispatches with the picked model_id', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    const select = (await screen.findByTestId('composer-model-select')) as HTMLSelectElement;
    // "Service default" + the two granted models.
    expect(select.options).toHaveLength(3);
    expect(screen.getByText('GPT-4o')).toBeTruthy();

    // Pick a specific model, then dispatch.
    fireEvent.change(select, { target: { value: 'm_claude' } });
    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'go' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]!.input).toMatchObject({ prompt: 'go', model_id: 'm_claude' });
  });

  it('omits model_id when the composer keeps "Service default"', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    await screen.findByTestId('composer-model-select');
    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'go' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]!.input.model_id).toBeUndefined();
  });

  it('shows the service default-model editor to an owner and PATCHes on change', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    const defSelect = (await screen.findByTestId('service-default-model-select')) as HTMLSelectElement;
    fireEvent.change(defSelect, { target: { value: 'm_gpt' } });
    await waitFor(() => expect(calls.serviceUpdates).toHaveLength(1));
    expect(calls.serviceUpdates[0]).toMatchObject({ sid: 'svc_default', input: { default_model_id: 'm_gpt' } });
  });

  it('hides the service default-model editor from a member (composer pick only)', async () => {
    const { client } = makeClient(project('member', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    // The per-run model pick is available to a member…
    await screen.findByTestId('composer-model-select');
    // …but not the owner-only service default editor.
    expect(screen.queryByTestId('service-default-model-select')).toBeNull();
  });
});

describe('ProjectDetailPage — multi-repo composer', () => {
  it('shows a repository selector and dispatches against the selected service', async () => {
    const services = [svc('svc_default', 'default'), svc('svc_web', 'web')];
    const { client, calls } = makeClient(project('owner', services));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('composer-service-select')).toBeTruthy());
    // The header collapses to a repo count once there is more than one repo.
    expect(screen.getByTestId('repo-count').textContent).toContain('2 repositories');

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'ship it' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    // Defaults to the 'default' service.
    expect(calls.serviceRuns[0]).toMatchObject({ sid: 'svc_default', input: { prompt: 'ship it' } });
  });
});

describe('ProjectDetailPage — model not configured (Feature A)', () => {
  it('disables the composer and links an admin to the Cluster page', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      modelConfigured: false,
    });
    renderPage(client, 'cluster-admin');

    await waitFor(() => expect(screen.getByTestId('model-not-configured')).toBeTruthy());
    // Input + Run button are disabled.
    expect((screen.getByTestId('run-input') as HTMLTextAreaElement).disabled).toBe(true);
    expect((screen.getByTestId('run-submit') as HTMLButtonElement).disabled).toBe(true);
    // Admin gets a link to configure it.
    expect(screen.getByTestId('model-config-link')).toBeTruthy();

    // Even forcing a submit dispatches nothing.
    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'do a thing' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await new Promise((r) => setTimeout(r, 0));
    expect(calls.serviceRuns).toHaveLength(0);
  });

  it('tells a non-admin to contact an administrator (no config link)', async () => {
    const { client } = makeClient(project('member', [svc('svc_default', 'default')]), {
      modelConfigured: false,
    });
    renderPage(client, 'project-admin');

    await waitFor(() => expect(screen.getByTestId('model-not-configured')).toBeTruthy());
    expect(screen.queryByTestId('model-config-link')).toBeNull();
    expect(screen.getByText(/contact a cluster administrator/i)).toBeTruthy();
  });

  it('keeps the composer usable with a neutral warning when the status check fails', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    (client as { listProjectModels?: unknown }).listProjectModels = async () => {
      throw new Error('network down');
    };
    renderPage(client, 'cluster-admin');

    // Neutral "couldn't verify" strip — NOT the blocking not-configured alert.
    await waitFor(() => expect(screen.getByTestId('model-unverified')).toBeTruthy());
    expect(screen.queryByTestId('model-not-configured')).toBeNull();
    // Composer stays enabled (the backend 409 is the backstop).
    expect((screen.getByTestId('run-input') as HTMLTextAreaElement).disabled).toBe(false);
    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'go' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
  });

  it('does not even fetch the model status for a viewer (enabled gating)', async () => {
    const { client } = makeClient(project('viewer', [svc('svc_default', 'default')]));
    const spy = vi.fn(async () => ({ models: [], env_fallback: true }));
    (client as { listProjectModels?: unknown }).listProjectModels = spy;
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('runs-empty')).toBeTruthy());
    expect(spy).not.toHaveBeenCalled();
  });
});

describe('ProjectDetailPage — zero-repo empty state', () => {
  it('replaces the composer with an empty state until a repository is added', async () => {
    const { client } = makeClient(project('owner', []));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('no-repo-empty')).toBeTruthy());
    expect(screen.queryByTestId('run-input')).toBeNull();
    expect(screen.getByText('No repositories yet')).toBeTruthy();
    // The owner can still attach the first repository.
    expect(screen.getByTestId('add-repo-trigger')).toBeTruthy();
  });
});

describe('ProjectDetailPage — viewer gating', () => {
  it('hides the composer, Settings and Add repository for a viewer', async () => {
    const { client } = makeClient(project('viewer', [svc('svc_default', 'default')]));
    renderPage(client);

    // The run list still renders (the empty state); the composer does not.
    await waitFor(() => expect(screen.getByTestId('runs-empty')).toBeTruthy());
    expect(screen.queryByTestId('run-input')).toBeNull();
    expect(screen.queryByTestId('project-settings-btn')).toBeNull();
    expect(screen.queryByTestId('add-repo-trigger')).toBeNull();
  });
});

describe('ProjectDetailPage — add repository', () => {
  it('opens the inline form and creates a service (owner)', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('add-repo-trigger')).toBeTruthy());
    fireEvent.click(screen.getByTestId('add-repo-trigger'));

    fireEvent.change(screen.getByTestId('add-repo-name'), { target: { value: 'web' } });
    fireEvent.change(screen.getByTestId('add-repo-url'), {
      target: { value: 'https://github.com/acme/web' },
    });
    fireEvent.click(screen.getByTestId('add-repo-submit'));

    await waitFor(() => expect(calls.services).toHaveLength(1));
    expect(calls.services[0]).toMatchObject({
      pid: 'p1',
      input: { name: 'web', repo_url: 'https://github.com/acme/web', git_mode: 'readonly' },
    });
  });

  it('blocks a Draft PR against a raw (git://) repo before submit', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('add-repo-trigger')).toBeTruthy());
    fireEvent.click(screen.getByTestId('add-repo-trigger'));

    fireEvent.change(screen.getByTestId('add-repo-name'), { target: { value: 'seed' } });
    fireEvent.change(screen.getByTestId('add-repo-url'), {
      target: { value: 'git://seed.internal/seed.git' },
    });
    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    fireEvent.click(screen.getByTestId('add-repo-submit'));

    await waitFor(() => expect(screen.getByText(/provider repository URL/i)).toBeTruthy());
    expect(calls.services).toHaveLength(0);
  });
});

describe('ProjectDetailPage — repo picker (Drone-style onboarding)', () => {
  it('lists provider repos and one click attaches with draft_pr + provider_repo_id', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    (client as { listProviderRepos?: unknown }).listProviderRepos = async () => [
      { id: 77, full_name: 'acme/frontend', description: 'SPA', default_branch: 'dev', private: true },
    ];
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('add-repo-trigger')).toBeTruthy());
    fireEvent.click(screen.getByTestId('add-repo-trigger'));

    const pick = await screen.findByTestId('repo-pick');
    expect(pick.getAttribute('data-repo')).toBe('acme/frontend');
    fireEvent.click(pick);

    await waitFor(() => expect(calls.services.length).toBe(1));
    expect(calls.services[0]!.input).toMatchObject({
      name: 'frontend',
      provider: 'gitea',
      owner_name: 'acme/frontend',
      default_branch: 'dev',
      git_mode: 'draft_pr',
      provider_repo_id: 77,
    });
  });

  it('falls back to manual URL entry when the listing 403s (unlinked account)', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    (client as { listProviderRepos?: unknown }).listProviderRepos = async () => {
      throw new Error('no gitea credential available — link your gitea account first');
    };
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('add-repo-trigger')).toBeTruthy());
    fireEvent.click(screen.getByTestId('add-repo-trigger'));

    await screen.findByTestId('repo-picker-error');
    // The manual form is still there as the fallback path.
    expect(screen.getByTestId('add-repo-url')).toBeTruthy();
  });
});
