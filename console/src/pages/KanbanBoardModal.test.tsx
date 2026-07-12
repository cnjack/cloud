/*
 * KanbanBoardModal (D31) — the embedded board modal. We MOCK `jtype-board-react`
 * at the module level so tests never mount the heavy real BoardSurface: the stub
 * `JTypeBoard` just reflects its props onto data-attributes, and `JTypeApiError`
 * is a light typed Error (the proxy client + resolver depend on it).
 */
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ApiError, type ApiClient } from '../api/client';
import type {
  BoardEmbedLink,
  JtypeBoardColumn,
} from '../api/types';
import { pickOption } from '../test/select';

vi.mock('jtype-board-react', () => ({
  JTypeBoard: (p: {
    workspaceId: string;
    boardRef: string;
    live?: boolean;
  }) => (
    <div
      data-testid="jtype-board"
      data-workspace={p.workspaceId}
      data-boardref={p.boardRef}
      data-live={String(p.live)}
    />
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
    enabled: true,
    ...over,
  };
}

const COLS: JtypeBoardColumn[] = [{ key: 'ai', name: 'AI' }];

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
  } as unknown as ApiClient;
}

function renderModal(api: ApiClient, links: BoardEmbedLink[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={api}>
        <KanbanBoardModal projectId="p1" links={links} onClose={() => {}} />
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('KanbanBoardModal', () => {
  it('single link: renders the board with the workspace and live=false', async () => {
    const api = makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] });
    renderModal(api, [link()]);

    // The board is a working surface, not a form: it opts into Modal's bounded
    // wide layout so horizontal board scrolling stays inside the dialog.
    expect(screen.getByTestId('kanban-board-modal').getAttribute('data-size')).toBe('wide');
    const board = await screen.findByTestId('jtype-board');
    expect(board.getAttribute('data-workspace')).toBe('ws_team');
    // No SSE proxy → the board is handed live=false (visible polling).
    expect(board.getAttribute('data-live')).toBe('false');
    // Single link → no selector.
    expect(screen.queryByTestId('kanban-board-select')).toBeNull();
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

  it('keeps an invalid automation link visibly marked while its board remains viewable', async () => {
    const api = makeApi({ ws_team: [{ path: 'jtype.board', configId: 'b_123' }] });
    renderModal(api, [link({ board_status: 'invalid' })]);

    const notice = await screen.findByTestId('kanban-board-link-invalid');
    expect(notice.textContent).toMatch(/Card-triggered runs and writeback are stopped/);
    expect(await screen.findByTestId('jtype-board')).toBeTruthy();
  });
});
