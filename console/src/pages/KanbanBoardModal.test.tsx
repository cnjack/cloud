/*
 * KanbanBoardModal (D31) — the embedded board modal. We MOCK `jtype-board-react`
 * at the module level so tests never mount the heavy real BoardSurface: the stub
 * `JTypeBoard` just reflects its props onto data-attributes, and `JTypeApiError`
 * is a light typed Error (the proxy client + resolver depend on it).
 */
import { describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ApiError, type ApiClient } from '../api/client';
import type { BoardEmbedLink } from '../api/types';
import { pickOption } from '../test/select';

vi.mock('jtype-board-react', () => ({
  JTypeBoard: (p: {
    workspaceId: string;
    boardRef: string;
    live?: boolean;
    readOnly?: boolean;
    initialCardPath?: string;
    additionalCardRoots?: readonly string[];
    renderCardSupplement?: (card: { id: string; title: string }) => ReactNode;
  }) => (
    <>
      <div
        data-testid="jtype-board"
        data-workspace={p.workspaceId}
        data-boardref={p.boardRef}
        data-live={String(p.live)}
        data-readonly={String(p.readOnly)}
        data-initial-card={p.initialCardPath}
        data-additional-card-roots={p.additionalCardRoots?.join(',')}
      />
      {p.renderCardSupplement?.({ id: 'cards/payment.md', title: 'Payment card' })}
    </>
  ),
  JTypeApiError: class extends Error {
    status: number;
    code: string;
    constructor(status: number, code: string) {
      super(code);
      this.name = 'JTypeApiError';
      this.status = status;
      this.code = code;
    }
  },
}));
vi.mock('jtype-board-react/style.css', () => ({}));

// Imported AFTER the mock so the modal + proxy client resolve the stubbed module.
import { KanbanBoardModal } from './KanbanBoardModal';

function link(over: Partial<BoardEmbedLink> = {}): BoardEmbedLink {
  return {
    id: 'kl_1',
    workspace_id: 'ws_team',
    board_ref: 'b_123',
    board_title: 'jtype',
    service_id: 'svc_1',
    trigger_column: 'ai',
	work_column: 'doing',
    enabled: true,
    ...over,
  };
}

const COLS = [{ key: 'ai', name: 'AI' }, { key: 'doing', name: 'Doing' }, { key: 'done', name: 'Done' }];

/**
 * A fake ApiClient whose board proxy serves `.board` docs from an in-memory
 * table keyed by workspace. Each entry maps a relativePath → config id.
 */
function makeApi(
  docsByWs: Record<string, { path: string; configId: string }[]>,
): ApiClient {
  return {
    boardListDocuments: async (_pid: string, ws: string) =>
      (docsByWs[ws] ?? []).map((d) => ({
        id: `doc_${d.configId}`,
        relativePath: d.path,
        title: d.path,
        isPublished: true,
        contentHash: 'h',
        updatedClock: 1,
        versionId: 'v1',
      })),
    boardGetDocument: async (_pid: string, ws: string, docId: string) => {
      const d = (docsByWs[ws] ?? []).find((x) => `doc_${x.configId}` === docId)!;
      return {
        relativePath: d.path,
        title: d.path,
        isPublished: true,
        content: JSON.stringify({ id: d.configId, title: d.path, columns: COLS }),
        contentHash: 'h',
        versionId: 'v1',
        updatedClock: 1,
      };
    },
    boardSaveDocument: async () => ({
      relativePath: 'x',
      contentHash: 'h',
      updatedClock: 2,
      mergeStatus: 'accepted' as const,
    }),
    getServiceKanbanPolicy: async (serviceId: string) => ({
      service_id: serviceId,
      service_name: 'Service',
      repository: 'acme/service',
      model: { label: 'Demo model' },
      board: { workspace_id: 'ws_team', ref: 'b_123' },
      trigger_column: { key: 'ai', label: 'AI' },
	  work_column: { key: 'doing', label: 'Doing' },
      done_column: { key: 'done', label: 'Done' },
      output: 'comment_and_move_on_success' as const,
      health: { state: 'ready' as const, blocker: null },
    }),
    listServiceKanbanCardExecutions: async () => ({ claim: null, items: [], next_cursor: null }),
  } as unknown as ApiClient;
}

