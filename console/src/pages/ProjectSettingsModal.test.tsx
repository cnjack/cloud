/*
 * ProjectSettingsModal — M4. Covers:
 *  - General tab PATCH (a rename is the only project-level edit now — repo
 *    config lives on services; save sends { name } only when it changed)
 *  - the Delete flow behind a confirm step
 *  - the Members tab: roster render, add-by-search, role change, remove
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type {
  AddMemberInput,
  Member,
  Project,
  UpdateProjectInput,
  UserSearchResult,
} from '../api/types';
import { ProjectSettingsModal } from './ProjectSettingsModal';

function baseProject(overrides: Partial<Project> = {}): Project {
  return {
    id: 'p1',
    name: 'demo',
    created_at: '2026-07-07T00:00:00Z',
    role: 'owner',
    services: [
      {
        id: 'svc_default',
        project_id: 'p1',
        name: 'default',
        repo_kind: 'provider',
        provider: 'gitea',
        repo_owner_name: 'acme/demo',
        default_branch: 'main',
        git_mode: 'readonly',
        created_at: '2026-07-07T00:00:00Z',
      },
    ],
    ...overrides,
  };
}

interface Ctl {
  patches: { id: string; input: UpdateProjectInput }[];
  deletes: string[];
  added: AddMemberInput[];
  removed: string[];
}

const USERS: UserSearchResult[] = [
  { id: 'u_ada', display_name: 'Ada Lovelace', is_cluster_admin: true },
  { id: 'u_grace', display_name: 'Grace Hopper', is_cluster_admin: false },
];

function makeClient(project: Project): { client: ApiClient; ctl: Ctl } {
  const ctl: Ctl = { patches: [], deletes: [], added: [], removed: [] };
  const members: Member[] = [
    { user_id: 'u_ada', role: 'owner', display_name: 'Ada Lovelace', is_cluster_admin: true },
  ];
  const client: Partial<ApiClient> = {
    updateProject: async (id, input) => {
      ctl.patches.push({ id, input });
      return { ...project, ...input } as Project;
    },
    deleteProject: async (id) => {
      ctl.deletes.push(id);
    },
    listMembers: async () => [...members],
    searchUsers: async (q) =>
      USERS.filter((u) => u.display_name.toLowerCase().includes(q.toLowerCase())),
    addMember: async (_pid, input: AddMemberInput) => {
      ctl.added.push(input);
      const u = USERS.find((x) => x.id === input.user_id)!;
      const m: Member = {
        user_id: u.id,
        role: input.role,
        display_name: u.display_name,
        is_cluster_admin: u.is_cluster_admin,
      };
      const i = members.findIndex((x) => x.user_id === u.id);
      if (i >= 0) members[i] = m;
      else members.push(m);
      return m;
    },
    removeMember: async (_pid, userId) => {
      ctl.removed.push(userId);
    },
  };
  return { client: client as ApiClient, ctl };
}

function renderModal(client: ApiClient, project: Project, onDeleted = vi.fn(), onClose = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <ProjectSettingsModal open project={project} onClose={onClose} onDeleted={onDeleted} />
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return { onDeleted, onClose };
}

describe('ProjectSettingsModal — General (PATCH)', () => {
  it('sends only the changed name (a rename is the only project-level edit)', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    const { onClose } = renderModal(client, project);

    fireEvent.change(screen.getByTestId('settings-name-input'), { target: { value: 'renamed' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.id).toBe('p1');
    expect(ctl.patches[0]!.input).toEqual({ name: 'renamed' });
    expect(onClose).toHaveBeenCalled();
  });

  it('does not PATCH at all when the name is unchanged', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    const { onClose } = renderModal(client, project);

    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(ctl.patches).toHaveLength(0);
  });

  it('pre-fills the name and carries no repo config fields', () => {
    const project = baseProject();
    const { client } = makeClient(project);
    renderModal(client, project);
    expect((screen.getByTestId('settings-name-input') as HTMLInputElement).value).toBe('demo');
    // Branch / git-mode / readonly repo moved to services on the project page.
    expect(screen.queryByTestId('settings-branch-input')).toBeNull();
    expect(screen.queryByTestId('git-mode-control')).toBeNull();
    expect(screen.queryByTestId('settings-repo')).toBeNull();
  });
});

describe('ProjectSettingsModal — Guardrails', () => {
  it('edits the numeric limits and provider allowlist and PATCHes them', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.change(screen.getByTestId('settings-max-concurrent'), { target: { value: '3' } });
    fireEvent.change(screen.getByTestId('settings-run-timeout'), { target: { value: '600' } });
    fireEvent.click(screen.getByTestId('allowlist-gitea'));
    fireEvent.click(screen.getByTestId('allowlist-raw'));
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.input).toEqual({
      max_concurrent_runs: 3,
      run_timeout_secs: 600,
      provider_allowlist: ['gitea', 'raw'],
    });
  });

  it('adds an injected env variable and sends it in the PATCH', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('env-add'));
    fireEvent.change(screen.getByTestId('env-key-0'), { target: { value: 'COMPANY_TOKEN' } });
    fireEvent.change(screen.getByTestId('env-value-0'), { target: { value: 'abc' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.input).toEqual({ injected_env: { COMPANY_TOKEN: 'abc' } });
  });

  it('blocks Save and shows an error for a reserved injected env key', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('env-add'));
    fireEvent.change(screen.getByTestId('env-key-0'), { target: { value: 'RUN_TOKEN' } });
    fireEvent.change(screen.getByTestId('env-value-0'), { target: { value: 'evil' } });

    expect(screen.getByTestId('env-error')).toBeTruthy();
    fireEvent.click(screen.getByTestId('project-settings-save'));

    // Save is blocked — no PATCH is issued for a reserved key.
    await new Promise((r) => setTimeout(r, 20));
    expect(ctl.patches).toHaveLength(0);
  });

  it('pre-fills existing guardrails and a rename-only save omits them', async () => {
    const project = baseProject({
      max_concurrent_runs: 2,
      run_timeout_secs: 900,
      provider_allowlist: ['gitea'],
      injected_env: { FOO: 'bar' },
    });
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    expect((screen.getByTestId('settings-max-concurrent') as HTMLInputElement).value).toBe('2');
    expect((screen.getByTestId('settings-run-timeout') as HTMLInputElement).value).toBe('900');
    expect((screen.getByTestId('allowlist-gitea') as HTMLInputElement).checked).toBe(true);
    expect((screen.getByTestId('env-key-0') as HTMLInputElement).value).toBe('FOO');

    // Change only the name — the unchanged guardrails must NOT be in the payload.
    fireEvent.change(screen.getByTestId('settings-name-input'), { target: { value: 'renamed' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.input).toEqual({ name: 'renamed' });
  });

  it('hides the guardrails editor for a non-owner', () => {
    const project = baseProject({ role: 'member' });
    const { client } = makeClient(project);
    renderModal(client, project);
    expect(screen.queryByTestId('guardrails')).toBeNull();
  });
});

describe('ProjectSettingsModal — Delete', () => {
  it('requires a confirm step, then deletes and fires onDeleted', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    const { onDeleted } = renderModal(client, project);

    fireEvent.click(screen.getByTestId('project-delete'));
    expect(screen.getByTestId('delete-confirm')).toBeTruthy();
    expect(ctl.deletes).toHaveLength(0);

    fireEvent.click(screen.getByTestId('project-delete-confirm'));
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
    expect(ctl.deletes).toEqual(['p1']);
  });
});

describe('ProjectSettingsModal — Members tab', () => {
  it('lists members and adds one via search', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-members'));
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeTruthy());

    fireEvent.change(screen.getByTestId('member-search-input'), { target: { value: 'grace' } });
    await waitFor(() => expect(screen.getByTestId('member-search-result')).toBeTruthy());
    fireEvent.click(screen.getByTestId('member-search-result'));

    await waitFor(() => expect(ctl.added).toHaveLength(1));
    expect(ctl.added[0]).toMatchObject({ user_id: 'u_grace', role: 'member' });
  });

  it('changes a member role and removes a member', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-members'));
    await waitFor(() => expect(screen.getByTestId('member-role-select')).toBeTruthy());

    fireEvent.change(screen.getByTestId('member-role-select'), { target: { value: 'viewer' } });
    await waitFor(() => expect(ctl.added).toHaveLength(1));
    expect(ctl.added[0]).toMatchObject({ user_id: 'u_ada', role: 'viewer' });

    fireEvent.click(screen.getByTestId('member-remove'));
    await waitFor(() => expect(ctl.removed).toEqual(['u_ada']));
  });
});
