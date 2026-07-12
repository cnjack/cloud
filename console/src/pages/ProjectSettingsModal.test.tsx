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
  ApiKey,
  CreateApiKeyInput,
  CreateIntegrationInput,
  CreateKanbanLinkInput,
  Integration,
  JtypeBoard,
  JtypeWorkspace,
  KanbanLink,
  Member,
  Project,
  SystemInfo,
  UpdateProjectInput,
  UserSearchResult,
} from '../api/types';
import { ApiError } from '../api/client';
import { ProjectSettingsPage } from './ProjectSettingsModal';
import { pickOption } from '../test/select';

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

function renderModal(client: ApiClient, project: Project, onDeleted = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const settingsClient = Object.assign({
    listMembers: async () => [],
    searchUsers: async () => [],
    listIntegrations: async () => [],
    listProjectKanbanLinks: async () => [],
    listJtypeWorkspaces: async () => [],
    listJtypeBoards: async () => [],
    listApiKeys: async () => [],
    listProjectModels: async () => ({ models: [], env_fallback: false }),
    getSystem: async () => ({
      version: { version: '', commit: '' },
      capacity: { max_concurrent_runs: 0, running: 0, queued: 0, scheduling: 0 },
      guardrails: { run_timeout_seconds: 0, job_ttl_seconds: 0 },
      provider: { gitea_enabled: false, gitea_url: '' },
      runner: { image: '' },
      namespace: '',
      launcher: '',
      kanban: { enabled: true },
    }),
  }, client) as ApiClient;
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={settingsClient}>
        <ToastProvider>
          <ProjectSettingsPage project={project} onDeleted={onDeleted} />
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return { onDeleted };
}

