/*
 * resolveBoardPathById — bridge the board_ref id/name gap (D31).
 *
 * A kanban link stores `board_ref` as the board's opaque `config.id` (`b_…`,
 * the value the poller matches on after canonicalization). But
 * `<JTypeBoard boardRef>` wants a **name / `.board` relativePath** — it
 * re-resolves the board by name via the package's `resolveBoard` (never by
 * config id). So before rendering the board we translate the config id into the
 * relativePath of the `.board` document whose parsed `config.id` matches.
 *
 * Pure + unit-testable: it drives only the injected `JTypeBoardDataClient`
 * (the member+ jcloud proxy), so it needs no owner-only discovery endpoint and
 * one authz threshold. On no match it throws a typed `JTypeApiError(404,
 * 'board_not_found')` so the modal shows a fail-visible panel, never a blank
 * board (red line #1).
 */
import { JTypeApiError, type JTypeBoardDataClient } from 'jtype-board-react';

/**
 * Translate a link's `board_ref` into the `.board` relativePath `<JTypeBoard>`
 * wants. A canonicalized link stores the opaque `config.id` (`b_…`) — resolve it
 * to the relativePath of the `.board` doc whose parsed `config.id` matches. But a
 * SOFT-created (unvalidated) link stores the raw name/path the owner typed (the
 * poller only canonicalizes it once it can reach the board), and that is already
 * a valid `boardRef` — pass it straight through (also avoids listing the
 * workspace). Only `b_…` refs need the id→path lookup.
 */
export async function resolveBoardPathById(
  client: JTypeBoardDataClient,
  workspaceId: string,
  boardConfigId: string,
): Promise<string> {
  if (!boardConfigId.startsWith('b_')) {
    // Not a config id — a soft link's typed name/path; JTypeBoard resolves it.
    return boardConfigId;
  }
  const docs = await client.listDocuments(workspaceId);
  const boards = docs.filter((d) =>
    d.relativePath.toLowerCase().endsWith('.board'),
  );
  for (const d of boards) {
    const doc = await client.getDocument(workspaceId, d.id);
    try {
      const parsed = JSON.parse(doc.content) as { id?: string };
      if (parsed.id === boardConfigId) return d.relativePath;
    } catch {
      // A `.board` document with unparseable content can't be the match — skip
      // it (a corrupt sibling must not mask a valid board elsewhere).
    }
  }
  throw new JTypeApiError(404, 'board_not_found');
}
