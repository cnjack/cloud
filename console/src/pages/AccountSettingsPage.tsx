import { ArrowLeft, CheckCircle, GitBranch, LinkSimple, Lightning, User, WarningCircle } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router-dom';
import { useRole } from '../api/ApiProvider';
import { useAccountModels, useAccountRepositories } from '../api/queries';
import { useOptionalAuth } from '../auth/AuthProvider';
import { LanguageToggle } from '../components/LanguageToggle';
import { AccountHeader } from '../components/AccountHeader';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { ThemeToggle } from '../components/ThemeToggle';
import { AccountUsagePanel } from './AccountUsagePanel';
import styles from './AccountSettingsPage.module.css';

type Section = 'profile' | 'connections' | 'models' | 'usage' | 'preferences';

const tabs: Array<[Section, string]> = [
  ['profile', 'profileTab'],
  ['connections', 'gitAccountsTab'],
  ['models', 'modelsTab'],
  ['usage', 'usageTab'],
  ['preferences', 'preferencesTab'],
];

function sectionFromQuery(value: string | null): Section {
  return tabs.some(([id]) => id === value) ? value as Section : 'profile';
}

function providerName(provider: string, providers: Array<{ id: string; name: string }>): string {
  return providers.find((candidate) => candidate.id === provider)?.name ?? provider;
}

