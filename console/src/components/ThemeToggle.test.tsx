/*
 * ThemeToggle — flips the document's data-theme, persists the choice, and shows
 * the icon for the theme you'd switch TO.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { ThemeToggle } from './ThemeToggle';
import { THEME_STORAGE_KEY } from '../theme';

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

beforeEach(() => {
  installMemoryStorage();
  document.documentElement.dataset.theme = 'dark';
});
afterEach(() => vi.unstubAllGlobals());

describe('ThemeToggle', () => {
  it('starts from the document theme and offers to switch to the other one', () => {
    render(<ThemeToggle />);
    const btn = screen.getByTestId('theme-toggle');
    expect(btn.getAttribute('data-theme-state')).toBe('dark');
    expect(btn.getAttribute('aria-label')).toBe('Switch to light mode');
  });

  it('toggles the document data-theme and persists the choice', () => {
    render(<ThemeToggle />);
    const btn = screen.getByTestId('theme-toggle');

    fireEvent.click(btn);
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('light');
    expect(btn.getAttribute('aria-label')).toBe('Switch to dark mode');

    fireEvent.click(btn);
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('dark');
  });
});
