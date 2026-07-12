import { Link } from 'react-router-dom';
import { EmptyState } from '../components/EmptyState';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { StatusBadge } from '../components/StatusBadge';
import { shortId, summarize, timeAgo } from '../lib/format';
import type { Run } from '../api/types';
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
}: {
  runs: readonly Run[];
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  filter: RunFilter;
  onFilterChange: (filter: RunFilter) => void;
  canRun: boolean;
}) {
  return (
    <section className={styles.section} aria-labelledby="recent-tasks-heading">
      <div className={styles.head}>
        <div>
          <span className={styles.eyebrow}>Activity</span>
          <h2 id="recent-tasks-heading">Recent tasks</h2>
        </div>
        <div className={styles.filters} aria-label="Filter recent tasks">
          {([
            ['all', 'All'],
            ['sessions', 'Sessions'],
            ['reviews', 'Reviews'],
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
        </div>
      </div>

      {isLoading ? (
        <LoadingBlock label="Loading tasks…" />
      ) : error ? (
        <ErrorBlock error={error} onRetry={onRetry} title="Couldn't load tasks" />
      ) : runs.length === 0 ? (
        <EmptyState
          data-testid="runs-empty"
          title={filter === 'all' ? 'No tasks yet' : `No ${filter} for this service`}
          description={
            filter === 'all'
              ? canRun
                ? 'Dispatch your first task using the composer above.'
                : 'No tasks have been dispatched in this project yet.'
              : 'Try another filter or choose a different service.'
          }
        />
      ) : (
        <ul className={styles.list} data-testid="runs-activity" aria-label="Recent tasks">
          {runs.map((run) => (
            <li key={run.id}>
              <Link
                to={`/runs/${run.id}`}
                className={styles.row}
                data-testid="run-row"
                data-run-id={run.id}
                data-status={run.status}
                aria-label={`${runKindLabel(run)}: ${summarize(run.prompt)} — ${run.status}`}
              >
                <span className={styles.origin} data-kind={run.kind ?? 'agent'} aria-hidden />
                <span className={styles.copy}>
                  <span className={styles.title}>
                    {summarize(run.prompt)}
                    {run.retried_from && <span className={styles.retry} title="Retry of an earlier run">retry</span>}
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
