import { Cpu, Info } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
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
  const { t } = useTranslation();
  const isAdmin = useRole() === 'cluster-admin';
  const system = useSystem(isAdmin);
  const providers = useModelProviders(isAdmin);
  if (!isAdmin) return <ClusterAccessDenied />;

  return (
    <>
      <ClusterSubnav />
      <SurfaceInner>
        {system.isLoading ? (
          <LoadingBlock label={t('cluster.overview.loadingSnapshot')} />
        ) : system.isError ? (
          <ErrorBlock error={system.error} onRetry={() => system.refetch()} title={t('cluster.overview.snapshotLoadError')} />
        ) : system.data ? (
          <ClusterSnapshot data={system.data} modelCount={(providers.data ?? []).reduce((sum, provider) => sum + provider.models.length, 0)} onRefresh={() => { void system.refetch(); void providers.refetch(); }} />
        ) : null}
      </SurfaceInner>
    </>
  );
}

function ClusterSnapshot({ data, modelCount, onRefresh }: { data: SystemInfo; modelCount: number; onRefresh: () => void }) {
  const { t } = useTranslation();
  const active = data.capacity.running + data.capacity.scheduling;
  const unlimited = data.capacity.max_concurrent_runs <= 0;
  const percent = unlimited ? 0 : Math.min(100, (active / data.capacity.max_concurrent_runs) * 100);
  const available = unlimited ? null : Math.max(0, data.capacity.max_concurrent_runs - active);

  return (
    <div data-testid="cluster-overview">
      <PageHeader
        eyebrow={t('cluster.overview.eyebrow')}
        title={t('cluster.overview.title')}
        description={t('cluster.overview.description')}
        meta={<><span>{t('cluster.overview.metaNamespace', { value: data.namespace || '—' })}</span><span>·</span><span>{t('cluster.overview.metaLauncher', { value: data.launcher || '—' })}</span><span>·</span><span>{t('cluster.overview.metaCommit', { value: data.version.commit || '—' })}</span></>}
        actions={<><StatusLabel tone="success">{t('cluster.overview.snapshotLoaded')}</StatusLabel><Button onClick={onRefresh}>{t('cluster.overview.refresh')}</Button></>}
      />

      <dl className={styles.metrics} aria-label={t('cluster.overview.capacitySummaryAria')}>
        <Metric label={t('cluster.overview.metricRunning')} value={data.capacity.running} note={t('cluster.overview.metricRunningNote')} />
        <Metric label={t('cluster.overview.metricScheduling')} value={data.capacity.scheduling} note={t('cluster.overview.metricSchedulingNote')} />
        <Metric label={t('cluster.overview.metricQueued')} value={data.capacity.queued} note={t('cluster.overview.metricQueuedNote')} />
        <Metric label={t('cluster.overview.metricMax')} value={unlimited ? '∞' : data.capacity.max_concurrent_runs} note={t('cluster.overview.metricMaxNote')} />
      </dl>

      <div className={styles.layout}>
        <div className={styles.main}>
          <SectionPanel title={t('cluster.overview.capacityTitle')} aside={<span className={styles.mono}>{t('cluster.overview.capacityAside', { active, max: unlimited ? '∞' : data.capacity.max_concurrent_runs })}</span>}>
            <div className={styles.capacityBody}>
              <div className={styles.capacityFigure}>
                <div><strong>{unlimited ? '∞' : `${percent.toFixed(percent % 1 === 0 ? 0 : 1)}%`}</strong><span>{unlimited ? t('cluster.overview.concurrencyUncapped') : t('cluster.overview.concurrencyInUse')}</span></div>
                <span className={styles.available}>{available === null ? t('cluster.overview.unlimitedSlots') : t('cluster.overview.slotsAvailable', { count: available })}</span>
              </div>
              {!unlimited && <div className={styles.track} role="progressbar" aria-valuenow={active} aria-valuemin={0} aria-valuemax={data.capacity.max_concurrent_runs}><span style={{ width: `${percent}%` }} /></div>}
              <div className={styles.legend}><span><i />{t('cluster.overview.legendRunning', { count: data.capacity.running })}</span><span><i data-muted="true" />{t('cluster.overview.legendScheduling', { count: data.capacity.scheduling })}</span></div>
            </div>
          </SectionPanel>

          <SectionPanel title={t('cluster.overview.gitPolicyTitle')} aside={<StatusLabel tone={data.provider.gitea_enabled ? 'success' : 'warning'}>{data.provider.gitea_enabled ? t('cluster.overview.giteaEnabled') : t('cluster.overview.giteaDisabled')}</StatusLabel>}>
            <DefinitionList items={[
              { label: t('cluster.overview.giteaUrlLabel'), value: <span className={styles.mono}>{data.provider.gitea_url || '—'}</span> },
              { label: t('cluster.overview.draftPrLabel'), value: data.provider.gitea_enabled ? t('cluster.overview.draftPrAvailable') : t('cluster.overview.draftPrUnavailable') },
              { label: t('cluster.overview.allowedHostsLabel'), value: <span className={styles.mono}>{data.provider.allowed_git_hosts?.length ? data.provider.allowed_git_hosts.join(', ') : t('cluster.overview.unrestricted')}</span> },
            ]} />
          </SectionPanel>

          <div className={styles.callout}><Cpu size={16} aria-hidden="true" /><span><strong>{t('cluster.overview.modelsConfigured', { count: modelCount })}</strong>{t('cluster.overview.modelsManagedPrefix')}<Link to="/cluster/models">{t('cluster.overview.modelsLink')}</Link>{t('cluster.overview.modelsManagedSuffix')}</span></div>
        </div>

        <aside className={styles.aside}>
          <SectionPanel title={t('cluster.overview.guardrailsTitle')}><DefinitionList items={[{ label: t('cluster.overview.runTimeoutLabel'), value: <span className={styles.mono}>{duration(data.guardrails.run_timeout_seconds)}</span> }, { label: t('cluster.overview.jobTtlLabel'), value: <span className={styles.mono}>{duration(data.guardrails.job_ttl_seconds)}</span> }]} /></SectionPanel>
          <SectionPanel title={t('cluster.overview.runnerTitle')}><DefinitionList items={[{ label: t('cluster.overview.imageLabel'), value: <span className={styles.mono}>{data.runner.image || '—'}</span> }, { label: t('cluster.overview.launcherLabel'), value: <span className={styles.mono}>{data.launcher || '—'}</span> }, { label: t('cluster.overview.workspaceLabel'), value: data.runner.persistent_workspace ? t('cluster.overview.persistent') : t('cluster.overview.ephemeral') }]} /></SectionPanel>
          <SectionPanel title={t('cluster.overview.buildTitle')}><DefinitionList items={[{ label: t('cluster.overview.versionLabel'), value: <span className={styles.mono}>{data.version.version || '—'}</span> }, { label: t('cluster.overview.commitLabel'), value: <span className={styles.mono}>{data.version.commit || '—'}</span> }, { label: t('cluster.overview.usersLabel'), value: <span className={styles.mono}>{data.auth?.users_count ?? 0}</span> }]} /></SectionPanel>
          {!data.archive?.enabled && data.archive?.reason && <div className={styles.archiveNote}><Info size={16} aria-hidden="true" /><span><strong>{t('cluster.overview.coldArchiveUnavailable')}</strong>{data.archive.reason}</span></div>}
        </aside>
      </div>
    </div>
  );
}

function Metric({ label, value, note }: { label: string; value: string | number; note: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd><small>{note}</small></div>;
}
