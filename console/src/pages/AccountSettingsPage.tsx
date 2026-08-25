import { ArrowLeft, CheckCircle, GitBranch, LinkSimple, Lightning, User } from '@phosphor-icons/react';
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
  ['profile', 'Profile'],
  ['connections', 'Git accounts'],
  ['models', 'Models'],
  ['usage', 'Usage'],
  ['preferences', 'Preferences'],
];

function sectionFromQuery(value: string | null): Section {
  return tabs.some(([id]) => id === value) ? value as Section : 'profile';
}

export function AccountSettingsPage() {
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
        <Link to="/" className={styles.back}><ArrowLeft size={15} />Back to Work Home</Link>
        <header className={styles.title}>
          <span>Account</span>
          <h1>Personal settings</h1>
          <p>Your identity, model access, linked Git accounts, usage, and interface preferences apply across every Repository and jcode Desktop.</p>
        </header>

        <nav className={styles.tabs} role="tablist" aria-label="Personal settings sections">
          {tabs.map(([id, label]) => <button key={id} type="button" role="tab" aria-selected={section === id} onClick={() => changeSection(id)}>{label}</button>)}
        </nav>

        <section className={styles.surface} role="tabpanel">
          {section === 'profile' && (
            <SettingsSection icon={<User size={18} />} title="Account profile" description="This is the identity used to execute Repository tasks and own conversations.">
              <dl className={styles.facts}>
                <div><dt>Display name</dt><dd>{me?.user.display_name ?? 'Current account'}</dd></div>
                <div><dt>Account ID</dt><dd>{me?.user.id ?? 'Session account'}</dd></div>
                <div><dt>Role</dt><dd>{role === 'cluster-admin' ? 'Cluster administrator' : 'Member'}</dd></div>
                <div><dt>Execution identity</dt><dd>Repository owner by default</dd></div>
              </dl>
            </SettingsSection>
          )}

          {section === 'connections' && (
            <SettingsSection icon={<GitBranch size={18} />} title="Git accounts" description="Repositories visible to these linked accounts appear directly in the Work Home context picker.">
              <div className={styles.cards}>
                {(me?.identities ?? []).map((identity) => (
                  <article key={`${identity.provider}:${identity.username}`}><CheckCircle size={19} /><span><strong>{identity.provider}/{identity.username}</strong><small>Connected to this account</small></span></article>
                ))}
                {(auth?.providers ?? []).filter((provider) => !linked.has(provider.id)).map((provider) => (
                  <a key={provider.id} href={`/auth/link/${provider.id}`}><LinkSimple size={19} /><span><strong>Link {provider.name}</strong><small>Authorize repositories for this account</small></span></a>
                ))}
              </div>
              {catalog.isLoading ? <LoadingBlock label="Checking Repository access…" /> : catalog.isError ? <ErrorBlock title="Repository access unavailable" error={catalog.error} onRetry={() => void catalog.refetch()} /> : (
                <div className={styles.sources}>{(catalog.data?.sources ?? []).map((source) => <span key={`${source.provider}:${source.account}`} data-state={source.status}><strong>{source.provider}</strong>{source.account} · {source.status}</span>)}</div>
              )}
            </SettingsSection>
          )}

          {section === 'models' && (
            <SettingsSection icon={<Lightning size={18} />} title="Model access" description="Models are authorized directly for you and apply across every Repository.">
              {models.isLoading ? <LoadingBlock label="Loading account models…" /> : models.isError ? <ErrorBlock title="Model access unavailable" error={models.error} onRetry={() => void models.refetch()} /> : (models.data ?? []).length === 0 ? (
                <div className={styles.empty} role="status">No model is authorized for your account. Contact a Cluster administrator.</div>
              ) : <div className={styles.modelList}>{(models.data ?? []).map((model) => <article key={model.id}><span><strong>{model.name}</strong><small>{model.model_name}</small></span><span className={styles.status}>Authorized for your account</span></article>)}</div>}
              {role === 'cluster-admin' && <Link to="/cluster/models" className={styles.inlineLink}>Manage account authorizations in Cluster settings</Link>}
            </SettingsSection>
          )}

          {section === 'usage' && <AccountUsagePanel />}

          {section === 'preferences' && (
            <SettingsSection icon={<User size={18} />} title="Preferences" description="Cloud and Desktop use the same product language and visual preferences where supported.">
              <div className={styles.preference}><span><strong>Language</strong><small>Change the interface language.</small></span><LanguageToggle /></div>
              <div className={styles.preference}><span><strong>Appearance</strong><small>Use light or dark theme.</small></span><ThemeToggle /></div>
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
