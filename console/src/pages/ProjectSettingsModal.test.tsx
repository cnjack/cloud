import { describe, expect, it, vi } from 'vitest';
import { useState } from 'react';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ApiError, type ApiClient } from '../api/client';
import { ToastProvider } from '../components/Toast';
import type {
  AddMemberInput,
  ApiKey,
  CreateApiKeyInput,
  Member,
  Project,
  UpdateProjectInput,
  UserSearchResult,
} from '../api/types';
import {
  ProjectSettingsPage,
  ProjectSettingsSubnav,
  resolveProjectSettingsSection,
  type ProjectSettingsSectionId,
} from './ProjectSettingsModal';

function baseProject(overrides: Partial<Project> = {}): Project {
  return {
    id: 'p1',
    name: 'demo',
    created_at: '2026-07-07T00:00:00Z',
    role: 'owner',
    services: [{
      id: 'svc_default', project_id: 'p1', name: 'default', repo_kind: 'provider',
      provider: 'gitea', repo_owner_name: 'acme/demo', default_branch: 'main',
      git_mode: 'readonly', created_at: '2026-07-07T00:00:00Z',
    }],
    ...overrides,
  };
}

interface Ctl {
  patches: { id: string; input: UpdateProjectInput }[];
  deletes: string[];
  added: AddMemberInput[];
  removed: string[];
}

const users: UserSearchResult[] = [
  { id: 'u_ada', display_name: 'Ada Lovelace', is_cluster_admin: true },
  { id: 'u_grace', display_name: 'Grace Hopper', is_cluster_admin: false },
];

function makeClient(project: Project): { client: ApiClient; ctl: Ctl } {
  const ctl: Ctl = { patches: [], deletes: [], added: [], removed: [] };
  const members: Member[] = [{ user_id: 'u_ada', role: 'owner', display_name: 'Ada Lovelace', is_cluster_admin: true }];
  const client: Partial<ApiClient> = {
    updateProject: async (id, input) => {
      ctl.patches.push({ id, input });
      return { ...project, ...input } as Project;
    },
    deleteProject: async (id) => { ctl.deletes.push(id); },
    listMembers: async () => [...members],
    searchUsers: async (query) => users.filter((user) => user.display_name.toLowerCase().includes(query.toLowerCase())),
    addMember: async (_projectID, input: AddMemberInput) => {
      ctl.added.push(input);
      const user = users.find((candidate) => candidate.id === input.user_id)!;
      const next: Member = { user_id: user.id, role: input.role, display_name: user.display_name, is_cluster_admin: user.is_cluster_admin };
      const index = members.findIndex((member) => member.user_id === user.id);
      if (index >= 0) members[index] = next;
      else members.push(next);
      return next;
    },
    removeMember: async (_projectID, userID) => { ctl.removed.push(userID); },
  };
  return { client: client as ApiClient, ctl };
}

function renderSettings(client: ApiClient, project: Project, initialSection: ProjectSettingsSectionId = 'general', onDeleted = vi.fn()) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const settingsClient = Object.assign({
    listMembers: async () => [],
    searchUsers: async () => [],
    listApiKeys: async () => [],
    listProjectPlugins: async () => [],
  }, client) as ApiClient;
  render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={settingsClient}>
          <ToastProvider>
            <ProjectSettingsHarness project={project} initialSection={initialSection} onDeleted={onDeleted} />
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return { onDeleted };
}

function ProjectSettingsHarness({ project, initialSection, onDeleted }: { project: Project; initialSection: ProjectSettingsSectionId; onDeleted: () => void }) {
  const [activeSection, setActiveSection] = useState<ProjectSettingsSectionId>(initialSection);
  const canManage = (project.role ?? 'owner') === 'owner';
  return <>
    <ProjectSettingsSubnav canManage={canManage} activeSection={activeSection} onSelect={setActiveSection} />
    <ProjectSettingsPage project={project} activeSection={activeSection} onDeleted={onDeleted} />
  </>;
}

