/*
 * AuthProvider — the runtime auth gate's state machine (M4).
 *
 * Primary auth is the httpOnly `jcloud_session` cookie set by the OAuth login
 * flow (blueprint §2). A same-origin fetch carries it automatically, so there is
 * no token in JS for the cookie path. The legacy CONSOLE_TOKEN survives as an
 * "Advanced" path: entered on the sign-in screen, stored in localStorage, and
 * sent as a Bearer header.
 *
 * ONE probe (GET /api/v1/me) answers the onboarding question:
 *   200 → authenticated (user / identities / is_service)  → ready
 *   401 → reachable but not signed in                      → unauthenticated
 *   network / 5xx                                          → unreachable (setup)
 *
 * Boot also fetches GET /auth/providers so the sign-in screen can render the
 * provider buttons. While unreachable we auto-reprobe every 3s so `make up` /
 * `make port-forward` finishing is detected without a manual refresh.
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
import type { AuthProviderInfo, Me } from '../api/types';
import { fetchAuthProviders, postLogout } from '../api/client';
import {
  clearSignedOut,
  clearStoredToken,
  loadConfig,
  markSignedOut,
  resolveInitialToken,
  writeStoredToken,
} from '../api/config';
import { readQueryParam, stripQueryParams } from '../lib/url';

export type AuthStatus = 'probing' | 'unreachable' | 'unauthenticated' | 'ready';

/** Why we are at the sign-in screen (drives the message shown). */
export type UnauthReason = 'none' | 'rejected' | 'expired' | 'signed-out';

/** Post-login landing card variant, from the ?welcome= redirect param. */
export type WelcomeKind = 'first-admin' | 'new';

export type ProbeResult =
  | { kind: 'ok'; me: Me }
  | { kind: 'unauthorized' }
  | { kind: 'unreachable'; detail: string };

