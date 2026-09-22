import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useOptionalAuth } from '../auth/AuthProvider';
import { useAccountProfile, useUpdateAccountProfile } from '../api/accountProfile';
import { useAccountModels } from '../api/queries';
import type { AccountPreferences } from '../api/types';
import { Button } from '../components/Button';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { LOCALE_LABELS, SUPPORTED_LOCALES, setLocale } from '../i18n';
import { getTheme, setTheme } from '../theme';
import styles from './AccountSettingsPage.module.css';

export function AccountProfileForm({ section }: { section: 'profile' | 'preferences' | 'models' }) {
  const { t, i18n } = useTranslation();
  const auth = useOptionalAuth();
  const profile = useAccountProfile();
  const save = useUpdateAccountProfile();
  const models = useAccountModels(section === 'models');
  const [name, setName] = useState('');
  const [preferences, setPreferences] = useState<AccountPreferences>();
  useEffect(() => {
    if (!profile.data) return;
    setName(profile.data.display_name);
    setPreferences(profile.data.preferences);
  }, [profile.data]);
  const change = <K extends keyof AccountPreferences>(key: K, value: AccountPreferences[K]) => {
    save.reset(); setPreferences(current => current ? { ...current, [key]: value } : current);
  };
  if (auth?.me?.is_service) return <p role="status">{t('cloudRefresh.accountRequired')}</p>;
  if (profile.isError) return <ErrorBlock error={profile.error} onRetry={() => void profile.refetch()} />;
  if (!preferences) return <LoadingBlock />;
  return <form className={styles.profileForm} onSubmit={(event) => {
    event.preventDefault();
    save.mutate({ display_name: name, preferences }, { onSuccess: () => {
      if (preferences.language) void setLocale(preferences.language);
      if (preferences.theme) setTheme(preferences.theme);
    } });
  }}>
    {section === 'profile' && <label className={styles.formRow}><span>{t('accountSettings.displayName')}</span><input value={name} required maxLength={100} onChange={(event) => { save.reset(); setName(event.target.value); }} /></label>}
    {section === 'models' && <>
      {models.isError && <ErrorBlock error={models.error} onRetry={() => void models.refetch()} />}
      <label className={styles.formRow}><span>{t('cloudRefresh.defaultModel')}</span><select value={preferences.default_model_id} onChange={(event) => change('default_model_id', event.target.value)} disabled={models.isLoading || models.isError}>
        <option value="">{t('cloudRefresh.automaticModel')}</option>
        {!!preferences.default_model_id && !models.data?.some((model) => model.id === preferences.default_model_id) && <option value={preferences.default_model_id} disabled>{t('cloudRefresh.unavailableModel')}</option>}
        {(models.data ?? []).filter((model) => model.capabilities.tools).map((model) => <option key={model.id} value={model.id}>{model.name}</option>)}
      </select></label>
    </>}
    {section === 'preferences' && <>
      <label className={styles.formRow}><span>{t('cloudRefresh.defaultPermission')}</span><select value={preferences.permission_mode || 'approval'} onChange={(event) => change('permission_mode', event.target.value as AccountPreferences['permission_mode'])}><option value="approval">{t('cloudRefresh.askApproval')}</option><option value="auto">{t('cloudRefresh.fullAccess')}</option><option value="plan">{t('cloudRefresh.planMode')}</option></select></label>
      <label className={styles.formRow}><span>{t('cloudRefresh.defaultEffort')}</span><select value={preferences.effort || 'auto'} onChange={(event) => change('effort', event.target.value as AccountPreferences['effort'])}>{['auto','low','medium','high'].map((effort) => <option key={effort} value={effort}>{t(`cloudRefresh.effort_${effort}`)}</option>)}</select></label>
      <label className={styles.formRow}><span>{t('accountSettings.language')}</span><select value={preferences.language || i18n.resolvedLanguage} onChange={(event) => change('language', event.target.value as AccountPreferences['language'])}>{SUPPORTED_LOCALES.map((locale) => <option key={locale} value={locale}>{LOCALE_LABELS[locale]}</option>)}</select></label>
      <label className={styles.formRow}><span>{t('accountSettings.appearance')}</span><select value={preferences.theme || getTheme()} onChange={(event) => change('theme', event.target.value as AccountPreferences['theme'])}><option value="light">{t('cloudRefresh.light')}</option><option value="dark">{t('cloudRefresh.dark')}</option></select></label>
      <label className={styles.formRow}><span>{t('cloudRefresh.sendKey')}</span><select value={preferences.send_key || 'enter'} onChange={(event) => change('send_key', event.target.value as AccountPreferences['send_key'])}><option value="mod_enter">⌘ / Ctrl + Enter</option><option value="enter">Enter</option></select></label>
    </>}
    {save.isError && <div role="alert" className={styles.empty}>{save.error.message} <Link to="/account/settings?section=models">{t('accountSettings.modelsTab')}</Link></div>}
    <div className={styles.formActions}><Button type="submit" disabled={save.isPending}>{t(save.isPending ? 'common.saving' : 'common.save')}</Button>{save.isSuccess && <span role="status">{t('common.saved')}</span>}</div>
  </form>;
}
