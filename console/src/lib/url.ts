/*
 * url.ts — tiny helpers for the OAuth redirect "flash" params the orchestrator
 * appends to CONSOLE_URL (blueprint §2/§3): ?welcome=, ?linked=, ?link_error=,
 * ?login_error=. Each is read once and then stripped from the address bar with
 * history.replaceState so a refresh doesn't replay the card/toast.
 */

/** Read a single query param from the current URL (SSR/`no window`-safe). */
export function readQueryParam(name: string): string | null {
  if (typeof window === 'undefined') return null;
  return new URLSearchParams(window.location.search).get(name);
}

/**
 * Remove the given query params from the address bar without a navigation,
 * preserving any others. No-op when none are present.
 */
export function stripQueryParams(names: string[]): void {
  if (typeof window === 'undefined') return;
  const url = new URL(window.location.href);
  let changed = false;
  for (const n of names) {
    if (url.searchParams.has(n)) {
      url.searchParams.delete(n);
      changed = true;
    }
  }
  if (!changed) return;
  const search = url.searchParams.toString();
  window.history.replaceState(
    null,
    '',
    url.pathname + (search ? `?${search}` : '') + url.hash,
  );
}
