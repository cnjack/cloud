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
