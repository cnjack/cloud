/*
 * SystemPage.test.tsx — the cluster-admin Cluster view:
 *   - cluster-admin: renders the snapshot cards (capacity, provider, runner…).
 *   - project-admin: presentation-only gate shows a plain notice, no snapshot.
 *   - error state: a failed getSystem shows the ErrorBlock with a Retry.
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import { ApiError, type ApiClient } from '../api/client';
import type { Role } from '../api/config';
import type { KanbanClusterConfig, KanbanLink, Model, Project, SystemInfo } from '../api/types';
import { SystemPage } from './SystemPage';

function snapshot(overrides: Partial<SystemInfo> = {}): SystemInfo {
  return {
    version: { version: '1.4.0', commit: 'abc1234' },
    capacity: { max_concurrent_runs: 4, running: 1, queued: 2, scheduling: 1 },
    guardrails: { run_timeout_seconds: 1800, job_ttl_seconds: 3600 },
    provider: { gitea_enabled: true, gitea_url: 'http://gitea:3000' },
    runner: { image: 'ghcr.io/acme/runner:v1', persistent_workspace: true },
    namespace: 'jcloud',
    launcher: 'kubernetes',
    ...overrides,
  };
}

/** GET /api/v1/system/kanban shape (D27). Defaults to the off (source=none) case. */
function kanbanConfig(overrides: Partial<KanbanClusterConfig> = {}): KanbanClusterConfig {
  return {
    base_url: '',
    token_set: false,
    source: 'none',
    effective_enabled: false,
    effective_base_url: '',
    cluster_token_set: false,
    poll_interval: '15s',
    ...overrides,
  };
}

