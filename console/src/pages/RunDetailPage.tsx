/*
 * RunDetailPage — the hero page (PRD §6 "Run 详情页").
 *
 * - Status header: badge + timing + failure_reason/message when failed +
 *   Retry / Cancel buttons per state (+ optional draft-MR link, ST-1).
 * - Two tabs: event timeline (SSE live-follow) and diff view.
 *
 * Live status comes from useRunStream's derived status (mirrored into the run
 * cache); the header re-renders as events arrive. On refresh, the stream hook
 * replays history then follows live, so the timeline is identical to before the
 * refresh (AC-7).
 */
import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useRun, useCancelRun, useRetryRun, useDiff } from '../api/queries';
import { useApi } from '../api/ApiProvider';
import { useRunStream } from '../hooks/useRunStream';
import { isTerminal, type FailureReason } from '../api/types';
import { Button } from '../components/Button';
import { StatusBadge } from '../components/StatusBadge';
import { Timeline } from '../components/Timeline';
import { DiffView } from '../components/DiffView';
import { LoadingBlock, ErrorBlock, InlineHint } from '../components/States';
import { Spinner } from '../components/Spinner';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import { formatDateTime, formatDuration, shortId } from '../lib/format';
import styles from './RunDetailPage.module.css';

const FAILURE_LABELS: Record<FailureReason, string> = {
  clone_failed: 'Repository clone failed',
  setup_failed: 'Project setup failed',
  agent_error: 'Agent error',
  timeout: 'Timed out',
};

type Tab = 'events' | 'diff';

