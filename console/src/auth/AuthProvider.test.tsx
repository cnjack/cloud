/*
 * AuthProvider + OnboardingGate — the M4 auth gate state machine (probe = /me):
 *   - orchestrator down → setup guide; recovers → advances to sign-in
 *   - reachable, no session → sign-in (provider buttons + Advanced console token)
 *   - valid console token → landing card → app; stored valid token → straight in
 *   - stored token rejected → sign-in with rotation notice, stored copy cleared
 *   - session 401 (http-client hook) → back to sign-in
 *   - sign-out clears the stored token
 *   - OAuth ?welcome= → welcome card, then Get started → app (param stripped)
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { AuthProvider, useAuth } from './AuthProvider';
import { OnboardingGate } from '../pages/OnboardingGate';
import { TOKEN_STORAGE_KEY } from '../api/config';
import type { AuthProviderInfo, Me } from '../api/types';

function me(): Me {
  return {
    user: { id: 'u1', display_name: 'Ada Lovelace', avatar_url: '', is_cluster_admin: true },
    is_service: false,
    identities: [{ provider: 'gitea', username: 'ada' }],
  };
}

const VALID_TOKEN = 'good-token';

function jsonRes(status: number, body: unknown): Response {
  const text = JSON.stringify(body);
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: `S${status}`,
    json: async () => JSON.parse(text),
    text: async () => text,
  } as unknown as Response;
}

/**
 * Controllable orchestrator stub. `mode.current` flips 'down' (refused) / 'up'.
 * /auth/providers returns `providers`; /api/v1/me returns the user for a valid
 * console token (or, when cookieSession, unconditionally) else 401.
 */
function stubFetch(
  mode: { current: 'down' | 'up' },
  opts: { providers?: AuthProviderInfo[]; cookieSession?: boolean } = {},
) {
  const providers = opts.providers ?? [];
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    if (mode.current === 'down') throw new TypeError('fetch failed');
    const u = String(url);
    if (u.includes('/auth/providers')) return jsonRes(200, { providers });
    if (u.includes('/auth/logout')) return jsonRes(200, { ok: true });
    // /api/v1/me
    const headers = (init?.headers ?? {}) as Record<string, string>;
    if (opts.cookieSession || headers.Authorization === `Bearer ${VALID_TOKEN}`) {
      return jsonRes(200, me());
    }
    return jsonRes(401, { error: { code: 'unauthorized' } });
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

/** jsdom here ships no window.localStorage — install an in-memory Storage. */
function installMemoryStorage(): Storage {
  const store = new Map<string, string>();
  const storage: Storage = {
    getItem: (k) => (store.has(k) ? store.get(k)! : null),
    setItem: (k, v) => void store.set(k, String(v)),
    removeItem: (k) => void store.delete(k),
    clear: () => void store.clear(),
    key: (i) => [...store.keys()][i] ?? null,
    get length() {
      return store.size;
    },
  };
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true });
  return storage;
}

function AuthProbe() {
  const auth = useAuth();
  return (
    <>
      <button data-testid="force-401" onClick={auth.handleUnauthorized} />
      <button data-testid="do-logout" onClick={auth.logout} />
    </>
  );
}

function renderGate() {
  return render(
    <AuthProvider>
      <MemoryRouter>
        <OnboardingGate>
          <div data-testid="app">the console</div>
          <AuthProbe />
        </OnboardingGate>
      </MemoryRouter>
    </AuthProvider>,
  );
}

async function signInWithToken(token: string) {
  fireEvent.change(screen.getByLabelText('Console token'), { target: { value: token } });
  fireEvent.click(screen.getByRole('button', { name: 'Sign in with token' }));
}

beforeEach(() => {
  installMemoryStorage();
  window.history.replaceState(null, '', '/');
});
afterEach(() => vi.unstubAllGlobals());

describe('OnboardingGate — environment layer', () => {
  it('shows the setup guide while unreachable, then advances when the orchestrator answers', async () => {
    const mode = { current: 'down' as 'down' | 'up' };
    stubFetch(mode);
    renderGate();

    await waitFor(() => expect(screen.getByTestId('setup-guide')).toBeTruthy());
    expect(screen.getByText('make up')).toBeTruthy();

    mode.current = 'up';
    fireEvent.click(screen.getByRole('button', { name: 'Check now' }));
    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
  });
});