function renderPage(client: Partial<ApiClient>, role: Role) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  // Benign defaults so the Model catalog card (useModels + useProjects) and the
  // Kanban card (useKanbanConfig) resolve; tests that exercise those cards override
  // these. The default kanban config is the off (source=none) case.
  const full: Partial<ApiClient> = {
    listModels: async (): Promise<Model[]> => [],
    listProjects: async () => [],
    getKanbanConfig: async () => kanbanConfig(),
    ...client,
  };
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={full as ApiClient} role={role}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/system']}>
            <SystemPage />
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('SystemPage', () => {
  it('renders the snapshot cards for a cluster-admin', async () => {
    const client = { getSystem: vi.fn().mockResolvedValue(snapshot()) };
    renderPage(client, 'cluster-admin');

    await waitFor(() => expect(screen.getByTestId('system-cards')).toBeTruthy());
    expect(screen.getByText('Capacity')).toBeTruthy();
    expect(screen.getByText('Guardrails')).toBeTruthy();
    // Runner image surfaces.
    expect(screen.getByText('ghcr.io/acme/runner:v1')).toBeTruthy();
    // Feature C: the persistent-workspace status surfaces in the Runner card.
    expect(screen.getByText('Persistent workspace')).toBeTruthy();
    // Provider enabled pill.
    expect(screen.getByTestId('provider-status').textContent).toContain('enabled');
  });

  it('shows the cluster git-host allowlist read-only (D20 / F5)', async () => {
    const client = {
      getSystem: vi.fn().mockResolvedValue(
        snapshot({
          provider: {
            gitea_enabled: true,
            gitea_url: 'http://gitea:3000',
            allowed_git_hosts: ['github.com', 'gitea.example.com'],
          },
        }),
      ),
    };
    renderPage(client, 'cluster-admin');

    await waitFor(() => expect(screen.getByTestId('system-cards')).toBeTruthy());
    expect(screen.getByText('Allowed git hosts')).toBeTruthy();
    expect(screen.getByText('github.com, gitea.example.com')).toBeTruthy();
    expect(screen.getByTestId('allowed-git-hosts-hint')).toBeTruthy();
  });

  it('renders "unrestricted" when the git-host allowlist is empty', async () => {
    const client = { getSystem: vi.fn().mockResolvedValue(snapshot()) }; // no allowed_git_hosts
    renderPage(client, 'cluster-admin');
    await waitFor(() => expect(screen.getByTestId('system-cards')).toBeTruthy());
    expect(screen.getByText('unrestricted (any host)')).toBeTruthy();
  });

  it('reflects the persistent-workspace switch as Off when disabled (Feature C)', async () => {
    const client = {
      getSystem: vi.fn().mockResolvedValue(
        snapshot({ runner: { image: 'r:1', persistent_workspace: false } }),
      ),
    };
    renderPage(client, 'cluster-admin');

    await waitFor(() => expect(screen.getByTestId('system-cards')).toBeTruthy());
    expect(screen.getByText('Persistent workspace')).toBeTruthy();
    // gitea stays enabled in the fixture (Draft PRs = On), so the only "Off" is
    // the persistent-workspace row.
    expect(screen.getByText('Off')).toBeTruthy();
  });

  it('shows the presentation-only gate notice for a project-admin (no snapshot fetch)', () => {
    const getSystem = vi.fn();
    renderPage({ getSystem }, 'project-admin');

    expect(screen.getByTestId('system-forbidden')).toBeTruthy();
    // The gate is client-side: we don't even call getSystem for a project-admin.
    expect(getSystem).not.toHaveBeenCalled();
  });

  it('shows an error state with Retry when the snapshot fails', async () => {
    const client = {
      getSystem: vi.fn().mockRejectedValue(new ApiError(500, 'boom')),
    };
    renderPage(client, 'cluster-admin');

    await waitFor(() =>
      expect(screen.getByText("Couldn't load the cluster snapshot")).toBeTruthy(),
    );
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
  });

  it('Model catalog: adds a model and shows a success toast (D21)', async () => {
    const createModel = vi.fn().mockResolvedValue({
      id: 'm1', name: 'GPT-4o', base_url: 'https://api.openai.com/v1', model_name: 'openai/gpt-4o',
      api_key_set: true, created_at: '', updated_at: '', updated_by: '', granted_project_ids: [],
    } satisfies Model);
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      listModels: vi.fn().mockResolvedValue([] as Model[]),
      listProjects: vi.fn().mockResolvedValue([]),
      createModel,
    };
    renderPage(client, 'cluster-admin');

    const nameInput = await screen.findByTestId('model-add-name');
    // Empty catalog reports "No models".
    expect(screen.getByTestId('model-status').textContent).toContain('No models');

    fireEvent.change(nameInput, { target: { value: 'GPT-4o' } });
    fireEvent.change(screen.getByTestId('model-add-base'), { target: { value: 'https://api.openai.com/v1' } });
    fireEvent.change(screen.getByTestId('model-add-model'), { target: { value: 'openai/gpt-4o' } });
    fireEvent.change(screen.getByTestId('model-add-key'), { target: { value: 'sk-secret' } });
    fireEvent.click(screen.getByTestId('model-add-submit'));

    await waitFor(() =>
      expect(createModel).toHaveBeenCalledWith({
        name: 'GPT-4o', base_url: 'https://api.openai.com/v1', model_name: 'openai/gpt-4o', api_key: 'sk-secret',
      }),
    );
    await waitFor(() => expect(screen.getByText('Model added.')).toBeTruthy());
  });

  it('Model catalog: surfaces a create error via toast (400 validation)', async () => {
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      listModels: vi.fn().mockResolvedValue([] as Model[]),
      listProjects: vi.fn().mockResolvedValue([]),
      createModel: vi.fn().mockRejectedValue(new ApiError(400, "model_name must be in 'provider/model' form")),
    };
    renderPage(client, 'cluster-admin');

    const nameInput = await screen.findByTestId('model-add-name');
    fireEvent.change(nameInput, { target: { value: 'Bad' } });
    fireEvent.change(screen.getByTestId('model-add-base'), { target: { value: 'http://x/v1' } });
    fireEvent.change(screen.getByTestId('model-add-model'), { target: { value: 'bad' } });
    fireEvent.click(screen.getByTestId('model-add-submit'));

    await waitFor(() =>
      expect(screen.getByText("model_name must be in 'provider/model' form")).toBeTruthy(),
    );
  });

  it('Model catalog: lists a model and toggles a project grant (D21)', async () => {
    const model: Model = {
      id: 'm1', name: 'GPT-4o', base_url: 'https://api.openai.com/v1', model_name: 'openai/gpt-4o',
      api_key_set: true, created_at: '', updated_at: '', updated_by: '', granted_project_ids: [],
    };
    const project: Project = { id: 'p1', name: 'demo', created_at: '' };
    const grantModel = vi.fn().mockResolvedValue({ ...model, granted_project_ids: ['p1'] });
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      listModels: vi.fn().mockResolvedValue([model]),
      listProjects: vi.fn().mockResolvedValue([project]),
      grantModel,
      revokeModel: vi.fn(),
    };
    renderPage(client, 'cluster-admin');

    // The model row renders with its provider/model id.
    await screen.findByTestId('model-row-m1');
    expect(screen.getByText('openai/gpt-4o')).toBeTruthy();

    // Toggling the project checkbox grants the model to that project.
    const checkbox = screen.getByTestId('model-grant-m1-p1') as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
    fireEvent.click(checkbox);
    await waitFor(() => expect(grantModel).toHaveBeenCalledWith('m1', 'p1'));
  });

  it('Model catalog: revokes a granted project and removes a model (D21)', async () => {
    const model: Model = {
      id: 'm1', name: 'GPT-4o', base_url: 'https://api.openai.com/v1', model_name: 'openai/gpt-4o',
      api_key_set: true, created_at: '', updated_at: '', updated_by: '', granted_project_ids: ['p1'],
    };
    const project: Project = { id: 'p1', name: 'demo', created_at: '' };
    const revokeModel = vi.fn().mockResolvedValue({ ...model, granted_project_ids: [] });
    const deleteModel = vi.fn().mockResolvedValue(undefined);
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      listModels: vi.fn().mockResolvedValue([model]),
      listProjects: vi.fn().mockResolvedValue([project]),
      grantModel: vi.fn(),
      revokeModel,
      deleteModel,
    };
    renderPage(client, 'cluster-admin');

    // The grant checkbox starts checked; unchecking revokes.
    const checkbox = (await screen.findByTestId('model-grant-m1-p1')) as HTMLInputElement;
    expect(checkbox.checked).toBe(true);
    fireEvent.click(checkbox);
    await waitFor(() => expect(revokeModel).toHaveBeenCalledWith('m1', 'p1'));

    // Removing the model calls deleteModel.
    fireEvent.click(screen.getByTestId('model-delete-m1'));
    await waitFor(() => expect(deleteModel).toHaveBeenCalledWith('m1'));
  });

  it('Model catalog: edit save reaches all three api_key states (omit/rotate/clear)', async () => {
    const model: Model = {
      id: 'm1', name: 'GPT-4o', base_url: 'https://api.openai.com/v1', model_name: 'openai/gpt-4o',
      api_key_set: true, created_at: '', updated_at: '', updated_by: '', granted_project_ids: [],
    };
    const updateModel = vi.fn().mockResolvedValue(model);
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      listModels: vi.fn().mockResolvedValue([model]),
      listProjects: vi.fn().mockResolvedValue([]),
      updateModel,
    };
    renderPage(client, 'cluster-admin');

    // (1) OMIT — edit the name only, leave the key blank: api_key must be absent.
    fireEvent.click(await screen.findByTestId('model-edit-m1'));
    fireEvent.change(screen.getByTestId('model-edit-name-m1'), { target: { value: 'GPT-4o v2' } });
    fireEvent.click(screen.getByTestId('model-save-m1'));
    await waitFor(() => expect(updateModel).toHaveBeenCalledTimes(1));
    expect(updateModel.mock.calls[0]![1]).not.toHaveProperty('api_key');
    expect(updateModel.mock.calls[0]![1]).toMatchObject({ name: 'GPT-4o v2' });

    // (2) ROTATE — type a new key: api_key carries the new value.
    fireEvent.click(await screen.findByTestId('model-edit-m1'));
    fireEvent.change(screen.getByTestId('model-edit-key-m1'), { target: { value: 'sk-new' } });
    fireEvent.click(screen.getByTestId('model-save-m1'));
    await waitFor(() => expect(updateModel).toHaveBeenCalledTimes(2));
    expect(updateModel.mock.calls[1]![1]).toMatchObject({ api_key: 'sk-new' });

    // (3) CLEAR — tick "Clear key": api_key is the empty string (keyless).
    fireEvent.click(await screen.findByTestId('model-edit-m1'));
    fireEvent.click(screen.getByTestId('model-edit-clear-key-m1'));
    fireEvent.click(screen.getByTestId('model-save-m1'));
    await waitFor(() => expect(updateModel).toHaveBeenCalledTimes(3));
    expect(updateModel.mock.calls[2]![1]).toMatchObject({ api_key: '' });
  });

  it('shows unlimited concurrency when max_concurrent_runs is 0 (no bar)', async () => {
    const client = {
      getSystem: vi
        .fn()
        .mockResolvedValue(
          snapshot({
            capacity: { max_concurrent_runs: 0, running: 3, queued: 0, scheduling: 0 },
          }),
        ),
    };
    renderPage(client, 'cluster-admin');

    await waitFor(() => expect(screen.getByTestId('system-cards')).toBeTruthy());
    expect(screen.getByText('unlimited concurrency')).toBeTruthy();
    // The progressbar is omitted when concurrency is unlimited.
    expect(screen.queryByRole('progressbar')).toBeNull();
  });

  // ---- Feature E / D27: editable cluster kanban config card ---------------

  it('kanban card shows the "off" source + an editable config form for a cluster admin', async () => {
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(kanbanConfig({ source: 'none' })),
    };
    renderPage(client, 'cluster-admin');
    // Source badge reads "off" when nothing is configured.
    await waitFor(() => expect(screen.getByTestId('kanban-status').textContent).toBe('off'));
    // The admin now gets an editable config form (D27) — base URL + token fields.
    expect(screen.getByTestId('kanban-config-base')).toBeTruthy();
    expect(screen.getByTestId('kanban-config-save')).toBeTruthy();
    // No DB override to clear, so no "Clear cluster config" action.
    expect(screen.queryByTestId('kanban-config-clear')).toBeNull();
    // Link MANAGEMENT still doesn't live here (owner-only, Project settings).
    expect(screen.queryByTestId('kanban-link-form')).toBeNull();
  });

  it('kanban card: source badge reflects db / env / off', async () => {
    const cases: [KanbanClusterConfig['source'], string, Partial<KanbanClusterConfig>][] = [
      ['db', 'DB (console)', { source: 'db', base_url: 'http://jtype:13345', effective_enabled: true, effective_base_url: 'http://jtype:13345' }],
      ['env', 'env (JTYPE_BASE_URL)', { source: 'env', base_url: '', effective_enabled: true, effective_base_url: 'http://env-jtype:13345' }],
      ['none', 'off', { source: 'none' }],
    ];
    for (const [, label, overrides] of cases) {
      const client = {
        getSystem: vi.fn().mockResolvedValue(snapshot()),
        getKanbanConfig: vi.fn().mockResolvedValue(kanbanConfig(overrides)),
        listKanbanLinks: vi.fn().mockResolvedValue([]),
      };
      const { unmount } = renderPage(client, 'cluster-admin');
      await waitFor(() => expect(screen.getByTestId('kanban-status').textContent).toBe(label));
      unmount();
    }
  });

  it('kanban card: a DB-config admin sees the base URL prefilled + the read-only link overview (F6)', async () => {
    const withTok: KanbanLink = {
      id: 'kl-1', workspace_id: 'ws', board_ref: 'jcloud-dev',
      project_id: 'p1', service_id: 's1', trigger_column: 'ai', done_column: 'done',
      enabled: true, token_set: true, credential_status: 'per_link',
      created_at: '2026-01-01T00:00:00Z',
    };
    const noTok: KanbanLink = {
      id: 'kl-2', workspace_id: 'ws2', board_ref: 'b_ef56gh78',
      project_id: 'p1', service_id: 's1', trigger_column: 'ai',
      enabled: true, token_set: false, credential_status: 'cluster_fallback',
      // D29: a canonicalized link shows its captured board_title, not the b_… ref.
      board_status: 'ok', board_title: 'My Board',
      created_at: '2026-01-02T00:00:00Z',
    };
    const dead: KanbanLink = {
      id: 'kl-3', workspace_id: 'ws3', board_ref: 'b3',
      project_id: 'p1', service_id: 's1', trigger_column: 'ai',
      enabled: true, token_set: false, credential_status: 'missing',
      // D29: a runtime check failed — the poller is skipping it (fail-visible).
      board_status: 'invalid',
      created_at: '2026-01-03T00:00:00Z',
    };
    const project: Project = {
      id: 'p1', name: 'kanban-proj', created_at: '2026-01-01T00:00:00Z', services: [],
    };
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(
        kanbanConfig({
          source: 'db', base_url: 'http://jtype:13345', token_set: true,
          effective_enabled: true, effective_base_url: 'http://jtype:13345', cluster_token_set: true,
        }),
      ),
      listKanbanLinks: vi.fn().mockResolvedValue([withTok, noTok, dead]),
      listProjects: vi.fn().mockResolvedValue([project]),
    };
    renderPage(client, 'cluster-admin');

    await screen.findByText('ws / jcloud-dev');
    expect(screen.getByTestId('kanban-status').textContent).toBe('DB (console)');
    // The editable base URL field is prefilled with the DB override's base_url.
    expect((screen.getByTestId('kanban-config-base') as HTMLInputElement).value).toBe('http://jtype:13345');
    // Project name resolved (shown per link) and all three token badges render.
    await waitFor(() => expect(screen.getAllByText(/kanban-proj/).length).toBeGreaterThan(0));
    expect(screen.getByText('own token')).toBeTruthy();
    expect(screen.getByText('cluster token')).toBeTruthy();
    expect(screen.getByText('no credential')).toBeTruthy();
    expect(screen.getByTestId('kanban-cred-kl-3').getAttribute('data-err')).toBe('true');
    // D29: board_title replaces the raw b_… ref, and a failed link shows a loud
    // "board/columns invalid" pill; a validated link shows no board-status pill.
    expect(screen.getByText('My Board')).toBeTruthy();
    expect(screen.queryByTestId('kanban-board-status-kl-2')).toBeNull();
    expect(screen.getByTestId('kanban-board-status-kl-3').textContent).toBe('board/columns invalid');
    expect(screen.getByTestId('kanban-board-status-kl-3').getAttribute('data-err')).toBe('true');
    // Read-only link overview: no per-link add/delete management here.
    expect(screen.queryByTestId('kanban-link-form')).toBeNull();
    expect(screen.queryByTestId('kanban-link-add')).toBeNull();
    expect(screen.queryByTestId('kanban-link-delete-kl-1')).toBeNull();
  });

  it('kanban card: edits the config and PUTs base_url + token (token never rendered back)', async () => {
    const updateKanbanConfig = vi.fn().mockResolvedValue(
      kanbanConfig({ source: 'none' }), // fixed so the editor key stays stable
    );
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(kanbanConfig({ source: 'none' })),
      updateKanbanConfig,
      listKanbanLinks: vi.fn().mockResolvedValue([]),
    };
    renderPage(client, 'cluster-admin');

    fireEvent.change(await screen.findByTestId('kanban-config-base'), {
      target: { value: 'http://jtype:13345' },
    });
    fireEvent.change(screen.getByTestId('kanban-config-token'), { target: { value: 'jt-pat' } });
    fireEvent.click(screen.getByTestId('kanban-config-save'));

    await waitFor(() =>
      expect(updateKanbanConfig).toHaveBeenCalledWith({ base_url: 'http://jtype:13345', token: 'jt-pat' }),
    );
    await waitFor(() => expect(screen.getByText('Kanban config saved.')).toBeTruthy());
    // The token is cleared from the field after saving — never echoed back.
    expect((screen.getByTestId('kanban-config-token') as HTMLInputElement).value).toBe('');
  });

  it('kanban card: token three-state — blank keeps, a value rotates, the checkbox clears', async () => {
    const cfg = kanbanConfig({
      source: 'db', base_url: 'http://jtype:13345', token_set: true,
      effective_enabled: true, effective_base_url: 'http://jtype:13345',
    });
    const updateKanbanConfig = vi.fn().mockResolvedValue(cfg);
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(cfg),
      updateKanbanConfig,
      listKanbanLinks: vi.fn().mockResolvedValue([]),
    };
    renderPage(client, 'cluster-admin');

    // (1) KEEP — leave the token blank: no api_key-style token key in the body.
    fireEvent.click(await screen.findByTestId('kanban-config-save'));
    await waitFor(() => expect(updateKanbanConfig).toHaveBeenCalledTimes(1));
    expect(updateKanbanConfig.mock.calls[0]![0]).not.toHaveProperty('token');
    expect(updateKanbanConfig.mock.calls[0]![0]).toMatchObject({ base_url: 'http://jtype:13345' });

    // (2) ROTATE — type a token: it rides in the body.
    fireEvent.change(screen.getByTestId('kanban-config-token'), { target: { value: 'rotated' } });
    fireEvent.click(screen.getByTestId('kanban-config-save'));
    await waitFor(() => expect(updateKanbanConfig).toHaveBeenCalledTimes(2));
    expect(updateKanbanConfig.mock.calls[1]![0]).toMatchObject({ token: 'rotated' });

    // (3) CLEAR — tick "Clear cluster token": token is the empty string.
    fireEvent.click(screen.getByTestId('kanban-config-clear-token'));
    fireEvent.click(screen.getByTestId('kanban-config-save'));
    await waitFor(() => expect(updateKanbanConfig).toHaveBeenCalledTimes(3));
    expect(updateKanbanConfig.mock.calls[2]![0]).toMatchObject({ token: '' });

    // (4) whitespace-only is a KEEP, not an accidental clear/rotate — the value
    // is trimmed BEFORE the three-state decision (the checkbox reset on save).
    fireEvent.change(screen.getByTestId('kanban-config-token'), { target: { value: '   ' } });
    fireEvent.click(screen.getByTestId('kanban-config-save'));
    await waitFor(() => expect(updateKanbanConfig).toHaveBeenCalledTimes(4));
    expect(updateKanbanConfig.mock.calls[3]![0]).not.toHaveProperty('token');
  });

  it('kanban card: renders the resolver reason loudly and keeps Clear for a BROKEN DB row', async () => {
    // A DB row whose token can't be decrypted (AUTH_TOKEN_KEY unset) resolves to
    // source "none" + a reason. The admin must see WHY it's off — and must still
    // be offered "Clear cluster config" (the way out), even though source≠db.
    const broken = kanbanConfig({
      source: 'none', base_url: 'http://jtype:13345', token_set: true,
      reason: 'kanban token is set but AUTH_TOKEN_KEY is not configured',
    });
    const deleteKanbanConfig = vi.fn().mockResolvedValue(kanbanConfig({ source: 'none' }));
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(broken),
      deleteKanbanConfig,
      listKanbanLinks: vi.fn().mockResolvedValue([]),
    };
    renderPage(client, 'cluster-admin');

    // The fail-visible reason notice renders (never a bare, unexplained "off").
    await waitFor(() =>
      expect(screen.getByTestId('kanban-config-reason').textContent).toBe(
        'kanban token is set but AUTH_TOKEN_KEY is not configured',
      ),
    );
    expect(screen.getByTestId('kanban-status').textContent).toBe('off');
    expect(screen.getByTestId('kanban-status').getAttribute('data-err')).toBe('true');
    // Clear is gated on DB-ROW EXISTENCE, not on source: the broken row is
    // deletable right here.
    fireEvent.click(screen.getByTestId('kanban-config-clear'));
    fireEvent.click(screen.getByTestId('kanban-config-clear-confirm'));
    await waitFor(() => expect(deleteKanbanConfig).toHaveBeenCalledTimes(1));
  });

  it('kanban card: falls back to system.kanban.reason when the config view carries none', async () => {
    const client = {
      getSystem: vi.fn().mockResolvedValue(
        snapshot({ kanban: { enabled: false, reason: 'kanban resolver: token cipher unavailable' } }),
      ),
      getKanbanConfig: vi.fn().mockResolvedValue(kanbanConfig({ source: 'none' })),
      listKanbanLinks: vi.fn().mockResolvedValue([]),
    };
    renderPage(client, 'cluster-admin');

    await waitFor(() =>
      expect(screen.getByTestId('kanban-config-reason').textContent).toBe(
        'kanban resolver: token cipher unavailable',
      ),
    );
  });

  it('kanban card: surfaces a 409 cipher_not_configured via toast (fail-visible)', async () => {
    const err = new ApiError(409, 'the token cipher (AUTH_TOKEN_KEY) is not configured', {
      error: { code: 'cipher_not_configured', message: 'the token cipher (AUTH_TOKEN_KEY) is not configured' },
    });
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(kanbanConfig({ source: 'none' })),
      updateKanbanConfig: vi.fn().mockRejectedValue(err),
      listKanbanLinks: vi.fn().mockResolvedValue([]),
    };
    renderPage(client, 'cluster-admin');

    fireEvent.change(await screen.findByTestId('kanban-config-base'), {
      target: { value: 'http://jtype:13345' },
    });
    fireEvent.change(screen.getByTestId('kanban-config-token'), { target: { value: 'jt-pat' } });
    fireEvent.click(screen.getByTestId('kanban-config-save'));

    await waitFor(() =>
      expect(screen.getByText('the token cipher (AUTH_TOKEN_KEY) is not configured')).toBeTruthy(),
    );
  });

  it('kanban card: clears the cluster config (DELETE) behind a confirm step', async () => {
    const deleteKanbanConfig = vi.fn().mockResolvedValue(kanbanConfig({ source: 'none' }));
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(
        kanbanConfig({
          source: 'db', base_url: 'http://jtype:13345', token_set: true,
          effective_enabled: true, effective_base_url: 'http://jtype:13345',
        }),
      ),
      deleteKanbanConfig,
      listKanbanLinks: vi.fn().mockResolvedValue([]),
    };
    renderPage(client, 'cluster-admin');

    // First click reveals the confirm step; no DELETE yet.
    fireEvent.click(await screen.findByTestId('kanban-config-clear'));
    expect(deleteKanbanConfig).not.toHaveBeenCalled();
    // Confirming issues the DELETE.
    fireEvent.click(screen.getByTestId('kanban-config-clear-confirm'));
    await waitFor(() => expect(deleteKanbanConfig).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByText('Cluster kanban config cleared.')).toBeTruthy());
  });

  // ---- D28: "Connect with jtype" device flow (cluster fallback token) -------

  const dbConfig = (overrides: Partial<KanbanClusterConfig> = {}) =>
    kanbanConfig({
      source: 'db',
      base_url: 'http://jtype:13345',
      effective_enabled: true,
      effective_base_url: 'http://jtype:13345',
      ...overrides,
    });

  const startPayload = (userCode: string) => ({
    connect_id: 'kc_1',
    user_code: userCode,
    verification_uri: 'http://jtype:13345/oauth/device',
    verification_uri_complete: `http://jtype:13345/oauth/device?code=${userCode}`,
    expires_in: 600,
    interval: 2,
  });

  const in90Days = () => new Date(Date.now() + 90 * 86_400_000).toISOString();

  it('kanban connect: the button is disabled with a hint until a base URL is saved', async () => {
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      // source=none ⇒ base_url is "" ⇒ connect can't target a DB row yet.
      getKanbanConfig: vi.fn().mockResolvedValue(kanbanConfig({ source: 'none' })),
      listKanbanLinks: vi.fn().mockResolvedValue([]),
    };
    renderPage(client, 'cluster-admin');

    const btn = (await screen.findByTestId('kanban-connect-start')) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(screen.getByTestId('kanban-connect-hint').textContent).toBe('Save the jtype base URL first');
  });

  it('kanban connect: clicking Connect shows the user_code + an external authorize link', async () => {
    const startKanbanConnect = vi.fn().mockResolvedValue(startPayload('482913'));
    // Stays pending so the code panel persists for the assertions.
    const pollKanbanConnect = vi.fn().mockResolvedValue({ status: 'pending', token_set: false });
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(dbConfig()),
      listKanbanLinks: vi.fn().mockResolvedValue([]),
      startKanbanConnect,
      pollKanbanConnect,
    };
    renderPage(client, 'cluster-admin');

    fireEvent.click(await screen.findByTestId('kanban-connect-start'));

    // The prominent 6-digit user_code + a deep link into jtype's approve page.
    await waitFor(() => expect(screen.getByTestId('kanban-connect-code').textContent).toBe('482913'));
    const link = screen.getByTestId('kanban-connect-link') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('http://jtype:13345/oauth/device?code=482913');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noopener');
    // A live status while polling.
    expect(screen.getByTestId('kanban-connect-status')).toBeTruthy();
  });

  it('kanban connect: pending→complete flips the connected badge + shows expiry; no token plaintext', async () => {
    const expiry = in90Days();
    const startKanbanConnect = vi.fn().mockResolvedValue(startPayload('112233'));
    // The start seeds a pending status; the first real poll returns complete,
    // carrying only status + expiry (never a token) — a fast pending→complete.
    const pollKanbanConnect = vi
      .fn()
      .mockResolvedValue({ status: 'complete', token_set: true, token_expires_at: expiry });
    // As on the real backend: the complete-edge invalidation REFETCHES the
    // config, which now reports token_set:true (+ the expiry). The editor must
    // NOT remount on that flip (its key excludes token_set) — the completion
    // panel has to survive the refetch tick.
    const getKanbanConfig = vi
      .fn()
      .mockResolvedValueOnce(dbConfig({ token_set: false }))
      .mockResolvedValue(dbConfig({ token_set: true, token_expires_at: expiry }));
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig,
      listKanbanLinks: vi.fn().mockResolvedValue([]),
      startKanbanConnect,
      pollKanbanConnect,
    };
    renderPage(client, 'cluster-admin');

    fireEvent.click(await screen.findByTestId('kanban-connect-start'));

    // The completion badge + a human expiry, both derived from the poll payload.
    await waitFor(() => expect(screen.getByTestId('kanban-connect-complete')).toBeTruthy());
    expect(screen.getByText('Connected — token set')).toBeTruthy();
    expect(screen.getByTestId('kanban-connect-complete').textContent).toMatch(/expires in 90 days/);

    // The invalidation-triggered refetch lands token_set:true — the persistent
    // expiry badge appears AND the completion panel PERSISTS (no key-change
    // remount wiping the in-flight connect state).
    await waitFor(() => expect(getKanbanConfig.mock.calls.length).toBeGreaterThanOrEqual(2));
    await waitFor(() =>
      expect(screen.getByTestId('kanban-connect-expiry').textContent).toMatch(/expires in 90 days/),
    );
    expect(screen.getByTestId('kanban-connect-complete')).toBeTruthy();
    // The write-only paste field is never populated with a token value.
    expect((screen.getByTestId('kanban-config-token') as HTMLInputElement).value).toBe('');
  });

  it('kanban connect: a non-unsupported start failure is fail-visible next to the button', async () => {
    // e.g. jtype unreachable at start — NOT the unsupported case; the flow must
    // not fail silently (button spins, then… nothing).
    const err = new ApiError(503, 'jtype is unreachable at http://jtype:13345', {
      error: { code: 'jtype_unreachable', message: 'jtype is unreachable at http://jtype:13345' },
    });
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(dbConfig()),
      listKanbanLinks: vi.fn().mockResolvedValue([]),
      startKanbanConnect: vi.fn().mockRejectedValue(err),
    };
    renderPage(client, 'cluster-admin');

    fireEvent.click(await screen.findByTestId('kanban-connect-start'));

    // The server's message verbatim, as a loud alert next to the idle button.
    await waitFor(() =>
      expect(screen.getByTestId('kanban-connect-start-error').textContent).toBe(
        'jtype is unreachable at http://jtype:13345',
      ),
    );
    // The button stays available for a retry.
    expect(screen.getByTestId('kanban-connect-start')).toBeTruthy();
  });

  it('kanban connect: a non-404 poll failure shows "failed — reconnect", never a stuck Waiting…', async () => {
    const startKanbanConnect = vi.fn().mockResolvedValue(startPayload('998877'));
    // e.g. a 403 mid-flow (session/role changed): terminal for this flow.
    const pollKanbanConnect = vi.fn().mockRejectedValue(
      new ApiError(403, 'forbidden', { error: { code: 'forbidden', message: 'forbidden' } }),
    );
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(dbConfig()),
      listKanbanLinks: vi.fn().mockResolvedValue([]),
      startKanbanConnect,
      pollKanbanConnect,
    };
    renderPage(client, 'cluster-admin');

    fireEvent.click(await screen.findByTestId('kanban-connect-start'));

    await waitFor(() =>
      expect(screen.getByTestId('kanban-connect-failed').textContent).toBe(
        'Connection failed — click Connect again.',
      ),
    );
    // No lingering pending panel.
    expect(screen.queryByTestId('kanban-connect-status')).toBeNull();
  });

  it('kanban connect: an old jtype (jtype_oauth_unsupported) shows a notice and keeps the paste field', async () => {
    const err = new ApiError(409, 'this jtype does not support Connect', {
      error: { code: 'jtype_oauth_unsupported', message: 'paste a token instead' },
    });
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot()),
      getKanbanConfig: vi.fn().mockResolvedValue(dbConfig()),
      listKanbanLinks: vi.fn().mockResolvedValue([]),
      startKanbanConnect: vi.fn().mockRejectedValue(err),
    };
    renderPage(client, 'cluster-admin');

    fireEvent.click(await screen.findByTestId('kanban-connect-start'));

    // Fail-visible inline notice — never a silent failure.
    await waitFor(() => expect(screen.getByTestId('kanban-connect-unsupported')).toBeTruthy());
    // The manual paste field remains available as the fallback.
    expect(screen.getByTestId('kanban-config-token')).toBeTruthy();
  });
});