export function RunDetailPage() {
  const { runId = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const api = useApi();

  const [tab, setTab] = useState<Tab>('events');

  // Live event stream (replay-then-live). Keep it open until terminal.
  // When the stream dies fatally, fall back to polling the run so status still
  // advances (see useRun's refetchInterval).
  const stream = useRunStream(runId);
  const streamFailed = stream.phase === 'error' && !stream.terminal;
  const run = useRun(runId, streamFailed);
  const cancel = useCancelRun();
  const retry = useRetryRun();

  const status = run.data?.status;
  const terminal = status ? isTerminal(status) : false;

  // Diff loads once the run has succeeded (or when the diff tab is opened).
  const diff = useDiff(runId, tab === 'diff' && status === 'succeeded');

  // Only dead-end when there's no cached run to show. A failed *refetch* (e.g.
  // the terminal-status invalidate hitting a network blip) keeps the previously
  // fetched data in TanStack Query v5 — don't discard the fully-rendered page.
  if (run.isLoading) return <LoadingBlock label="Loading run…" />;
  if (run.isError && !run.data)
    return (
      <ErrorBlock error={run.error} onRetry={() => run.refetch()} title="Couldn't load run" />
    );

  const r = run.data!;
  const canCancel = !isTerminal(r.status);
  const canRetry = isTerminal(r.status);
  const failed = r.status === 'failed';

  const doCancel = () =>
    cancel.mutate(runId, {
      onSuccess: () => toast.push({ kind: 'info', message: 'Run canceled.' }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Cancel failed.',
        }),
    });

  const doRetry = () =>
    retry.mutate(runId, {
      onSuccess: (newRun) => {
        toast.push({ kind: 'success', message: 'Retry dispatched.' });
        navigate(`/runs/${newRun.id}`);
      },
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Retry failed.',
        }),
    });

  const live = !terminal && stream.phase === 'live';

  return (
    <div className={styles.page}>
      <nav className={styles.crumbs}>
        <Link to="/" className={styles.crumbLink}>
          Projects
        </Link>
        <span className={styles.crumbSep}>/</span>
        <Link to={`/projects/${r.project_id}`} className={styles.crumbLink}>
          {shortId(r.project_id)}
        </Link>
        <span className={styles.crumbSep}>/</span>
        <span className={styles.crumbCurrent}>run {shortId(r.id)}</span>
      </nav>

      {/* Status header */}
      <header className={styles.header} data-testid="run-status-header" data-status={r.status}>
        <div className={styles.headerMain}>
          <div className={styles.headerTop}>
            <StatusBadge status={r.status} />
            {live && (
              <span className={styles.liveHint}>
                <Spinner />
              </span>
            )}
            <span className={styles.runId}>{shortId(r.id)}</span>
            {r.retried_from && (
              <Link
                to={`/runs/${r.retried_from}`}
                className={styles.retriedFrom}
                title="View the run this was retried from"
              >
                retry of {shortId(r.retried_from)}
              </Link>
            )}
          </div>
          <p className={styles.prompt}>{r.prompt}</p>
          <dl className={styles.timing}>
            <Timing label="Created" value={formatDateTime(r.created_at)} />
            {r.started_at && <Timing label="Started" value={formatDateTime(r.started_at)} />}
            {r.finished_at && <Timing label="Finished" value={formatDateTime(r.finished_at)} />}
            {r.started_at && (
              <Timing
                label="Duration"
                value={formatDuration(r.started_at, r.finished_at)}
              />
            )}
          </dl>
        </div>

        <div className={styles.headerActions}>
          {canCancel && (
            <Button
              variant="danger"
              onClick={doCancel}
              loading={cancel.isPending}
              data-testid="cancel-btn"
            >
              Cancel
            </Button>
          )}
          {canRetry && (
            <Button
              variant="primary"
              onClick={doRetry}
              loading={retry.isPending}
              data-testid="retry-btn"
            >
              Retry
            </Button>
          )}
        </div>
      </header>

      {/* Failure banner */}
      {failed && (
        <div className={styles.failBanner} role="alert" data-testid="failure-banner">
          <div className={styles.failReason} data-reason={r.failure_reason}>
            {r.failure_reason ? FAILURE_LABELS[r.failure_reason] : 'Run failed'}
          </div>
          <div className={styles.failMsg}>
            {r.failure_message || r.error || 'The run failed without a message.'}
          </div>
        </div>
      )}

      {/* Live-stream lost while the run is still active: surface it (the stream
          won't auto-recover) with a Reconnect action. Status keeps advancing via
          the polling fallback (useRun refetchInterval) in the meantime. */}
      {streamFailed && (
        <div className={styles.streamError} role="alert" data-testid="stream-error">
          <span className={styles.streamErrorText}>
            Live updates disconnected. Falling back to periodic refresh.
          </span>
          <Button
            variant="secondary"
            size="sm"
            onClick={stream.reconnect}
            data-testid="stream-reconnect"
          >
            Reconnect
          </Button>
        </div>
      )}

      {/* Non-blocking notice when the latest run refresh failed but we still have
          the cached run to show (we don't dead-end the whole page for this). */}
      {run.isError && run.data && (
        <InlineHint>Couldn't refresh the latest run details — showing the last known state.</InlineHint>
      )}

      {/* Stretch: draft MR link (ST-1) — only when present. */}
      {r.mr_url && (
        <a className={styles.mrLink} href={r.mr_url} target="_blank" rel="noreferrer" data-testid="mr-link">
          View draft MR ↗
        </a>
      )}

      {/* Tabs */}
      <div className={styles.tabs} role="tablist">
        <button
          role="tab"
          aria-selected={tab === 'events'}
          className={styles.tab}
          data-active={tab === 'events' || undefined}
          onClick={() => setTab('events')}
          data-testid="tab-events"
          type="button"
        >
          Events
          {stream.events.length > 0 && (
            <span className={styles.tabCount}>{stream.events.length}</span>
          )}
        </button>
        <button
          role="tab"
          aria-selected={tab === 'diff'}
          className={styles.tab}
          data-active={tab === 'diff' || undefined}
          onClick={() => setTab('diff')}
          data-testid="tab-diff"
          type="button"
        >
          Diff
        </button>
      </div>

      {/* Tab panels */}
      <div className={styles.panel}>
        {tab === 'events' ? (
          stream.events.length === 0 ? (
            <div className={styles.waiting}>
              <Spinner label={terminal ? 'No events recorded.' : 'Waiting for events…'} />
            </div>
          ) : (
            <Timeline events={stream.events} live={live} />
          )
        ) : status !== 'succeeded' ? (
          <div className={styles.waiting}>
            <span className={styles.waitingText}>
              {failed
                ? 'This run failed, so no diff was produced.'
                : 'The diff will be available once the run succeeds.'}
            </span>
          </div>
        ) : diff.isLoading ? (
          <LoadingBlock label="Loading diff…" />
        ) : diff.isError ? (
          <ErrorBlock error={diff.error} onRetry={() => diff.refetch()} title="Couldn't load diff" />
        ) : (
          <DiffView
            patch={diff.data?.content ?? ''}
            downloadUrl={api.diffDownloadUrl(runId)}
            downloadName={`${shortId(runId)}.diff`}
          />
        )}
      </div>
    </div>
  );
}

function Timing({ label, value }: { label: string; value: string }) {
  if (!value) return null;
  return (
    <div className={styles.timingItem}>
      <dt className={styles.timingLabel}>{label}</dt>
      <dd className={styles.timingValue}>{value}</dd>
    </div>
  );
}
