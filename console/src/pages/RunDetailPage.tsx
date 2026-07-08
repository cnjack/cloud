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
import {
  useRun,
  useProject,
  useCancelRun,
  useRetryRun,
  useDiff,
  useSendMessage,
  useFinishSession,
  useRespondPermission,
} from '../api/queries';
import { useApi } from '../api/ApiProvider';
import { useRunStream } from '../hooks/useRunStream';
import { isTerminal, type FailureReason } from '../api/types';
import { Button } from '../components/Button';
import { StatusBadge } from '../components/StatusBadge';
import { useModelGate } from '../components/ModelGate';
import { Timeline, type PermissionControls } from '../runview';
import { DiffView } from '../components/DiffView';
import { Markdown } from '../components/Markdown';
import { PrPanel } from '../components/PrPanel';
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
  push_failed: 'Branch push failed',
};

type Tab = 'events' | 'diff' | 'pr';

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
  // The run's project carries the requesting principal's role — a viewer sees no
  // Retry/Cancel affordances (blueprint §2; the backend also 403s these).
  const project = useProject(run.data?.project_id ?? '');
  const canAct = (project.data?.role ?? 'owner') !== 'viewer';
  const cancel = useCancelRun();
  const retry = useRetryRun();

  // D22 multi-turn session: the follow-up composer + Finish button.
  const sendMessage = useSendMessage();
  const finishSession = useFinishSession();
  const [followUp, setFollowUp] = useState('');

  // F8b permission approval: runview's PermissionCard is app-agnostic — the
  // decide callback + optimistic state are injected from HERE. permDecided
  // maps request_id → option_id the user already submitted, greying the card
  // until the agent.permission_resolved event lands on the stream; a rejected
  // POST rolls the optimistic entry back (the card becomes answerable again
  // unless the stream shows it resolved anyway).
  const respondPermission = useRespondPermission();
  const [permDecided, setPermDecided] = useState<Record<string, string>>({});
  const permissionControls: PermissionControls = {
    disabled: !canAct,
    decided: permDecided,
    onDecide: (requestId, optionId) => {
      if (!canAct) return;
      setPermDecided((cur) => ({ ...cur, [requestId]: optionId }));
      respondPermission.mutate(
        { runId, requestId, optionId },
        {
          onError: (err) => {
            setPermDecided((cur) => {
              const next = { ...cur };
              delete next[requestId];
              return next;
            });
            toast.push({
              kind: 'error',
              message:
                err instanceof ApiError ? err.message : 'Could not send the permission decision.',
            });
          },
        },
      );
    },
  };

  const status = run.data?.status;
  const terminal = status ? isTerminal(status) : false;

  // Fail-visible (D21): Retry creates a fresh run, so it gets the same treatment
  // as the composer — disabled with a notice when the run's project has no
  // available model (the backend 409 remains the backstop). Queried only where
  // the Retry affordance can exist (member+ on a terminal run).
  const modelGate = useModelGate(run.data?.project_id ?? '', canAct && terminal);

  // D18/D26: a succeeded run with no code changes has nothing to fetch — the
  // Diff tab shows a dedicated empty state instead (see below) without ever
  // hitting the diff endpoint.
  const noChanges = status === 'succeeded' && run.data?.result === 'no_changes';

  // Diff loads once the run has succeeded (or when the diff tab is opened).
  const diff = useDiff(runId, tab === 'diff' && status === 'succeeded' && !noChanges);

  // Only dead-end when there's no cached run to show. A failed *refetch* (e.g.
  // the terminal-status invalidate hitting a network blip) keeps the previously
  // fetched data in TanStack Query v5 — don't discard the fully-rendered page.
  if (run.isLoading) return <LoadingBlock label="Loading run…" />;
  if (!run.data)
    // Covers plain errors AND any state where no run is available (e.g. 403 on
    // a run in a project you're not a member of) — never white-screen (M7 find).
    return (
      <ErrorBlock error={run.error} onRetry={() => run.refetch()} title="Couldn't load run" />
    );

  const r = run.data;
  const canCancel = !isTerminal(r.status);
  const canRetry = isTerminal(r.status);
  const failed = r.status === 'failed';
  // D22 session: the follow-up composer shows while the session can take a
  // message — awaiting_input (handled immediately) AND running (queued behind
  // the in-flight turn; the backend accepts both). queued/scheduling sessions
  // show a neutral waiting note instead. Finish is idempotent server-side.
  const isSession = r.session === true;
  const sessionAwaiting = isSession && r.status === 'awaiting_input';
  const sessionTurnRunning = isSession && r.status === 'running';
  const sessionLive = isSession && !isTerminal(r.status);
  // A review run (blueprint §5) has no Diff/PR tabs — its body IS the review
  // output. An agent run gets a third "PR" tab once its draft PR exists.
  const isReview = r.kind === 'review';
  const hasPRTab = !isReview && !!r.pr_url;

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

  // D22 session actions.
  const doSendMessage = (e: React.FormEvent) => {
    e.preventDefault();
    const prompt = followUp.trim();
    if (!prompt) return;
    sendMessage.mutate(
      { runId, prompt },
      {
        // Clear ONLY if the box still holds exactly what we submitted — text the
        // user typed while the request was in flight must never be discarded.
        onSuccess: () => setFollowUp((cur) => (cur.trim() === prompt ? '' : cur)),
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Could not send the message.',
          }),
      },
    );
  };

  const doFinishSession = () =>
    finishSession.mutate(runId, {
      onSuccess: () => toast.push({ kind: 'info', message: 'Session finishing — the agent is wrapping up.' }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Could not finish the session.',
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
            {/* D18/D26: a succeeded run that made no code changes is still a
                success — this badge says so up front instead of making the
                user open the Diff tab to discover it's empty. */}
            {noChanges && (
              <span className={styles.noChangesChip} data-testid="no-changes-badge">
                No changes
              </span>
            )}
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
            {/* M7 (blueprint §8): a run triggered by a Gitea PR comment shows a
                chip linking back to that comment. */}
            {r.origin === 'webhook' && r.origin_comment_url && (
              <a
                className={styles.originChip}
                href={r.origin_comment_url}
                target="_blank"
                rel="noreferrer"
                title="Open the pull-request comment that triggered this run"
                data-testid="origin-chip"
              >
                from PR comment
                <span className={styles.originArrow} aria-hidden>
                  ↗
                </span>
              </a>
            )}
            {/* Stretch (ST-1): draft PR chip — bordered secondary chip with a
                mono PR number, opens the Gitea PR in a new tab. Only when the
                orchestrator has opened the draft PR (git_mode=draft_pr). */}
            {r.pr_url && (
              <a
                className={styles.prChip}
                href={r.pr_url}
                target="_blank"
                rel="noreferrer"
                title="Open the draft pull request on Gitea"
                data-testid="pr-link"
              >
                Draft PR
                {typeof r.pr_number === 'number' && r.pr_number > 0 && (
                  <span className={styles.prNumber}>#{r.pr_number}</span>
                )}
                <span className={styles.prArrow} aria-hidden>
                  ↗
                </span>
              </a>
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
          {canCancel && canAct && (
            <Button
              variant="danger"
              onClick={doCancel}
              loading={cancel.isPending}
              // D22: a session has TWO ways out — make the destructive one say so.
              title={
                isSession
                  ? 'Stop immediately — the turn in progress is discarded. Use “Finish session” below to let the agent wrap up cleanly.'
                  : undefined
              }
              data-testid="cancel-btn"
            >
              Cancel
            </Button>
          )}
          {canRetry && canAct && (
            <Button
              variant="primary"
              onClick={doRetry}
              loading={retry.isPending}
              disabled={!modelGate.configured}
              data-testid="retry-btn"
            >
              Retry
            </Button>
          )}
        </div>
      </header>

      {/* Model gate notice (Feature A): explains a disabled Retry. */}
      {canRetry && canAct && modelGate.notice}

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

      {/* D22 multi-turn session: while the run waits for input, offer the
          follow-up composer + a Finish button. The message lands in the timeline
          as a user bubble; Finish winds the session down to succeeded. */}
      {sessionLive && canAct && (
        <div className={styles.sessionPanel} data-testid="session-panel">
          {sessionAwaiting || sessionTurnRunning ? (
            <form onSubmit={doSendMessage} className={styles.sessionForm} noValidate>
              <textarea
                className={styles.sessionInput}
                placeholder={
                  sessionAwaiting
                    ? 'Send a follow-up message to the agent…'
                    : 'Queue a follow-up — it will be handled after the current turn finishes…'
                }
                value={followUp}
                onChange={(e) => setFollowUp(e.target.value)}
                rows={2}
                data-testid="session-message-input"
              />
              <div className={styles.sessionActions}>
                <Button
                  type="submit"
                  variant="primary"
                  size="sm"
                  loading={sendMessage.isPending}
                  disabled={!followUp.trim()}
                  data-testid="session-message-send"
                >
                  Send
                </Button>
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  onClick={doFinishSession}
                  loading={finishSession.isPending}
                  title="Let the agent wrap up gracefully — the run ends as succeeded"
                  data-testid="session-finish-btn"
                >
                  Finish session
                </Button>
              </div>
              {/* Two ways out, very different semantics — say so where both live. */}
              <span className={styles.sessionActionsHint} data-testid="session-actions-hint">
                Finish lets the agent wrap up and end cleanly; Cancel (top right)
                stops immediately and discards the turn in progress.
              </span>
            </form>
          ) : (
            // queued / scheduling: the session has not started a turn yet — show a
            // neutral waiting note (a queued session may be held by the project's
            // max_live_sessions gate), never "the agent is working".
            <div className={styles.sessionBusy}>
              <span className={styles.sessionBusyText} data-testid="session-pending-note">
                {r.status === 'queued'
                  ? 'Session queued — waiting for a free session slot in this project.'
                  : 'Session starting — the runner is being scheduled.'}
              </span>
            </div>
          )}
        </div>
      )}

      {/* A review run's body IS its markdown output — no Diff/PR tabs. */}
      {isReview ? (
        <div className={styles.panel} data-testid="review-panel">
          {r.review_output ? (
            <div className={styles.reviewOutput} data-testid="review-output">
              <Markdown source={r.review_output} />
            </div>
          ) : !terminal ? (
            <div className={styles.reviewProgress}>
              <div className={styles.reviewProgressHint} data-testid="review-in-progress">
                <Spinner label="Review in progress…" />
              </div>
              {stream.events.length > 0 && (
                <Timeline events={stream.events} live={live} permissions={permissionControls} />
              )}
            </div>
          ) : (
            <div className={styles.waiting}>
              <span className={styles.waitingText}>
                {failed
                  ? 'This review run failed, so no output was produced.'
                  : 'No review output was produced.'}
              </span>
            </div>
          )}
        </div>
      ) : (
        <>
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
            {hasPRTab && (
              <button
                role="tab"
                aria-selected={tab === 'pr'}
                className={styles.tab}
                data-active={tab === 'pr' || undefined}
                onClick={() => setTab('pr')}
                data-testid="tab-pr"
                type="button"
              >
                PR
              </button>
            )}
          </div>

          {/* Tab panels */}
          <div className={styles.panel}>
            {tab === 'pr' ? (
              <PrPanel runId={runId} projectId={run.data?.project_id ?? ''} canReview={canAct} />
            ) : tab === 'events' ? (
              stream.events.length === 0 ? (
                <div className={styles.waiting}>
                  <Spinner label={terminal ? 'No events recorded.' : 'Waiting for events…'} />
                </div>
              ) : (
                <Timeline events={stream.events} live={live} permissions={permissionControls} />
              )
            ) : status !== 'succeeded' ? (
              <div className={styles.waiting}>
                <span className={styles.waitingText}>
                  {failed
                    ? 'This run failed, so no diff was produced.'
                    : 'The diff will be available once the run succeeds.'}
                </span>
              </div>
            ) : noChanges ? (
              // D18/D26: contract with the backend — result: "no_changes" on an
              // otherwise-succeeded run means there's nothing to diff. Skips the
              // diff fetch entirely (see the useDiff `enabled` gate above).
              <div className={styles.waiting} data-testid="diff-no-changes">
                <span className={styles.waitingText}>
                  This run made no code changes.
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
        </>
      )}
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