describe('Project settings General', () => {
  it('keeps only one page section visible and normalizes retired settings links', () => {
    const project = baseProject();
    const { client } = makeClient(project);
    renderSettings(client, project);
    expect(screen.getByTestId('project-settings-page')).toBeTruthy();
    expect(screen.getByTestId('tab-general').getAttribute('aria-current')).toBe('page');
    expect(screen.queryByTestId('tab-integrations')).toBeNull();
    expect(screen.queryByTestId('tab-kanban')).toBeNull();
    fireEvent.click(screen.getByTestId('tab-members'));
    expect(screen.getByTestId('tab-members').getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('heading', { name: 'Members and permissions' })).toBeTruthy();
    expect(resolveProjectSettingsSection('integrations', true)).toBe('general');
    expect(resolveProjectSettingsSection('kanban', true)).toBe('general');
    expect(resolveProjectSettingsSection('models', false)).toBe('models');
  });

  it('PATCHes only a changed project name', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderSettings(client, project);
    fireEvent.change(screen.getByTestId('settings-name-input'), { target: { value: 'renamed' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));
    await waitFor(() => expect(ctl.patches).toEqual([{ id: 'p1', input: { name: 'renamed' } }]));
  });

  it('does not PATCH unchanged general settings and carries no repository controls', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderSettings(client, project);
    expect((screen.getByTestId('settings-name-input') as HTMLInputElement).value).toBe('demo');
    expect(screen.queryByTestId('settings-branch-input')).toBeNull();
    expect(screen.queryByTestId('git-mode-control')).toBeNull();
    expect(screen.queryByTestId('settings-repo')).toBeNull();
    fireEvent.click(screen.getByTestId('project-settings-save'));
    await waitFor(() => expect(screen.getByTestId('project-settings-save')).toBeTruthy());
    expect(ctl.patches).toHaveLength(0);
  });

  it('pre-fills the project name without resurrecting repository fields', () => {
    const project = baseProject();
    const { client } = makeClient(project);
    renderSettings(client, project);
    expect((screen.getByTestId('settings-name-input') as HTMLInputElement).value).toBe('demo');
    expect(screen.queryByTestId('settings-repo')).toBeNull();
  });
});

describe('Project settings Guardrails', () => {
  it('PATCHes numeric limits without a provider allowlist editor', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderSettings(client, project);
    expect(screen.queryByTestId('settings-allowlist')).toBeNull();
    fireEvent.change(screen.getByTestId('settings-max-concurrent'), { target: { value: '3' } });
    fireEvent.change(screen.getByTestId('settings-run-timeout'), { target: { value: '600' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));
    await waitFor(() => expect(ctl.patches[0]?.input).toEqual({ max_concurrent_runs: 3, run_timeout_secs: 600 }));
  });

  it('PATCHes injected env and blocks a reserved key', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderSettings(client, project);
    fireEvent.click(screen.getByTestId('env-add'));
    fireEvent.change(screen.getByTestId('env-key-0'), { target: { value: 'COMPANY_TOKEN' } });
    fireEvent.change(screen.getByTestId('env-value-0'), { target: { value: 'abc' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));
    await waitFor(() => expect(ctl.patches[0]?.input).toEqual({ injected_env: { COMPANY_TOKEN: 'abc' } }));

    const second = baseProject();
    const made = makeClient(second);
    renderSettings(made.client, second);
    fireEvent.click(screen.getAllByTestId('env-add')[1]!);
    fireEvent.change(screen.getAllByTestId('env-key-0')[1]!, { target: { value: 'RUN_TOKEN' } });
    expect(screen.getAllByTestId('env-error')[0]).toBeTruthy();
    fireEvent.click(screen.getAllByTestId('project-settings-save')[1]!);
    expect(made.ctl.patches).toHaveLength(0);
  });

  it('blocks a reserved injected env key before issuing a PATCH', () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderSettings(client, project);
    fireEvent.click(screen.getByTestId('env-add'));
    fireEvent.change(screen.getByTestId('env-key-0'), { target: { value: 'RUN_TOKEN' } });
    expect(screen.getByTestId('env-error')).toBeTruthy();
    fireEvent.click(screen.getByTestId('project-settings-save'));
    expect(ctl.patches).toHaveLength(0);
  });

  it('omits unchanged existing guardrails on a rename and hides them from members', async () => {
    const project = baseProject({ max_concurrent_runs: 2, run_timeout_secs: 900, injected_env: { FOO: 'bar' } });
    const { client, ctl } = makeClient(project);
    renderSettings(client, project);
    expect((screen.getByTestId('settings-max-concurrent') as HTMLInputElement).value).toBe('2');
    expect((screen.getByTestId('env-key-0') as HTMLInputElement).value).toBe('FOO');
    fireEvent.change(screen.getByTestId('settings-name-input'), { target: { value: 'renamed' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));
    await waitFor(() => expect(ctl.patches[0]?.input).toEqual({ name: 'renamed' }));

    const member = baseProject({ role: 'member' });
    const made = makeClient(member);
    renderSettings(made.client, member);
    expect(screen.getAllByTestId('guardrails')).toHaveLength(1);
  });

  it('hides guardrail controls for a member', () => {
    const project = baseProject({ role: 'member' });
    const { client } = makeClient(project);
    renderSettings(client, project);
    expect(screen.queryByTestId('guardrails')).toBeNull();
  });
});

