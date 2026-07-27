import { GitBranch, HardDrive, Lock, Users, Warning } from '@phosphor-icons/react';
import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useRole } from '../api/ApiProvider';
import {
  useSystem,
  useClusterProviderConfig,
  useTestClusterProviderConfig,
  useUpdateClusterProviderConfig,
} from '../api/queries';
import { Button } from '../components/Button';
import { TextAreaField, TextField } from '../components/Field';
import { ClusterSubnav, DefinitionList, PageHeader, StatusLabel, SurfaceInner } from '../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { ClusterAccessDenied } from './ClusterAccessDenied';
import styles from './ClusterConnectionsPage.module.css';

export function ClusterConnectionsPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<'integrations' | 'login'>('integrations');
  const isAdmin = useRole() === 'cluster-admin';
  const system = useSystem(isAdmin);
  if (!isAdmin) return <ClusterAccessDenied />;

  if (system.isLoading) return <><ClusterSubnav /><SurfaceInner><LoadingBlock label={t('cluster.connections.loading')} /></SurfaceInner></>;
  if (system.isError) return <><ClusterSubnav /><SurfaceInner><ErrorBlock error={system.error} onRetry={() => system.refetch()} title={t('cluster.connections.statusError')} /></SurfaceInner></>;
  if (!system.data) return null;

  return (
    <>
      <ClusterSubnav />
      <SurfaceInner>
        <PageHeader eyebrow={t('cluster.connections.eyebrow')} title={t('cluster.connections.title')} description={t('cluster.connections.description')} />
        <div className={styles.tabs} role="tablist" aria-label={t('cluster.connections.configurationTabs')}>
          <button type="button" role="tab" aria-selected={tab === 'integrations'} onClick={() => setTab('integrations')}>
            {t('cluster.connections.integrationsTab')}
          </button>
          <button type="button" role="tab" aria-selected={tab === 'login'} onClick={() => setTab('login')}>
            {t('cluster.connections.loginTab')}
          </button>
        </div>
        <section className={styles.configuration} role="tabpanel">
          <ProviderConfigs mode={tab} />
        </section>
        <div className={styles.statusHead}>
          <span>{t('cluster.connections.statusEyebrow')}</span>
          <h2>{t('cluster.connections.statusTitle')}</h2>
          <p>{t('cluster.connections.statusDescription')}</p>
        </div>
        <div className={styles.layout}>
          <div className={styles.stack}>
            <ConnectionCard icon={<GitBranch size={18} />} title={t('cluster.connections.gitPolicyTitle')} subtitle={t('cluster.connections.gitPolicySubtitle')} status={<StatusLabel tone={system.data.provider.gitea_enabled ? 'success' : 'warning'}>{system.data.provider.gitea_enabled ? t('cluster.connections.giteaEnabled') : t('cluster.connections.giteaDisabled')}</StatusLabel>}>
              <DefinitionList items={[
                { label: t('cluster.connections.giteaUrlLabel'), value: <span className={styles.mono}>{system.data.provider.gitea_url || '—'}</span> },
                { label: t('cluster.connections.clusterPatLabel'), value: system.data.provider.gitea_enabled ? t('cluster.connections.patConfigured') : t('common.notConfigured') },
                { label: t('cluster.connections.allowedHostsLabel'), value: <span className={styles.mono}>{system.data.provider.allowed_git_hosts?.length ? system.data.provider.allowed_git_hosts.join(', ') : t('cluster.connections.unrestricted')}</span> },
              ]} />
              <div className={styles.callout}><span><strong>{t('cluster.connections.oobTitle')}</strong>{t('cluster.connections.oobBody')}</span></div>
            </ConnectionCard>
            <ArchiveConnection archive={system.data.archive} />
          </div>
          <aside className={styles.stack}>
            <ConnectionCard icon={<Users size={18} />} title={t('cluster.connections.authTitle')} subtitle={t('cluster.connections.registeredUsers', { count: system.data.auth?.users_count ?? 0 })} status={<StatusLabel tone={(system.data.auth?.providers.length ?? 0) > 0 ? 'success' : 'warning'}>{(system.data.auth?.providers.length ?? 0) > 0 ? t('cluster.connections.oauthOn') : t('cluster.connections.tokenOnly')}</StatusLabel>}>
              <DefinitionList items={[
                { label: t('cluster.connections.giteaOauthLabel'), value: system.data.auth?.providers.includes('gitea') ? t('common.configured') : t('common.notConfigured') },
                { label: t('cluster.connections.githubOauthLabel'), value: system.data.auth?.providers.includes('github') ? t('common.configured') : t('common.notConfigured') },
                { label: t('cluster.connections.consoleTokenLabel'), value: t('cluster.connections.consoleTokenValue') },
              ]} />
              <div className={styles.security}><Lock size={14} aria-hidden="true" /><span>{t('cluster.connections.oauthSecurityNote')}</span></div>
            </ConnectionCard>
            {!system.data.auth?.providers.includes('github') && <div className={styles.warning}><Warning size={16} aria-hidden="true" /><span><strong>{t('cluster.connections.githubUnavailableTitle')}</strong>{t('cluster.connections.githubUnavailableBody')}</span></div>}
          </aside>
        </div>
      </SurfaceInner>
    </>
  );
}

