/*
 * ThemeToggle — a hairline icon button that flips light/dark. Shows the icon of
 * the theme you'd switch TO. Colors come from currentColor (no hex → lint-safe).
 */
import { Moon, Sun } from '@phosphor-icons/react';
import { useTheme } from '../theme';
import styles from './ThemeToggle.module.css';

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
      {theme === 'dark' ? <Sun size={16} weight="regular" aria-hidden="true" /> : <Moon size={16} weight="regular" aria-hidden="true" />}
    </button>
  );
}
