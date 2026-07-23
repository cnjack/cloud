import { useCallback, useState } from 'react';

export const MOBILE_THEME_STORAGE_KEY = 'jmobile_theme';
export type MobileTheme = 'light' | 'dark';

function isMobileTheme(value: string | null | undefined): value is MobileTheme {
  return value === 'light' || value === 'dark';
}

export function currentTheme(): MobileTheme {
  if (typeof document !== 'undefined' && isMobileTheme(document.documentElement.dataset.theme)) {
    return document.documentElement.dataset.theme;
  }
  try {
    const stored = localStorage.getItem(MOBILE_THEME_STORAGE_KEY);
    if (isMobileTheme(stored)) return stored;
  } catch {
    /* storage unavailable */
  }
  return 'dark';
}

export function applyTheme(theme: MobileTheme) {
  const root = document.documentElement;
  root.dataset.theme = theme;
  root.classList.toggle('dark', theme === 'dark');
  try {
    localStorage.setItem(MOBILE_THEME_STORAGE_KEY, theme);
  } catch {
    /* storage unavailable */
  }

  // Keep the native status/navigation chrome aligned with the active surface
  // without duplicating palette values outside tokens.css.
  const themeColor = getComputedStyle(root).getPropertyValue('--color-bg').trim();
  if (themeColor) document.querySelector('meta[name="theme-color"]')?.setAttribute('content', themeColor);
}

export function useMobileTheme() {
  const [theme, setThemeState] = useState<MobileTheme>(currentTheme);
  const setTheme = useCallback((next: MobileTheme) => {
    applyTheme(next);
    setThemeState(next);
  }, []);
  return { theme, setTheme };
}