function ProviderConfigs({ mode }: { mode: 'integrations' | 'login' }) {
  const providers = mode === 'login'
    ? (['github', 'gitlab', 'gitea'] as const)
    : (['github', 'gitlab', 'gitea', 'jtype'] as const);
  return <div className={styles.layout} data-testid="cluster-provider-configs">
    {providers.map((provider) => <ProviderConfigCard key={provider} provider={provider} mode={mode} />)}
  </div>;
}
function ProviderConfigCard({ provider, mode }: { provider: import('../api/types').ProviderKind; mode: 'integrations' | 'login' }) {
  const { t } = useTranslation();
  const query = useClusterProviderConfig(provider);
  const update = useUpdateClusterProviderConfig(provider);
  const test = useTestClusterProviderConfig(provider);
  const [url, setUrl] = useState('');
  const [clientID, setClientID] = useState('');
  const [clientSecret, setClientSecret] = useState('');
  const [appID, setAppID] = useState('');
  const [appPrivateKey, setAppPrivateKey] = useState('');
  const [webhookSecret, setWebhookSecret] = useState('');
  const [loginEnabled, setLoginEnabled] = useState(false);
  const [pluginEnabled, setPluginEnabled] = useState(false);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (!query.data || dirty) return;
    setUrl(query.data.base_url ?? '');
    setClientID(query.data.client_id ?? '');
    setAppID(query.data.app_id ?? '');
    setLoginEnabled(query.data.login_enabled);
    setPluginEnabled(query.data.plugin_enabled);
  }, [dirty, query.data]);

  if (query.isLoading) return <section className={styles.card}><LoadingBlock /></section>;
  if (query.isError || !query.data) return <section className={styles.card}><ErrorBlock error={query.error} onRetry={() => void query.refetch()} /></section>;

  const item = query.data;
  const githubAppIncomplete = mode === 'integrations' && provider === 'github' && pluginEnabled && (
    !appID.trim()
    || (!item.app_private_key_set && !appPrivateKey.trim())
    || (!item.webhook_secret_set && !webhookSecret.trim())
  );
  const save = () => update.mutate({
    base_url: url,
    client_id: clientID,
    client_secret: clientSecret,
    login_enabled: loginEnabled,
    plugin_enabled: pluginEnabled,
    app_id: appID,
    app_private_key: appPrivateKey,
    webhook_secret: webhookSecret,
  }, {
    onSuccess: (saved) => {
      setUrl(saved.base_url ?? '');
      setClientID(saved.client_id ?? '');
      setAppID(saved.app_id ?? '');
      setLoginEnabled(saved.login_enabled);
      setPluginEnabled(saved.plugin_enabled);
      setClientSecret('');
      setAppPrivateKey('');
      setWebhookSecret('');
      setDirty(false);
    },
  });

  return (
    <section className={styles.card}>
      <header className={styles.cardHead}>
        <span className={styles.providerMark}>{provider}</span>
        <span className={styles.cardCopy}>
          <strong>{provider === 'jtype' ? 'JType Kanban' : provider}</strong>
        </span>
      </header>
      <div className={styles.cardBody}>
        {mode === 'integrations' && (
          <>
            <TextField label={t('cluster.connections.instanceUrl')} value={url} disabled={provider === 'github'} onChange={(event) => { setUrl(event.target.value); setDirty(true); }} placeholder={provider === 'github' ? 'https://github.com' : 'https://provider.example'} />
            <fieldset className={styles.capabilityFields}>
              <legend>{t('cluster.connections.providerCapabilities')}</legend>
              <label className={styles.checkLabel}>
                <input aria-label={t('cluster.connections.pluginEnabled')} type="checkbox" checked={pluginEnabled} onChange={(event) => { setPluginEnabled(event.target.checked); setDirty(true); }} />
                <span className={styles.checkCopy}><strong>{t('cluster.connections.pluginCapability')}</strong><small>{t('cluster.connections.pluginCapabilityHint')}</small></span>
              </label>
            </fieldset>
          </>
        )}
        {mode === 'login' && (
          <label className={styles.checkLabel}>
            <input aria-label={t('cluster.connections.loginEnabled')} type="checkbox" checked={loginEnabled} onChange={(event) => { setLoginEnabled(event.target.checked); setDirty(true); }} />
            <span className={styles.checkCopy}><strong>{t('cluster.connections.loginCapability')}</strong><small>{t('cluster.connections.loginCapabilityHint')}</small></span>
          </label>
        )}
        {(mode === 'login' || provider === 'jtype') && <>
          <TextField label={t('cluster.connections.oauthClientId')} value={clientID} onChange={(event) => { setClientID(event.target.value); setDirty(true); }} />
          <TextField label={t('cluster.connections.oauthClientSecret')} type="password" autoComplete="new-password" value={clientSecret} placeholder={item.client_secret_set ? t('cluster.connections.configuredReplace') : t('cluster.connections.enterSecret')} onChange={(event) => { setClientSecret(event.target.value); setDirty(true); }} />
        </>}
        {mode === 'integrations' && provider === 'github' && <>
          <div className={styles.sectionLabel}>GitHub App</div>
          <TextField label={t('cluster.connections.githubAppId')} inputMode="numeric" value={appID} placeholder={t('cluster.connections.githubAppId')} onChange={(event) => { setAppID(event.target.value); setDirty(true); }} />
          <TextAreaField label={t('cluster.connections.githubAppPrivateKey')} autoComplete="off" spellCheck={false} value={appPrivateKey} placeholder={item.app_private_key_set ? t('cluster.connections.configuredPemReplace') : t('cluster.connections.pastePrivateKeyPem')} hint={t('cluster.connections.privateKeyHint')} onChange={(event) => { setAppPrivateKey(event.target.value); setDirty(true); }} />
          <TextField label={t('cluster.connections.webhookSecret')} type="password" autoComplete="new-password" value={webhookSecret} placeholder={item.webhook_secret_set ? t('cluster.connections.configuredReplace') : t('cluster.connections.webhookSecretPlaceholder')} onChange={(event) => { setWebhookSecret(event.target.value); setDirty(true); }} />
          {githubAppIncomplete && <div className={styles.warning}><Warning size={16} aria-hidden="true" /><span><strong>{t('cluster.connections.githubAppConfigurationIncomplete')}</strong>{t('cluster.connections.githubAppConfigurationIncompleteHint')}</span></div>}
        </>}
        <div className={styles.actions}>
          {mode === 'integrations' && <Button type="button" variant="secondary" loading={test.isPending} onClick={() => test.mutate()}>{t('cluster.connections.testConnection')}</Button>}
          <Button type="button" loading={update.isPending} onClick={save}>{t('common.save')}</Button>
        </div>
      </div>
    </section>
  );
}

