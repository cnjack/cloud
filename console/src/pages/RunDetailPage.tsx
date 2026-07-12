import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { ChatInput, RuntimeProvider, ToolRegistryProvider } from 'jcode-ui';
import type { ChatRuntime, RuntimeActions, RuntimeState } from 'jcode-ui-core/runtime';
import {
  useCancelRun,
  useDiff,
  useFinishSession,
  useProject,
  useRespondPermission,
  useResumeSession,
  useRetryRun,
  useRun,
  useSendMessage,
} from '../api/queries';
import { useApi } from '../api/ApiProvider';
import { ApiError } from '../api/client';
import { isTerminal, type FailureReason, type Run } from '../api/types';
import { Button } from '../components/Button';
import { DiffView } from '../components/DiffView';
import { Markdown } from '../components/Markdown';
import { useModelGate } from '../components/ModelGate';
import { PrPanel } from '../components/PrPanel';
import { LoadingBlock, ErrorBlock, InlineHint } from '../components/States';
import { Spinner } from '../components/Spinner';
import { StatusBadge } from '../components/StatusBadge';
import { ThemeToggle } from '../components/ThemeToggle';
import { useToast } from '../components/Toast';
import { Wordmark } from '../components/Wordmark';
import { useRunStream } from '../hooks/useRunStream';
import { formatDateTime, formatDuration, shortId } from '../lib/format';
import { ProjectWorkspaceShell } from '../project-workspace/ProjectWorkspaceShell';
import { ProjectSettingsAction } from '../project-workspace/ProjectSettingsAction';
import { Timeline, type PermissionControls } from '../runview';
import styles from './RunDetailPage.module.css';

const FAILURE_LABELS: Record<FailureReason, string> = {
  clone_failed: 'Repository clone failed',
  setup_failed: 'Project setup failed',
  agent_error: 'Agent error',
  timeout: 'Timed out',
  push_failed: 'Branch push failed',
};

type View = 'conversation' | 'diff' | 'pr';
type FailedSubmission = { runId: string; kind: 'follow_up' | 'resume'; text: string };

