/*
 * ApiProvider.tsx — exposes the single ApiClient (real HTTP or in-memory mock)
 * to the tree, and reports which mode we're in so the shell can show a DEMO tag.
 */
import { createContext, useContext, useMemo } from 'react';
import type { ReactNode } from 'react';
import type { ApiClient } from './client';
import { createHttpClient } from './client';
import { createMockClient } from './mockClient';
import { loadConfig, resolveRole, type Role } from './config';

interface ApiContextValue {
  client: ApiClient;
  demo: boolean;
  role: Role;
}

const ApiContext = createContext<ApiContextValue | null>(null);

export function ApiProvider({
  children,
  client,
  role,
}: {
  children: ReactNode;
  /** Injectable for tests; defaults to env-selected client. */
  client?: ApiClient;
  /** Injectable role for tests/stories; defaults to the env-resolved role. */
  role?: Role;
}) {
  const value = useMemo<ApiContextValue>(() => {
    if (client) {
      return { client, demo: false, role: resolveRole(role) };
    }
    const cfg = loadConfig();
    return {
      client: cfg.demo ? createMockClient() : createHttpClient(cfg.consoleToken),
      demo: cfg.demo,
      role: role ?? cfg.role,
    };
  }, [client, role]);

  return <ApiContext.Provider value={value}>{children}</ApiContext.Provider>;
}

export function useApi(): ApiClient {
  const ctx = useContext(ApiContext);
  if (!ctx) throw new Error('useApi must be used within <ApiProvider>');
  return ctx.client;
}

export function useDemoMode(): boolean {
  const ctx = useContext(ApiContext);
  return ctx?.demo ?? false;
}

/** The current console role signal (presentation-only until OIDC/RBAC lands). */
export function useRole(): Role {
  const ctx = useContext(ApiContext);
  return ctx?.role ?? 'cluster-admin';
}
