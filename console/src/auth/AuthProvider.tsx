/*
 * AuthProvider — the runtime login gate's state machine.
 *
 * One probe (GET /api/v1/system) answers BOTH onboarding questions at once:
 *   200 → orchestrator reachable AND token valid   → ready
 *   401 → orchestrator reachable, token missing/bad → unauthenticated (login)
 *   anything else / network error                   → unreachable (setup guide)
 *
 * /healthz is NOT used from the browser on purpose: the dev proxy only
 * forwards /api, and /system doubles as token validation + gives us the
 * snapshot for the landing card ("you're in — here's your cluster").
 *
 * Token resolution: localStorage (manual sign-in) > VITE_CONSOLE_TOKEN (env).
 * While unreachable we auto-reprobe every 3s so `make up` / `make port-forward`
 * finishing is detected without a manual refresh.
 *
 * This is still the single-token MVP: signing in verifies the SHARED console
 * token; it is a verified trust level, not per-user identity. When OIDC lands
 * (docs/02-decision-log.md D03) only the "how a token is obtained" edge of this
 * machine changes — the states stay.
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import type { ReactNode } from 'react';
import type { SystemInfo } from '../api/types';
import {
  clearStoredToken,
  loadConfig,
  resolveInitialToken,
  writeStoredToken,
} from '../api/config';

export type AuthStatus = 'probing' | 'unreachable' | 'unauthenticated' | 'ready';

/** Why we are at the login screen (drives the message shown). */
export type UnauthReason = 'none' | 'rejected' | 'expired' | 'signed-out';

export type ProbeResult =
  | { kind: 'ok'; system: SystemInfo }
  | { kind: 'unauthorized' }
  | { kind: 'unreachable'; detail: string };

