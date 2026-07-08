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
import type { KanbanLink, Model, Project, SystemInfo } from '../api/types';
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

function renderPage(client: Partial<ApiClient>, role: Role) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  // Benign defaults so the Model catalog card (useModels + useProjects) resolves;
  // tests that exercise the Model card override these.
  const full: Partial<ApiClient> = {
    listModels: async (): Promise<Model[]> => [],
    listProjects: async () => [],
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

  // ---- Feature E: kanban integration card ---------------------------------

  it('kanban card shows the "off" state when the integration is disabled', async () => {
    const client = {
      getSystem: vi.fn().mockResolvedValue(snapshot({ kanban: { enabled: false } })),
      listKanbanLinks: vi.fn().mockResolvedValue([]),
    };
    renderPage(client, 'cluster-admin');
    await waitFor(() => expect(screen.getByTestId('kanban-card')).toBeTruthy());
    expect(screen.getByTestId('kanban-status').textContent).toContain('off');
    // The add form is mounted only when enabled.
    expect(screen.queryByTestId('kanban-link-form')).toBeNull();
  });

  it('kanban card lists existing links and adds one via the form (integration on)', async () => {
    const existing: KanbanLink = {
      id: 'kl-1', workspace_id: 'ws', board_ref: 'jcloud-dev',
      project_id: 'p1', service_id: 's1', trigger_column: 'ai', done_column: 'done',
      enabled: true, created_at: '2026-01-01T00:00:00Z',
    };
    const createKanbanLink = vi.fn().mockResolvedValue({
      id: 'kl-2', workspace_id: 'ws', board_ref: 'b2',
      project_id: 'p1', service_id: 's1', trigger_column: 'ai',
      enabled: true, created_at: '2026-01-02T00:00:00Z',
    } satisfies KanbanLink);
    const project: Project = {
      id: 'p1', name: 'kanban-proj', created_at: '2026-01-01T00:00:00Z',
      services: [
        { id: 's1', project_id: 'p1', name: 'default', repo_kind: 'raw',
          default_branch: 'main', git_mode: 'readonly', created_at: '2026-01-01T00:00:00Z' },
      ],
    };
    const client = {
      getSystem: vi.fn().mockResolvedValue(
        snapshot({ kanban: { enabled: true, base_url: 'http://jtype:13345', poll_interval: '15s' } }),
      ),
      listKanbanLinks: vi.fn().mockResolvedValue([existing]),
      createKanbanLink,
      // Project picker + service select resolution.
      listProjects: vi.fn().mockResolvedValue([project]),
      getProject: vi.fn().mockResolvedValue(project),
      deleteKanbanLink: vi.fn().mockResolvedValue(undefined),
    };
    renderPage(client, 'cluster-admin');

    // Existing link renders with the board ref.
    await screen.findByText('ws / jcloud-dev');
    expect(screen.getByTestId('kanban-status').textContent).toContain('linked');

    // Wait for the project picker to populate, then choose the project.
    const projectSelect = await screen.findByTestId('kanban-link-project');
    await waitFor(() => screen.getByText('kanban-proj'));
    fireEvent.change(projectSelect, { target: { value: 'p1' } });

    // Wait for the service option to arrive, then choose it.
    await waitFor(() => screen.getByText('default'));
    fireEvent.change(screen.getByTestId('kanban-link-service'), { target: { value: 's1' } });

    // Fill the board fields and submit.
    fireEvent.change(screen.getByTestId('kanban-link-workspace'), { target: { value: 'ws' } });
    fireEvent.change(screen.getByTestId('kanban-link-board'), { target: { value: 'b2' } });
    fireEvent.change(screen.getByTestId('kanban-link-trigger'), { target: { value: 'ai' } });
    fireEvent.click(screen.getByTestId('kanban-link-add'));

    await waitFor(() =>
      expect(createKanbanLink).toHaveBeenCalledWith({
        workspace_id: 'ws', board_ref: 'b2', project_id: 'p1', service_id: 's1',
        trigger_column: 'ai', done_column: undefined,
      }),
    );
    await waitFor(() => expect(screen.getByText('Kanban link added.')).toBeTruthy());
  });
});