export function RunDetailPage() {
  const { runId = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const api = useApi();
  const [view, setView] = useState<View>('conversation');
  const [failedSubmission, setFailedSubmission] = useState<FailedSubmission | null>(null);

  useEffect(() => {
    setFailedSubmission(null);
    setView('conversation');
  }, [runId]);

  const stream = useRunStream(runId);
  const streamFailed = stream.phase === 'error' && !stream.terminal;
  const run = useRun(runId, streamFailed);
  const project = useProject(run.data?.project_id ?? '');
  const canAct = (project.data?.role ?? 'owner') !== 'viewer';
  const cancel = useCancelRun();
  const retry = useRetryRun();
  const sendMessage = useSendMessage();
  const finishSession = useFinishSession();
  const resumeSession = useResumeSession();
  const respondPermission = useRespondPermission();
  const [permDecided, setPermDecided] = useState<Record<string, string>>({});

  const status = run.data?.status;
  const terminal = status ? isTerminal(status) : false;
  const noChanges = status === 'succeeded' && run.data?.result === 'no_changes';
  const modelGate = useModelGate(run.data?.project_id ?? '', canAct && terminal);
  const artifactReady = stream.events.some(
    (event) => event.type === 'run.artifact' && event.payload?.kind === 'diff',
  );
  const diff = useDiff(runId, !noChanges && (status === 'succeeded' || artifactReady));

  const permissionControls: PermissionControls = {
    disabled: !canAct,
    decided: permDecided,
    onDecide: (requestId, optionId) => {
      if (!canAct) return;
      setPermDecided((current) => ({ ...current, [requestId]: optionId }));
      respondPermission.mutate(
        { runId, requestId, optionId },
        {
          onError: (error) => {
            setPermDecided((current) => {
              const next = { ...current };
              delete next[requestId];
              return next;
            });
            toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : 'Could not send the permission decision.' });
          },
        },
      );
    },
  };

  const runtime = useMemo<ChatRuntime>(() => {
    const state: RuntimeState = {
      items: [],
      isRunning: status === 'running',
      tokenSnapshot: null,
      goal: null,
      todos: [],
      queued: [],
    };
    const sendFollowUp = (text: string) => {
      setFailedSubmission(null);
      sendMessage.mutate(
        { runId, prompt: text },
        {
          onError: (error) => {
            setFailedSubmission({ runId, kind: 'follow_up', text });
            toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : 'Could not send the message.' });
          },
        },
      );
    };
    const continueSession = (text: string) => {
      setFailedSubmission(null);
      resumeSession.mutate(
        { runId, prompt: text },
        {
          onSuccess: (nextRun) => {
            toast.push({ kind: 'success', message: 'Session resumed.' });
            navigate(`/runs/${nextRun.id}`);
          },
          onError: (error) => {
            setFailedSubmission({ runId, kind: 'resume', text });
            toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : 'Could not resume the session.' });
          },
        },
      );
    };
    const actions: RuntimeActions = {
      sendMessage: terminal ? continueSession : sendFollowUp,
      enqueueMessage: sendFollowUp,
      removeQueuedMessage: () => {},
      stop: () => cancel.mutate(runId, {
        onSuccess: () => toast.push({ kind: 'info', message: 'Run canceled.' }),
        onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : 'Cancel failed.' }),
      }),
      resolveApproval: () => {},
      submitAskUser: () => {},
      editMessage: () => {},
    };
    return { getState: () => state, subscribe: () => () => {}, actions };
  }, [cancel, navigate, resumeSession, runId, sendMessage, status, terminal, toast]);

  if (run.isLoading) return <LoadingBlock label="Loading run…" />;
  if (!run.data) return <ErrorBlock error={run.error} onRetry={() => run.refetch()} title="Couldn't load run" />;
  if (project.isLoading) return <LoadingBlock label="Loading project workspace…" />;

  const current = run.data;
  const services = project.data?.services ?? [];
  const service = services.find((entry) => entry.id === current.service_id);
  const activeServiceId = service?.id ?? current.service_id ?? services[0]?.id ?? '';
  const projectName = project.data?.name ?? `Project ${shortId(current.project_id)}`;
  const terminalRun = isTerminal(current.status);
  const failed = current.status === 'failed';
  const isSession = current.session === true;
  const sessionAwaiting = isSession && current.status === 'awaiting_input';
  const sessionTurnRunning = isSession && current.status === 'running';
  const sessionLive = isSession && !terminalRun;
  const isReview = current.kind === 'review';
  const live = current.status === 'running' && stream.phase === 'live';
  const inferredStartedAt = current.started_at ?? stream.events.find(
    (event) => event.type === 'run.status' && event.payload?.status === 'running',
  )?.ts;

  const doCancel = () => cancel.mutate(runId, {
    onSuccess: () => toast.push({ kind: 'info', message: 'Run canceled.' }),
    onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : 'Cancel failed.' }),
  });
  const doRetry = () => retry.mutate(runId, {
    onSuccess: (nextRun) => {
      toast.push({ kind: 'success', message: 'Retry dispatched.' });
      navigate(`/runs/${nextRun.id}`);
    },
    onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : 'Retry failed.' }),
  });
  const doFinishSession = () => finishSession.mutate(runId, {
    onSuccess: () => toast.push({ kind: 'info', message: 'Session finishing — the agent is wrapping up.' }),
    onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : 'Could not finish the session.' }),
  });

  return (
    <RuntimeProvider runtime={runtime}>
      <ToolRegistryProvider>
        <div className={styles.page} data-testid="run-workspace">
          <ProjectWorkspaceShell
            mode="detail"
            projectName={projectName}
            services={services}
            activeServiceId={activeServiceId}
            activeTab="tasks"
            canManage={(project.data?.role ?? 'owner') === 'owner'}
            onSelectService={(serviceId) => navigate(`/projects/${current.project_id}?service=${encodeURIComponent(serviceId)}&tab=tasks`)}
            onSelectTab={() => navigate(`/projects/${current.project_id}`)}
            railTop={<><Wordmark /><Link to="/" className={styles.projectsLink}>Projects</Link></>}
            railFooter={<div className={styles.railFooter}><span>Project workspace</span><ThemeToggle /></div>}
            projectAction={(project.data?.role ?? 'owner') === 'owner' ? (
              <ProjectSettingsAction
                to={`/projects/${current.project_id}?service=${encodeURIComponent(activeServiceId)}&tab=tasks&view=project-settings`}
              />
            ) : undefined}
            utility={
              <>
                <nav className={styles.breadcrumbs} aria-label="Breadcrumb">
                  <Link to="/">Projects</Link><span>/</span>
                  <Link to={`/projects/${current.project_id}`}>{projectName}</Link><span>/</span>
                  <span>Tasks</span><span>/</span><span>Detail</span>
                </nav>
                <ThemeToggle />
              </>
            }
          >
            <div className={styles.taskDetail}>
              <header className={styles.taskHeader} data-testid="run-status-header" data-status={current.status}>
                <Link to={`/projects/${current.project_id}`} className={styles.backToProject} data-testid="run-back-to-project">← Recent tasks</Link>
                <div className={styles.taskTitleRow}>
                  <div>
                    <h1>{current.prompt}</h1>
                    <p>{runKindLabel(current)} · {service?.name ?? 'Service unavailable'} · {runOriginLabel(current)} · {formatDateTime(current.created_at)}</p>
                  </div>
                  <div className={styles.headerActions}>
                    <StatusBadge status={current.status} />
                    {noChanges && <span className={styles.noChangesBadge} data-testid="no-changes-badge">No changes</span>}
                    {!terminalRun && canAct && <Button variant="secondary" size="sm" onClick={doCancel} loading={cancel.isPending} data-testid="cancel-btn">Stop</Button>}
                    {terminalRun && canAct && <Button variant="secondary" size="sm" onClick={doRetry} loading={retry.isPending} disabled={!modelGate.configured} data-testid="retry-btn">Retry</Button>}
                  </div>
                </div>
              </header>

              <div className={styles.taskLayout}>
                <main className={styles.conversation}>
                  {terminalRun && canAct && modelGate.notice}
                  {failed && (
                    <div className={styles.failBanner} role="alert" data-testid="failure-banner">
                      <strong>{current.failure_reason ? FAILURE_LABELS[current.failure_reason] : 'Run failed'}</strong>
                      <span>{current.failure_message || current.error || 'The run failed without a message.'}</span>
                    </div>
                  )}
                  {streamFailed && (
                    <div className={styles.streamError} role="alert" data-testid="stream-error">
                      <span>Live updates disconnected. Showing periodic refreshes.</span>
                      <Button variant="secondary" size="sm" onClick={stream.reconnect} data-testid="stream-reconnect">Reconnect</Button>
                    </div>
                  )}
                  {run.isError && run.data && <InlineHint>Couldn't refresh the latest run details — showing the last known state.</InlineHint>}

                  {view === 'diff' ? (
                    <RunDiff run={current} noChanges={noChanges} diff={diff} downloadUrl={api.diffDownloadUrl(runId)} onBack={() => setView('conversation')} />
                  ) : view === 'pr' ? (
                    <div className={styles.subview}><button type="button" onClick={() => setView('conversation')}>← Conversation</button><PrPanel runId={runId} projectId={current.project_id} canReview={canAct} /></div>
                  ) : (
                    <>
                      <div className={styles.dateDivider}><span>{new Date(current.created_at).toLocaleDateString()}</span></div>
                      <article className={styles.initialPrompt} data-testid="run-initial-prompt">
                        <div className={styles.userAvatar}>U</div>
                        <div><div className={styles.messageMeta}><strong>You</strong><time>{formatDateTime(current.created_at)}</time></div><p>{current.prompt}</p></div>
                      </article>

                      {isReview && current.review_output ? (
                        <div className={styles.reviewOutput} data-testid="review-output"><Markdown source={current.review_output} /></div>
                      ) : isReview && !terminalRun ? (
                        <div className={styles.reviewProgress} data-testid="review-in-progress"><Spinner label="Review in progress…" /><Timeline events={stream.events} isRunning={live} permissions={permissionControls} /></div>
                      ) : isReview ? (
                        <p className={styles.empty}>{failed ? 'This review failed, so no output was produced.' : 'No review output was produced.'}</p>
                      ) : stream.events.length > 0 || live ? (
                        <Timeline events={stream.events} isRunning={live} permissions={permissionControls} />
                      ) : (
                        <p className={styles.empty}>{terminalRun ? 'No conversation events were recorded.' : 'Waiting for the agent…'}</p>
                      )}

                      <SessionComposer
                        current={current}
                        canAct={canAct}
                        sessionLive={sessionLive}
                        sessionAwaiting={sessionAwaiting}
                        sessionTurnRunning={sessionTurnRunning}
                        modelConfigured={modelGate.configured}
                        sendPending={sendMessage.isPending}
                        resumePending={resumeSession.isPending}
                        cancelPending={cancel.isPending}
                        finishPending={finishSession.isPending}
                        failedSubmission={failedSubmission?.runId === runId ? failedSubmission : null}
                        onRetryFailed={(text) => runtime.actions.sendMessage(text)}
                        onFinish={doFinishSession}
                      />
                    </>
                  )}
                </main>

                <RunInspector
                  run={current}
                  serviceName={service?.name}
                  noChanges={noChanges}
                  diffState={diff.isLoading ? 'loading' : diff.isError ? 'error' : diff.data ? 'ready' : 'unavailable'}
                  diffContent={diff.data?.content}
                  startedAt={inferredStartedAt}
                  onDiff={() => setView('diff')}
                  onPr={() => setView('pr')}
                  showPr={!isReview && !!current.pr_url}
                />
              </div>
            </div>
          </ProjectWorkspaceShell>
        </div>
      </ToolRegistryProvider>
    </RuntimeProvider>
  );
}

