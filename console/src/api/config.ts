/*
 * config.ts — resolves runtime config from Vite env vars (all VITE_-prefixed so
 * they're safe to expose to the browser bundle).
 */

/**
 * Console role. The MVP is single-tenant with ONE static console token and no
 * OIDC/RBAC yet (that's a documented future). So role is NOT real authz — it's a
 * build/runtime signal that names the current trust level of the token holder:
 *  - `cluster-admin` (default) — the operator who holds the console token; sees
 *    the read-only Cluster view.
 *  - `project-admin` — a scoped-down presentation that hides the Cluster link.
 *
 * This is honest: it labels the trust level rather than faking per-request
 * authorization. When OIDC lands, this resolves from the identity claim instead.
 */
export type Role = 'cluster-admin' | 'project-admin';

export interface RuntimeConfig {
  demo: boolean;
  consoleToken: string | undefined;
  role: Role;
}

export function resolveRole(raw: string | undefined): Role {
  return raw === 'project-admin' ? 'project-admin' : 'cluster-admin';
}

export function loadConfig(): RuntimeConfig {
  const env = import.meta.env;
  return {
    demo: env.VITE_DEMO === '1' || env.VITE_DEMO === 'true',
    consoleToken: env.VITE_CONSOLE_TOKEN || undefined,
    role: resolveRole(env.VITE_ROLE),
  };
}

/* --- runtime token storage (login gate) ----------------------------------
 *
 * The login gate persists the console token in localStorage so a manual
 * sign-in survives reloads. Resolution order: stored token > VITE_CONSOLE_TOKEN.
 *
 * Security note (deliberate MVP tradeoff): localStorage is readable by any
 * script on the origin, i.e. an XSS hole leaks the token. Acceptable for the
 * single-tenant dev console (localhost + a dev token); a real deployment moves
 * to httpOnly-cookie sessions / OIDC (see docs/02-decision-log.md D03).
 */

export const TOKEN_STORAGE_KEY = 'jcloud.console.token';

/** Read the stored token; tolerates storage being unavailable (SSR/private mode). */
export function readStoredToken(): string | undefined {
  try {
    return window.localStorage.getItem(TOKEN_STORAGE_KEY) || undefined;
  } catch {
    return undefined;
  }
}

export function writeStoredToken(token: string): void {
  try {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, token);
  } catch {
    /* storage unavailable — session just won't survive a reload */
  }
}

export function clearStoredToken(): void {
  try {
    window.localStorage.removeItem(TOKEN_STORAGE_KEY);
  } catch {
    /* ignore */
  }
}

/**
 * Explicit-sign-out marker (M6 live find): signing out reloads the page, and
 * without this the VITE_CONSOLE_TOKEN dev default silently signed the user
 * straight back in as the service principal. An explicit sign-out must stick —
 * the marker suppresses the env fallback until the user signs in again.
 */
export const SIGNED_OUT_KEY = 'jcloud.console.signed-out';

export function markSignedOut(): void {
  try {
    window.localStorage.setItem(SIGNED_OUT_KEY, '1');
  } catch {
    /* ignore */
  }
}

export function clearSignedOut(): void {
  try {
    window.localStorage.removeItem(SIGNED_OUT_KEY);
  } catch {
    /* ignore */
  }
}

function isSignedOut(): boolean {
  try {
    return window.localStorage.getItem(SIGNED_OUT_KEY) === '1';
  } catch {
    return false;
  }
}

/**
 * Boot-time token: a manually saved token wins over the env default; after an
 * explicit sign-out the env default is suppressed entirely.
 */
export function resolveInitialToken(): string | undefined {
  const stored = readStoredToken();
  if (stored) return stored;
  if (isSignedOut()) return undefined;
  return loadConfig().consoleToken;
}
