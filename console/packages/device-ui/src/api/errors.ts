/*
 * errors.ts — the typed API error + token-source primitives shared by every
 * HTTP layer in this package. These are the SAME definitions the console's
 * api/client.ts historically owned; client.ts now re-exports them from here so
 * both codebases throw/branch on one ApiError class.
 */

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public body?: unknown,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

/**
 * The typed error `code` from the repo-standard `{ "error": { code, message } }`
 * envelope (11-api.md §0), or undefined for a non-ApiError / bodyless error. Used
 * to branch on codes the UI must treat specially (e.g. `device_offline`) rather
 * than string-matching the human message.
 */
export function apiErrorCode(err: unknown): string | undefined {
  if (err instanceof ApiError && err.body && typeof err.body === 'object') {
    const e = (err.body as { error?: { code?: string } }).error;
    if (e && typeof e === 'object' && typeof e.code === 'string') return e.code;
  }
  return undefined;
}

/**
 * Token source: a static string (tests/legacy) or a getter (login gate) so the
 * client picks up runtime token changes without being rebuilt.
 */
export type TokenSource = string | undefined | (() => string | undefined);
