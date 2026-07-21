/*
 * cloudOAuth.ts — the mobile OAuth round trip (M11 W2).
 *
 * "Sign in with cloud" opens the SYSTEM browser on
 * `<cloud>/auth/login/<provider>?client=mobile`; after the provider round
 * trip the orchestrator callback mints the session and 302s to the FIXED
 * `jcode://auth#token=<session-token>` deep link, which the OS delivers back
 * to this app (tauri-plugin-deep-link; Android intent-filter / iOS URL types
 * for the jcode scheme). The token rides in the URL fragment so it never
 * lands in cloud access logs or browser history.
 *
 * The cloud URL typed on the login page is remembered in localStorage so the
 * deep-link return (which carries only the token) knows which cloud to
 * validate against.
 */
import { getCurrent, onOpenUrl } from '@tauri-apps/plugin-deep-link';
import { openUrl } from '@tauri-apps/plugin-opener';
import { DEFAULT_CLOUD_URL, validateCloudUrl } from './auth';

const PENDING_CLOUD_KEY = 'jmobile.oauth_cloud_url';

/** Fallback provider when /auth/providers cannot be reached (see report). */
const DEFAULT_PROVIDER = 'gitea';

interface AuthProvidersView {
  providers?: { id: string; name?: string; login_url?: string }[];
}

/** pickProvider prefers gitea, else the first configured provider. */
export async function pickProvider(cloudUrl: string): Promise<string> {
  try {
    const res = await fetch(`${cloudUrl}/auth/providers`, { headers: { Accept: 'application/json' } });
    if (res.ok) {
      const data = (await res.json()) as AuthProvidersView;
      const ids = (data.providers ?? []).map((p) => p.id).filter((id): id is string => Boolean(id));
      if (ids.includes(DEFAULT_PROVIDER)) return DEFAULT_PROVIDER;
      if (ids.length > 0) return ids[0] as string;
    }
  } catch {
    /* unreachable — fall back to the default */
  }
  return DEFAULT_PROVIDER;
}

export type StartCloudLoginResult = { ok: true } | { ok: false; reason: 'invalid' | 'http_not_allowed' | 'open_failed' };

/** startCloudLogin remembers the cloud URL and opens the system browser on
 * the provider's authorize flow (client=mobile). */
export async function startCloudLogin(rawCloudUrl: string): Promise<StartCloudLoginResult> {
  const url = validateCloudUrl(rawCloudUrl);
  if (!url.ok) return { ok: false, reason: url.reason };
  const provider = await pickProvider(url.url);
  try {
    localStorage.setItem(PENDING_CLOUD_KEY, url.url);
  } catch {
    /* storage unavailable — the deep-link return uses the current form value */
  }
  try {
    await openUrl(`${url.url}/auth/login/${provider}?client=mobile`);
  } catch {
    return { ok: false, reason: 'open_failed' };
  }
  return { ok: true };
}

/** parseAuthDeepLink extracts the session token from `jcode://auth#token=…`. */
export function parseAuthDeepLink(raw: string): string | null {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return null;
  }
  if (u.protocol !== 'jcode:' || u.hostname !== 'auth') return null;
  const m = /^#token=(.+)$/.exec(u.hash);
  return m?.[1] ? decodeURIComponent(m[1]) : null;
}

/** consumePendingCloudUrl reads (and clears) the cloud remembered at login
 * start; falls back to the stored session cloud, then the default. */
export function consumePendingCloudUrl(fallback: string): string {
  let pending = '';
  try {
    pending = localStorage.getItem(PENDING_CLOUD_KEY) ?? '';
    if (pending) localStorage.removeItem(PENDING_CLOUD_KEY);
  } catch {
    /* storage unavailable */
  }
  const candidate = pending || fallback || DEFAULT_CLOUD_URL;
  const url = validateCloudUrl(candidate);
  return url.ok ? url.url : DEFAULT_CLOUD_URL;
}

/** watchAuthDeepLinks invokes cb(token) for every jcode://auth deep link —
 * warm (onOpenUrl, app already running) and cold start (getCurrent). Returns
 * an unlisten function. Outside Tauri it is a no-op. */
export async function watchAuthDeepLinks(cb: (token: string) => void): Promise<() => void> {
  if (!('__TAURI_INTERNALS__' in window)) return () => {};
  const unlisten = await onOpenUrl((urls) => {
    for (const raw of urls) {
      const token = parseAuthDeepLink(raw);
      if (token) cb(token);
    }
  });
  try {
    const current = await getCurrent();
    for (const raw of current ?? []) {
      const token = parseAuthDeepLink(raw);
      if (token) cb(token);
    }
  } catch {
    /* not launched via a deep link */
  }
  return unlisten;
}