/** Single source of truth for "can I talk to the orchestrator with this token?" */
export async function probeSystem(
  token: string | undefined,
): Promise<ProbeResult> {
  try {
    const res = await fetch('/api/v1/system', {
      headers: {
        Accept: 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
    });
    if (res.status === 200) {
      return { kind: 'ok', system: (await res.json()) as SystemInfo };
    }
    if (res.status === 401) return { kind: 'unauthorized' };
    // Vite's proxy answers 5xx when the forward target refuses the connection
    // (port-forward down); the orchestrator itself never 5xxes this route
    // unless it is genuinely broken. Either way: environment, not auth.
    return { kind: 'unreachable', detail: `HTTP ${res.status}` };
  } catch {
    return { kind: 'unreachable', detail: 'network error' };
  }
}

export interface AuthContextValue {
  status: AuthStatus;
  /** Meaningful only when status === 'unauthenticated'. */
  reason: UnauthReason;
  /** Cluster snapshot captured by the last successful probe. */
  system: SystemInfo | null;
  /** True right after a MANUAL sign-in — the gate shows the landing card. */
  landing: boolean;
  demo: boolean;
  /** Stable getter for the http client (reads a ref, never goes stale). */
  getToken: () => string | undefined;
  /** Validate + persist a token. Resolves ok=false with a message on rejection. */
  login: (token: string) => Promise<{ ok: boolean; error?: string }>;
  logout: () => void;
  /** Dismiss the landing card and enter the console. */
  enterConsole: () => void;
  retryProbe: () => void;
  /** Session-level 401 hook for the http client (token rotated/revoked). */
  handleUnauthorized: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

const REPROBE_MS = 3_000;

export function AuthProvider({ children }: { children: ReactNode }) {
  const demo = useMemo(() => loadConfig().demo, []);
  const [status, setStatus] = useState<AuthStatus>(demo ? 'ready' : 'probing');
  const [reason, setReason] = useState<UnauthReason>('none');
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [landing, setLanding] = useState(false);

  // The active token lives in a ref so getToken stays referentially stable —
  // the http client is built once and always reads the current value.
  const tokenRef = useRef<string | undefined>(demo ? undefined : resolveInitialToken());
  // Epoch guard: a probe started before a login/logout must not clobber newer state.
  const epochRef = useRef(0);

  const getToken = useCallback(() => tokenRef.current, []);

  const applyProbe = useCallback(
    (result: ProbeResult, epoch: number, hadToken: boolean) => {
      if (epoch !== epochRef.current) return; // stale probe — a newer action won
      switch (result.kind) {
        case 'ok':
          setSystem(result.system);
          setStatus('ready');
          break;
        case 'unauthorized':
          if (hadToken) {
            // The stored/env token is wrong — drop the stored copy so a reload
            // doesn't silently retry a known-bad credential.
            clearStoredToken();
            tokenRef.current = undefined;
            setReason('rejected');
          } else {
            setReason('none');
          }
          setStatus('unauthenticated');
          break;
        case 'unreachable':
          setStatus('unreachable');
          break;
      }
    },
    [],
  );

  const runProbe = useCallback(() => {
    const epoch = ++epochRef.current;
    const tok = tokenRef.current;
    setStatus('probing');
    void probeSystem(tok).then((r) => applyProbe(r, epoch, tok !== undefined));
  }, [applyProbe]);

  // Boot probe (skipped entirely in demo mode).
  useEffect(() => {
    if (!demo) runProbe();
  }, [demo, runProbe]);

  // While unreachable, quietly re-probe every 3s (no status flicker back to
  // 'probing' — the setup guide stays up until the environment recovers).
  useEffect(() => {
    if (status !== 'unreachable') return;
    const id = window.setInterval(() => {
      const epoch = ++epochRef.current;
      const tok = tokenRef.current;
      void probeSystem(tok).then((r) => {
        if (r.kind === 'unreachable') return; // still down — stay put
        applyProbe(r, epoch, tok !== undefined);
      });
    }, REPROBE_MS);
    return () => window.clearInterval(id);
  }, [status, applyProbe]);

  const login = useCallback(
    async (token: string): Promise<{ ok: boolean; error?: string }> => {
      const trimmed = token.trim();
      if (!trimmed) return { ok: false, error: 'Enter the console token.' };
      const epoch = ++epochRef.current;
      const result = await probeSystem(trimmed);
      if (epoch !== epochRef.current) return { ok: false, error: 'Superseded.' };
      switch (result.kind) {
        case 'ok':
          writeStoredToken(trimmed);
          tokenRef.current = trimmed;
          setSystem(result.system);
          setLanding(true); // manual sign-in → show the landing card once
          setStatus('ready');
          return { ok: true };
        case 'unauthorized':
          return { ok: false, error: 'Token rejected by the orchestrator.' };
        case 'unreachable':
          setStatus('unreachable');
          return { ok: false, error: 'Lost the orchestrator — see setup steps.' };
      }
    },
    [],
  );

  const logout = useCallback(() => {
    epochRef.current++;
    clearStoredToken();
    tokenRef.current = undefined;
    setSystem(null);
    setLanding(false);
    setReason('signed-out');
    setStatus('unauthenticated');
  }, []);

  const handleUnauthorized = useCallback(() => {
    // Any in-session 401: the token was rotated/revoked out from under us.
    epochRef.current++;
    clearStoredToken();
    tokenRef.current = undefined;
    setSystem(null);
    setLanding(false);
    setReason('expired');
    setStatus('unauthenticated');
  }, []);

  const enterConsole = useCallback(() => setLanding(false), []);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      reason,
      system,
      landing,
      demo,
      getToken,
      login,
      logout,
      enterConsole,
      retryProbe: runProbe,
      handleUnauthorized,
    }),
    [status, reason, system, landing, demo, getToken, login, logout, enterConsole, runProbe, handleUnauthorized],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within <AuthProvider>');
  return ctx;
}

/** Null-safe variant so ApiProvider keeps working in tests without the gate. */
export function useOptionalAuth(): AuthContextValue | null {
  return useContext(AuthContext);
}