/** Single source of truth for "who am I, with this (optional) token?". */
export async function probeMe(token: string | undefined): Promise<ProbeResult> {
  try {
    const res = await fetch('/api/v1/me', {
      credentials: 'same-origin',
      headers: {
        Accept: 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
    });
    if (res.status === 200) {
      return { kind: 'ok', me: (await res.json()) as Me };
    }
    if (res.status === 401) return { kind: 'unauthorized' };
    // 5xx from the proxy target refusing the connection → environment, not auth.
    return { kind: 'unreachable', detail: `HTTP ${res.status}` };
  } catch {
    return { kind: 'unreachable', detail: 'network error' };
  }
}

/** The synthetic principal used in demo mode (no backend). */
const DEMO_ME: Me = {
  user: {
    id: 'u_demo',
    display_name: 'Demo user',
    avatar_url: '',
    is_cluster_admin: true,
  },
  is_service: false,
  identities: [{ provider: 'gitea', username: 'demo' }],
};

export interface AuthContextValue {
  status: AuthStatus;
  /** Meaningful only when status === 'unauthenticated'. */
  reason: UnauthReason;
  /** The current principal (null until ready). */
  me: Me | null;
  /** Configured OAuth providers for the sign-in screen (may be empty). */
  providers: AuthProviderInfo[];
  /** Set when a failed OAuth callback redirected with ?login_error=. */
  loginError: string | null;
  /** Non-null right after an OAuth login (?welcome=) → shows the welcome card. */
  welcome: WelcomeKind | null;
  /** True right after a MANUAL console-token sign-in — shows the landing card. */
  landing: boolean;
  demo: boolean;
  /** Stable getter for the http client (reads a ref, never goes stale). */
  getToken: () => string | undefined;
  /** Validate + persist a console token (Advanced path). */
  login: (token: string) => Promise<{ ok: boolean; error?: string }>;
  /** Revoke the session (POST /auth/logout) and return to sign-in. */
  logout: () => void;
  /** Dismiss the manual-login landing card and enter the console. */
  enterConsole: () => void;
  /** Dismiss the OAuth welcome card (also strips ?welcome= from the URL). */
  dismissWelcome: () => void;
  retryProbe: () => void;
  /** Session-level 401 hook for the http client (session revoked/expired). */
  handleUnauthorized: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

const REPROBE_MS = 3_000;

const LOGIN_ERROR_COPY: Record<string, string> = {
  provider_denied: 'The provider denied the sign-in request. Try again.',
  exchange_failed: 'Sign-in could not complete (token exchange failed).',
  profile_failed: 'Sign-in could not read your profile from the provider.',
  server_misconfigured: 'Sign-in is not configured on the server.',
  server_error: 'The server hit an error completing sign-in.',
};

export function AuthProvider({ children }: { children: ReactNode }) {
  const demo = useMemo(() => loadConfig().demo, []);
  const [status, setStatus] = useState<AuthStatus>(demo ? 'ready' : 'probing');
  const [reason, setReason] = useState<UnauthReason>('none');
  const [me, setMe] = useState<Me | null>(demo ? DEMO_ME : null);
  const [providers, setProviders] = useState<AuthProviderInfo[]>([]);
  const [landing, setLanding] = useState(false);

  // OAuth redirect flash params (read once at mount, then stripped from the URL).
  const [welcome, setWelcome] = useState<WelcomeKind | null>(() => {
    const w = readQueryParam('welcome');
    return w === 'first-admin' || w === 'new' ? w : null;
  });
  // Read once at mount; the param is stripped in the boot effect below.
  const [loginError] = useState<string | null>(() => {
    const e = readQueryParam('login_error');
    return e ? (LOGIN_ERROR_COPY[e] ?? 'Sign-in failed. Please try again.') : null;
  });

  // The active console token (Advanced path) lives in a ref so getToken stays
  // referentially stable — the http client is built once and reads the current
  // value. Undefined means "rely on the session cookie".
  const tokenRef = useRef<string | undefined>(demo ? undefined : resolveInitialToken());
  // Epoch guard: a probe started before a login/logout must not clobber newer state.
  const epochRef = useRef(0);

  const getToken = useCallback(() => tokenRef.current, []);

  const applyProbe = useCallback(
    (result: ProbeResult, epoch: number, hadToken: boolean) => {
      if (epoch !== epochRef.current) return; // stale probe — a newer action won
      switch (result.kind) {
        case 'ok':
          setMe(result.me);
          setStatus('ready');
          break;
        case 'unauthorized':
          if (hadToken) {
            // A stored console token is wrong — drop it so a reload doesn't
            // silently retry a known-bad credential.
            clearStoredToken();
            tokenRef.current = undefined;
            setReason('rejected');
          } else {
            setReason('none');
          }
          setMe(null);
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
    void probeMe(tok).then((r) => applyProbe(r, epoch, tok !== undefined));
  }, [applyProbe]);

  // Boot: strip the flash params we own, fetch providers, then probe. Demo skips
  // the network entirely (AuthProvider boots straight to 'ready').
  useEffect(() => {
    stripQueryParams(['welcome', 'login_error']);
    if (demo) return;
    void fetchAuthProviders().then(setProviders);
    runProbe();
  }, [demo, runProbe]);

  // While unreachable, quietly re-probe every 3s (no flicker back to 'probing').
  useEffect(() => {
    if (status !== 'unreachable') return;
    const id = window.setInterval(() => {
      const epoch = ++epochRef.current;
      const tok = tokenRef.current;
      void probeMe(tok).then((r) => {
        if (r.kind === 'unreachable') return; // still down — stay put
        applyProbe(r, epoch, tok !== undefined);
        // Providers may not have loaded if boot raced the outage; refresh them.
        void fetchAuthProviders().then(setProviders);
      });
    }, REPROBE_MS);
    return () => window.clearInterval(id);
  }, [status, applyProbe]);

  const login = useCallback(
    async (token: string): Promise<{ ok: boolean; error?: string }> => {
      const trimmed = token.trim();
      if (!trimmed) return { ok: false, error: 'Enter the console token.' };
      const epoch = ++epochRef.current;
      const result = await probeMe(trimmed);
      if (epoch !== epochRef.current) return { ok: false, error: 'Superseded.' };
      switch (result.kind) {
        case 'ok':
          writeStoredToken(trimmed);
          clearSignedOut(); // an explicit sign-in lifts the sign-out suppression
          tokenRef.current = trimmed;
          setMe(result.me);
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
    const tok = tokenRef.current;
    epochRef.current++;
    // Best-effort server-side session revoke (clears the cookie); local state is
    // cleared regardless of the result.
    void postLogout(tok);
    clearStoredToken();
    // Persist the intent: a reload must NOT resurrect the env dev token and
    // silently sign the user back in as the service principal (M6 live find).
    markSignedOut();
    tokenRef.current = undefined;
    setMe(null);
    setLanding(false);
    setWelcome(null);
    setReason('signed-out');
    setStatus('unauthenticated');
  }, []);

  const handleUnauthorized = useCallback(() => {
    // Any in-session 401: the session expired / was revoked out from under us.
    epochRef.current++;
    clearStoredToken();
    tokenRef.current = undefined;
    setMe(null);
    setLanding(false);
    setWelcome(null);
    setReason('expired');
    setStatus('unauthenticated');
  }, []);

  const enterConsole = useCallback(() => setLanding(false), []);
  const dismissWelcome = useCallback(() => {
    setWelcome(null);
    stripQueryParams(['welcome']);
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      reason,
      me,
      providers,
      loginError,
      welcome,
      landing,
      demo,
      getToken,
      login,
      logout,
      enterConsole,
      dismissWelcome,
      retryProbe: runProbe,
      handleUnauthorized,
    }),
    [
      status,
      reason,
      me,
      providers,
      loginError,
      welcome,
      landing,
      demo,
      getToken,
      login,
      logout,
      enterConsole,
      dismissWelcome,
      runProbe,
      handleUnauthorized,
    ],
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
