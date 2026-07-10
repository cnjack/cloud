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
  Integration,
  MemberRole,
  Project,
  ProjectModel,
  ProviderRepo,
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
  opts: {
    modelConfigured?: boolean;
    models?: ProjectModel[];
    // D19 / F5: the project's integrations + what their bot token can list.
    integrations?: Integration[];
    integrationRepos?: ProviderRepo[];
  } = {},
): { client: ApiClient; calls: Calls } {
  const calls: Calls = { serviceRuns: [], services: [], serviceUpdates: [] };
  const client: Partial<ApiClient> = {
    getProject: async () => p,
    listRuns: async () => [] as Run[],
    // D19 / F5: loaded eagerly for member+ (the add-repo entry gates on it).
    listIntegrations: async () => opts.integrations ?? [],
    listIntegrationRepos: async () => opts.integrationRepos ?? [],
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

describe('ProjectDetailPage — session-only composer (D22)', () => {
  it('always sends session:true and labels the submit button "Send"', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    // The headless opt-in is gone — this composer only ever starts sessions.
    expect(screen.queryByTestId('composer-session-toggle')).toBeNull();
    expect(screen.getByTestId('run-submit').textContent).toContain('Send');

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'chat with me' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]!.input).toMatchObject({ prompt: 'chat with me', session: true });
  });
});

describe('ProjectDetailPage — permission mode (F8b)', () => {
  it('defaults to Full access: a session omits permission_mode', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    // The permission pill is always shown (every run is a session) and defaults
    // to Full access.
    const perm = screen.getByTestId('composer-approval-toggle') as HTMLSelectElement;
    expect(perm.value).toBe('');

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'chat' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]!.input).toMatchObject({ session: true });
    expect('permission_mode' in calls.serviceRuns[0]!.input).toBe(false);
  });

  it('sends permission_mode:"approval" when "Ask before actions" is picked', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    fireEvent.change(screen.getByTestId('composer-approval-toggle'), {
      target: { value: 'approval' },
    });

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'careful chat' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]!.input).toMatchObject({
      prompt: 'careful chat',
      session: true,
      permission_mode: 'approval',
    });
  });

  it('switching back to Full access drops permission_mode from the request', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    const perm = screen.getByTestId('composer-approval-toggle');
    fireEvent.change(perm, { target: { value: 'approval' } });
    // Change of heart: back to Full access — approval must NOT ride on the request.
    fireEvent.change(perm, { target: { value: '' } });

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'go' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect('permission_mode' in calls.serviceRuns[0]!.input).toBe(false);
    // …but it is still a session.
    expect(calls.serviceRuns[0]!.input).toMatchObject({ session: true });
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

describe('ProjectDetailPage — member builds via integration (D19 / F5)', () => {
  const integ: Integration = {
    id: 'integ1',
    project_id: 'p1',
    name: 'default',
    provider: 'gitea',
    host: 'gitea.example.com',
    cred_type: 'pat',
    bot_username: 'jcloud-bot',
    token_set: true,
    created_at: '',
    updated_at: '',
  };
  const widgetRepo: ProviderRepo = {
    id: 42,
    full_name: 'acme/widget',
    description: 'Widget service',
    default_branch: 'main',
    private: true,
  };

  it('member + integration: "+ Add repository" appears and a pick submits integration_id (deadlock regression)', async () => {
    // Regression for the F5 review finding: the integrations query must load
    // EAGERLY for a member — the old code enabled it only while the add-repo card
    // was open, but the card's only entry button was itself gated on the loaded
    // data, so a member could never see "+ Add repository" at all.
    const { client, calls } = makeClient(project('member', [svc('svc_default', 'default')]), {
      integrations: [integ],
      integrationRepos: [widgetRepo],
    });
    renderPage(client);

    // The entry renders once the integration list loads (never with the old gating).
    const trigger = await screen.findByTestId('add-repo-trigger');
    fireEvent.click(trigger);

    // The picker lists the integration bot's repos; the member path has NO
    // owner-only manual URL fallback.
    const pick = await screen.findByTestId('repo-pick');
    expect(pick.getAttribute('data-repo')).toBe('acme/widget');
    expect(screen.queryByTestId('add-repo-url')).toBeNull();

    fireEvent.click(pick);
    await waitFor(() => expect(calls.services).toHaveLength(1));
    expect(calls.services[0]!.input).toMatchObject({
      name: 'widget',
      owner_name: 'acme/widget',
      integration_id: 'integ1',
      default_branch: 'main',
      git_mode: 'draft_pr',
      provider_repo_id: 42,
    });
    // The provider is the integration's to decide server-side — never sent.
    expect('provider' in calls.services[0]!.input).toBe(false);
  });

  it('member + no integration: no entry, a fail-visible hint instead', async () => {
    const { client } = makeClient(project('member', [svc('svc_default', 'default')]), {
      integrations: [],
    });
    renderPage(client);

    await screen.findByTestId('add-repo-needs-integration');
    expect(screen.queryByTestId('add-repo-trigger')).toBeNull();
    expect(
      screen.getByTestId('add-repo-needs-integration').textContent,
    ).toMatch(/integration/i);
  });

  it('owner regression: entry shows with zero integrations and Direct source keeps the manual URL form', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      integrations: [],
    });
    renderPage(client);

    // Owner entry never depends on integrations existing.
    await waitFor(() => expect(screen.getByTestId('add-repo-trigger')).toBeTruthy());
    expect(screen.queryByTestId('add-repo-needs-integration')).toBeNull();
    fireEvent.click(screen.getByTestId('add-repo-trigger'));

    // Direct (owner-credential) mode: the manual URL fallback is present and works.
    fireEvent.change(screen.getByTestId('add-repo-name'), { target: { value: 'web' } });
    fireEvent.change(screen.getByTestId('add-repo-url'), {
      target: { value: 'https://github.com/acme/web' },
    });
    fireEvent.click(screen.getByTestId('add-repo-submit'));
    await waitFor(() => expect(calls.services).toHaveLength(1));
    expect('integration_id' in calls.services[0]!.input).toBe(false);
  });

  it('owner + integration: the Source select offers Direct plus the integration', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      integrations: [integ],
    });
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('add-repo-trigger')).toBeTruthy());
    fireEvent.click(screen.getByTestId('add-repo-trigger'));

    const source = (await screen.findByTestId('repo-source-select')) as HTMLSelectElement;
    // Direct + the one integration; the owner defaults to Direct.
    expect(source.options).toHaveLength(2);
    expect(source.value).toBe('');
    expect(source.options[1]!.textContent).toContain('jcloud-bot');
  });

  it('does not fetch integrations for a viewer (enabled gating)', async () => {
    const { client } = makeClient(project('viewer', [svc('svc_default', 'default')]));
    const spy = vi.fn(async () => [] as Integration[]);
    (client as { listIntegrations?: unknown }).listIntegrations = spy;
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('runs-empty')).toBeTruthy());
    expect(spy).not.toHaveBeenCalled();
  });
});
