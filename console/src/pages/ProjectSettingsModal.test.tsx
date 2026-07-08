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
  CreateIntegrationInput,
  CreateKanbanLinkInput,
  Integration,
  KanbanLink,
  Member,
  Project,
  UpdateProjectInput,
  UserSearchResult,
} from '../api/types';
import { ApiError } from '../api/client';
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
  it('edits the numeric limits and PATCHes them (no provider allowlist editor)', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    // The provider_allowlist editor was removed (D20/F5): git-host policy is now a
    // cluster allowlist + integrations.
    expect(screen.queryByTestId('settings-allowlist')).toBeNull();

    fireEvent.change(screen.getByTestId('settings-max-concurrent'), { target: { value: '3' } });
    fireEvent.change(screen.getByTestId('settings-run-timeout'), { target: { value: '600' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.input).toEqual({
      max_concurrent_runs: 3,
      run_timeout_secs: 600,
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
      injected_env: { FOO: 'bar' },
    });
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    expect((screen.getByTestId('settings-max-concurrent') as HTMLInputElement).value).toBe('2');
    expect((screen.getByTestId('settings-run-timeout') as HTMLInputElement).value).toBe('900');
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

describe('ProjectSettingsModal — Kanban tab (F6 / D25)', () => {
  interface KanbanCtl {
    creates: { projectId: string; input: CreateKanbanLinkInput }[];
    updates: { projectId: string; linkId: string; token: string }[];
    deletes: { projectId: string; linkId: string }[];
    links: KanbanLink[];
  }

  function kanbanClient(
    project: Project,
    seed?: KanbanLink[],
  ): { client: ApiClient; kctl: KanbanCtl } {
    const kctl: KanbanCtl = {
      creates: [],
      updates: [],
      deletes: [],
      links: seed ?? [
        {
          id: 'kl-1', workspace_id: 'ws', board_ref: 'jcloud-dev',
          project_id: project.id, service_id: 'svc_default', trigger_column: 'ai',
          done_column: 'done', enabled: true, token_set: true,
          credential_status: 'per_link', created_at: '2026-01-01T00:00:00Z',
        },
      ],
    };
    const client: Partial<ApiClient> = {
      updateProject: async (_id, input) => ({ ...project, ...input }) as Project,
      listProjectKanbanLinks: async () => [...kctl.links],
      createProjectKanbanLink: async (projectId, input) => {
        kctl.creates.push({ projectId, input });
        const link: KanbanLink = {
          id: 'kl-new', workspace_id: input.workspace_id, board_ref: input.board_ref,
          project_id: projectId, service_id: input.service_id, trigger_column: input.trigger_column,
          done_column: input.done_column, enabled: true, token_set: !!input.token,
          credential_status: input.token ? 'per_link' : 'missing',
          created_at: '2026-01-02T00:00:00Z',
        };
        kctl.links.push(link);
        return link;
      },
      updateProjectKanbanLinkToken: async (projectId, linkId, token) => {
        kctl.updates.push({ projectId, linkId, token });
        const l = kctl.links.find((x) => x.id === linkId)!;
        l.token_set = !!token;
        l.credential_status = token ? 'per_link' : 'missing';
        return { ...l };
      },
      deleteProjectKanbanLink: async (projectId, linkId) => {
        kctl.deletes.push({ projectId, linkId });
        kctl.links = kctl.links.filter((l) => l.id !== linkId);
      },
    };
    return { client: client as ApiClient, kctl };
  }

  it('owner sees the Kanban tab', () => {
    const { client } = kanbanClient(baseProject());
    renderModal(client, baseProject());
    expect(screen.getByTestId('tab-kanban')).toBeTruthy();
  });

  it('a non-owner does not see the Kanban tab (owner-only management)', () => {
    const { client } = kanbanClient(baseProject());
    renderModal(client, baseProject({ role: 'member' }));
    expect(screen.queryByTestId('tab-kanban')).toBeNull();
  });

  it('lists a project link with its token badge and creates one with a write-only token', async () => {
    const project = baseProject();
    const { client, kctl } = kanbanClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    // Existing link + its "own token" badge render.
    await waitFor(() => expect(screen.getByText('ws / jcloud-dev')).toBeTruthy());
    expect(screen.getByTestId('kanban-cred-kl-1').textContent).toBe('own token');

    // Fill and submit the add form (service select is populated from the project's
    // own services).
    fireEvent.change(screen.getByTestId('kanban-link-service'), { target: { value: 'svc_default' } });
    fireEvent.change(screen.getByTestId('kanban-link-workspace'), { target: { value: 'ws2' } });
    fireEvent.change(screen.getByTestId('kanban-link-board'), { target: { value: 'b2' } });
    fireEvent.change(screen.getByTestId('kanban-link-trigger'), { target: { value: 'ai' } });
    fireEvent.change(screen.getByTestId('kanban-link-token'), { target: { value: 'jtype-pat' } });
    fireEvent.click(screen.getByTestId('kanban-link-add'));

    await waitFor(() => expect(kctl.creates).toHaveLength(1));
    expect(kctl.creates[0]).toEqual({
      projectId: 'p1',
      input: {
        workspace_id: 'ws2', board_ref: 'b2', service_id: 'svc_default',
        trigger_column: 'ai', done_column: undefined, token: 'jtype-pat',
      },
    });
  });

  it('deletes a project link', async () => {
    const project = baseProject();
    const { client, kctl } = kanbanClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    await waitFor(() => expect(screen.getByTestId('kanban-link-delete-kl-1')).toBeTruthy());
    fireEvent.click(screen.getByTestId('kanban-link-delete-kl-1'));

    await waitFor(() => expect(kctl.deletes).toEqual([{ projectId: 'p1', linkId: 'kl-1' }]));
  });

  it('renders the three credential states — missing is a loud error badge (P1)', async () => {
    const project = baseProject();
    const mk = (id: string, cred: KanbanLink['credential_status']): KanbanLink => ({
      id, workspace_id: 'ws-' + id, board_ref: 'b-' + id,
      project_id: project.id, service_id: 'svc_default', trigger_column: 'ai',
      enabled: true, token_set: cred === 'per_link', credential_status: cred,
      created_at: '2026-01-01T00:00:00Z',
    });
    const { client } = kanbanClient(project, [
      mk('a', 'per_link'), mk('b', 'cluster_fallback'), mk('c', 'missing'),
    ]);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    await waitFor(() => expect(screen.getByTestId('kanban-cred-a')).toBeTruthy());
    expect(screen.getByTestId('kanban-cred-a').textContent).toBe('own token');
    expect(screen.getByTestId('kanban-cred-a').getAttribute('data-state')).toBe('per_link');
    expect(screen.getByTestId('kanban-cred-b').textContent).toBe('cluster token');
    expect(screen.getByTestId('kanban-cred-b').getAttribute('data-state')).toBe('cluster_fallback');
    // The dead link screams: explicit error copy + the error-styled state attr.
    expect(screen.getByTestId('kanban-cred-c').textContent).toBe('no credential — set a token');
    expect(screen.getByTestId('kanban-cred-c').getAttribute('data-state')).toBe('missing');
  });

  it('rotates and clears a link token via the write-only Update token editor (P2)', async () => {
    const project = baseProject();
    const { client, kctl } = kanbanClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    await waitFor(() => expect(screen.getByTestId('kanban-token-edit-kl-1')).toBeTruthy());

    // Rotate: open the editor, type a new token, save.
    fireEvent.click(screen.getByTestId('kanban-token-edit-kl-1'));
    fireEvent.change(screen.getByTestId('kanban-token-input-kl-1'), {
      target: { value: 'rotated-pat' },
    });
    fireEvent.click(screen.getByTestId('kanban-token-save-kl-1'));
    await waitFor(() => expect(kctl.updates).toHaveLength(1));
    expect(kctl.updates[0]).toEqual({ projectId: 'p1', linkId: 'kl-1', token: 'rotated-pat' });

    // Clear: empty submit sends "" (server clears back to the cluster fallback).
    await waitFor(() => expect(screen.getByTestId('kanban-token-edit-kl-1')).toBeTruthy());
    fireEvent.click(screen.getByTestId('kanban-token-edit-kl-1'));
    fireEvent.click(screen.getByTestId('kanban-token-save-kl-1'));
    await waitFor(() => expect(kctl.updates).toHaveLength(2));
    expect(kctl.updates[1]).toEqual({ projectId: 'p1', linkId: 'kl-1', token: '' });
  });
});

describe('ProjectSettingsModal — Integrations tab (D19 / F5)', () => {
  interface IntegCtl {
    creates: { projectId: string; input: CreateIntegrationInput }[];
    list: Integration[];
    createErr?: ApiError;
  }

  function integClient(project: Project, opts: { seed?: Integration[]; createErr?: ApiError } = {}) {
    const ictl: IntegCtl = { creates: [], list: opts.seed ?? [], createErr: opts.createErr };
    const client: Partial<ApiClient> = {
      updateProject: async (_id, input) => ({ ...project, ...input }) as Project,
      listIntegrations: async () => [...ictl.list],
      createIntegration: async (projectId, input) => {
        if (ictl.createErr) throw ictl.createErr;
        ictl.creates.push({ projectId, input });
        const integ: Integration = {
          id: 'integ-new', project_id: projectId, name: input.name || 'default',
          provider: input.provider, host: input.host, cred_type: 'pat',
          bot_username: `${input.provider}-bot`, token_set: true,
          created_at: '2026-01-02T00:00:00Z', updated_at: '2026-01-02T00:00:00Z',
        };
        ictl.list.push(integ);
        return integ;
      },
    };
    return { client: client as ApiClient, ictl };
  }

  it('owner sees the Integrations tab; a member does not', () => {
    const { client } = integClient(baseProject());
    renderModal(client, baseProject());
    expect(screen.getByTestId('tab-integrations')).toBeTruthy();

    const { client: c2 } = integClient(baseProject({ role: 'member' }));
    renderModal(c2, baseProject({ role: 'member' }));
    expect(screen.queryAllByTestId('tab-integrations').length).toBe(1); // only the owner one above
  });

  it('creates an integration with a write-only token', async () => {
    const project = baseProject();
    const { client, ictl } = integClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-integrations'));
    await waitFor(() => expect(screen.getByTestId('integrations-empty')).toBeTruthy());

    fireEvent.change(screen.getByTestId('integration-host'), { target: { value: 'gitea.example.com' } });
    fireEvent.change(screen.getByTestId('integration-token'), { target: { value: 'bot-pat' } });
    fireEvent.click(screen.getByTestId('integration-add'));

    await waitFor(() => expect(ictl.creates).toHaveLength(1));
    expect(ictl.creates[0]).toEqual({
      projectId: 'p1',
      input: { name: undefined, provider: 'gitea', host: 'gitea.example.com', token: 'bot-pat' },
    });
  });

  it('surfaces a host_not_allowed error readably', async () => {
    const project = baseProject();
    const err = new ApiError(400, 'the git host is not allowed', {
      error: { code: 'host_not_allowed', message: 'the git host is not allowed' },
    });
    const { client } = integClient(project, { createErr: err });
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-integrations'));
    fireEvent.change(screen.getByTestId('integration-host'), { target: { value: 'evil.example.com' } });
    fireEvent.change(screen.getByTestId('integration-token'), { target: { value: 'x' } });
    fireEvent.click(screen.getByTestId('integration-add'));

    // The typed server message reaches the toast verbatim (fail-visible).
    await waitFor(() => expect(screen.getByText('the git host is not allowed')).toBeTruthy());
  });
});