function renderModal(api: ApiClient, links: BoardEmbedLink[], serviceId?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={api}>
        <KanbanBoardModal projectId="p1" serviceId={serviceId} links={links} onClose={() => {}} />
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('KanbanBoardModal', () => {
  it('shows setup without a duplicate board preview and saves the selected columns', async () => {
    const putServiceKanban = vi.fn(async (_serviceId: string, input: { installation_id: string; board_ref: string; trigger_column?: string; work_column?: string; done_column?: string }) => ({
      automation: { id: 'a1', service_id: 'svc_1', name: 'Kanban', trigger_kind: 'kanban' as const, prompt_template: '', enabled: true, ignore_jcode: true, created_at: '', updated_at: '' },
	  kanban: { installation_id: input.installation_id, board_ref: input.board_ref, trigger_column: input.trigger_column ?? 'ai', work_column: input.work_column ?? 'doing', done_column: input.done_column ?? 'done' },
    }));
    const api = {
      ...makeApi({}),
      listProjectPlugins: async () => [{ id: 'jtype-1', project_id: 'p1', provider: 'jtype' as const, status: 'enabled' as const, workspace_id: 'ws_team', scopes: [] }],
      listPluginBoards: async () => [{
        id: 'b_stable',
        ref: 'delivery.board',
        title: 'Delivery',
		columns: [{ key: 'ai', name: 'AI' }, { key: 'review', name: 'Review' }, { key: 'doing', name: 'Doing' }, { key: 'done', name: 'Done' }],
      }],
      putServiceKanban,
      deleteServiceKanban: async () => undefined,
    } as unknown as ApiClient;
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <ApiProvider client={api}>
          <KanbanBoardModal projectId="p1" serviceId="svc_1" links={[]} canManage onClose={() => {}} />
        </ApiProvider>
      </QueryClientProvider>,
    );
    await screen.findByText('Delivery');
    expect(screen.queryByTestId('kanban-readonly-preview')).toBeNull();
    expect(screen.queryByTestId('jtype-board')).toBeNull();
    await pickOption('kanban-trigger-column', 'Review');
    fireEvent.click(screen.getByTestId('kanban-enable'));
    await waitFor(() => expect(putServiceKanban).toHaveBeenCalledWith('svc_1', {
      installation_id: 'jtype-1',
      board_ref: 'delivery.board',
      trigger_column: 'review',
	  work_column: 'doing',
      done_column: 'done',
      enabled: true,
    }));
  });

  it('single link: renders the board with the workspace and live=false', async () => {
    const api = makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <ApiProvider client={api}>
          <KanbanBoardModal
            projectId="p1"
            links={[link()]}
            initialCardPath="cards/payment.md"
            canManage
            onClose={() => {}}
          />
        </ApiProvider>
      </QueryClientProvider>,
    );

    // The board is a working surface, not a form: it opts into Modal's bounded
    // wide layout so horizontal board scrolling stays inside the dialog.
    expect(screen.getByTestId('kanban-board-modal').getAttribute('data-size')).toBe('wide');
    const board = await screen.findByTestId('jtype-board');
    expect(board.getAttribute('data-workspace')).toBe('ws_team');
    // No SSE proxy → the board is handed live=false (visible polling).
    expect(board.getAttribute('data-live')).toBe('false');
    expect(board.getAttribute('data-readonly')).toBe('false');
    expect(board.getAttribute('data-initial-card')).toBe('cards/payment.md');
    expect(board.getAttribute('data-additional-card-roots')).toBe('jcode-automation');
    // Single link → no selector.
    expect(screen.queryByTestId('kanban-board-select')).toBeNull();
  });

  it('makes the embedded board read-only when the caller cannot manage Cards', async () => {
    const api = makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] });
    renderModal(api, [link()]);

    const board = await screen.findByTestId('jtype-board');
    expect(board.getAttribute('data-readonly')).toBe('true');
  });

  it('shows the execution policy and blocked receipt inside native Card details', async () => {
    const listExecutions = vi.fn(async () => ({
      claim: { document_path: 'cards/payment.md', external_ref_available: true },
      usage_summary: {
        availability: 'available' as const,
        requests: 1,
        capture: { reported: 1, partial: 0, unavailable: 0, parse_error: 0 },
        tokens: { input: 800, output: 120, cache_read: null, cache_write: null },
        costs: { reported: [], estimated: [], uncosted: [] },
      },
      items: [{
        id: 'occ_1',
        status: 'blocked' as const,
        summary: 'Execution is blocked',
        reason: 'Choose an allowed model for this Service.',
        reason_code: 'model_not_configured',
        repair_role: 'project_owner' as const,
        requested_actor: { label: 'External editor', precision: 'display_only' as const },
        run: null,
        receipt: { external: 'written' as const, writeback: 'not_required' as const },
        created_at: '2026-07-31T00:00:00Z',
        updated_at: '2026-07-31T00:00:00Z',
      }],
      next_cursor: null,
    }));
    const api = {
      ...makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] }),
      getServiceKanbanPolicy: async () => ({
        service_id: 'svc_1',
        service_name: 'payments-api',
        repository: 'acme/payments',
        model: { id: 'model_1', label: 'Claude Sonnet' },
        board: { workspace_id: 'ws_team', ref: 'b_123' },
        trigger_column: { key: 'ai', label: 'Agent queue' },
        done_column: { key: 'done', label: 'Done' },
        output: 'comment_and_move_on_success' as const,
        health: { state: 'ready' as const, blocker: null },
      }),
      listServiceKanbanCardExecutions: listExecutions,
    } as unknown as ApiClient;
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <ApiProvider client={api}>
          <KanbanBoardModal
            projectId="p1"
            serviceId="svc_1"
            links={[link()]}
            onClose={() => {}}
          />
        </ApiProvider>
      </QueryClientProvider>,
    );

    expect((await screen.findByTestId('kanban-policy')).textContent).toContain('payments-api');
    expect(screen.getByTestId('kanban-policy').textContent).toContain('Agent queue');
    const receipt = await screen.findByTestId('kanban-execution-current');
    expect(receipt.textContent).toContain('Model not configured');
    expect(receipt.textContent).toContain('Project owner');
    expect(receipt.textContent).not.toContain('Card writeback pending');
    expect(screen.getByTestId('kanban-card-usage').textContent).toContain('800');
    expect(listExecutions).toHaveBeenCalledWith('svc_1', 'ws_team', 'cards/payment.md', undefined);
  });

  it('keeps execution load failures visible and retries into the empty state', async () => {
    const listExecutions = vi.fn()
      .mockRejectedValueOnce(new Error('temporary API failure'))
      .mockResolvedValueOnce({ claim: null, items: [], next_cursor: null });
    const api = {
      ...makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] }),
      listServiceKanbanCardExecutions: listExecutions,
    } as unknown as ApiClient;
    renderModal(api, [link()], 'svc_1');

    expect(await screen.findByText('Cloud executions could not be loaded.')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText(/Move this Card into the Starts jcode column/)).toBeTruthy();
    expect(listExecutions).toHaveBeenCalledTimes(2);
  });

  it('preserves deleted-Card history and loads older occurrences by opaque cursor', async () => {
    const listExecutions = vi.fn(async (_serviceId: string, _workspaceId: string, _path: string, before?: string) => (
      before
        ? {
            claim: { document_path: 'cards/payment.md', external_ref_available: false },
            items: [{
              id: 'occ_old',
              status: 'terminal' as const,
              outcome: 'failed',
              summary: 'Run failed',
              reason: null,
              repair_role: null,
              requested_actor: null,
              run: null,
              receipt: { external: 'written' as const, writeback: 'complete' as const },
              created_at: '2026-07-30T00:00:00Z',
              updated_at: '2026-07-30T00:01:00Z',
            }],
            next_cursor: null,
          }
        : {
            claim: { document_path: 'cards/payment.md', external_ref_available: false },
            items: [{
              id: 'occ_current',
              status: 'terminal' as const,
              outcome: 'succeeded',
              summary: 'Run completed successfully',
              reason: null,
              repair_role: null,
              requested_actor: null,
              run: null,
              receipt: { external: 'written' as const, writeback: 'complete' as const },
              created_at: '2026-07-31T00:00:00Z',
              updated_at: '2026-07-31T00:01:00Z',
            }],
            next_cursor: 'opaque-before',
          }
    ));
    const api = {
      ...makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] }),
      listServiceKanbanCardExecutions: listExecutions,
    } as unknown as ApiClient;
    renderModal(api, [link()], 'svc_1');

    expect(await screen.findByText(/no longer exists in JType/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Load earlier executions' }));
    expect(await screen.findByText('1 prior execution')).toBeTruthy();
    expect(listExecutions).toHaveBeenLastCalledWith(
      'svc_1',
      'ws_team',
      'cards/payment.md',
      'opaque-before',
    );
  });

  it('resolves board_ref (config id) → relativePath before rendering', async () => {
    const api = makeApi({
      ws_team: [
        { path: 'other.board', configId: 'b_other' },
        { path: 'jtype.board', configId: 'b_123' },
      ],
    });
    renderModal(api, [link({ board_ref: 'b_123' })]);

    const board = await screen.findByTestId('jtype-board');
    // The component gets the relativePath, NOT the b_… config id.
    expect(board.getAttribute('data-boardref')).toBe('jtype.board');
  });

  it('multi link: shows a selector; choosing the second board re-renders it', async () => {
    const api = makeApi({
      ws_team: [{ path: 'jtype.board', configId: 'b_123' }],
      ws_solo: [{ path: 'personal.board', configId: 'b_solo' }],
    });
    renderModal(api, [
      link({ id: 'kl_1', workspace_id: 'ws_team', board_ref: 'b_123', board_title: 'jtype' }),
      link({ id: 'kl_2', workspace_id: 'ws_solo', board_ref: 'b_solo', board_title: 'Personal' }),
    ]);

    // First (enabled) link renders by default.
    let board = await screen.findByTestId('jtype-board');
    expect(board.getAttribute('data-workspace')).toBe('ws_team');

    expect(screen.getByTestId('kanban-board-select')).toBeTruthy();
    await pickOption('kanban-board-select', 'Personal');

    await waitFor(() => {
      const b = screen.getByTestId('jtype-board');
      expect(b.getAttribute('data-workspace')).toBe('ws_solo');
      expect(b.getAttribute('data-boardref')).toBe('personal.board');
    });
  });

  it('keeps the bound board visible while editing trigger and completion columns inline', async () => {
    const putServiceKanban = vi.fn(async (_serviceId: string, input: {
      installation_id: string;
      board_ref: string;
      trigger_column?: string;
      done_column?: string;
    }) => ({
      automation: { id: 'a1', service_id: 'svc_1', name: 'Kanban', trigger_kind: 'kanban' as const, prompt_template: '', enabled: true, ignore_jcode: true, created_at: '', updated_at: '' },
      kanban: {
        installation_id: input.installation_id,
        board_ref: 'b_123',
        trigger_column: input.trigger_column ?? 'ai',
        done_column: input.done_column ?? 'done',
      },
    }));
    const api = {
      ...makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] }),
      listProjectPlugins: async () => [{ id: 'jtype-1', project_id: 'p1', provider: 'jtype' as const, status: 'enabled' as const, workspace_id: 'ws_team', scopes: [] }],
      listPluginBoards: async () => [{
        id: 'b_123',
        ref: 'jtype.board',
        title: 'jtype',
		columns: [{ key: 'ai', name: 'AI' }, { key: 'review', name: 'Review' }, { key: 'doing', name: 'Doing' }, { key: 'done', name: 'Done' }],
      }],
      putServiceKanban,
      deleteServiceKanban: async () => undefined,
    } as unknown as ApiClient;
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <ApiProvider client={api}>
          <KanbanBoardModal
            projectId="p1"
            serviceId="svc_1"
            links={[link({ done_column: 'done' })]}
            canManage
            onClose={() => {}}
          />
        </ApiProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId('jtype-board')).toBeTruthy();
    expect(screen.queryByTestId('kanban-enable-panel')).toBeNull();
    await pickOption('kanban-trigger-column', 'Review');
    fireEvent.click(screen.getByTestId('kanban-columns-save'));

    await waitFor(() => expect(putServiceKanban).toHaveBeenCalledWith('svc_1', {
      installation_id: 'jtype-1',
      board_ref: 'jtype.board',
      trigger_column: 'review',
	  work_column: 'doing',
      done_column: 'done',
      enabled: true,
    }));
    expect(screen.getByTestId('jtype-board')).toBeTruthy();
  });

  it('keeps Tab from the opened board selector inside the Kanban dialog', async () => {
    const rectSpy = vi
      .spyOn(HTMLElement.prototype, 'getClientRects')
      .mockReturnValue([{ width: 1, height: 1 }] as unknown as DOMRectList);
    try {
      const api = makeApi({
        ws_team: [{ path: 'jtype.board', configId: 'b_123' }],
        ws_solo: [{ path: 'personal.board', configId: 'b_solo' }],
      });
      renderModal(api, [
        link({ id: 'kl_1', workspace_id: 'ws_team', board_ref: 'b_123' }),
        link({ id: 'kl_2', workspace_id: 'ws_solo', board_ref: 'b_solo' }),
      ]);

      await screen.findByTestId('jtype-board');
      fireEvent.click(screen.getByTestId('kanban-board-select'));
      const listbox = await screen.findByRole('listbox');
      listbox.focus();
      fireEvent.keyDown(listbox, { key: 'Tab' });

      await waitFor(() => {
        const modal = screen.getByTestId('kanban-board-modal');
        expect(modal.contains(document.activeElement)).toBe(true);
      });
    } finally {
      rectSpy.mockRestore();
    }
  });

  it('unresolvable board: shows the fail-visible panel, not a blank modal', async () => {
    // The workspace has a .board doc but none whose config id matches the link.
    const api = makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_OTHER' }] });
    renderModal(api, [link({ board_ref: 'b_123', board_title: 'jtype' })]);

    const panel = await screen.findByTestId('kanban-board-fail');
    expect(within(panel).getByText('This board could not be opened')).toBeTruthy();
    // No board rendered.
    expect(screen.queryByTestId('jtype-board')).toBeNull();
  });

  it('maps an unavailable jtype service to an actionable retry state', async () => {
    const api = makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] });
    (api as { boardListDocuments?: unknown }).boardListDocuments = async () => {
      throw new ApiError(503, 'jtype is unreachable', {
        error: { code: 'jtype_unreachable', message: 'jtype is unreachable' },
      });
    };
    renderModal(api, [link()]);

    const panel = await screen.findByTestId('kanban-board-fail');
    expect(within(panel).getByText('Kanban is unavailable')).toBeTruthy();
    expect(within(panel).getByTestId('kanban-board-retry')).toBeTruthy();
  });

});