function ConnectionCard({ icon, title, subtitle, status, children }: { icon: ReactNode; title: string; subtitle: string; status: ReactNode; children: ReactNode }) {
  return (
    <section className={styles.card}>
      <header className={styles.cardHead}><span className={styles.providerMark}>{icon}</span><span className={styles.cardCopy}><strong>{title}</strong><small>{subtitle}</small></span>{status}</header>
      <div className={styles.cardBody}>{children}</div>
    </section>
  );
}

function ArchiveConnection({ archive }: { archive: import('../api/types').SystemInfo['archive'] }) {
  const { t } = useTranslation();
  const enabled = !!archive?.enabled;
  return (
    <ConnectionCard icon={<HardDrive size={18} />} title={t('cluster.connections.archiveTitle')} subtitle={t('cluster.connections.archiveSubtitle')} status={<StatusLabel tone={enabled ? 'success' : 'warning'}>{enabled ? t('cluster.connections.archiveEnabled') : t('cluster.connections.archiveUnavailable')}</StatusLabel>}>
      {enabled ? <DefinitionList items={[{ label: t('cluster.connections.endpointLabel'), value: <span className={styles.mono}>{archive?.endpoint}</span> }, { label: t('cluster.connections.bucketLabel'), value: <span className={styles.mono}>{archive?.bucket}</span> }, { label: t('cluster.connections.idleWindowLabel'), value: t('cluster.connections.idleDays', { count: archive?.idle_days }) }]} /> : <div className={styles.warning}><Warning size={16} aria-hidden="true" /><span><strong>{t('cluster.connections.archiveDisabledTitle')}</strong>{archive?.reason || t('cluster.connections.archiveDisabledReason')}</span></div>}
    </ConnectionCard>
  );
}
