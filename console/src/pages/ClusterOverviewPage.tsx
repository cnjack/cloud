import { Cpu, Info } from '@phosphor-icons/react';
import { Link } from 'react-router-dom';
import { useRole } from '../api/ApiProvider';
import { useModelProviders, useSystem } from '../api/queries';
import type { SystemInfo } from '../api/types';
import { Button } from '../components/Button';
import {
  ClusterSubnav,
  DefinitionList,
  PageHeader,
  SectionPanel,
  StatusLabel,
  SurfaceInner,
} from '../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { ClusterAccessDenied } from './ClusterAccessDenied';
import styles from './ClusterOverviewPage.module.css';

function duration(seconds: number): string {
  if (seconds >= 86400 && seconds % 86400 === 0) return `${seconds / 86400}d`;
  if (seconds >= 3600 && seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds >= 60 && seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

export function ClusterOverviewPage() {
  const isAdmin = useRole() === 'cluster-admin';
  const system = useSystem(isAdmin);
  const providers = useModelProviders(isAdmin);
  if (!isAdmin) return <ClusterAccessDenied />;

  return (
    <>
      <ClusterSubnav />
      <SurfaceInner>
        {system.isLoading ? (
          <LoadingBlock label="Loading cluster snapshot…" />
        ) : system.isError ? (
          <ErrorBlock error={system.error} onRetry={() => system.refetch()} title="Couldn't load the cluster snapshot" />
        ) : system.data ? (
          <ClusterSnapshot data={system.data} modelCount={(providers.data ?? []).reduce((sum, provider) => sum + provider.models.length, 0)} onRefresh={() => { void system.refetch(); void providers.refetch(); }} />
        ) : null}
      </SurfaceInner>
    </>
  );
}

function ClusterSnapshot({ data, modelCount, onRefresh }: { data: SystemInfo; modelCount: number; onRefresh: () => void }) {
  const active = data.capacity.running + data.capacity.scheduling;
  const unlimited = data.capacity.max_concurrent_runs <= 0;
  const percent = unlimited ? 0 : Math.min(100, (active / data.capacity.max_concurrent_runs) * 100);
  const available = unlimited ? null : Math.max(0, data.capacity.max_concurrent_runs - active);

  return (
    <div data-testid="cluster-overview">
      <PageHeader
        eyebrow="Orchestrator snapshot"
        title="Cluster"
        description="Capacity, guardrails, and runtime wiring for this orchestrator. Editable configuration stays in its owning section."
        meta={<><span>namespace / {data.namespace || '—'}</span><span>·</span><span>launcher / {data.launcher || '—'}</span><span>·</span><span>commit / {data.version.commit || '—'}</span></>}
        actions={<><StatusLabel tone="success">snapshot loaded</StatusLabel><Button onClick={onRefresh}>Refresh</Button></>}
      />

      <dl className={styles.metrics} aria-label="Run capacity summary">
        <Metric label="Running" value={data.capacity.running} note="active now" />
        <Metric label="Scheduling" value={data.capacity.scheduling} note="awaiting pod" />
        <Metric label="Queued" value={data.capacity.queued} note="waiting for capacity" />
        <Metric label="Max" value={unlimited ? '∞' : data.capacity.max_concurrent_runs} note="concurrent Runs" />
      </dl>

      <div className={styles.layout}>
        <div className={styles.main}>
          <SectionPanel title="Capacity" aside={<span className={styles.mono}>{active} active / {unlimited ? '∞' : data.capacity.max_concurrent_runs} max</span>}>
            <div className={styles.capacityBody}>
              <div className={styles.capacityFigure}>
                <div><strong>{unlimited ? '∞' : `${percent.toFixed(percent % 1 === 0 ? 0 : 1)}%`}</strong><span>{unlimited ? 'Concurrency is not capped' : 'of configured concurrency is in use'}</span></div>
                <span className={styles.available}>{available === null ? 'unlimited slots' : `${available} slots available`}</span>
              </div>
              {!unlimited && <div className={styles.track} role="progressbar" aria-valuenow={active} aria-valuemin={0} aria-valuemax={data.capacity.max_concurrent_runs}><span style={{ width: `${percent}%` }} /></div>}
              <div className={styles.legend}><span><i />{data.capacity.running} running</span><span><i data-muted="true" />{data.capacity.scheduling} scheduling</span></div>
            </div>
          </SectionPanel>

          <SectionPanel title="Git provider policy" aside={<StatusLabel tone={data.provider.gitea_enabled ? 'success' : 'warning'}>{data.provider.gitea_enabled ? 'Gitea enabled' : 'Gitea disabled'}</StatusLabel>}>
            <DefinitionList items={[
              { label: 'Gitea URL', value: <span className={styles.mono}>{data.provider.gitea_url || '—'}</span> },
              { label: 'Draft PR output', value: data.provider.gitea_enabled ? 'Available to authorized identities' : 'Unavailable — diff only' },
              { label: 'Allowed Git hosts', value: <span className={styles.mono}>{data.provider.allowed_git_hosts?.length ? data.provider.allowed_git_hosts.join(', ') : 'unrestricted'}</span> },
            ]} />
          </SectionPanel>

          <div className={styles.callout}><Cpu size={16} aria-hidden="true" /><span><strong>{modelCount} catalog {modelCount === 1 ? 'model is' : 'models are'} configured.</strong>Model credentials and Project grants are managed in <Link to="/cluster/models">Models</Link>; Projects never receive provider keys.</span></div>
        </div>

        <aside className={styles.aside}>
          <SectionPanel title="Guardrails"><DefinitionList items={[{ label: 'Run timeout', value: <span className={styles.mono}>{duration(data.guardrails.run_timeout_seconds)}</span> }, { label: 'Job TTL', value: <span className={styles.mono}>{duration(data.guardrails.job_ttl_seconds)}</span> }]} /></SectionPanel>
          <SectionPanel title="Runner"><DefinitionList items={[{ label: 'Image', value: <span className={styles.mono}>{data.runner.image || '—'}</span> }, { label: 'Launcher', value: <span className={styles.mono}>{data.launcher || '—'}</span> }, { label: 'Workspace', value: data.runner.persistent_workspace ? 'Persistent' : 'Ephemeral' }]} /></SectionPanel>
          <SectionPanel title="Build"><DefinitionList items={[{ label: 'Version', value: <span className={styles.mono}>{data.version.version || '—'}</span> }, { label: 'Commit', value: <span className={styles.mono}>{data.version.commit || '—'}</span> }, { label: 'Users', value: <span className={styles.mono}>{data.auth?.users_count ?? 0}</span> }]} /></SectionPanel>
          {!data.archive?.enabled && data.archive?.reason && <div className={styles.archiveNote}><Info size={16} aria-hidden="true" /><span><strong>Cold archive is unavailable.</strong>{data.archive.reason}</span></div>}
        </aside>
      </div>
    </div>
  );
}

function Metric({ label, value, note }: { label: string; value: string | number; note: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd><small>{note}</small></div>;
}
