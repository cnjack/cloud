/*
 * AuthProvider + OnboardingGate — the runtime login gate state machine:
 *   - orchestrator down → setup guide; recovers → advances to sign-in
 *   - reachable, no token → sign-in; valid token → landing card → app
 *   - stored token valid → straight to the app (no landing ceremony)
 *   - stored token rejected → sign-in with rotation notice, stored copy cleared
 *   - session 401 (http-client hook) → back to sign-in
 *   - sign-out clears the stored token
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { AuthProvider, useAuth } from './AuthProvider';
import { OnboardingGate } from '../pages/OnboardingGate';
import { TOKEN_STORAGE_KEY } from '../api/config';
import type { SystemInfo } from '../api/types';

function snapshot(): SystemInfo {
  return {
    version: { version: '1.4.0', commit: 'abc1234' },
    capacity: { max_concurrent_runs: 4, running: 1, queued: 2, scheduling: 0 },
    guardrails: { run_timeout_seconds: 1800, job_ttl_seconds: 3600 },
    provider: { gitea_enabled: true, gitea_url: 'http://gitea:3000' },
    runner: { image: 'jcloud/runner:dev' },
    namespace: 'jcloud',
    launcher: 'kubernetes',
  };
}

/**
 * Controllable orchestrator stub: `mode.current` flips between 'down'
 * (connection refused) and 'up' (answers 200 for VALID_TOKEN, else 401).
 */
const VALID_TOKEN = 'good-token';

function stubFetch(mode: { current: 'down' | 'up' }) {
  const fn = vi.fn(async (_url: string, init?: RequestInit) => {
    if (mode.current === 'down') throw new TypeError('fetch failed');
    const headers = (init?.headers ?? {}) as Record<string, string>;
    if (headers.Authorization === `Bearer ${VALID_TOKEN}`) {
      const body = JSON.stringify(snapshot());
      return {
        ok: true,
        status: 200,
        statusText: 'OK',
        json: async () => JSON.parse(body),
        text: async () => body,
      } as unknown as Response;
    }
    return {
      ok: false,
      status: 401,
      statusText: 'Unauthorized',
      json: async () => ({ error: { code: 'unauthorized' } }),
      text: async () => '',
    } as unknown as Response;
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

/**
 * This jsdom build ships no window.localStorage; install an in-memory Storage
 * so the token-persistence paths are actually exercised (the production code
 * only touches storage behind try/catch, so it must not silently no-op here).
 */
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
  Object.defineProperty(window, 'localStorage', {
    value: storage,
    configurable: true,
  });
  return storage;
}

/** Exposes imperative auth actions the shell would normally wire up. */
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
  // MemoryRouter mirrors production, where BrowserRouter wraps the gate
  // (the Wordmark on gate screens is a router <Link>).
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

async function signIn(token: string) {
  fireEvent.change(screen.getByLabelText('Console token'), {
    target: { value: token },
  });
  fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
}

beforeEach(() => installMemoryStorage());
afterEach(() => vi.unstubAllGlobals());

describe('OnboardingGate — environment layer', () => {
  it('shows the setup guide while unreachable, then advances when the orchestrator answers', async () => {
    const mode = { current: 'down' as 'down' | 'up' };
    stubFetch(mode);
    renderGate();

    await waitFor(() => expect(screen.getByTestId('setup-guide')).toBeTruthy());
    // The three deploy commands are shown, copyable.
    expect(screen.getByText('make up')).toBeTruthy();

    mode.current = 'up';
    fireEvent.click(screen.getByRole('button', { name: 'Check now' }));
    // No token configured → reachable now lands on sign-in, not the app.
    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
  });
});

describe('OnboardingGate — sign-in layer', () => {
  it('signs in with a valid token: landing card, stored token, then the app', async () => {
    stubFetch({ current: 'up' });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    await signIn(VALID_TOKEN);

    // Manual sign-in gets the landing ceremony with the cluster snapshot.
    await waitFor(() => expect(screen.getByTestId('landing-card')).toBeTruthy());
    expect(screen.getByText('jcloud')).toBeTruthy(); // namespace fact
    expect(window.localStorage.getItem(TOKEN_STORAGE_KEY)).toBe(VALID_TOKEN);

    fireEvent.click(screen.getByRole('button', { name: 'Enter console' }));
    expect(screen.getByTestId('app')).toBeTruthy();
  });

  it('rejects a bad token inline without leaving the sign-in screen', async () => {
    stubFetch({ current: 'up' });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    await signIn('wrong-token');

    await waitFor(() =>
      expect(screen.getByText(/rejected by the orchestrator/i)).toBeTruthy(),
    );
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
    stubFetch({ current: 'up' });
    renderGate();

    await waitFor(() => expect(screen.getByTestId('sign-in')).toBeTruthy());
    expect(screen.getByText(/saved token was rejected/i)).toBeTruthy();
    // The known-bad copy must not survive to silently retry on reload.
    expect(window.localStorage.getItem(TOKEN_STORAGE_KEY)).toBeNull();
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
    expect(screen.getByText(/stopped working/i)).toBeTruthy();
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
