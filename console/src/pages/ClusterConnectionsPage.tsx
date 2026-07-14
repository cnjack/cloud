import { GitBranch, HardDrive, Kanban, Lock, Users, Warning } from '@phosphor-icons/react';
import { useEffect, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
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
  const isAdmin = useRole() === 'cluster-admin';
  const system = useSystem(isAdmin);
  const config = useKanbanConfig(isAdmin);
  if (!isAdmin) return <ClusterAccessDenied />;

  if (system.isLoading || config.isLoading) return <><ClusterSubnav /><SurfaceInner><LoadingBlock label="Loading cluster connections…" /></SurfaceInner></>;
  if (system.isError) return <><ClusterSubnav /><SurfaceInner><ErrorBlock error={system.error} onRetry={() => system.refetch()} title="Couldn't load connection status" /></SurfaceInner></>;
  if (config.isError) return <><ClusterSubnav /><SurfaceInner><ErrorBlock error={config.error} onRetry={() => config.refetch()} title="Couldn't load jtype configuration" /></SurfaceInner></>;
  if (!system.data || !config.data) return null;

  const configured = Number(config.data.effective_enabled) + Number(system.data.provider.gitea_enabled) + Number((system.data.auth?.providers.length ?? 0) > 0) + Number(system.data.archive?.enabled);
  return (
    <>
      <ClusterSubnav />
      <SurfaceInner>
        <PageHeader eyebrow="External systems" title="Connections" description="Cluster-wide endpoints and identity providers. Project-specific repository authorization remains inside each Project." actions={<StatusLabel tone="success">{configured} configured</StatusLabel>} />
        <div className={styles.layout}>
          <div className={styles.stack}>
            <JtypeConnection config={config.data} />
            <ConnectionCard icon={<GitBranch size={18} />} title="Git provider policy" subtitle="Environment-owned · read only" status={<StatusLabel tone={system.data.provider.gitea_enabled ? 'success' : 'warning'}>{system.data.provider.gitea_enabled ? 'Gitea enabled' : 'Gitea disabled'}</StatusLabel>}>
              <DefinitionList items={[
                { label: 'Gitea URL', value: <span className={styles.mono}>{system.data.provider.gitea_url || '—'}</span> },
                { label: 'Cluster PAT', value: system.data.provider.gitea_enabled ? 'Configured · value hidden' : 'Not configured' },
                { label: 'Allowed hosts', value: <span className={styles.mono}>{system.data.provider.allowed_git_hosts?.length ? system.data.provider.allowed_git_hosts.join(', ') : 'unrestricted'}</span> },
              ]} />
              <div className={styles.callout}><span><strong>Changes are made out of band.</strong>Provider URL, PAT, and host allowlist remain deployment configuration; this page reports effective state.</span></div>
            </ConnectionCard>
            <ArchiveConnection archive={system.data.archive} />
          </div>
          <aside className={styles.stack}>
            <ConnectionCard icon={<Users size={18} />} title="Authentication" subtitle={`${system.data.auth?.users_count ?? 0} registered users`} status={<StatusLabel tone={(system.data.auth?.providers.length ?? 0) > 0 ? 'success' : 'warning'}>{(system.data.auth?.providers.length ?? 0) > 0 ? 'OAuth on' : 'token only'}</StatusLabel>}>
              <DefinitionList items={[
                { label: 'Gitea OAuth', value: system.data.auth?.providers.includes('gitea') ? 'Configured' : 'Not configured' },
                { label: 'GitHub OAuth', value: system.data.auth?.providers.includes('github') ? 'Configured' : 'Not configured' },
                { label: 'Console token', value: 'Cluster-admin fallback' },
              ]} />
              <div className={styles.security}><Lock size={14} aria-hidden="true" /><span>OAuth redirect URIs are changed through the provider UI because API patching rotates the client secret.</span></div>
            </ConnectionCard>
            {!system.data.auth?.providers.includes('github') && <div className={styles.warning}><Warning size={16} aria-hidden="true" /><span><strong>GitHub sign-in is unavailable.</strong>No GitHub OAuth provider is configured. The UI does not show a working GitHub button until the provider exists.</span></div>}
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
      onSuccess: () => { setToken(''); setClearToken(false); toast.push({ kind: 'success', message: 'jtype connection saved.' }); },
      onError: (error) => toast.push({ kind: 'error', message: message(error, 'Could not save jtype.') }),
    });
  };
  const clear = () => remove.mutate(undefined, {
    onSuccess: () => toast.push({ kind: 'success', message: 'jtype DB override removed.' }),
    onError: (error) => toast.push({ kind: 'error', message: message(error, 'Could not remove the override.') }),
  });
  const startConnect = () => start.mutate(undefined, { onSuccess: (result) => setConnectId(result.connect_id) });
  const source = config.source === 'db' ? 'DB override' : config.source === 'env' ? 'environment fallback' : 'not configured';
  const expiry = expiryLabel(config.token_expires_at);
  return (
    <form className={styles.card} onSubmit={submit}>
      <header className={styles.cardHead}><span className={`${styles.providerMark} ${styles.accent}`}><Kanban size={18} /></span><span className={styles.cardCopy}><strong>jtype Kanban</strong><small>{source} · polling every {config.poll_interval}</small></span><StatusLabel tone={config.effective_enabled ? 'success' : config.reason ? 'danger' : 'warning'}>{config.effective_enabled ? 'effective' : config.reason ? 'broken' : 'off'}</StatusLabel></header>
      <div className={styles.cardBody}>
        <TextField label="Base URL" value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://jtype.example.com" />
        <TextField label="Rotate cluster fallback token" type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} placeholder={config.token_set ? 'Leave blank to keep current token' : 'Optional token'} hint={config.token_set ? `Encrypted credential set${expiry ? ` · ${expiry}` : ''}` : 'No DB fallback token is stored.'} />
        {config.token_set && <label className={styles.clearToken}><input type="checkbox" checked={clearToken} onChange={(event) => setClearToken(event.target.checked)} />Clear stored fallback token on save</label>}
        <KanbanConnectFlow idPrefix="kanban-connect" disabled={!config.base_url} disabledHint="Save a DB Base URL before starting Connect." active={!!connectId} starting={start.isPending} startError={start.error} connectStart={start.data} status={poll.data} statusError={poll.error} onStart={startConnect} onReset={() => { setConnectId(undefined); start.reset(); }} />
        <div className={styles.security}><Lock size={14} aria-hidden="true" /><span>Board traffic passes through the server proxy. The jtype token never enters the browser.</span></div>
        {config.reason && <div className={styles.warning}><Warning size={16} aria-hidden="true" /><span><strong>jtype configuration is unavailable.</strong>{config.reason}</span></div>}
        <div className={styles.actions}>{config.source === 'db' && <Button type="button" variant="ghost" onClick={clear} loading={remove.isPending}>Remove override</Button>}<Button type="submit" variant="primary" loading={update.isPending}>Save jtype</Button></div>
      </div>
    </form>
  );
}

function ArchiveConnection({ archive }: { archive: import('../api/types').SystemInfo['archive'] }) {
  const enabled = !!archive?.enabled;
  return (
    <ConnectionCard icon={<HardDrive size={18} />} title="Workspace archive" subtitle="Persistent workspace cold storage" status={<StatusLabel tone={enabled ? 'success' : 'warning'}>{enabled ? 'enabled' : 'unavailable'}</StatusLabel>}>
      {enabled ? <DefinitionList items={[{ label: 'Endpoint', value: <span className={styles.mono}>{archive?.endpoint}</span> }, { label: 'Bucket', value: <span className={styles.mono}>{archive?.bucket}</span> }, { label: 'Idle window', value: `${archive?.idle_days} days` }]} /> : <div className={styles.warning}><Warning size={16} aria-hidden="true" /><span><strong>Long-term archive is not enabled.</strong>{archive?.reason || 'Object storage is not configured.'}</span></div>}
    </ConnectionCard>
  );
}
