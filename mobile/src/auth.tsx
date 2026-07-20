/*
 * auth.tsx — the mobile login gate (docs/17 §7.2).
 *
 * Login decision (see reports/M6-mobile.md): the console's OAuth cookie flow
 * targets a browser (redirects, same-origin cookies); inside a Tauri webview
 * the simplest RELIABLE credential is a user session token sent as a Bearer
 * header — the same credential the e2e rigs seed and jcode's device API
 * accepts. The user pastes the token once; it persists in localStorage.
 *
 * The cloud URL follows jcode login's rule (internal/cloud/client.go
 * ValidateCloudURL): https everywhere; plain http only for loopback. The
 * Android emulator's host-loopback alias 10.0.2.2 is exempted too, mirroring
 * the loopback intent (documented deviation).
 */
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';

export const DEFAULT_CLOUD_URL = 'https://cloud.j-code.net';

const URL_KEY = 'jmobile.cloud_url';
const TOKEN_KEY = 'jmobile.token';

/** Loopback hosts allowed to use plain http (dev rigs only). */
const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '::1', '[::1]', '10.0.2.2']);

export type UrlValidation = { ok: true; url: string } | { ok: false; reason: 'invalid' | 'http_not_allowed' };

/** Mirrors jcode's ValidateCloudURL (+ the 10.0.2.2 emulator alias). */
export function validateCloudUrl(raw: string): UrlValidation {
  const trimmed = raw.trim();
  if (!trimmed) return { ok: false, reason: 'invalid' };
  let u: URL;
  try {
    u = new URL(trimmed);
  } catch {
    return { ok: false, reason: 'invalid' };
  }
  if (!u.host) return { ok: false, reason: 'invalid' };
  if (u.protocol === 'https:') return { ok: true, url: trimmed.replace(/\/+$/, '') };
  if (u.protocol === 'http:' && LOOPBACK_HOSTS.has(u.hostname)) {
    return { ok: true, url: trimmed.replace(/\/+$/, '') };
  }
  if (u.protocol === 'http:') return { ok: false, reason: 'http_not_allowed' };
  return { ok: false, reason: 'invalid' };
}

export interface Me {
  user: { id: string; display_name: string; avatar_url?: string };
  is_service: boolean;
}

export type LoginResult =
  | { ok: true }
  | { ok: false; reason: 'unauthorized' | 'unreachable' | 'failed'; message?: string };

export interface AuthState {
  /** False until a token has been validated (the router shows LoginPage). */
  signedIn: boolean;
  cloudUrl: string;
  token: string;
  me: Me | null;
  login: (cloudUrl: string, token: string) => Promise<LoginResult>;
  logout: () => void;
}

const AuthContext = createContext<AuthState | null>(null);

function loadStored(): { cloudUrl: string; token: string } | null {
  try {
    const cloudUrl = localStorage.getItem(URL_KEY);
    const token = localStorage.getItem(TOKEN_KEY);
    if (cloudUrl && token) return { cloudUrl, token };
  } catch {
    /* storage unavailable */
  }
  return null;
}

export function MobileAuthProvider({ children }: { children: ReactNode }) {
  const [stored, setStored] = useState(loadStored);
  const [me, setMe] = useState<Me | null>(null);

  const logout = useCallback(() => {
    try {
      localStorage.removeItem(URL_KEY);
      localStorage.removeItem(TOKEN_KEY);
    } catch {
      /* storage unavailable */
    }
    setMe(null);
    setStored(null);
  }, []);

  // Boot: validate the restored token once (a revoked session drops back to
  // the login page instead of failing on every device call).
  useEffect(() => {
    if (!stored || me) return;
    let cancelled = false;
    void (async () => {
      try {
        const res = await fetch(`${stored.cloudUrl}/api/v1/me`, {
          headers: { Accept: 'application/json', Authorization: `Bearer ${stored.token}` },
        });
        if (cancelled) return;
        if (res.status === 401) {
          logout();
          return;
        }
        if (res.ok) setMe((await res.json()) as Me);
        // Other failures (offline cloud): keep the session; pages surface
        // their own error/retry states.
      } catch {
        /* unreachable — pages surface retry states */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [stored, me, logout]);

  const login = useCallback(async (cloudUrl: string, token: string): Promise<LoginResult> => {
    const trimmed = token.trim();
    if (!trimmed) return { ok: false, reason: 'unauthorized' };
    try {
      const res = await fetch(`${cloudUrl}/api/v1/me`, {
        headers: { Accept: 'application/json', Authorization: `Bearer ${trimmed}` },
      });
      if (res.status === 401) return { ok: false, reason: 'unauthorized' };
      if (!res.ok) return { ok: false, reason: 'failed', message: `HTTP ${res.status}` };
      const meJson = (await res.json()) as Me;
      try {
        localStorage.setItem(URL_KEY, cloudUrl);
        localStorage.setItem(TOKEN_KEY, trimmed);
      } catch {
        /* storage unavailable */
      }
      setMe(meJson);
      setStored({ cloudUrl, token: trimmed });
      return { ok: true };
    } catch (err) {
      return { ok: false, reason: 'unreachable', message: String(err) };
    }
  }, []);

  const value = useMemo<AuthState>(
    () => ({
      signedIn: stored !== null,
      cloudUrl: stored?.cloudUrl ?? '',
      token: stored?.token ?? '',
      me,
      login,
      logout,
    }),
    [stored, me, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

/** Always present; check `signedIn` to decide between login and the app. */
export function useMobileAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useMobileAuth must be used within <MobileAuthProvider>');
  return ctx;
}