describe('Project settings Delete and Members', () => {
  it('requires confirmation before deleting and notifies the route owner', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    const { onDeleted } = renderSettings(client, project);
    fireEvent.click(screen.getByTestId('project-delete'));
    expect(screen.getByTestId('delete-confirm')).toBeTruthy();
    fireEvent.click(screen.getByTestId('project-delete-confirm'));
    await waitFor(() => expect(ctl.deletes).toEqual(['p1']));
    expect(onDeleted).toHaveBeenCalled();
  });

  it('lists, adds, changes, and removes Members', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderSettings(client, project);
    fireEvent.click(screen.getByTestId('tab-members'));
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeTruthy());
    fireEvent.change(screen.getByTestId('member-search-input'), { target: { value: 'grace' } });
    await waitFor(() => expect(screen.getByTestId('member-search-result')).toBeTruthy());
    fireEvent.click(screen.getByTestId('member-search-result'));
    await waitFor(() => expect(ctl.added[0]).toMatchObject({ user_id: 'u_grace', role: 'member' }));
    fireEvent.click(screen.getAllByTestId('member-role-select')[0]!);
    fireEvent.click(await screen.findByRole('option', { name: 'viewer' }));
    await waitFor(() => expect(ctl.added[1]).toMatchObject({ user_id: 'u_ada', role: 'viewer' }));
    fireEvent.click(screen.getAllByTestId('member-remove')[0]!);
    await waitFor(() => expect(ctl.removed).toEqual(['u_ada']));
  });

  it('changes an existing member role', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderSettings(client, project);
    fireEvent.click(screen.getByTestId('tab-members'));
    await waitFor(() => expect(screen.getByTestId('member-role-select')).toBeTruthy());
    fireEvent.click(screen.getByTestId('member-role-select'));
    fireEvent.click(await screen.findByRole('option', { name: 'viewer' }));
    await waitFor(() => expect(ctl.added).toEqual([expect.objectContaining({ user_id: 'u_ada', role: 'viewer' })]));
  });
});