function SessionComposer({
  current,
  canAct,
  sessionLive,
  sessionAwaiting,
  sessionTurnRunning,
  modelConfigured,
  sendPending,
  resumePending,
  cancelPending,
  finishPending,
  failedSubmission,
  onRetryFailed,
  onFinish,
}: {
  current: Run;
  canAct: boolean;
  sessionLive: boolean;
  sessionAwaiting: boolean;
  sessionTurnRunning: boolean;
  modelConfigured: boolean;
  sendPending: boolean;
  resumePending: boolean;
  cancelPending: boolean;
  finishPending: boolean;
  failedSubmission: FailedSubmission | null;
  onRetryFailed: (text: string) => void;
  onFinish: () => void;
}) {
  if (!current.session || !canAct) return null;
  if (sessionLive && !sessionAwaiting && !sessionTurnRunning) {
    return <div className={styles.sessionPending} data-testid="session-panel"><span data-testid="session-pending-note">{current.status === 'queued' ? 'Session queued — waiting for a free session slot in this project.' : 'Session starting — the workspace is being scheduled.'}</span></div>;
  }
  if (sessionLive) {
    return (
      <div className={styles.sessionPanel} data-testid="session-panel">
        <fieldset disabled={sendPending || cancelPending}>
          <ChatInput placeholder={sessionAwaiting ? 'Continue this task…' : 'Queue a follow-up — it will run after the current turn…'} showContextBar={false} />
        </fieldset>
        {failedSubmission?.kind === 'follow_up' && <FailedSubmissionNotice submission={failedSubmission} onRetry={() => onRetryFailed(failedSubmission.text)} />}
        <div className={styles.sessionActions}>
          <span data-testid="session-actions-hint">Finish lets the agent wrap up; Cancel stops immediately.</span>
          <Button type="button" variant="ghost" size="sm" onClick={onFinish} loading={finishPending} data-testid="session-finish-btn">Finish session</Button>
        </div>
      </div>
    );
  }
  return (
    <div className={styles.sessionPanel} data-testid="resume-session-panel">
      <span>Continue this session — the agent keeps the same context.</span>
      <fieldset disabled={!modelConfigured || resumePending}><ChatInput placeholder="Continue this task…" showContextBar={false} /></fieldset>
      {failedSubmission?.kind === 'resume' && <FailedSubmissionNotice submission={failedSubmission} onRetry={() => onRetryFailed(failedSubmission.text)} />}
    </div>
  );
}

