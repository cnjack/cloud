import {
  ArrowLeft,
  Check,
  GlobeHemisphereWest,
  Moon,
  SignOut,
  Sun,
} from '@phosphor-icons/react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useMobileAuth } from '../auth';
import {
  LOCALE_LABELS,
  SUPPORTED_LOCALES,
  setLocale,
  type Locale,
} from '../i18n';
import { useMobileTheme, type MobileTheme } from '../theme';

function initials(name: string): string {
  return Array.from(name.trim()).slice(0, 2).join('').toUpperCase() || 'JC';
}

export function SettingsPage() {
  const { t, i18n } = useTranslation();
  const auth = useMobileAuth();
  const { theme, setTheme } = useMobileTheme();
  const name = auth.me?.user.display_name || t('mobile.settings.accountPending');
  const locale = (i18n.resolvedLanguage || i18n.language) as Locale;

  return (
    <div className="app-shell">
      <header className="topbar">
        <Link to="/" className="topbar-back" aria-label={t('device.list.title')}>
          <ArrowLeft size={18} />
        </Link>
        <div className="topbar-title">{t('mobile.settings.title')}</div>
      </header>

      <div className="content content-pad-bottom settings-page" data-testid="settings-page">
        <section className="settings-section" aria-labelledby="settings-account-title">
          <h2 className="settings-section-title" id="settings-account-title">
            {t('mobile.settings.account')}
          </h2>
          <div className="account-plate">
            <span className="account-avatar" aria-hidden>
              {auth.me?.user.avatar_url ? (
                <img src={auth.me.user.avatar_url} alt="" referrerPolicy="no-referrer" />
              ) : (
                initials(name)
              )}
            </span>
            <span className="account-copy">
              <strong>{name}</strong>
              <span>{auth.cloudUrl}</span>
            </span>
          </div>
        </section>

        <section className="settings-section" aria-labelledby="settings-appearance-title">
          <h2 className="settings-section-title" id="settings-appearance-title">
            {t('mobile.settings.appearance')}
          </h2>
          <div className="settings-card">
            <SettingRow
              icon={theme === 'dark' ? <Moon size={20} /> : <Sun size={20} />}
              title={t('mobile.settings.theme')}
              hint={t('mobile.settings.themeHint')}
            >
              <div className="theme-segments" role="group" aria-label={t('mobile.settings.theme')}>
                <ThemeButton
                  theme="light"
                  active={theme === 'light'}
                  label={t('mobile.settings.themeLight')}
                  onClick={setTheme}
                />
                <ThemeButton
                  theme="dark"
                  active={theme === 'dark'}
                  label={t('mobile.settings.themeDark')}
                  onClick={setTheme}
                />
              </div>
            </SettingRow>

            <SettingRow
              icon={<GlobeHemisphereWest size={20} />}
              title={t('mobile.settings.language')}
              hint={t('mobile.settings.languageHint')}
            >
              <label className="language-control">
                <span className="sr-only">{t('mobile.settings.language')}</span>
                <select
                  value={locale}
                  onChange={(event) => void setLocale(event.target.value as Locale)}
                  data-testid="language-select"
                >
                  {SUPPORTED_LOCALES.map((option) => (
                    <option key={option} value={option}>{LOCALE_LABELS[option]}</option>
                  ))}
                </select>
              </label>
            </SettingRow>
          </div>
        </section>

        <section className="settings-section">
          <button
            type="button"
            className="settings-signout"
            onClick={auth.logout}
            data-testid="logout"
          >
            <SignOut size={20} aria-hidden />
            <span>
              <strong>{t('mobile.common.logout')}</strong>
              <small>{t('mobile.settings.signOutHint')}</small>
            </span>
          </button>
        </section>
      </div>
    </div>
  );
}

function SettingRow({
  icon,
  title,
  hint,
  children,
}: {
  icon: ReactNode;
  title: string;
  hint: string;
  children: ReactNode;
}) {
  return (
    <div className="settings-row">
      <span className="settings-row-icon" aria-hidden>{icon}</span>
      <span className="settings-row-copy">
        <strong>{title}</strong>
        <small>{hint}</small>
      </span>
      <span className="settings-row-control">{children}</span>
    </div>
  );
}

function ThemeButton({
  theme,
  active,
  label,
  onClick,
}: {
  theme: MobileTheme;
  active: boolean;
  label: string;
  onClick: (theme: MobileTheme) => void;
}) {
  return (
    <button
      type="button"
      data-active={active || undefined}
      data-testid={`theme-${theme}`}
      aria-pressed={active}
      onClick={() => onClick(theme)}
    >
      {theme === 'light' ? <Sun size={16} aria-hidden /> : <Moon size={16} aria-hidden />}
      <span>{label}</span>
      {active && <Check size={13} weight="bold" aria-hidden />}
    </button>
  );
}
