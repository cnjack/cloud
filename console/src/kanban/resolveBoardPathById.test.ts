import { describe, expect, it, vi } from 'vitest';
import { JTypeApiError, type JTypeBoardDataClient } from 'jtype-board-react';
import { resolveBoardPathById } from './resolveBoardPathById';

function doc(relativePath: string, id: string) {
  return {
    list: {
      id: `doc-${relativePath}`,
      relativePath,
      title: relativePath,
      isPublished: true,
      contentHash: 'h',
      updatedClock: 1,
      versionId: 'v1' as string | null,
    },
    full: {
      relativePath,
      title: relativePath,
      isPublished: true,
      content: JSON.stringify({ id }),
      contentHash: 'h',
      versionId: 'v1',
      updatedClock: 1,
    },
  };
}

/**
 * A fake client backed by an in-memory doc list. `contentOverride` lets a test
 * inject unparseable content for a specific docId.
 */
function fakeClient(
  docs: ReturnType<typeof doc>[],
  contentOverride: Record<string, string> = {},
): { client: JTypeBoardDataClient; getCalls: string[] } {
  const getCalls: string[] = [];
  const byId = new Map(docs.map((d) => [d.list.id, d]));
  const client: JTypeBoardDataClient = {
    listDocuments: async () => docs.map((d) => d.list),
    getDocument: async (_ws, docId) => {
      getCalls.push(docId);
      const d = byId.get(docId)!;
      const override = contentOverride[docId];
      return override !== undefined ? { ...d.full, content: override } : d.full;
    },
    saveDocument: async () => {
      throw new Error('unused');
    },
  };
  return { client, getCalls };
}

describe('resolveBoardPathById', () => {
  it('returns the relativePath of the .board doc whose config.id matches', async () => {
    const { client } = fakeClient([doc('jtype.board', 'b_123')]);
    const path = await resolveBoardPathById(client, 'ws_1', 'b_123');
    expect(path).toBe('jtype.board');
  });

  it('with multiple .board docs, only the id match wins', async () => {
    const { client } = fakeClient([
      doc('alpha.board', 'b_aaa'),
      doc('beta.board', 'b_bbb'),
      doc('gamma.board', 'b_ccc'),
    ]);
    expect(await resolveBoardPathById(client, 'ws_1', 'b_bbb')).toBe('beta.board');
  });

  it('skips non-.board documents (never inspects a card .md)', async () => {
    const notBoard = doc('cards/task.md', 'b_123');
    const { client, getCalls } = fakeClient([notBoard, doc('real.board', 'b_123')]);
    const path = await resolveBoardPathById(client, 'ws_1', 'b_123');
    expect(path).toBe('real.board');
    // The .md was filtered out before any getDocument call.
    expect(getCalls).not.toContain(notBoard.list.id);
  });

  it('skips a .board doc with unparseable content and finds a valid sibling', async () => {
    const broken = doc('broken.board', 'b_x');
    const good = doc('good.board', 'b_123');
    const { client } = fakeClient([broken, good], {
      [broken.list.id]: 'not json {{{',
    });
    expect(await resolveBoardPathById(client, 'ws_1', 'b_123')).toBe('good.board');
  });

  it('throws JTypeApiError(404, board_not_found) when nothing matches', async () => {
    const { client } = fakeClient([doc('jtype.board', 'b_other')]);
    await expect(resolveBoardPathById(client, 'ws_1', 'b_missing')).rejects.toMatchObject({
      status: 404,
      code: 'board_not_found',
    });
    await expect(resolveBoardPathById(client, 'ws_1', 'b_missing')).rejects.toBeInstanceOf(
      JTypeApiError,
    );
  });

  it('is case-insensitive on the .board extension', async () => {
    const { client } = fakeClient([doc('Jcode.BOARD', 'b_up')]);
    expect(await resolveBoardPathById(client, 'ws_1', 'b_up')).toBe('Jcode.BOARD');
  });

  it('passes a soft-link ref (a typed name/path, not a b_ id) straight through, without fetching', async () => {
    // A soft-created link stores board_ref as the raw name/path; it is already a
    // valid <JTypeBoard boardRef>, so no id→path lookup (no workspace fetch) runs.
    const { client, getCalls } = fakeClient([doc('jtype.board', 'b_123')]);
    expect(await resolveBoardPathById(client, 'ws_1', 'jtype')).toBe('jtype');
    expect(await resolveBoardPathById(client, 'ws_1', 'jtype.board')).toBe('jtype.board');
    expect(getCalls).toHaveLength(0);
  });

  it('matches the mocked listDocuments/getDocument spies with the workspace id', async () => {
    const list = vi.fn(async () => [doc('jtype.board', 'b_123').list]);
    const get = vi.fn(async () => doc('jtype.board', 'b_123').full);
    const client = {
      listDocuments: list,
      getDocument: get,
      saveDocument: async () => {
        throw new Error('unused');
      },
    } as unknown as JTypeBoardDataClient;
    await resolveBoardPathById(client, 'ws_42', 'b_123');
    expect(list).toHaveBeenCalledWith('ws_42');
    expect(get).toHaveBeenCalledWith('ws_42', 'doc-jtype.board');
  });
});
