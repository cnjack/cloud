/*
 * boardProxyClient — the injected `JTypeBoardDataClient` the embedded board
 * runs on (D31).
 *
 * Every board request (loads, polling, writes, board-ref resolution) goes
 * through the jcloud server-side proxy via the console's `ApiClient` (same-origin,
 * httpOnly `jcloud_session` cookie — see client.ts `req`). The jtype token is
 * resolved and applied server-side and NEVER reaches the browser.
 *
 * `subscribeBoardEvents` is intentionally ABSENT: post PR #45 the jtype live
 * WS/SSE surface rejects the mcp-scoped cluster/per-link token (403), so a proxy
 * would only relay a 403. Omitting the method makes `<JTypeBoard>` settle on
 * *visible* polling (the modal also passes `live={false}` to drop the misleading
 * "live unavailable" hint). No fake-live — fail-visible by construction.
 *
 * `deleteDocument` is likewise absent until the DELETE proxy endpoint ships (its
 * absence surfaces the board's card-delete action as a visible failure, not a
 * silent no-op).
 */
import { ApiError, apiErrorCode, type ApiClient } from '../api/client';
import { JTypeApiError, type JTypeBoardDataClient } from 'jtype-board-react';

/** Map a jcloud `ApiError` to the package's typed `JTypeApiError`. */
function toJTypeError(e: unknown): JTypeApiError {
  if (e instanceof ApiError) {
    return new JTypeApiError(e.status, apiErrorCode(e) ?? `http_${e.status}`);
  }
  if (e instanceof JTypeApiError) return e;
  // Never rethrow the raw error: it could embed request details. A generic
  // typed error routes to the board's own error panel.
  return new JTypeApiError(0, 'network_error');
}

async function wrap<T>(p: Promise<T>): Promise<T> {
  try {
    return await p;
  } catch (e) {
    throw toJTypeError(e);
  }
}

/**
 * Build the memoizable proxy-backed board client. Memoize the result: a new
 * `client` identity per render restarts the board (JTypeBoard prop contract).
 */
export function makeBoardProxyClient(
  api: ApiClient,
  projectId: string,
): JTypeBoardDataClient {
  return {
    listDocuments: (ws) => wrap(api.boardListDocuments(projectId, ws)),
    getDocument: (ws, docId) => wrap(api.boardGetDocument(projectId, ws, docId)),
    saveDocument: (ws, request) => wrap(api.boardSaveDocument(projectId, ws, request)),
    // deleteDocument: omitted until the DELETE endpoint ships (A.6).
    // subscribeBoardEvents: omitted → visible polling (A.5).
  };
}