export function AccountSettingsPage() {
  const { t } = useTranslation();
  const auth = useOptionalAuth();
  const role = useRole();
  const [params, setParams] = useSearchParams();
  const section = sectionFromQuery(params.get('section'));
  const catalog = useAccountRepositories();
  const models = useAccountModels(section === 'models');
  const me = auth?.me;
  const linked = new Set(me?.identities.map((identity) => identity.provider) ?? []);

  const changeSection = (next: Section) => {
    const copy = new URLSearchParams(params);
    copy.set('section', next);
    setParams(copy, { replace: true });
  };

  return (
    <div className={styles.page} data-testid="account-settings">
      <AccountHeader />
      <main className={styles.main}>
        <Link to="/" className={styles.back}><ArrowLeft size={15} />{t('accountSettings.back')}</Link>
        <header className={styles.title}>
          <span>{t('accountSettings.eyebrow')}</span>
          <h1>{t('accountSettings.title')}</h1>
          <p>{t('accountSettings.description')}</p>
        </header>

        <nav className={styles.tabs} role="tablist" aria-label={t('accountSettings.tabsAria')}>
          {tabs.map(([id, key]) => <button key={id} type="button" role="tab" aria-selected={section === id} onClick={() => changeSection(id)}>{t(`accountSettings.${key}`)}</button>)}
        </nav>

        <section className={styles.surface} role="tabpanel">
          {section === 'profile' && (
            <SettingsSection icon={<User size={18} />} title={t('accountSettings.profileTitle')} description={t('accountSettings.profileDescription')}>
              <dl className={styles.facts}>
                <div><dt>{t('accountSettings.displayName')}</dt><dd>{me?.user.display_name ?? t('accountSettings.currentAccount')}</dd></div>
                <div><dt>{t('accountSettings.accountId')}</dt><dd>{me?.user.id ?? t('accountSettings.sessionAccount')}</dd></div>
                <div><dt>{t('accountSettings.role')}</dt><dd>{role === 'cluster-admin' ? t('accountSettings.clusterAdministrator') : t('accountSettings.member')}</dd></div>
                <div><dt>{t('accountSettings.executionIdentity')}</dt><dd>{t('accountSettings.repositoryOwnerDefault')}</dd></div>
              </dl>
            </SettingsSection>
          )}

          {section === 'connections' && (
            <SettingsSection icon={<GitBranch size={18} />} title={t('accountSettings.gitAccountsTitle')} description={t('accountSettings.gitAccountsDescription')}>
              <div className={styles.cards}>
                {(me?.identities ?? []).map((identity) => {
                  const source = catalog.data?.sources.find((candidate) => candidate.provider === identity.provider && candidate.account === identity.username)
                    ?? catalog.data?.sources.find((candidate) => candidate.provider === identity.provider);
                  const unavailable = source?.status === 'unavailable';
                  const name = providerName(identity.provider, auth?.providers ?? []);
                  return (
                    <article key={`${identity.provider}:${identity.username}`} data-state={unavailable ? 'unavailable' : 'ready'}>
                      {unavailable ? <WarningCircle size={19} weight="fill" /> : <CheckCircle size={19} />}
                      <span>
                        <strong>{identity.provider}/{identity.username}</strong>
                        <small>{unavailable ? source.message ?? t('accountSettings.repositoryAccessUnavailable') : t('accountSettings.connected')}</small>
                      </span>
                      {unavailable && <a className={styles.reauthorize} href={`/auth/link/${identity.provider}`} aria-label={t('accountSettings.reauthorize', { provider: name })}>{t('accountSettings.reauthorize', { provider: name })}</a>}
                    </article>
                  );
                })}
                {(auth?.providers ?? []).filter((provider) => !linked.has(provider.id)).map((provider) => (
                  <a key={provider.id} href={`/auth/link/${provider.id}`} aria-label={t('accountSettings.linkProvider', { provider: provider.name })}><LinkSimple size={19} /><span><strong>{t('accountSettings.linkProvider', { provider: provider.name })}</strong><small>{t('accountSettings.authorizeRepositories')}</small></span></a>
                ))}
              </div>
              {catalog.isLoading ? <LoadingBlock label={t('accountSettings.checkingRepositoryAccess')} /> : catalog.isError ? <ErrorBlock title={t('accountSettings.repositoryAccessUnavailableTitle')} error={catalog.error} onRetry={() => void catalog.refetch()} /> : null}
            </SettingsSection>
          )}

          {section === 'models' && (
            <SettingsSection icon={<Lightning size={18} />} title={t('accountSettings.modelsTitle')} description={t('accountSettings.modelsDescription')}>
              {models.isLoading ? <LoadingBlock label={t('accountSettings.loadingModels')} /> : models.isError ? <ErrorBlock title={t('accountSettings.modelAccessUnavailable')} error={models.error} onRetry={() => void models.refetch()} /> : (models.data ?? []).length === 0 ? (
                <div className={styles.empty} role="status">{t('accountSettings.noModels')}</div>
              ) : <div className={styles.modelList}>{(models.data ?? []).map((model) => <article key={model.id}><span><strong>{model.name}</strong><small>{model.model_name}</small></span><span className={styles.status}>{t('accountSettings.modelAuthorized')}</span></article>)}</div>}
              {role === 'cluster-admin' && <Link to="/cluster/models" className={styles.inlineLink}>{t('accountSettings.manageAuthorizations')}</Link>}
            </SettingsSection>
          )}

          {section === 'usage' && <AccountUsagePanel />}

          {section === 'preferences' && (
            <SettingsSection icon={<User size={18} />} title={t('accountSettings.preferencesTitle')} description={t('accountSettings.preferencesDescription')}>
              <div className={styles.preference}><span><strong>{t('accountSettings.language')}</strong><small>{t('accountSettings.languageDescription')}</small></span><LanguageToggle /></div>
              <div className={styles.preference}><span><strong>{t('accountSettings.appearance')}</strong><small>{t('accountSettings.appearanceDescription')}</small></span><ThemeToggle /></div>
            </SettingsSection>
          )}
        </section>
      </main>
    </div>
  );
}

function SettingsSection({ icon, title, description, children }: { icon: React.ReactNode; title: string; description: string; children: React.ReactNode }) {
  return <section className={styles.section}><header><span className={styles.icon}>{icon}</span><span><h2>{title}</h2><p>{description}</p></span></header>{children}</section>;
}
