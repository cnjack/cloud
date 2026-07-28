/*
 * ProjectDetailPage — M4 composer + role gating (blueprint §5):
 *  - single-repo project: composer, no repository selector; runs dispatch
 *    against the sole service (createServiceRun — the project shim is gone)
 *  - multi-repo project: composer shows a repository selector; runs dispatch
 *    against the selected service
 *  - zero-repo project: an empty state replaces the composer
 *  - viewer: no composer, no Settings, no "+ Add repository"
 *  - owner: "+ Add repository" opens a dialog that creates a service, with the
 *    draft_pr-needs-a-provider-repo validation inline
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type {
  CreateRunInput,
  CreateServiceInput,
  MemberRole,
  PluginRepositoryResource,
  Project,
  ProjectAutomationSpec,
  ProjectModel,
  ProjectPlugin,
  Run,
  Service,
	ServiceBranch,
  UpdateServiceInput,
} from '../api/types';
import { pickOption } from '../test/select';
import { qk } from '../api/queries';

import { ProjectDetailPage } from './ProjectDetailPage';

function svc(id: string, name: string): Service {
  return {
    id,
    project_id: 'p1',
    name,
    repo_kind: 'provider',
    provider: 'gitea',
    repo_owner_name: `acme/${name}`,
    repo_html_url: `https://git.example.test/acme/${name}`,
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
  serviceDeletes: string[];
  attachmentUploads: File[];
}

function makeClient(
  p: Project,
  opts: {
    modelConfigured?: boolean;
    models?: ProjectModel[];
    plugins?: ProjectPlugin[];
    pluginRepos?: PluginRepositoryResource[];
    projectAutomations?: ProjectAutomationSpec[];
	branches?: ServiceBranch[];
  } = {},
): { client: ApiClient; calls: Calls } {
  const calls: Calls = {
    serviceRuns: [],
    services: [],
    serviceUpdates: [],
    serviceDeletes: [],
    attachmentUploads: [],
  };
  const client: Partial<ApiClient> = {
    getProject: async () => p,
    listRuns: async () => [] as Run[],
    listProjectPlugins: async () => opts.plugins ?? [{
      id: 'plugin-gitea',
      project_id: p.id,
      provider: 'gitea',
      status: 'enabled',
      external_account: 'acme',
      scopes: ['repository:write'],
    }],
    listPluginRepositories: async () => opts.pluginRepos ?? [],
    // D21: the composer keys enable/disable off the project's models AND populates
    // its model select. Default configured via the env fallback (empty catalog).
    listProjectModels: async () => ({
      models: opts.models ?? [],
      env_fallback: opts.models ? false : (opts.modelConfigured ?? true),
    }),
    listProjectAutomations: async () => opts.projectAutomations ?? [],
	listServiceBranches: async () => opts.branches ?? [
		{ name: 'main', default: true, protected: true },
		{ name: 'release/2026.07', default: false },
	],
    createServiceRun: async (sid, input) => {
      calls.serviceRuns.push({ sid, input });
      return { id: 'r2', project_id: 'p1', service_id: sid, prompt: input.prompt, status: 'queued', created_at: '' } as Run;
    },
    uploadRunAttachment: async (_sid, file) => {
      calls.attachmentUploads.push(file);
      return {
        stage: {
          id: `stage-${calls.attachmentUploads.length}`,
          project_id: p.id,
          display_name: file.name,
          content_type: file.type,
          size_bytes: file.size,
          created_at: new Date().toISOString(),
          expires_at: new Date(Date.now() + 60_000).toISOString(),
        },
        upload_url: `/upload/${calls.attachmentUploads.length}`,
        expires_at: new Date(Date.now() + 60_000).toISOString(),
      };
    },
    createService: async (pid, input) => {
      calls.services.push({ pid, input });
      const created = svc('svc_new', input.name ?? 'default');
      p.services = [...(p.services ?? []), created];
      return created;
    },
    updateService: async (sid, input) => {
      calls.serviceUpdates.push({ sid, input });
      return { ...svc(sid, 'default'), default_model_id: input.default_model_id ?? null };
    },
    deleteService: async (sid) => {
      calls.serviceDeletes.push(sid);
      p.services = (p.services ?? []).filter((service) => service.id !== sid);
    },
  };
  return { client: client as ApiClient, calls };
}

function renderPage(
  client: ApiClient,
  role?: 'cluster-admin' | 'project-admin',
  initialEntry = '/projects/p1',
) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rendered = render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client} role={role}>
        <ToastProvider>
          <MemoryRouter initialEntries={[initialEntry]}>
            <LocationProbe />
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
  return { qc, ...rendered };
}

function LocationProbe() {
  const location = useLocation();
  const navigate = useNavigate();
  return (
    <>
      <output data-testid="workspace-location">{location.search}</output>
      <button type="button" data-testid="history-back" onClick={() => navigate(-1)}>Back</button>
    </>
  );
}

function ProjectRouteSwitchHarness() {
  const navigate = useNavigate();
  return (
    <>
      <button type="button" data-testid="switch-project" onClick={() => navigate('/projects/p2')}>
        Switch project
      </button>
      <Routes>
        <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
        <Route path="/runs/:runId" element={<div data-testid="switched-run" />} />
      </Routes>
    </>
  );
}

function renderSwitchablePage(client: ApiClient) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/projects/p1']}>
            <ProjectRouteSwitchHarness />
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('ProjectDetailPage — single-repo composer', () => {
  it('carries the terminal list snapshot into the run detail cache on navigation', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    const succeeded: Run = {
      id: 'run-terminal',
      project_id: 'p1',
      service_id: 'svc_default',
      prompt: 'finished task',
      status: 'succeeded',
      created_at: '2026-07-28T08:00:00Z',
      finished_at: '2026-07-28T08:05:00Z',
    };
    (client as { listRuns: ApiClient['listRuns'] }).listRuns = async () => [succeeded];

    const { qc } = renderPage(client);
    fireEvent.click(await screen.findByTestId('run-row'));

    expect(qc.getQueryData(qk.run(succeeded.id))).toEqual(succeeded);
    expect(await screen.findByTestId('run-page')).toBeTruthy();
  });

  it('opens the server-derived provider repository URL from the Service header', async () => {
    const service = svc('svc_default', 'default');
    const { client } = makeClient(project('owner', [service]));
    renderPage(client);

    const link = await screen.findByRole('link', { name: 'Open Gitea' });
    expect(link.getAttribute('href')).toBe(service.repo_html_url);
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noopener');
  });

  it('has no repository selector and dispatches against the sole service', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    expect(screen.queryByTestId('composer-service-select')).toBeNull();
    expect(screen.getByRole('tab', { name: 'Service settings' })).toBeTruthy();
    expect(screen.queryByTestId('project-settings-btn')).toBeNull();
    // The header shows the sole repo's identity (label + git-mode badge).
    expect(screen.getByText('acme/default')).toBeTruthy();
    expect(screen.getByTestId('git-mode-badge')).toBeTruthy();

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'do a thing' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]).toMatchObject({ sid: 'svc_default', input: { prompt: 'do a thing' } });
  });

  it('defaults to the Service branch and submits a selected discovered branch as base_branch', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    const branch = await screen.findByTestId('composer-branch-select');
    await waitFor(() => expect(branch.textContent).toContain('main'));
    await pickOption('composer-branch-select', 'release/2026.07');
    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'ship release' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]!.input).toMatchObject({
      prompt: 'ship release',
      base_branch: 'release/2026.07',
    });
  });
});

describe('ProjectDetailPage — project and service settings stay separate', () => {
  it('opens Project settings as a full workspace page, never as a modal inside Service settings', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    const projectSettings = await screen.findByTestId('project-settings-trigger');
    expect(projectSettings.closest('[data-testid="project-administration"]')).toBeTruthy();
    expect(projectSettings.closest('[data-testid="project-summary"]')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Project settings' }).textContent).toBe('');
    fireEvent.click(projectSettings);
    expect(await screen.findByTestId('project-settings-page')).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByTestId('workspace-location').textContent).toContain('view=project-settings');
    expect(screen.getByTestId('project-workspace-scroll').getAttribute('data-scroll-owner')).toBe('settings');
    expect(screen.getByTestId('project-settings-trigger').getAttribute('data-active')).not.toBeNull();
    expect(screen.queryByRole('tab', { name: 'Service settings' })).toBeNull();

    const projectCrumb = screen.getByTestId('project-settings-back');
    expect(projectCrumb.textContent).toBe('demo');
    expect(projectCrumb.querySelector('svg')).toBeNull();
    fireEvent.click(projectCrumb);

    fireEvent.click(screen.getByRole('tab', { name: 'Service settings' }));
    expect(await screen.findByText('Service default model')).toBeTruthy();
    expect(screen.queryByTestId('project-settings-btn')).toBeNull();
  });

  it('uses the outer shell placement for theme, account, and version in Project settings', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client, 'cluster-admin');

    fireEvent.click(await screen.findByTestId('project-settings-trigger'));
    expect(await screen.findByTestId('project-settings-page')).toBeTruthy();

    const footer = screen.getByTestId('project-rail-footer');
    expect(within(footer).getByText('orchestrator')).toBeTruthy();
    expect(within(footer).getByText('v0.0.79')).toBeTruthy();
    expect(within(footer).getByTestId('identity-chip')).toBeTruthy();
    expect(within(footer).queryByTestId('theme-toggle')).toBeNull();

    const utility = screen.getByTestId('project-utility-actions');
    expect(within(utility).getByTestId('theme-toggle')).toBeTruthy();
    expect(within(utility).queryByTestId('identity-chip')).toBeNull();
    expect(screen.queryByRole('link', { name: 'Cluster' })).toBeNull();
  });

  it('uses a shell-level section nav and restores the active settings section from the URL', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(
      client,
      undefined,
      '/projects/p1?service=svc_default&tab=tasks&view=project-settings&settings=models',
    );

    const settingsNav = await screen.findByRole('navigation', { name: 'Project settings sections' });
    expect(settingsNav.closest('[data-testid="project-workspace-scroll"]')).toBeNull();
    expect(within(settingsNav).getByTestId('tab-models').getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('heading', { name: 'Model access' })).toBeTruthy();
    expect(screen.queryByTestId('settings-name-input')).toBeNull();

    const scrollOwner = screen.getByTestId('project-workspace-scroll');
    scrollOwner.scrollTop = 240;
    fireEvent.click(within(settingsNav).getByTestId('tab-members'));

    await waitFor(() =>
      expect(screen.getByTestId('workspace-location').textContent).toContain('settings=members'),
    );
    await waitFor(() => expect(scrollOwner.scrollTop).toBe(0));
    expect(await screen.findByRole('heading', { name: 'Members and permissions' })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'Model access' })).toBeNull();

    fireEvent.click(within(settingsNav).getByTestId('tab-general'));

    await waitFor(() =>
      expect(screen.getByTestId('workspace-location').textContent).not.toContain('settings='),
    );
    expect(screen.getByRole('heading', { name: 'Settings' })).toBeTruthy();
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
    const perm = screen.getByTestId('composer-approval-toggle');
    expect(perm.textContent).toBe('Full access');

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
    await pickOption('composer-approval-toggle', 'Ask before actions');

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
    await pickOption('composer-approval-toggle', 'Ask before actions');
    // Change of heart: back to Full access — approval must NOT ride on the request.
    await pickOption('composer-approval-toggle', 'Full access');

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
    { id: 'm_gpt', name: 'GPT-4o', model_name: 'openai/gpt-4o', capabilities: { reasoning: true, tools: true, image: false } },
    { id: 'm_claude', name: 'Claude', model_name: 'anthropic/claude', capabilities: { reasoning: false, tools: true, image: false } },
  ];

  it('renders a model select from granted models and dispatches with the picked model_id', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    const select = await screen.findByTestId('composer-model-select');
    // "Service default" + the two granted models.
    fireEvent.click(select);
    const options = await screen.findAllByRole('option');
    expect(options).toHaveLength(3);
    expect(screen.getByRole('option', { name: 'GPT-4o' })).toBeTruthy();

    // Pick a specific model, then dispatch.
    fireEvent.click(screen.getByRole('option', { name: 'Claude' }));
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

  it('uploads attachments and dispatches branch, reasoning effort, and native goal mode together', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    await screen.findByTestId('composer-model-select');
    await pickOption('composer-model-select', 'GPT-4o');
    await pickOption('composer-effort-select', 'Effort · High');
    fireEvent.click(screen.getByTestId('composer-add-menu'));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Goal mode · Enable' }));
    expect(screen.getByTestId('composer-goal-active').textContent).toContain('Goal mode');
    const notes = new File(['acceptance criteria'], 'notes.txt', { type: 'text/plain' });
    fireEvent.change(screen.getByTestId('composer-attachment-input'), { target: { files: [notes] } });
    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'ship it' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.attachmentUploads).toEqual([notes]);
    expect(calls.serviceRuns[0]!.input).toMatchObject({
      prompt: 'ship it',
      base_branch: 'main',
      model_id: 'm_gpt',
      model_effort: 'high',
      goal_mode: true,
      attachment_stage_ids: ['stage-1'],
    });
  });

  it('rejects attachments that would push one Run over 100 MiB before upload', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    await screen.findByTestId('composer-attachment-input');
    const sizedFile = (name: string, size: number) => {
      const file = new File(['x'], name, { type: 'application/octet-stream' });
      Object.defineProperty(file, 'size', { value: size });
      return file;
    };
    const twentyFiveMiB = 25 * 1024 * 1024;
    const firstBatch = [0, 1, 2, 3].map((index) => sizedFile(`part-${index}.bin`, twentyFiveMiB));
    fireEvent.change(screen.getByTestId('composer-attachment-input'), { target: { files: firstBatch } });
    expect(screen.getAllByText(/part-\d\.bin/)).toHaveLength(4);

    const overflow = sizedFile('overflow.bin', 1);
    fireEvent.change(screen.getByTestId('composer-attachment-input'), { target: { files: [overflow] } });
    expect((await screen.findByRole('alert')).textContent).toContain('Attachments can total at most 100 MiB per Run.');
    expect(screen.queryByTitle('overflow.bin')).toBeNull();

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'ship it' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.attachmentUploads).toEqual(firstBatch);
  });

  it('does not offer a reasoning effort for a model that lacks the capability', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    await screen.findByTestId('composer-model-select');
    await pickOption('composer-model-select', 'Claude');
    expect(screen.queryByTestId('composer-effort-select')).toBeNull();
  });

  it('keeps the service default-model editor in Settings and PATCHes on change', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      models: grantedModels,
    });
    renderPage(client);

    await screen.findByTestId('run-input');
    expect(screen.queryByTestId('service-default-model-select')).toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'Service settings' }));
    await screen.findByTestId('service-default-model-select');
    await pickOption('service-default-model-select', 'GPT-4o');
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

  it('keeps model policy unverified when the model-grant lookup fails', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    (client as { listProjectModels?: unknown }).listProjectModels = async () => {
      throw new Error('network down');
    };
    renderPage(client);

    await screen.findByTestId('model-unverified');
    fireEvent.click(screen.getByRole('tab', { name: 'Service settings' }));

    expect(await screen.findByTestId('service-model-policy-unverified')).toBeTruthy();
    expect(screen.queryByTestId('service-model-policy-unavailable')).toBeNull();
  });
});

describe('ProjectDetailPage — multi-repo workspace', () => {
  it('uses the service rail as the only selector and dispatches against its active service', async () => {
    const services = [svc('svc_default', 'default'), svc('svc_web', 'web')];
    const { client, calls } = makeClient(project('owner', services));
    renderPage(client);

    await screen.findByTestId('run-input');
    expect(screen.queryByTestId('composer-service-select')).toBeNull();
    expect(screen.queryByTestId('repo-count')).toBeNull();

    fireEvent.click(screen.getByTestId('service-rail-svc_web'));

    await waitFor(() => expect((screen.getByTestId('run-submit') as HTMLButtonElement).disabled).toBe(false));
    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'ship it' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]).toMatchObject({ sid: 'svc_web', input: { prompt: 'ship it' } });
  });

  it('uses the service rail as the active execution target', async () => {
    const services = [svc('svc_default', 'default'), svc('svc_web', 'web')];
    const { client } = makeClient(project('owner', services));
    renderPage(client);

    const railTarget = await screen.findByTestId('service-rail-svc_web');
    fireEvent.click(railTarget);

    expect(railTarget.getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('heading', { name: 'web' })).toBeTruthy();
  });

  it('makes the active service and workspace tab deep-linkable', async () => {
    const services = [svc('svc_default', 'default'), svc('svc_web', 'web')];
    const { client } = makeClient(project('owner', services));
    renderPage(client);

    await screen.findByTestId('run-input');
    await waitFor(() => expect(screen.getByTestId('workspace-location').textContent).toContain('service=svc_default'));
    fireEvent.click(screen.getByTestId('service-rail-svc_web'));
    await waitFor(() => expect(screen.getByTestId('workspace-location').textContent).toContain('service=svc_web'));

    fireEvent.click(screen.getByRole('tab', { name: 'Automations' }));
    await waitFor(() => expect(screen.getByTestId('workspace-location').textContent).toContain('tab=automations'));
  });

  it('never carries a selected service into another project route', async () => {
    const p1Services = [svc('svc_p1_default', 'default'), svc('svc_p1_web', 'web')];
    const p1 = project('owner', p1Services);
    const p2Service = { ...svc('svc_p2_default', 'default'), id: 'svc_p2_default', project_id: 'p2' };
    const p2 = { ...project('owner', [p2Service]), id: 'p2', name: 'second project' };
    const { client, calls } = makeClient(p1);
    const models = vi.fn(async (id: string) => ({
      models:
        id === 'p2'
          ? [{ id: 'm_p2', name: 'P2 model', model_name: 'provider/p2', capabilities: { reasoning: false, tools: true, image: false } }]
          : [{ id: 'm_p1', name: 'P1 model', model_name: 'provider/p1', capabilities: { reasoning: false, tools: true, image: false } }],
      env_fallback: false,
    }));
    (client as { getProject?: unknown }).getProject = async (id: string) => (id === 'p2' ? p2 : p1);
    (client as { listRuns?: unknown }).listRuns = async () => [];
    (client as { listProjectModels?: unknown }).listProjectModels = models;
    renderSwitchablePage(client);

    const firstProjectTarget = await screen.findByTestId('service-rail-svc_p1_web');
    fireEvent.click(firstProjectTarget);
    expect(firstProjectTarget.getAttribute('aria-pressed')).toBe('true');
    await screen.findByTestId('composer-model-select');
    await pickOption('composer-model-select', 'P1 model');

    fireEvent.click(screen.getByTestId('switch-project'));
    await screen.findByTestId('service-rail-svc_p2_default');
    await waitFor(() => expect(models).toHaveBeenCalledWith('p2'));

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'work in p2' } });
    fireEvent.click(screen.getByTestId('run-submit'));
    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    expect(calls.serviceRuns[0]?.sid).toBe('svc_p2_default');
    expect(calls.serviceRuns[0]?.input.model_id).toBeUndefined();
  });
});

describe('ProjectDetailPage — workspace sections', () => {
  it('renders the unified Project Automation list without legacy schedule surfaces', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      projectAutomations: [{
        automation: {
          id: 'auto-1', service_id: 'svc_default', name: 'Gitea PR automatic review',
          trigger_kind: 'scm', prompt_template: 'Review security and regressions.',
          enabled: true, ignore_jcode: true, last_error: 'Automation model is unavailable.',
          created_at: '', updated_at: '',
        },
        scm: { branch: 'main' },
        actions: [{ event_family: 'pull_request', action: 'opened' }],
      }],
    });
    renderPage(client);

    await screen.findByTestId('run-input');
    fireEvent.click(screen.getByRole('tab', { name: 'Automations' }));

    expect(await screen.findByText('Gitea PR automatic review')).toBeTruthy();
    expect(screen.getByText('Review security and regressions.')).toBeTruthy();
    expect(screen.getByText(/No model could be selected when this event was received/)).toBeTruthy();
  });

  it('links the Automation primary action to the independent editor route', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await screen.findByTestId('run-input');
    fireEvent.click(screen.getByRole('tab', { name: 'Automations' }));
    const link = await screen.findByRole('link', { name: /New automation/i });
    expect(link.getAttribute('href')).toBe('/projects/p1/automations/new?service=svc_default');
  });

  it('makes the guided PR review setup the primary GitHub Automation entry', async () => {
    const github = { ...svc('svc_github', 'review-lab'), provider: 'github' as const };
    const { client } = makeClient(project('owner', [github]));
    renderPage(client);

    await screen.findByTestId('run-input');
    fireEvent.click(screen.getByRole('tab', { name: 'Automations' }));
    const link = await screen.findByRole('link', { name: 'Review pull requests' });
    expect(link.getAttribute('href')).toBe(
      '/projects/p1/automations/new?service=svc_github&preset=review',
    );
  });

  it('resets the workspace scroll when moving between Tasks and Automations', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await screen.findByTestId('run-input');
    const scrollSurface = screen.getByTestId('project-workspace-scroll');
    Object.defineProperty(scrollSurface, 'scrollTop', {
      configurable: true,
      writable: true,
      value: 240,
    });
    fireEvent.click(screen.getByRole('tab', { name: 'Automations' }));

    expect(scrollSurface.scrollTop).toBe(0);
  });

  it('keeps Automations active while changing the selected service', async () => {
    const services = [svc('svc_default', 'default'), svc('svc_web', 'web')];
    const { client } = makeClient(project('owner', services));
    renderPage(client);

    await screen.findByTestId('run-input');
    fireEvent.click(screen.getByRole('tab', { name: 'Automations' }));
    await screen.findByTestId('project-automations-panel');
    fireEvent.click(screen.getByTestId('service-rail-svc_web'));

    expect(screen.getByRole('tab', { name: 'Automations' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByRole('heading', { name: 'web' })).toBeTruthy();
  });

  it('loads Service Kanban bindings but hides the action without an enabled JType Plugin', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    const retiredLinks = vi.fn(async () => []);
    (client as { listProjectBoardLinks?: unknown }).listProjectBoardLinks = retiredLinks;
    renderPage(client);

    await screen.findByTestId('run-input');
    expect(retiredLinks).toHaveBeenCalledWith('p1');
    expect(screen.queryByTestId('project-kanban-btn')).toBeNull();
    expect(screen.queryByTestId('project-kanban-retry')).toBeNull();
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
  it('shows one focused first-service onboarding state without inactive workspace chrome', async () => {
    const { client } = makeClient(project('owner', []));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('no-repo-empty')).toBeTruthy());
    expect(screen.queryByTestId('run-input')).toBeNull();
    expect(screen.queryByTestId('runs-empty')).toBeNull();
    expect(screen.queryByRole('tab')).toBeNull();
    expect(screen.queryByTestId('add-repo-trigger')).toBeNull();
    expect(await screen.findByTestId('empty-add-service')).toBeTruthy();
  });

  it('keeps Add service actionable when no Git plugin is connected', async () => {
    const { client } = makeClient(project('owner', []), { plugins: [] });
    renderPage(client);

    fireEvent.click(await screen.findByTestId('empty-add-service'));

    expect(await screen.findByRole('dialog', { name: 'Add service' })).toBeTruthy();
    expect(screen.getByTestId('add-repo-needs-plugin')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Configure project plugins' })).toBeTruthy();
  });

  it('renders Add service as a dialog directly from the URL', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client, undefined, '/projects/p1?service=svc_default&tab=tasks&add=service');

    expect(await screen.findByRole('dialog', { name: 'Add service' })).toBeTruthy();
    expect(await screen.findByTestId('repo-picker')).toBeTruthy();
  });

  it('opens first-service setup as a dialog over the focused onboarding state', async () => {
    const { client } = makeClient(project('owner', []));
    renderPage(client);

    fireEvent.click(await screen.findByTestId('empty-add-service'));

    expect(await screen.findByRole('dialog', { name: 'Add service' })).toBeTruthy();
    expect(screen.getByTestId('add-service-dialog')).toBeTruthy();
    expect(screen.getByTestId('no-repo-empty')).toBeTruthy();
    expect(screen.queryByTestId('runs-empty')).toBeNull();
    expect(screen.getByTestId('repo-picker')).toBeTruthy();

    fireEvent.click(within(screen.getByTestId('add-service-dialog')).getByRole('button', { name: 'Cancel' }));
    expect(await screen.findByTestId('no-repo-empty')).toBeTruthy();
    expect(screen.queryByRole('dialog', { name: 'Add service' })).toBeNull();
  });

  it('activates a newly attached first service instead of remaining in the empty workspace', async () => {
    const { client } = makeClient(project('owner', []), {
      pluginRepos: [{ id: '77', full_name: 'acme/frontend', description: 'SPA', default_branch: 'main', private: false }],
    });
    renderPage(client);

    fireEvent.click(await screen.findByTestId('empty-add-service'));
    fireEvent.click(await screen.findByTestId('repo-pick'));

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    expect(screen.getByRole('heading', { name: 'frontend' })).toBeTruthy();
  });

  it('cascade-deletes a service and lands on the empty Service state', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    fireEvent.click(await screen.findByRole('button', { name: 'Delete default service' }));

    await waitFor(() => expect(calls.serviceDeletes).toEqual(['svc_default']));
    expect(await screen.findByTestId('no-repo-empty')).toBeTruthy();
  });

  it('lands on the first remaining service after deleting the active service', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const services = [
      svc('svc_first', 'first'),
      svc('svc_default', 'default'),
      svc('svc_active', 'active'),
    ];
    const { client, calls } = makeClient(project('owner', services));
    renderPage(client, undefined, '/projects/p1?service=svc_active&tab=settings');

    fireEvent.click(await screen.findByRole('button', { name: 'Delete active service' }));

    await waitFor(() => expect(calls.serviceDeletes).toEqual(['svc_active']));
    await waitFor(() => {
      expect(screen.getByTestId('workspace-location').textContent).toContain('service=svc_first');
      expect(screen.getByTestId('workspace-location').textContent).toContain('tab=tasks');
    });
    expect(screen.getByRole('heading', { name: 'first' })).toBeTruthy();
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

  it('does not query or misrepresent service automations for a viewer', async () => {
    const { client } = makeClient(project('viewer', [svc('svc_default', 'default')]));
    renderPage(client);

    await screen.findByTestId('runs-empty');
    fireEvent.click(screen.getByRole('tab', { name: 'Automations' }));

    expect(await screen.findByTestId('project-automations-panel')).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'New Automation' })).toBeNull();
  });
});

describe('ProjectDetailPage — add repository', () => {
  const githubPlugin: ProjectPlugin = {
    id: 'plugin-github',
    project_id: 'p1',
    provider: 'github',
    status: 'enabled',
    external_account: 'cnjack',
    scopes: ['contents:write'],
  };
  const repository: PluginRepositoryResource = {
    id: 77,
    full_name: 'cnjack/codespace_demo',
    description: 'E2E repository',
    default_branch: 'main',
    private: false,
  };

  it('creates a Service from an enabled Project Plugin repository', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      plugins: [githubPlugin],
      pluginRepos: [repository],
    });
    renderPage(client);

    fireEvent.click(await screen.findByTestId('add-repo-trigger'));
    expect(screen.queryByTestId('add-repo-url')).toBeNull();
    const pick = await screen.findByTestId('repo-pick');
    expect(pick.getAttribute('data-repo')).toBe('cnjack/codespace_demo');
    fireEvent.click(pick);

    await waitFor(() => expect(calls.services).toHaveLength(1));
    expect(calls.services[0]).toMatchObject({
      pid: 'p1',
      input: {
        name: 'codespace_demo',
        installation_id: 'plugin-github',
        provider_repo_id: '77',
        git_mode: 'draft_pr',
      },
    });
    expect('repo_url' in calls.services[0]!.input).toBe(false);
  });

  it('opens Add Service as a dialog without leaving the Automation workspace', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      plugins: [githubPlugin],
      pluginRepos: [repository],
    });
    renderPage(client, undefined, '/projects/p1?service=svc_default&tab=automations');

    fireEvent.click(await screen.findByTestId('add-repo-trigger'));
    expect(await screen.findByRole('dialog', { name: 'Add service' })).toBeTruthy();
    expect(await screen.findByTestId('repo-picker')).toBeTruthy();
    await waitFor(() => expect(screen.getByTestId('workspace-location').textContent).toContain('tab=automations'));
  });

  it('closes Add Service when browser history removes the add URL state', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      plugins: [githubPlugin],
      pluginRepos: [repository],
    });
    renderPage(client, undefined, '/projects/p1?service=svc_default&tab=automations');

    fireEvent.click(await screen.findByTestId('add-repo-trigger'));
    expect(await screen.findByTestId('repo-picker')).toBeTruthy();
    fireEvent.click(screen.getByTestId('history-back'));

    await waitFor(() => expect(screen.getByTestId('workspace-location').textContent).toContain('tab=automations'));
    await waitFor(() => expect(screen.queryByTestId('repo-picker')).toBeNull());
  });

  it('lets a Member use the same Project Plugin without a personal credential', async () => {
    const { client, calls } = makeClient(project('member', [svc('svc_default', 'default')]), {
      plugins: [githubPlugin],
      pluginRepos: [repository],
    });
    renderPage(client);

    fireEvent.click(await screen.findByTestId('add-repo-trigger'));
    fireEvent.click(await screen.findByTestId('repo-pick'));
    await waitFor(() => expect(calls.services).toHaveLength(1));
    expect(calls.services[0]!.input.installation_id).toBe('plugin-github');
  });

  it('shows a fail-visible Plugin requirement when none is enabled', async () => {
    const { client } = makeClient(project('owner', [svc('svc_default', 'default')]), {
      plugins: [],
    });
    renderPage(client);

    expect(await screen.findByTestId('add-repo-needs-plugin')).toBeTruthy();
    expect(screen.getByTestId('add-repo-trigger')).toBeTruthy();
    expect(screen.getByTestId('add-repo-needs-plugin').textContent).toMatch(/Project Plugin/i);
  });

  it('does not fetch Project Plugins for a viewer', async () => {
    const { client } = makeClient(project('viewer', [svc('svc_default', 'default')]));
    const spy = vi.fn(async () => [] as ProjectPlugin[]);
    (client as { listProjectPlugins?: unknown }).listProjectPlugins = spy;
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('runs-empty')).toBeTruthy());
    expect(spy).not.toHaveBeenCalled();
  });
});