describe('OnboardingGate — sign-in layer (providers + Advanced)', () => {
  it('renders provider buttons and keeps the console token behind Advanced', async () => {
    stubFetch(
      { current: 'up' },
      { providers: [{ id: 'gitea', name: 'Gitea', login_url: '/auth/login/gitea' }] },
    );
    renderGate();

    await waitFor(() => expect(screen.getByTestId('provider-buttons')).toBeTruthy());
    const btn = screen.getByText('Continue with Gitea').closest('a')!;
    expect(btn.getAttribute('href')).toBe('/auth/login/gitea');

    // Advanced (console token) is collapsed until toggled.
    expect(screen.queryByTestId('console-token-form')).toBeNull();
    fireEvent.click(screen.getByTestId('advanced-toggle'));
    expect(screen.getByTestId('console-token-form')).toBeTruthy();
  });

  it('auto-expands Advanced when no provider is configured', async () => {
    stubFetch({ current: 'up' }, { providers: [] });
    renderGate();
    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    expect(screen.queryByTestId('provider-buttons')).toBeNull();
    expect(screen.getByTestId('console-token-form')).toBeTruthy();
  });

  it('signs in with a valid console token: landing card, stored token, then the app', async () => {
    stubFetch({ current: 'up' }, { providers: [] });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    await signInWithToken(VALID_TOKEN);

    await waitFor(() => expect(screen.getByTestId('landing-card')).toBeTruthy());
    expect(screen.getByText('Ada Lovelace')).toBeTruthy(); // principal fact
    expect(window.localStorage.getItem(TOKEN_STORAGE_KEY)).toBe(VALID_TOKEN);

    fireEvent.click(screen.getByRole('button', { name: 'Enter console' }));
    expect(screen.getByTestId('app')).toBeTruthy();
  });

  it('rejects a bad token inline without leaving the sign-in screen', async () => {
    stubFetch({ current: 'up' }, { providers: [] });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    await signInWithToken('wrong-token');

    await waitFor(() => expect(screen.getByText(/rejected by the orchestrator/i)).toBeTruthy());
    expect(window.localStorage.getItem(TOKEN_STORAGE_KEY)).toBeNull();
  });

  it('boots straight into the app when the stored token is valid (no landing card)', async () => {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, VALID_TOKEN);
    stubFetch({ current: 'up' });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('app')).toBeTruthy());
    expect(screen.queryByTestId('landing-card')).toBeNull();
  });

  it('drops a stale stored token and explains the rotation on sign-in', async () => {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, 'rotated-away');
    stubFetch({ current: 'up' }, { providers: [] });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    expect(screen.getByText(/saved token was rejected/i)).toBeTruthy();
    expect(window.localStorage.getItem(TOKEN_STORAGE_KEY)).toBeNull();
  });
});

describe('OnboardingGate — welcome card (OAuth redirect)', () => {
  it('shows the first-admin welcome from ?welcome=first-admin, then enters the console', async () => {
    window.history.replaceState(null, '', '/?welcome=first-admin');
    stubFetch({ current: 'up' }, { cookieSession: true });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('welcome-card')).toBeTruthy());
    expect(screen.getByText(/first user/i)).toBeTruthy();
    // The param is stripped from the address bar so a refresh won't replay it.
    expect(window.location.search).not.toContain('welcome');

    fireEvent.click(screen.getByTestId('welcome-enter'));
    expect(screen.getByTestId('app')).toBeTruthy();
  });
});

describe('OnboardingGate — session layer', () => {
  it('returns to sign-in when the http client reports a session 401', async () => {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, VALID_TOKEN);
    stubFetch({ current: 'up' });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('app')).toBeTruthy());
    fireEvent.click(screen.getByTestId('force-401'));

    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    expect(screen.getByText(/session ended/i)).toBeTruthy();
    expect(window.localStorage.getItem(TOKEN_STORAGE_KEY)).toBeNull();
  });

  it('sign-out clears the stored token and shows the signed-out notice', async () => {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, VALID_TOKEN);
    stubFetch({ current: 'up' });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('app')).toBeTruthy());
    fireEvent.click(screen.getByTestId('do-logout'));

    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    expect(screen.getByText('Signed out.')).toBeTruthy();
    expect(window.localStorage.getItem(TOKEN_STORAGE_KEY)).toBeNull();
  });
});