function RunInspector({
  run,
  serviceName,
  noChanges,
  diffState,
  diffContent,
  startedAt,
  onDiff,
  onPr,
  showPr,
}: {
  run: Run;
  serviceName?: string;
  noChanges: boolean;
  diffState: 'loading' | 'error' | 'ready' | 'unavailable';
  diffContent?: string;
  startedAt?: string | null;
  onDiff: () => void;
  onPr: () => void;
  showPr: boolean;
}) {
  return (
    <aside className={styles.inspector} data-testid="run-inspector" aria-label="Run details">
      <InspectorSection title="Run overview">
        <dl className={styles.facts}>
          <InspectorFact label="Service">{serviceName ?? (run.service_id ? shortId(run.service_id) : 'Unavailable')}</InspectorFact>
          <InspectorFact label="Trigger">{runOriginLabel(run)}</InspectorFact>
          <InspectorFact label="Model">{run.model_name || run.model_id || 'Not reported'}</InspectorFact>
          <InspectorFact label="Permission">{run.permission_mode === 'approval' ? 'Ask before actions' : 'Full access'}</InspectorFact>
          <InspectorFact label="Workspace">{run.k8s_job_name || 'Not reported'}</InspectorFact>
        </dl>
      </InspectorSection>
      {!run.kind || run.kind === 'agent' ? (
        <InspectorSection title="Changes">
          <p className={styles.inspectorHint}>{noChanges ? 'No code changes' : diffState === 'ready' ? 'Review the complete patch' : diffState === 'loading' ? 'Loading change summary…' : diffState === 'error' ? 'Diff unavailable' : 'Available after the agent produces a diff'}</p>
          {diffContent && <DiffSummary patch={diffContent} />}
          <button type="button" className={styles.inspectorAction} onClick={onDiff} data-testid="tab-diff">↗ View complete diff</button>
          {showPr && <button type="button" className={styles.inspectorAction} onClick={onPr} data-testid="tab-pr">↗ Open pull request details</button>}
          {run.pr_url && <a className={styles.inspectorAction} href={run.pr_url} target="_blank" rel="noreferrer" data-testid="pr-link">Draft PR {run.pr_number ? `#${run.pr_number}` : ''} ↗</a>}
        </InspectorSection>
      ) : null}
      <InspectorSection title="Execution">
        <dl className={styles.facts}>
          <InspectorFact label="Started">{startedAt ? formatDateTime(startedAt) : 'Not started'}</InspectorFact>
          <InspectorFact label="Duration">{startedAt ? formatDuration(startedAt, run.finished_at) : 'Not started'}</InspectorFact>
          <InspectorFact label="Run ID"><code>{run.id}</code></InspectorFact>
          {run.retried_from && <InspectorFact label="Retry of"><Link to={`/runs/${run.retried_from}`}>{shortId(run.retried_from)}</Link></InspectorFact>}
          {run.resumed_from && <InspectorFact label="Resumed from"><Link to={`/runs/${run.resumed_from}`} data-testid="resumed-from">resumed from {shortId(run.resumed_from)}</Link></InspectorFact>}
        </dl>
        <OriginReference run={run} />
      </InspectorSection>
    </aside>
  );
}

