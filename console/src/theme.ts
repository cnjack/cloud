/*
 * theme.ts — light/dark theme state.
 *
 * The single source of truth for the ACTIVE theme is the `data-theme` attribute
 * on <html>: index.html's inline head script stamps it before first paint (so
 * there's no flash), and this module is the only place that flips it afterwards.
 * The chosen theme is mirrored to localStorage under THEME_STORAGE_KEY (kept in
 * sync with that inline script). tokens.css maps `[data-theme='light']` to the
 * light palette; everything else just reads the semantic `--color-*` tokens.
 */
import { useCallback, useEffect, useState } from 'react';

export type Theme = 'light' | 'dark';

/** Keep in sync with the inline script in index.html. */
export const THEME_STORAGE_KEY = 'jcloud.console.theme';

function root(): HTMLElement | null {
  return typeof document === 'undefined' ? null : document.documentElement;
}

/** The theme currently applied to the document (falls back to dark). */
export function getTheme(): Theme {
  return root()?.dataset.theme === 'light' ? 'light' : 'dark';
}

export function setTheme(theme: Theme): void {
  const el = root();
  if (el) el.dataset.theme = theme;
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    /* storage unavailable — the choice just won't persist across reloads */
  }
}

/**
 * React binding: returns the active theme and a toggle. Re-reads on mount so it
 * reflects whatever the inline script resolved, and stays in sync if another
 * tab changes the stored preference.
 */
export function useTheme(): { theme: Theme; toggle: () => void } {
  const [theme, setThemeState] = useState<Theme>(getTheme);

  useEffect(() => {
    // Reconcile with the DOM the inline script already stamped.
    setThemeState(getTheme());
    const onStorage = (e: StorageEvent) => {
      if (e.key === THEME_STORAGE_KEY && (e.newValue === 'light' || e.newValue === 'dark')) {
        const el = root();
        if (el) el.dataset.theme = e.newValue;
        setThemeState(e.newValue);
      }
    };
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, []);

  const toggle = useCallback(() => {
    setThemeState((prev) => {
      const next: Theme = prev === 'dark' ? 'light' : 'dark';
      setTheme(next);
      return next;
    });
  }, []);

  return { theme, toggle };
}
