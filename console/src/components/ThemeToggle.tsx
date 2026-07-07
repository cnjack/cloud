/*
 * ThemeToggle — a hairline icon button that flips light/dark. Shows the icon of
 * the theme you'd switch TO. Colors come from currentColor (no hex → lint-safe).
 */
import { useTheme } from '../theme';
import styles from './ThemeToggle.module.css';

function SunIcon() {
  return (
    <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true" fill="none"
      stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
      <circle cx="12" cy="12" r="4.2" />
      <path d="M12 2.5v2.2M12 19.3v2.2M4.4 4.4l1.6 1.6M18 18l1.6 1.6M2.5 12h2.2M19.3 12h2.2M4.4 19.6l1.6-1.6M18 6l1.6-1.6" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true"
      fill="currentColor">
      <path d="M20 14.2A8 8 0 0 1 9.8 4a0.6 0.6 0 0 0-0.82-0.78A9.2 9.2 0 1 0 20.8 15a0.6 0.6 0 0 0-0.8-0.8z" />
    </svg>
  );
}

export function ThemeToggle() {
  const { theme, toggle } = useTheme();
  const next = theme === 'dark' ? 'light' : 'dark';
  return (
    <button
      type="button"
      className={styles.toggle}
      onClick={toggle}
      data-testid="theme-toggle"
      data-theme-state={theme}
      aria-label={`Switch to ${next} theme`}
      title={`Switch to ${next} theme`}
    >
      {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
    </button>
  );
}
