import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { EmptyState } from '../components/EmptyState';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { StatusBadge } from '../components/StatusBadge';
import { shortId, summarize, timeAgo } from '../lib/format';
import type { Run } from '../api/types';
import { qk } from '../api/queries';
import { reconcileRunSnapshot } from '../api/runCache';
import { runKindLabel } from './presentation';
import styles from './RunActivityList.module.css';

export type RunFilter = 'all' | 'sessions' | 'reviews';

export function RunActivityList({
  runs,
  isLoading,
  error,
  onRetry,
  filter,
  onFilterChange,
  canRun,
  showFilters = true,
  emptyTitle,
  emptyDescription,
}: {
  runs: readonly Run[];
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  filter: RunFilter;
  onFilterChange: (filter: RunFilter) => void;
  canRun: boolean;
  showFilters?: boolean;
  emptyTitle?: string;
  emptyDescription?: string;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<'all' | 'active' | 'attention' | 'completed'>('all');
  const matchesStatus = (run: Run, group: typeof status) => group === 'all'
    || (group === 'active' && ['queued', 'scheduling', 'running'].includes(run.status))
    || (group === 'attention' && ['awaiting_input', 'blocked', 'failed'].includes(run.status))
    || (group === 'completed' && ['succeeded', 'canceled'].includes(run.status));
  const visible = runs.filter((run) => matchesStatus(run, status) && `${run.prompt} ${run.id}`.toLowerCase().includes(query.trim().toLowerCase()))
    .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  return (
    <section className={styles.section} aria-labelledby="recent-tasks-heading">
      <div className={styles.head}>
        <div>
          <span className={styles.eyebrow}>{t('runActivity.eyebrow')}</span>
          <h2 id="recent-tasks-heading">{t('runActivity.title')}</h2>
        </div>
        {showFilters && <div className={styles.filters} aria-label={t('runActivity.filtersAria')}>
          {([
            ['all', t('runActivity.filterAll')],
            ['sessions', t('runActivity.filterSessions')],
            ['reviews', t('runActivity.filterReviews')],
          ] as const).map(([value, label]) => (
            <button
              key={value}
              type="button"
              className={styles.filter}
              aria-pressed={filter === value}
              onClick={() => onFilterChange(value)}
            >
              {label}
            </button>
          ))}
        </div>}
      </div>

      <div className={styles.toolbar}>
        <div className={styles.statusFilters}>{(['all', 'active', 'attention', 'completed'] as const).map((group) => <button key={group} type="button" aria-pressed={status === group} onClick={() => setStatus(group)}>{t(`cloudRefresh.${group}`)}<span>{runs.filter((run) => matchesStatus(run, group)).length}</span></button>)}</div>
        <input type="search" aria-label={t('cloudRefresh.searchTasks')} placeholder={t('cloudRefresh.searchTasks')} value={query} onChange={(event) => setQuery(event.target.value)} />
      </div>
      {isLoading ? (
        <LoadingBlock label={t('runActivity.loadingTasks')} />
      ) : error ? (
        <ErrorBlock error={error} onRetry={onRetry} title={t('runActivity.errorTitle')} />
      ) : runs.length === 0 ? (
        <EmptyState
          data-testid="runs-empty"
          title={emptyTitle ?? (filter === 'all' ? t('runActivity.emptyTitleAll') : t('runActivity.emptyTitleFiltered', { filter }))}
          description={
            emptyDescription ?? (filter === 'all'
              ? canRun
                ? t('runActivity.emptyDescDispatch')
                : t('runActivity.emptyDescNone')
              : t('runActivity.emptyDescFiltered'))
          }
        />
      ) : !visible.length ? <EmptyState title={t('cloudRefresh.noMatch')} description={t('cloudRefresh.noMatchDescription')} action={<button type="button" onClick={() => { setStatus('all'); setQuery(''); }}>{t('cloudRefresh.clearFilters')}</button>} /> : (
        <ul className={styles.list} data-testid="runs-activity" aria-label={t('runActivity.title')}>
          {visible.map((run) => (
            <li key={run.id}>
              <Link
                to={`/runs/${run.id}`}
                className={styles.row}
                data-testid="run-row"
                data-run-id={run.id}
                data-status={run.status}
                aria-label={t('runActivity.runAria', { kind: runKindLabel(run), summary: summarize(run.prompt), status: run.status })}
                onClick={() => {
                  qc.setQueryData<Run>(qk.run(run.id), (current) =>
                    reconcileRunSnapshot(current, run),
                  );
                }}
              >
                <span className={styles.origin} data-kind={run.kind ?? 'agent'} aria-hidden />
                <span className={styles.copy}>
                  <span className={styles.title}>
                    {summarize(run.prompt)}
                    {run.retried_from && <span className={styles.retry} title={t('runActivity.retryTitle')}>{t('runActivity.retryBadge')}</span>}
                  </span>
                  <span className={styles.meta}>
                    {runKindLabel(run)} · <code>{shortId(run.id)}</code>
                  </span>
                </span>
                <span className={styles.status}>
                  <StatusBadge status={run.status} size="sm" />
                  <time dateTime={run.created_at}>{timeAgo(run.created_at)}</time>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
