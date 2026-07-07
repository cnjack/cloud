/*
 * config — token resolution honors the explicit sign-out marker (M6 live find):
 * a page reload after sign-out must not resurrect the VITE_CONSOLE_TOKEN dev
 * default and silently re-enter as the service principal.
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  clearSignedOut,
  markSignedOut,
  resolveInitialToken,
  SIGNED_OUT_KEY,
  TOKEN_STORAGE_KEY,
  writeStoredToken,
} from './config';

function installMemoryStorage() {
  const store = new Map<string, string>();
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: {
      getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
      setItem: (k: string, v: string) => void store.set(k, String(v)),
      removeItem: (k: string) => void store.delete(k),
      clear: () => void store.clear(),
      key: (i: number) => [...store.keys()][i] ?? null,
      get length() {
        return store.size;
      },
    },
  });
}

beforeEach(() => installMemoryStorage());
afterEach(() => {
  window.localStorage.clear();
});

describe('resolveInitialToken × signed-out marker', () => {
  // Vitest env carries VITE_CONSOLE_TOKEN? Derive expectations from the env so
  // the assertions hold either way.
  const envToken = import.meta.env.VITE_CONSOLE_TOKEN || undefined;

  it('falls back to the env token normally', () => {
    expect(resolveInitialToken()).toBe(envToken);
  });

  it('suppresses the env fallback after an explicit sign-out', () => {
    markSignedOut();
    expect(window.localStorage.getItem(SIGNED_OUT_KEY)).toBe('1');
    expect(resolveInitialToken()).toBeUndefined();
  });

  it('a manually stored token wins even while marked signed-out', () => {
    markSignedOut();
    writeStoredToken('manual-token');
    expect(window.localStorage.getItem(TOKEN_STORAGE_KEY)).toBe('manual-token');
    expect(resolveInitialToken()).toBe('manual-token');
  });

  it('clearSignedOut lifts the suppression', () => {
    markSignedOut();
    clearSignedOut();
    expect(resolveInitialToken()).toBe(envToken);
  });
});
