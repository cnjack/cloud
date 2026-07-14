/*
 * LanguageToggle — a hairline icon button (mirrors ThemeToggle) that opens a
 * menu of the supported UI locales. Picking one calls setLocale(), which swaps
 * the i18next language, updates <html lang>, and persists the choice to
 * localStorage. Colors come from currentColor / theme tokens (lint-safe).
 */
import { Menu, MenuButton, MenuItem, MenuItems } from '@headlessui/react';
import { Check, Translate } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { LOCALE_LABELS, SUPPORTED_LOCALES, setLocale, type SupportedLocale } from '../i18n';
import styles from './LanguageToggle.module.css';

export function LanguageToggle() {
  const { t, i18n } = useTranslation();
  const current = i18n.language as SupportedLocale;
  return (
    <Menu as="div" className={styles.root}>
      <MenuButton
        className={styles.toggle}
        data-testid="language-toggle"
        aria-label={t('language.label')}
        title={t('language.label')}
      >
        <Translate size={16} weight="regular" aria-hidden="true" />
      </MenuButton>
      <MenuItems
        modal={false}
        anchor={{ to: 'bottom end', gap: 4 }}
        className={styles.menu}
      >
        {SUPPORTED_LOCALES.map((locale) => (
          <MenuItem key={locale}>
            <button
              type="button"
              className={styles.item}
              data-active={locale === current || undefined}
              onClick={() => void setLocale(locale)}
            >
              <span>{LOCALE_LABELS[locale]}</span>
              {locale === current && <Check size={14} weight="bold" aria-hidden="true" />}
            </button>
          </MenuItem>
        ))}
      </MenuItems>
    </Menu>
  );
}