describe('Project settings API keys', () => {
  interface ApiKeyCtl { creates: { projectId: string; input: CreateApiKeyInput }[]; revokes: { projectId: string; keyId: string }[]; keys: ApiKey[] }
  function apiKeyClient(project: Project, options: { seed?: ApiKey[]; createError?: ApiError } = {}) {
    const ctl: ApiKeyCtl = { creates: [], revokes: [], keys: options.seed ?? [] };
    const client: Partial<ApiClient> = {
      updateProject: async (_id, input) => ({ ...project, ...input }) as Project,
      listApiKeys: async () => [...ctl.keys],
      createApiKey: async (projectId, input) => {
        if (options.createError) throw options.createError;
        ctl.creates.push({ projectId, input });
        const key: ApiKey = { id: 'ak-new', project_id: projectId, name: input.name, prefix: 'jck_a1b2', created_at: '2026-01-02T00:00:00Z', last_used_at: null, revoked_at: null };
        ctl.keys.push(key);
        return { ...key, key: 'jck_a1b2c3d4e5f6' };
      },
      revokeApiKey: async (projectId, keyId) => {
        ctl.revokes.push({ projectId, keyId });
        ctl.keys.find((key) => key.id === keyId)!.revoked_at = '2026-01-03T00:00:00Z';
      },
    };
    return { client: client as ApiClient, ctl };
  }

  it('is owner-only and lists active/revoked status correctly', async () => {
    const project = baseProject();
    const seed: ApiKey[] = [
      { id: 'ak-1', project_id: project.id, name: 'ci', prefix: 'jck_a1b2', created_at: '2026-01-01T00:00:00Z', last_used_at: null, revoked_at: null },
      { id: 'ak-2', project_id: project.id, name: 'old', prefix: 'jck_c3d4', created_at: '2026-01-01T00:00:00Z', last_used_at: null, revoked_at: '2026-01-02T00:00:00Z' },
    ];
    const made = apiKeyClient(project, { seed });
    renderSettings(made.client, project);
    expect(screen.getByTestId('tab-apikeys')).toBeTruthy();
    fireEvent.click(screen.getByTestId('tab-apikeys'));
    await waitFor(() => expect(screen.getByTestId('apikey-ak-1')).toBeTruthy());
    expect(screen.getByTestId('apikey-status-ak-1').textContent).toBe('active');
    expect(screen.getByTestId('apikey-status-ak-2').textContent).toBe('revoked');
    expect(screen.queryByTestId('apikey-revoke-ak-2')).toBeNull();
  });

  it('does not offer API key management to a member', () => {
    const project = baseProject({ role: 'member' });
    const made = apiKeyClient(project);
    renderSettings(made.client, project);
    expect(screen.queryByTestId('tab-apikeys')).toBeNull();
  });

  it('shows the revoked status and hides its revoke action', async () => {
    const project = baseProject();
    const made = apiKeyClient(project, { seed: [{ id: 'ak-old', project_id: project.id, name: 'old', prefix: 'jck_old', created_at: '2026-01-01T00:00:00Z', last_used_at: null, revoked_at: '2026-01-02T00:00:00Z' }] });
    renderSettings(made.client, project);
    fireEvent.click(screen.getByTestId('tab-apikeys'));
    await waitFor(() => expect(screen.getByTestId('apikey-status-ak-old').textContent).toBe('revoked'));
    expect(screen.queryByTestId('apikey-revoke-ak-old')).toBeNull();
  });

  it('creates, reveals once, copies, dismisses, and revokes a key', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    const project = baseProject();
    const made = apiKeyClient(project);
    renderSettings(made.client, project);
    fireEvent.click(screen.getByTestId('tab-apikeys'));
    await waitFor(() => expect(screen.getByTestId('apikeys-empty')).toBeTruthy());
    fireEvent.change(screen.getByTestId('apikey-name'), { target: { value: 'ci-bot' } });
    fireEvent.click(screen.getByTestId('apikey-create'));
    await waitFor(() => expect(made.ctl.creates).toEqual([{ projectId: 'p1', input: { name: 'ci-bot' } }]));
    expect(screen.getByTestId('apikey-reveal-value').textContent).toBe('jck_a1b2c3d4e5f6');
    fireEvent.click(screen.getByTestId('apikey-reveal-copy'));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('jck_a1b2c3d4e5f6'));
    fireEvent.click(screen.getByTestId('apikey-reveal-dismiss'));
    await waitFor(() => expect(screen.queryByTestId('apikey-reveal-value')).toBeNull());
    fireEvent.click(screen.getByTestId('apikey-revoke-ak-new'));
    await waitFor(() => expect(made.ctl.revokes).toEqual([{ projectId: 'p1', keyId: 'ak-new' }]));
  });

  it('surfaces a create failure', async () => {
    const project = baseProject();
    const made = apiKeyClient(project, { createError: new ApiError(400, 'name is required', { error: { code: 'bad_request', message: 'name is required' } }) });
    renderSettings(made.client, project);
    fireEvent.click(screen.getByTestId('tab-apikeys'));
    fireEvent.change(screen.getByTestId('apikey-name'), { target: { value: 'ci-bot' } });
    fireEvent.click(screen.getByTestId('apikey-create'));
    await waitFor(() => expect(screen.getByText('name is required')).toBeTruthy());
  });
});

describe('Project settings Plugins surface', () => {
  it('replaces legacy Git and Kanban settings with one Plugins section', async () => {
    const project = baseProject();
    const { client } = makeClient(project);
    renderSettings(client, project, 'plugins');
    expect(screen.getByTestId('tab-plugins')).toBeTruthy();
    expect(screen.queryByTestId('tab-integrations')).toBeNull();
    expect(screen.queryByTestId('tab-kanban')).toBeNull();
    expect(await screen.findByTestId('project-plugins-panel')).toBeTruthy();
  });

  it('keeps Plugins available to a member while normalizing retired links', async () => {
    const project = baseProject({ role: 'member' });
    const { client } = makeClient(project);
    renderSettings(client, project, 'plugins');
    expect(await screen.findByTestId('project-plugins-panel')).toBeTruthy();
    expect(resolveProjectSettingsSection('plugins', false)).toBe('plugins');
    expect(resolveProjectSettingsSection('integrations', false)).toBe('general');
  });
});