function DiffSummary({ patch }: { patch: string }) {
  const files = patch.split('\n').flatMap((line) => {
    if (!line.startsWith('+++ b/')) return [];
    return [line.slice(6)];
  });
  const added = patch.split('\n').filter((line) => line.startsWith('+') && !line.startsWith('+++')).length;
  const removed = patch.split('\n').filter((line) => line.startsWith('-') && !line.startsWith('---')).length;
  return (
    <div className={styles.changeList}>
      {files.slice(0, 3).map((file) => <code key={file}>{file}</code>)}
      <span><b>+{added}</b> <i>−{removed}</i></span>
    </div>
  );
}

function InspectorSection({ title, children }: { title: string; children: ReactNode }) {
  return <section className={styles.inspectorSection}><h2>{title}</h2>{children}</section>;
}

function InspectorFact({ label, children }: { label: string; children: ReactNode }) {
  return <div><dt>{label}</dt><dd>{children}</dd></div>;
}

function OriginReference({ run }: { run: Run }) {
  if (run.origin === 'webhook' && run.origin_comment_url) {
    return <a className={styles.originRef} href={run.origin_comment_url} target="_blank" rel="noreferrer" data-testid="origin-chip">from PR comment ↗</a>;
  }
  if (run.origin === 'schedule') return <span className={styles.originRef} data-testid="origin-chip-schedule">scheduled</span>;
  return null;
}

function RunDiff({
  run,
  noChanges,
  diff,
  downloadUrl,
  onBack,
}: {
  run: Run;
  noChanges: boolean;
  diff: ReturnType<typeof useDiff>;
  downloadUrl: string;
  onBack: () => void;
}) {
  return (
    <div className={styles.subview}>
      <button type="button" onClick={onBack}>← Conversation</button>
      {run.status !== 'succeeded' && !diff.data ? <p className={styles.empty}>{run.status === 'failed' ? 'This run failed, so no diff was produced.' : 'The diff will be available after the agent produces an artifact.'}</p>
        : noChanges ? <p className={styles.empty} data-testid="diff-no-changes">This run made no code changes.</p>
          : diff.isLoading ? <LoadingBlock label="Loading diff…" />
            : diff.isError ? <ErrorBlock error={diff.error} onRetry={() => diff.refetch()} title="Couldn't load diff" />
              : <DiffView patch={diff.data?.content ?? ''} downloadUrl={downloadUrl} downloadName={`${shortId(run.id)}.diff`} />}
    </div>
  );
}

function FailedSubmissionNotice({ submission, onRetry }: { submission: FailedSubmission; onRetry: () => void }) {
  return (
    <div className={styles.failedSubmission} role="alert" data-testid="failed-submission">
      <strong>Message not sent.</strong><span>Your draft is preserved.</span><pre>{submission.text}</pre>
      <Button type="button" variant="secondary" size="sm" onClick={onRetry}>Retry unsent message</Button>
    </div>
  );
}

function runKindLabel(run: Run): string {
  if (run.kind === 'review') return 'Code review';
  return run.session ? 'Session' : 'Manual task';
}

function runOriginLabel(run: Run): string {
  if (run.origin === 'webhook') return 'Provider webhook';
  if (run.origin === 'schedule') return 'Schedule';
  return 'Manual';
}
