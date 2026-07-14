/*
 * ThemeToggle — a hairline icon button that flips light/dark. Shows the icon of
 * the theme you'd switch TO. Colors come from currentColor (no hex → lint-safe).
 */
import { Moon, Sun } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { useTheme } from '../theme';
import styles from './ThemeToggle.module.css';

export function ThemeToggle() {
  const { t } = useTranslation();
  const { theme, toggle } = useTheme();
  const label = theme === 'dark' ? t('shell.switchToLight') : t('shell.switchToDark');
  return (
    <button
      type="button"
      className={styles.toggle}
      onClick={toggle}
      data-testid="theme-toggle"
      data-theme-state={theme}
      aria-label={label}
      title={label}
    >
      {theme === 'dark' ? <Sun size={16} weight="regular" aria-hidden="true" /> : <Moon size={16} weight="regular" aria-hidden="true" />}
    </button>
  );
}