describe('ProjectSettingsPage — full-page layout and General PATCH', () => {
  it('renders every Project settings section together without dialog chrome', () => {
    const project = baseProject();
    const { client } = makeClient(project);
    renderModal(client, project);

    expect(screen.getByTestId('project-settings-page')).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
    for (const heading of ['General', 'Members and permissions', 'Git integrations', 'Kanban links', 'Model access', 'Project API keys']) {
      expect(screen.getByRole('heading', { name: heading })).toBeTruthy();
    }
  });

  it('sends only the changed name (a rename is the only project-level edit)', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.change(screen.getByTestId('settings-name-input'), { target: { value: 'renamed' } });
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.id).toBe('p1');
    expect(ctl.patches[0]!.input).toEqual({ name: 'renamed' });
  });

  it('does not PATCH at all when the name is unchanged', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(screen.getByTestId('project-settings-save')).toBeTruthy());
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

    await pickOption('member-role-select', 'viewer');
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

  // A minimal /system snapshot whose only relevant field is kanban.enabled (D27):
  // the KanbanPanel gates its add form on it.
  function sysInfo(kanbanEnabled: boolean): SystemInfo {
    return {
      version: { version: '', commit: '' },
      capacity: { max_concurrent_runs: 0, running: 0, queued: 0, scheduling: 0 },
      guardrails: { run_timeout_seconds: 0, job_ttl_seconds: 0 },
      provider: { gitea_enabled: false, gitea_url: '' },
      runner: { image: '' },
      namespace: '',
      launcher: '',
      kanban: { enabled: kanbanEnabled },
    };
  }

  // D29: fake jtype discovery data for the cascading pickers.
  const WORKSPACES: JtypeWorkspace[] = [
    { id: 'ws_team', name: 'My Team' },
    { id: 'ws_solo', name: 'Personal' },
  ];
  const BOARDS: Record<string, JtypeBoard[]> = {
    ws_team: [
      {
        id: 'b_ab12cd34', ref: 'jtype.board', title: 'jtype',
        columns: [
          { key: 'todo', name: 'To do' },
          { key: 'ai', name: 'AI' },
          { key: 'done', name: 'Done' },
        ],
      },
    ],
    ws_solo: [
      {
        id: 'b_solo0001', ref: 'personal.board', title: 'Personal',
        columns: [{ key: 'inbox', name: 'Inbox' }, { key: 'run', name: 'Run' }],
      },
    ],
  };

  function kanbanClient(
    project: Project,
    seed?: KanbanLink[],
    kanbanEnabled = true,
    opts: { discoveryErr?: ApiError; boardsErr?: ApiError } = {},
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
      getSystem: async () => sysInfo(kanbanEnabled),
      listProjectKanbanLinks: async () => [...kctl.links],
      listJtypeWorkspaces: async () => {
        if (opts.discoveryErr) throw opts.discoveryErr;
        return WORKSPACES.map((w) => ({ ...w }));
      },
      listJtypeBoards: async (_projectId, workspaceId) => {
        const err = opts.discoveryErr ?? opts.boardsErr;
        if (err) throw err;
        return (BOARDS[workspaceId] ?? []).map((b) => ({ ...b, columns: b.columns.map((c) => ({ ...c })) }));
      },
      createProjectKanbanLink: async (projectId, input) => {
        kctl.creates.push({ projectId, input });
        const link: KanbanLink = {
          id: 'kl-new', workspace_id: input.workspace_id, board_ref: input.board_ref,
          project_id: projectId, service_id: input.service_id, trigger_column: input.trigger_column,
          done_column: input.done_column, enabled: true, token_set: !!input.token,
          credential_status: input.token ? 'per_link' : 'missing',
          board_status: input.token ? 'ok' : 'unvalidated',
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

  it('lists a project link with its token badge and creates one via the cascading pickers', async () => {
    const project = baseProject();
    const { client, kctl } = kanbanClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    // Existing link + its "own token" badge render.
    await waitFor(() => expect(screen.getByText('ws / jcloud-dev')).toBeTruthy());
    expect(screen.getByTestId('kanban-cred-kl-1').textContent).toBe('own token');

    // Service comes from the project's own services.
    await pickOption('kanban-link-service', 'default');

    // Workspace → board → trigger/done cascade. The board select is disabled
    // until a workspace is picked; the column selects until a board is picked.
    expect((screen.getByTestId('kanban-link-board-select') as HTMLButtonElement).disabled).toBe(true);
    await pickOption('kanban-link-workspace-select', 'My Team');

    // Board options populate from the chosen workspace.
    await waitFor(() =>
      expect((screen.getByTestId('kanban-link-board-select') as HTMLButtonElement).disabled).toBe(false),
    );
    expect((screen.getByTestId('kanban-link-trigger-select') as HTMLButtonElement).disabled).toBe(true);
    await pickOption('kanban-link-board-select', 'jtype');

    // Column selects now list the chosen board's columns (by human name).
    await waitFor(() =>
      expect((screen.getByTestId('kanban-link-trigger-select') as HTMLButtonElement).disabled).toBe(false),
    );
    await pickOption('kanban-link-trigger-select', 'AI');
    await pickOption('kanban-link-done-select', 'Done');
    fireEvent.change(screen.getByTestId('kanban-link-token'), { target: { value: 'jtype-pat' } });
    fireEvent.click(screen.getByTestId('kanban-link-add'));

    await waitFor(() => expect(kctl.creates).toHaveLength(1));
    // The picker submits the board's relativePath ref (server canonicalizes it),
    // and the column KEYS (not their display names).
    expect(kctl.creates[0]).toEqual({
      projectId: 'p1',
      input: {
        workspace_id: 'ws_team', board_ref: 'jtype.board', service_id: 'svc_default',
        trigger_column: 'ai', done_column: 'done', token: 'jtype-pat',
      },
    });
  });

  it('auto-falls-back to manual entry (with the server message) when discovery errors', async () => {
    const project = baseProject();
    const err = new ApiError(409, 'the cluster jtype integration is not configured', {
      error: { code: 'kanban_not_configured', message: 'the cluster jtype integration is not configured' },
    });
    const { client, kctl } = kanbanClient(project, undefined, true, { discoveryErr: err });
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));

    // The typed discovery error surfaces (fail-visible) and the free-text fields
    // appear so the owner can still enter values by hand.
    await waitFor(() =>
      expect(screen.getByTestId('kanban-link-discovery-error').textContent).toBe(
        'the cluster jtype integration is not configured',
      ),
    );
    await waitFor(() => expect(screen.getByTestId('kanban-link-workspace')).toBeTruthy());

    // A manually entered board name/path is submitted verbatim (server resolves it).
    await pickOption('kanban-link-service', 'default');
    fireEvent.change(screen.getByTestId('kanban-link-workspace'), { target: { value: 'ws_team' } });
    fireEvent.change(screen.getByTestId('kanban-link-board'), { target: { value: 'jtype' } });
    fireEvent.change(screen.getByTestId('kanban-link-trigger'), { target: { value: 'ai' } });
    fireEvent.click(screen.getByTestId('kanban-link-add'));

    await waitFor(() => expect(kctl.creates).toHaveLength(1));
    expect(kctl.creates[0]).toEqual({
      projectId: 'p1',
      input: {
        workspace_id: 'ws_team', board_ref: 'jtype', service_id: 'svc_default',
        trigger_column: 'ai', done_column: undefined, token: undefined,
      },
    });
  });

  it('surfaces a BOARD-list discovery error (not just workspaces) and falls back to manual', async () => {
    const project = baseProject();
    const err = new ApiError(400, 'jtype workspace was not found', {
      error: { code: 'workspace_not_found', message: 'jtype workspace was not found' },
    });
    // Workspaces list fine; only the board fetch errors — the previously-silent path.
    const { client } = kanbanClient(project, undefined, true, { boardsErr: err });
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    // Wait for the workspace options to load (select enabled) — workspaces list fine.
    await waitFor(() =>
      expect((screen.getByTestId('kanban-link-workspace-select') as HTMLButtonElement).disabled).toBe(false),
    );
    expect(screen.queryByTestId('kanban-link-discovery-error')).toBeNull();

    // Picking a workspace fires the board fetch, which errors → fail-visible fallback
    // to manual entry (the bug was a silent empty board dropdown reading as "no boards").
    await pickOption('kanban-link-workspace-select', 'My Team');
    await waitFor(() =>
      expect(screen.getByTestId('kanban-link-discovery-error').textContent).toBe(
        'jtype workspace was not found',
      ),
    );
    await waitFor(() => expect(screen.getByTestId('kanban-link-board')).toBeTruthy());
  });

  it('surfaces board_status fail-visibly: unvalidated (amber) and invalid (loud) with board_title', async () => {
    const project = baseProject();
    const mk = (
      id: string,
      board_status: KanbanLink['board_status'],
      extra: Partial<KanbanLink> = {},
    ): KanbanLink => ({
      id, workspace_id: 'ws-' + id, board_ref: 'b_' + id,
      project_id: project.id, service_id: 'svc_default', trigger_column: 'ai',
      enabled: true, token_set: true, credential_status: 'per_link',
      board_status, created_at: '2026-01-01T00:00:00Z', ...extra,
    });
    const { client } = kanbanClient(project, [
      mk('ok', 'ok', { board_title: 'jtype' }),
      mk('unv', 'unvalidated'),
      mk('inv', 'invalid', { board_title: 'renamed-board' }),
    ]);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    await waitFor(() => expect(screen.getByTestId('kanban-cred-ok')).toBeTruthy());

    // ok: a validated link shows no board-status badge/notice.
    expect(screen.queryByTestId('kanban-board-status-ok')).toBeNull();
    expect(screen.queryByTestId('kanban-board-notice-ok')).toBeNull();
    // A captured board_title is shown as the row label instead of the raw b_… ref.
    expect(screen.getByText('jtype')).toBeTruthy();

    // unvalidated: amber badge + a connect-a-token hint.
    expect(screen.getByTestId('kanban-board-status-unv').textContent).toBe('columns not validated');
    expect(screen.getByTestId('kanban-board-status-unv').getAttribute('data-state')).toBe('unvalidated');
    expect(screen.getByTestId('kanban-board-notice-unv').textContent).toMatch(/haven’t been checked/);

    // invalid: loud badge + a "poller is skipping this link" alert.
    expect(screen.getByTestId('kanban-board-status-inv').textContent).toBe('board/columns invalid');
    expect(screen.getByTestId('kanban-board-status-inv').getAttribute('data-state')).toBe('invalid');
    expect(screen.getByTestId('kanban-board-notice-inv').textContent).toMatch(/skipping this link/);
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

  it('disables the add form and shows a notice when the cluster integration is off (D27)', async () => {
    const project = baseProject();
    // kanbanEnabled=false ⇒ system.kanban.enabled === false ⇒ add form disabled.
    const { client } = kanbanClient(project, [], false);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));

    // The fail-visible notice points the owner at a cluster admin.
    await waitFor(() => expect(screen.getByTestId('kanban-disabled')).toBeTruthy());
    expect(
      screen.getByText(/ask a cluster admin to configure it on the Cluster page/),
    ).toBeTruthy();

    // The add form's controls are disabled — a link that could never fire can't
    // be created here.
    expect((screen.getByTestId('kanban-link-add') as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId('kanban-link-workspace-select') as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId('kanban-link-service') as HTMLButtonElement).disabled).toBe(true);
    // The "Enter manually" toggle is hidden while the integration is off (there
    // is nothing to reach even by hand until a cluster admin configures jtype).
    expect(screen.queryByTestId('kanban-link-manual-toggle')).toBeNull();
  });

  // ---- D28: per-link "Connect with jtype" device flow ----------------------

  it('per-link Connect is disabled with a hint when the cluster integration is off', async () => {
    const project = baseProject();
    const { client } = kanbanClient(project, undefined, false); // kanbanOff
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));

    const btn = (await screen.findByTestId(
      'kanban-link-connect-kl-1-start',
    )) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(screen.getByTestId('kanban-link-connect-kl-1-hint').textContent).toBe(
      'Enable jtype on the Cluster page first',
    );
  });

  it('per-link Connect completes and flips the credential badge to per_link', async () => {
    const project = baseProject();
    const in90Days = new Date(Date.now() + 90 * 86_400_000).toISOString();
    // A tokenless link (create-then-connect) starts as a loud "missing".
    let links: KanbanLink[] = [
      {
        id: 'kl-1', workspace_id: 'ws', board_ref: 'jcloud-dev',
        project_id: project.id, service_id: 'svc_default', trigger_column: 'ai',
        enabled: true, token_set: false, credential_status: 'missing',
        created_at: '2026-01-01T00:00:00Z',
      },
    ];
    const client: Partial<ApiClient> = {
      getSystem: async () => sysInfo(true),
      listProjectKanbanLinks: async () => links.map((l) => ({ ...l })),
      startLinkConnect: async () => ({
        connect_id: 'c1', user_code: '246810',
        verification_uri: 'http://jtype:13345/oauth/device',
        verification_uri_complete: 'http://jtype:13345/oauth/device?code=246810',
        expires_in: 600, interval: 2,
      }),
      pollLinkConnect: async () => {
        // Completing the flow seals a per-link token server-side.
        links = links.map((l) =>
          l.id === 'kl-1'
            ? { ...l, token_set: true, credential_status: 'per_link', token_expires_at: in90Days }
            : l,
        );
        return { status: 'complete', token_set: true, token_expires_at: in90Days };
      },
    };
    renderModal(client as ApiClient, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    // Initially the dead link screams "no credential".
    await waitFor(() =>
      expect(screen.getByTestId('kanban-cred-kl-1').textContent).toBe('no credential — set a token'),
    );

    // Connect → poll completes → the credential badge flips to the per-link state.
    fireEvent.click(screen.getByTestId('kanban-link-connect-kl-1-start'));
    await waitFor(() =>
      expect(screen.getByTestId('kanban-cred-kl-1').textContent).toBe('own token'),
    );
    // And the row now carries a device-flow expiry badge.
    await waitFor(() =>
      expect(screen.getByTestId('kanban-link-expiry-kl-1').textContent).toMatch(/expires in 90 days/),
    );
  });

  it('per-link Connect surfaces a non-unsupported start failure inline (fail-visible)', async () => {
    const project = baseProject();
    const { client } = kanbanClient(project);
    // e.g. the cipher is missing — the start 409s with a typed error whose
    // message must land next to the button, not vanish into a silent no-op.
    (client as Partial<ApiClient>).startLinkConnect = async () => {
      throw new ApiError(409, 'the token cipher (AUTH_TOKEN_KEY) is not configured', {
        error: {
          code: 'cipher_not_configured',
          message: 'the token cipher (AUTH_TOKEN_KEY) is not configured',
        },
      });
    };
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-kanban'));
    fireEvent.click(await screen.findByTestId('kanban-link-connect-kl-1-start'));

    await waitFor(() =>
      expect(screen.getByTestId('kanban-link-connect-kl-1-start-error').textContent).toBe(
        'the token cipher (AUTH_TOKEN_KEY) is not configured',
      ),
    );
    // Still idle: the button remains for a retry once the cipher is configured.
    expect(screen.getByTestId('kanban-link-connect-kl-1-start')).toBeTruthy();
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

    fireEvent.click(screen.getByTestId('integration-mode-token'));
    fireEvent.change(screen.getByTestId('integration-host'), { target: { value: 'gitea.example.com' } });
    fireEvent.change(screen.getByTestId('integration-token'), { target: { value: 'bot-pat' } });
    fireEvent.click(screen.getByTestId('integration-add'));

    await waitFor(() => expect(ictl.creates).toHaveLength(1));
    expect(ictl.creates[0]).toEqual({
      projectId: 'p1',
      input: { name: undefined, provider: 'gitea', host: 'gitea.example.com', token: 'bot-pat' },
    });
  });

  it('offers OAuth client authorization first and keeps the token path optional', async () => {
    const project = baseProject();
    const { client } = integClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-integrations'));
    await screen.findByTestId('integration-client-id');

    const form = screen.getByTestId('integration-form');
    expect(form.getAttribute('method')).toBe('post');
    expect(form.getAttribute('action')).toBe('/auth/integrations/gitea');
    expect(screen.getByTestId('integration-client-secret')).toBeTruthy();
    expect(screen.queryByTestId('integration-token')).toBeNull();
    expect((screen.getByTestId('integration-add') as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(screen.getByTestId('integration-host'), { target: { value: 'https://gitea.example.com' } });
    fireEvent.change(screen.getByTestId('integration-client-id'), { target: { value: 'client' } });
    fireEvent.change(screen.getByTestId('integration-client-secret'), { target: { value: 'secret' } });
    expect((screen.getByTestId('integration-add') as HTMLButtonElement).disabled).toBe(false);
  });

  it('labels OAuth integrations without offering token rotation', async () => {
    const project = baseProject();
    const { client } = integClient(project, {
      seed: [{
        id: 'oauth-1', project_id: project.id, name: 'automation-bot', provider: 'gitea',
        host: 'https://gitea.example.com', cred_type: 'oauth', bot_username: 'jcode-bot',
        token_set: true, created_at: '2026-01-02T00:00:00Z', updated_at: '2026-01-02T00:00:00Z',
      }],
    });
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-integrations'));
    expect((await screen.findByTestId('integration-credential-oauth-1')).textContent).toBe('OAuth');
    expect(screen.queryByTestId('integration-rotate-oauth-1')).toBeNull();
  });

  it('surfaces a host_not_allowed error readably', async () => {
    const project = baseProject();
    const err = new ApiError(400, 'the git host is not allowed', {
      error: { code: 'host_not_allowed', message: 'the git host is not allowed' },
    });
    const { client } = integClient(project, { createErr: err });
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-integrations'));
    fireEvent.click(screen.getByTestId('integration-mode-token'));
    fireEvent.change(screen.getByTestId('integration-host'), { target: { value: 'evil.example.com' } });
    fireEvent.change(screen.getByTestId('integration-token'), { target: { value: 'x' } });
    fireEvent.click(screen.getByTestId('integration-add'));

    // The typed server message reaches the toast verbatim (fail-visible).
    await waitFor(() => expect(screen.getByText('the git host is not allowed')).toBeTruthy());
  });
});

describe('ProjectSettingsModal — API keys tab (F12 / D24)', () => {
  interface ApiKeyCtl {
    creates: { projectId: string; input: CreateApiKeyInput }[];
    revokes: { projectId: string; keyId: string }[];
    keys: ApiKey[];
  }

  function apiKeyClient(
    project: Project,
    opts: { seed?: ApiKey[]; createErr?: ApiError } = {},
  ): { client: ApiClient; kctl: ApiKeyCtl } {
    const kctl: ApiKeyCtl = {
      creates: [],
      revokes: [],
      keys: opts.seed ?? [],
    };
    const client: Partial<ApiClient> = {
      updateProject: async (_id, input) => ({ ...project, ...input }) as Project,
      listApiKeys: async () => [...kctl.keys],
      createApiKey: async (projectId, input) => {
        if (opts.createErr) throw opts.createErr;
        kctl.creates.push({ projectId, input });
        const k: ApiKey = {
          id: 'ak-new',
          project_id: projectId,
          name: input.name,
          prefix: 'jck_a1b2',
          created_at: '2026-01-02T00:00:00Z',
          last_used_at: null,
          revoked_at: null,
        };
        kctl.keys.push(k);
        return { ...k, key: 'jck_a1b2c3d4e5f6' };
      },
      revokeApiKey: async (projectId, keyId) => {
        kctl.revokes.push({ projectId, keyId });
        const k = kctl.keys.find((x) => x.id === keyId)!;
        k.revoked_at = '2026-01-03T00:00:00Z';
      },
    };
    return { client: client as ApiClient, kctl };
  }

  it('owner sees the API keys tab; a non-owner does not', () => {
    const { client } = apiKeyClient(baseProject());
    renderModal(client, baseProject());
    expect(screen.getByTestId('tab-apikeys')).toBeTruthy();

    const { client: c2 } = apiKeyClient(baseProject({ role: 'member' }));
    renderModal(c2, baseProject({ role: 'member' }));
    expect(screen.queryAllByTestId('tab-apikeys').length).toBe(1); // only the owner one above
  });

  it('lists keys with a status badge and prefix', async () => {
    const project = baseProject();
    const seed: ApiKey[] = [
      {
        id: 'ak-1', project_id: project.id, name: 'ci-bot', prefix: 'jck_a1b2',
        created_at: '2026-01-01T00:00:00Z', last_used_at: '2026-01-02T00:00:00Z', revoked_at: null,
      },
      {
        id: 'ak-2', project_id: project.id, name: 'old-key', prefix: 'jck_c3d4',
        created_at: '2026-01-01T00:00:00Z', last_used_at: null, revoked_at: '2026-01-02T00:00:00Z',
      },
    ];
    const { client } = apiKeyClient(project, { seed });
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-apikeys'));
    await waitFor(() => expect(screen.getByTestId('apikey-ak-1')).toBeTruthy());

    expect(screen.getByTestId('apikey-status-ak-1').textContent).toBe('active');
    expect(screen.getByTestId('apikey-status-ak-1').getAttribute('data-state')).toBe('per_link');
    expect(screen.getByTestId('apikey-status-ak-2').textContent).toBe('revoked');
    expect(screen.getByTestId('apikey-status-ak-2').getAttribute('data-state')).toBe('missing');

    // A revoked key has no Revoke button (nothing left to revoke).
    expect(screen.getByTestId('apikey-revoke-ak-1')).toBeTruthy();
    expect(screen.queryByTestId('apikey-revoke-ak-2')).toBeNull();
  });

  it('creates a key and reveals the plaintext exactly once, with a copy button', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });

    const project = baseProject();
    const { client, kctl } = apiKeyClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-apikeys'));
    await waitFor(() => expect(screen.getByTestId('apikeys-empty')).toBeTruthy());

    fireEvent.change(screen.getByTestId('apikey-name'), { target: { value: 'ci-bot' } });
    fireEvent.click(screen.getByTestId('apikey-create'));

    await waitFor(() => expect(kctl.creates).toHaveLength(1));
    expect(kctl.creates[0]).toEqual({ projectId: 'p1', input: { name: 'ci-bot' } });

    // The plaintext appears in the one-time reveal card.
    await waitFor(() => expect(screen.getByTestId('apikey-reveal-value')).toBeTruthy());
    expect(screen.getByTestId('apikey-reveal-value').textContent).toBe('jck_a1b2c3d4e5f6');

    fireEvent.click(screen.getByTestId('apikey-reveal-copy'));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('jck_a1b2c3d4e5f6'));

    // Dismissing the reveal does not revoke the key — it stays listed as active,
    // and the plaintext is gone from the DOM (no other read-back surface).
    fireEvent.click(screen.getByTestId('apikey-reveal-dismiss'));
    await waitFor(() => expect(screen.queryByTestId('apikey-reveal-value')).toBeNull());
    expect(kctl.revokes).toHaveLength(0);
    expect(screen.getByTestId('apikey-status-ak-new').textContent).toBe('active');
  });

  it('revokes a key', async () => {
    const project = baseProject();
    const seed: ApiKey[] = [
      {
        id: 'ak-1', project_id: project.id, name: 'ci-bot', prefix: 'jck_a1b2',
        created_at: '2026-01-01T00:00:00Z', last_used_at: null, revoked_at: null,
      },
    ];
    const { client, kctl } = apiKeyClient(project, { seed });
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-apikeys'));
    await waitFor(() => expect(screen.getByTestId('apikey-revoke-ak-1')).toBeTruthy());
    fireEvent.click(screen.getByTestId('apikey-revoke-ak-1'));

    await waitFor(() => expect(kctl.revokes).toEqual([{ projectId: 'p1', keyId: 'ak-1' }]));
    await waitFor(() => expect(screen.getByTestId('apikey-status-ak-1').textContent).toBe('revoked'));
    expect(screen.queryByTestId('apikey-revoke-ak-1')).toBeNull();
  });

  it('surfaces a create error readably', async () => {
    const project = baseProject();
    const err = new ApiError(400, 'name is required', {
      error: { code: 'bad_request', message: 'name is required' },
    });
    const { client } = apiKeyClient(project, { createErr: err });
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('tab-apikeys'));
    fireEvent.change(screen.getByTestId('apikey-name'), { target: { value: 'ci-bot' } });
    fireEvent.click(screen.getByTestId('apikey-create'));

    await waitFor(() => expect(screen.getByText('name is required')).toBeTruthy());
  });
});
