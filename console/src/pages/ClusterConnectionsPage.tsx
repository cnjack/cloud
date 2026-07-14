import { GitBranch, HardDrive, Kanban, Lock, Users, Warning } from '@phosphor-icons/react';
import { useEffect, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useRole } from '../api/ApiProvider';
import { ApiError } from '../api/client';
import {
  useDeleteKanbanConfig,
  useKanbanConfig,
  useKanbanConnectStatus,
  useStartKanbanConnect,
  useSystem,
  useUpdateKanbanConfig,
} from '../api/queries';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { expiryLabel, KanbanConnectFlow } from '../components/KanbanConnect';
import { ClusterSubnav, DefinitionList, PageHeader, StatusLabel, SurfaceInner } from '../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { useToast } from '../components/Toast';
import { ClusterAccessDenied } from './ClusterAccessDenied';
import styles from './ClusterConnectionsPage.module.css';

function message(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.message : fallback;
}

export function ClusterConnectionsPage() {
  const { t } = useTranslation();
  const isAdmin = useRole() === 'cluster-admin';
  const system = useSystem(isAdmin);
  const config = useKanbanConfig(isAdmin);
  if (!isAdmin) return <ClusterAccessDenied />;

  if (system.isLoading || config.isLoading) return <><ClusterSubnav /><SurfaceInner><LoadingBlock label={t('cluster.connections.loading')} /></SurfaceInner></>;
  if (system.isError) return <><ClusterSubnav /><SurfaceInner><ErrorBlock error={system.error} onRetry={() => system.refetch()} title={t('cluster.connections.statusError')} /></SurfaceInner></>;
  if (config.isError) return <><ClusterSubnav /><SurfaceInner><ErrorBlock error={config.error} onRetry={() => config.refetch()} title={t('cluster.connections.configError')} /></SurfaceInner></>;
  if (!system.data || !config.data) return null;

  const configured = Number(config.data.effective_enabled) + Number(system.data.provider.gitea_enabled) + Number((system.data.auth?.providers.length ?? 0) > 0) + Number(system.data.archive?.enabled);
  return (
    <>
      <ClusterSubnav />
      <SurfaceInner>
        <PageHeader eyebrow={t('cluster.connections.eyebrow')} title={t('cluster.connections.title')} description={t('cluster.connections.description')} actions={<StatusLabel tone="success">{t('cluster.connections.configuredCount', { count: configured })}</StatusLabel>} />
        <div className={styles.layout}>
          <div className={styles.stack}>
            <JtypeConnection config={config.data} />
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

function ConnectionCard({ icon, title, subtitle, status, children }: { icon: ReactNode; title: string; subtitle: string; status: ReactNode; children: ReactNode }) {
  return (
    <section className={styles.card}>
      <header className={styles.cardHead}><span className={styles.providerMark}>{icon}</span><span className={styles.cardCopy}><strong>{title}</strong><small>{subtitle}</small></span>{status}</header>
      <div className={styles.cardBody}>{children}</div>
    </section>
  );
}

function JtypeConnection({ config }: { config: import('../api/types').KanbanClusterConfig }) {
  const { t } = useTranslation();
  const [baseUrl, setBaseUrl] = useState(config.base_url || config.effective_base_url);
  const [token, setToken] = useState('');
  const [clearToken, setClearToken] = useState(false);
  const [connectId, setConnectId] = useState<string>();
  const update = useUpdateKanbanConfig();
  const remove = useDeleteKanbanConfig();
  const start = useStartKanbanConnect();
  const poll = useKanbanConnectStatus(connectId, !!connectId);
  const toast = useToast();

  useEffect(() => setBaseUrl(config.base_url || config.effective_base_url), [config.base_url, config.effective_base_url]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const input: { base_url: string; token?: string } = { base_url: baseUrl.trim() };
    if (clearToken) input.token = '';
    else if (token) input.token = token;
    update.mutate(input, {
      onSuccess: () => { setToken(''); setClearToken(false); toast.push({ kind: 'success', message: t('cluster.connections.jtypeSaved') }); },
      onError: (error) => toast.push({ kind: 'error', message: message(error, t('cluster.connections.jtypeSaveError')) }),
    });
  };
  const clear = () => remove.mutate(undefined, {
    onSuccess: () => toast.push({ kind: 'success', message: t('cluster.connections.jtypeOverrideRemoved') }),
    onError: (error) => toast.push({ kind: 'error', message: message(error, t('cluster.connections.jtypeRemoveError')) }),
  });
  const startConnect = () => start.mutate(undefined, { onSuccess: (result) => setConnectId(result.connect_id) });
  const source = config.source === 'db' ? t('cluster.connections.sourceDb') : config.source === 'env' ? t('cluster.connections.sourceEnv') : t('cluster.connections.sourceNone');
  const expiry = expiryLabel(config.token_expires_at);
  return (
    <form className={styles.card} onSubmit={submit}>
      <header className={styles.cardHead}><span className={`${styles.providerMark} ${styles.accent}`}><Kanban size={18} /></span><span className={styles.cardCopy}><strong>{t('cluster.connections.jtypeKanban')}</strong><small>{source} · {t('cluster.connections.pollingEvery', { interval: config.poll_interval })}</small></span><StatusLabel tone={config.effective_enabled ? 'success' : config.reason ? 'danger' : 'warning'}>{config.effective_enabled ? t('cluster.connections.jtypeEffective') : config.reason ? t('cluster.connections.jtypeBroken') : t('cluster.connections.jtypeOff')}</StatusLabel></header>
      <div className={styles.cardBody}>
        <TextField label={t('cluster.connections.baseUrlLabel')} value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://jtype.example.com" />
        <TextField label={t('cluster.connections.rotateTokenLabel')} type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} placeholder={config.token_set ? t('cluster.connections.tokenPlaceholderKeep') : t('cluster.connections.tokenPlaceholderOptional')} hint={config.token_set ? `${t('cluster.connections.tokenHintSet')}${expiry ? ` · ${expiry}` : ''}` : t('cluster.connections.tokenHintNone')} />
        {config.token_set && <label className={styles.clearToken}><input type="checkbox" checked={clearToken} onChange={(event) => setClearToken(event.target.checked)} />{t('cluster.connections.clearTokenLabel')}</label>}
        <KanbanConnectFlow idPrefix="kanban-connect" disabled={!config.base_url} disabledHint={t('cluster.connections.connectDisabledHint')} active={!!connectId} starting={start.isPending} startError={start.error} connectStart={start.data} status={poll.data} statusError={poll.error} onStart={startConnect} onReset={() => { setConnectId(undefined); start.reset(); }} />
        <div className={styles.security}><Lock size={14} aria-hidden="true" /><span>{t('cluster.connections.boardTrafficNote')}</span></div>
        {config.reason && <div className={styles.warning}><Warning size={16} aria-hidden="true" /><span><strong>{t('cluster.connections.jtypeUnavailableTitle')}</strong>{config.reason}</span></div>}
        <div className={styles.actions}>{config.source === 'db' && <Button type="button" variant="ghost" onClick={clear} loading={remove.isPending}>{t('cluster.connections.removeOverride')}</Button>}<Button type="submit" variant="primary" loading={update.isPending}>{t('cluster.connections.saveJtype')}</Button></div>
      </div>
    </form>
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
