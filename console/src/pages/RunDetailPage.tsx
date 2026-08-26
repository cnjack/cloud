import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { ArrowClockwise, ArrowLeft, ArrowSquareOut, Check, Cpu, ShieldWarning, Stop } from '@phosphor-icons/react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { RuntimeProvider, ToolRegistryProvider, type ThreadItem } from 'jcode-ui';
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
  useRuns,
  useSendMessage,
  useProjectModels,
} from '../api/queries';
import { useApi } from '../api/ApiProvider';
import { ApiError } from '../api/client';
import { isTerminal, type FailureReason, type ProjectModel, type ProvenanceActorRef, type ResumeSessionOptions, type ReviewPlan, type ReviewResult, type Run, type RunProvenance, type SCMGrant, type WorkflowContract } from '../api/types';
import { Button } from '../components/Button';
import { DiffView } from '../components/DiffView';
import { useModelGate } from '../components/ModelGate';
import { PrPanel } from '../components/PrPanel';
import { LoadingBlock, ErrorBlock, InlineHint } from '../components/States';
import { Spinner } from '../components/Spinner';
import { StatusBadge } from '../components/StatusBadge';
import { AccountHeader } from '../components/AccountHeader';
import { Modal } from '../components/Modal';
import { useToast } from '../components/Toast';
import { UsageSummary } from '../components/UsageSummary';
import { useRunStream } from '../hooks/useRunStream';
import { formatDateTime, formatDuration, shortId } from '../lib/format';
import { Timeline, toThreadItems } from '../runview';
import { followConversationScroll } from '../runview/conversationScroll';
import { ConversationRail } from '../work-home/ConversationRail';
import { RunSessionComposer } from './RunSessionComposer';
import styles from './RunDetailPage.module.css';

function failureLabel(reason: FailureReason, t: TFunction): string {
  const labels: Record<FailureReason, string> = {
    clone_failed: t('runDetail.failure.cloneFailed'),
    setup_failed: t('runDetail.failure.setupFailed'),
    agent_error: t('runDetail.failure.agentError'),
    model_rate_limited: t('runDetail.failure.modelRateLimited'),
    timeout: t('runDetail.failure.timeout'),
    push_failed: t('runDetail.failure.pushFailed'),
  };
  return labels[reason];
}

function permissionModeLabel(mode: Run['permission_mode'], t: TFunction): string {
  if (mode === 'approval') return t('runDetail.inspector.askBeforeActions');
  if (mode === 'plan') return t('runDetail.permission.plan');
  if (mode === 'auto') return t('device.composer.modeAuto');
  return t('runDetail.permission.fullAccess');
}

type View = 'conversation' | 'diff' | 'pr';
type FailedSubmission = { runId: string; kind: 'follow_up' | 'resume'; text: string; options?: ResumeSessionOptions };

export function RunDetailPage() {
  const { runId = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const api = useApi();
  const { t } = useTranslation();
  const [view, setView] = useState<View>('conversation');
  const [failedSubmission, setFailedSubmission] = useState<FailedSubmission | null>(null);
	const [conversationRailCollapsed, setConversationRailCollapsed] = useState(false);
	const [retryDialogOpen, setRetryDialogOpen] = useState(false);
	const [retryModelId, setRetryModelId] = useState('');
  const conversationRef = useRef<HTMLElement>(null);
  const conversationContentRef = useRef<HTMLDivElement>(null);
  const followConversationRef = useRef(true);
  const programmaticConversationScrollRef = useRef(false);

  useEffect(() => {
    setFailedSubmission(null);
	setRetryDialogOpen(false);
	setRetryModelId('');
    setView('conversation');
  }, [runId]);

  const stream = useRunStream(runId);
  const streamFailed = stream.phase === 'error' && !stream.terminal;
  const run = useRun(runId, streamFailed);
  const project = useProject(run.data?.project_id ?? '');
  const conversationRuns = useRuns(run.data?.project_id ?? '');
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
  const projectModels = useProjectModels(run.data?.project_id ?? '', canAct);
	const retryModelOptions = useMemo(() => {
		const currentModelID = run.data?.model_id;
		const currentModelName = run.data?.model_name;
		return (projectModels.data?.models ?? []).filter((model) =>
			currentModelID ? model.id !== currentModelID : model.model_name !== currentModelName,
		);
	}, [projectModels.data?.models, run.data?.model_id, run.data?.model_name]);
	const sameModelRetryAvailable = useMemo(() => {
		if (!run.data || !projectModels.data) return false;
		if (run.data.model_id) {
			return projectModels.data.models.some((model) => model.id === run.data?.model_id);
		}
		return projectModels.data.env_fallback;
	}, [projectModels.data, run.data]);
  const artifactReady = stream.events.some(
    (event) => event.type === 'run.artifact' && event.payload?.kind === 'diff',
  );
  const diff = useDiff(runId, !noChanges && (status === 'succeeded' || artifactReady));

  useEffect(() => {
    followConversationRef.current = true;
  }, [runId]);

  useEffect(() => {
    if (view !== 'conversation' || !followConversationRef.current) return;
    const conversation = conversationRef.current;
    const content = conversationContentRef.current;
    if (!conversation || !content) return;

    let releaseTimer = 0;
    const applyFollow = () => {
      if (!followConversationRef.current) return;
      programmaticConversationScrollRef.current = true;
      followConversationScroll(conversation, status);
      window.clearTimeout(releaseTimer);
      releaseTimer = window.setTimeout(() => {
        programmaticConversationScrollRef.current = false;
      }, 100);
    };

    // The historical event backlog can land before this scroll container
    // mounts, while rich Markdown may change its height after the first paint.
    // Cover both DOM restoration and later layout without depending solely on
    // requestAnimationFrame (background tabs can pause it indefinitely).
    applyFollow();
    const resizeObserver = typeof ResizeObserver === 'undefined'
      ? null
      : new ResizeObserver(applyFollow);
    const mutationObserver = typeof MutationObserver === 'undefined'
      ? null
      : new MutationObserver(applyFollow);
    resizeObserver?.observe(content);
    mutationObserver?.observe(content, { childList: true, subtree: true, characterData: true });
    return () => {
      resizeObserver?.disconnect();
      mutationObserver?.disconnect();
      window.clearTimeout(releaseTimer);
      programmaticConversationScrollRef.current = false;
    };
  }, [project.data?.id, runId, status, stream.events.length, view]);

  const decidePermission = useCallback((requestId: string, optionId: string) => {
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
            toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : t('runDetail.toast.permissionDecisionFailed') });
          },
        },
      );
  }, [canAct, respondPermission, runId, t, toast]);

  const sendFollowUp = useCallback((text: string) => {
    setFailedSubmission(null);
    sendMessage.mutate(
      { runId, prompt: text },
      {
        onError: (error) => {
          setFailedSubmission({ runId, kind: 'follow_up', text });
          toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : t('runDetail.toast.messageFailed') });
        },
      },
    );
  }, [runId, sendMessage, toast]);

  const continueSession = useCallback((text: string, options?: ResumeSessionOptions) => {
    setFailedSubmission(null);
    resumeSession.mutate(
      { runId, prompt: text, options },
      {
        onSuccess: (nextRun) => {
          toast.push({ kind: 'success', message: t('runDetail.toast.sessionResumed') });
          navigate(`/runs/${nextRun.id}`);
        },
        onError: (error) => {
          setFailedSubmission({ runId, kind: 'resume', text, options });
          toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : t('runDetail.toast.resumeFailed') });
        },
      },
    );
  }, [navigate, resumeSession, runId, toast]);

  const runtimeItems = useMemo<ThreadItem[]>(() => {
    const current = run.data;
    if (!current) return toThreadItems(stream.events, { decided: permDecided, disabled: !canAct });
    return [
      {
        kind: 'message',
        seq: 0,
        data: {
          id: `run-prompt-${current.id}`,
          role: 'user',
          content: current.prompt,
          timestamp: Date.parse(current.created_at),
        },
      },
      ...toThreadItems(stream.events, { decided: permDecided, disabled: !canAct }),
    ];
  }, [canAct, permDecided, run.data, stream.events]);

  const runtime = useMemo<ChatRuntime>(() => {
    const state: RuntimeState = {
      items: runtimeItems,
      isRunning: status === 'running',
      tokenSnapshot: null,
      goal: null,
      todos: [],
      queued: [],
      connection: 'connected',
    };
    const actions: RuntimeActions = {
      sendMessage: (text) => terminal ? continueSession(text) : sendFollowUp(text),
      enqueueMessage: sendFollowUp,
      removeQueuedMessage: () => {},
      stop: () => cancel.mutate(runId, {
        onSuccess: () => toast.push({ kind: 'info', message: t('runDetail.toast.canceled') }),
        onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : t('runDetail.toast.cancelFailed') }),
      }),
      resolveApproval: () => {},
      resolveApprovalOption: decidePermission,
      submitAskUser: () => {},
      editMessage: () => {},
    };
    return { getState: () => state, subscribe: () => () => {}, actions };
  }, [cancel, continueSession, decidePermission, runId, runtimeItems, sendFollowUp, status, terminal, toast]);

  if (run.isLoading) return <LoadingBlock label={t('runDetail.loadingRun')} />;
  if (!run.data) return <ErrorBlock error={run.error} onRetry={() => run.refetch()} title={t('runDetail.loadRunError')} />;
  if (project.isLoading) return <LoadingBlock label={t('runDetail.loadingWorkspace')} />;

  const current = run.data;
  const services = project.data?.services ?? [];
  const service = services.find((entry) => entry.id === current.service_id);
  const activeServiceId = service?.id ?? current.service_id ?? services[0]?.id ?? '';
  const serviceTasksPath = activeServiceId
    ? `/repositories/${encodeURIComponent(activeServiceId)}?tab=tasks`
    : '/repositories';
  const repositoryName = service?.repo_owner_name ?? service?.name ?? t('runDetail.repositoryUnavailable');
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
    onSuccess: () => toast.push({ kind: 'info', message: t('runDetail.toast.canceled') }),
    onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : t('runDetail.toast.cancelFailed') }),
  });
	const doRetry = (modelId?: string) => retry.mutate({
		runId,
		options: modelId ? { model_id: modelId } : undefined,
	}, {
    onSuccess: (nextRun) => {
		setRetryDialogOpen(false);
      toast.push({ kind: 'success', message: t('runDetail.toast.retryDispatched') });
      navigate(`/runs/${nextRun.id}`);
    },
    onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : t('runDetail.toast.retryFailed') }),
  });
  const doFinishSession = () => finishSession.mutate(runId, {
    onSuccess: () => toast.push({ kind: 'info', message: t('runDetail.toast.sessionFinishing') }),
    onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : t('runDetail.toast.finishFailed') }),
  });

  return (
    <RuntimeProvider runtime={runtime}>
      <ToolRegistryProvider>
        <div className={styles.page} data-testid="run-workspace" data-rail-collapsed={conversationRailCollapsed || undefined}>
          <ConversationRail
            repositories={services}
            runs={conversationRuns.data ?? [current]}
            isLoading={conversationRuns.isLoading}
            collapsed={conversationRailCollapsed}
            onCollapsedChange={setConversationRailCollapsed}
          />
          <section className={styles.runSurface} data-testid="run-surface">
            <AccountHeader sectionTitle={repositoryName} />
            <div className={styles.taskDetail}>
              <header className={styles.taskHeader} data-testid="run-status-header" data-status={current.status}>
                <Link to={serviceTasksPath} className={styles.backToProject} data-testid="run-back-to-project"><ArrowLeft size={16} weight="regular" aria-hidden="true" /><span>{t('runDetail.recentTasks')}</span></Link>
                <div className={styles.taskTitleRow}>
                  <div>
                    <h1 className={styles.taskTitle} data-testid="run-task-title" title={isReview ? current.pr_title || current.prompt : current.prompt}>{isReview ? current.pr_title || t('runDetail.review.titleFallback', { number: current.pr_number || '—' }) : current.prompt}</h1>
                    <p>{runKindLabel(current, t)} · {repositoryName} · {runOriginLabel(current, t)} · {formatDateTime(current.created_at)}</p>
                  </div>
                  <div className={styles.headerActions}>
                    <StatusBadge status={current.status} />
                    {isReview && <ReviewDeliveryState run={current} />}
                    {noChanges && <span className={styles.noChangesBadge} data-testid="no-changes-badge">{t('runDetail.noChangesBadge')}</span>}
                    {!terminalRun && canAct && <Button variant="secondary" size="sm" onClick={doCancel} loading={cancel.isPending} data-testid="cancel-btn"><Stop size={15} weight="regular" aria-hidden="true" /><span>{t('runDetail.action.stop')}</span></Button>}
					{terminalRun && canAct && <Button variant="secondary" size="sm" onClick={() => doRetry()} loading={retry.isPending && !retryDialogOpen} disabled={!modelGate.configured || projectModels.isLoading || !sameModelRetryAvailable} data-testid="retry-btn"><ArrowClockwise size={15} weight="regular" aria-hidden="true" /><span>{t('runDetail.action.retry')}</span></Button>}
					{terminalRun && canAct && retryModelOptions.length > 0 && <Button variant="secondary" size="sm" onClick={() => {
						const first = retryModelOptions[0];
						if (!first) return;
						setRetryModelId(first.id);
						setRetryDialogOpen(true);
					}} data-testid="retry-other-model-btn"><Cpu size={15} weight="regular" aria-hidden="true" /><span>{t('runDetail.action.retryOtherModel')}</span></Button>}
                  </div>
                </div>
              </header>

              <div className={styles.taskLayout}>
                <div className={styles.conversationColumn} data-testid="conversation-column">
                  <main
                    ref={conversationRef}
                    className={styles.conversation}
                    data-testid="conversation-scroll"
                    data-scrollbar="hidden"
                    onScroll={(event) => {
                      if (programmaticConversationScrollRef.current) return;
                      const target = event.currentTarget;
                      followConversationRef.current = target.scrollHeight - target.scrollTop - target.clientHeight < 80;
                    }}
                  >
                    <div ref={conversationContentRef} className={styles.conversationContent}>
                      {terminalRun && canAct && modelGate.notice}
					  {terminalRun && canAct && !projectModels.isLoading && !sameModelRetryAvailable && retryModelOptions.length > 0 && (
						<InlineHint>{t('runDetail.retryModel.originalUnavailable')}</InlineHint>
					  )}
                      {failed && (
                        <div className={styles.failBanner} role="alert" data-testid="failure-banner">
                          <strong>{current.failure_reason ? failureLabel(current.failure_reason, t) : t('runDetail.failure.runFailed')}</strong>
                          <span>{current.failure_message || current.error || t('runDetail.failure.noMessage')}</span>
                        </div>
                      )}
                      {streamFailed && (
                        <div className={styles.streamError} role="alert" data-testid="stream-error">
                          <span>{t('runDetail.streamDisconnected')}</span>
                          <Button variant="secondary" size="sm" onClick={stream.reconnect} data-testid="stream-reconnect">{t('runDetail.reconnect')}</Button>
                        </div>
                      )}
                      {run.isError && run.data && <InlineHint>{t('runDetail.refreshError')}</InlineHint>}

                      {view === 'diff' ? (
                        <RunDiff run={current} noChanges={noChanges} diff={diff} downloadUrl={api.diffDownloadUrl(runId)} onBack={() => setView('conversation')} />
                      ) : view === 'pr' ? (
                        <div className={styles.subview}><button type="button" onClick={() => setView('conversation')}><ArrowLeft size={16} weight="regular" aria-hidden="true" /><span>{t('runDetail.action.conversation')}</span></button><PrPanel runId={runId} projectId={current.project_id} canReview={canAct} /></div>
                      ) : (
                        <>
                          <div className={styles.dateDivider}><span>{new Date(current.created_at).toLocaleDateString()}</span></div>
                          {isReview ? (
                            <ReviewWorkspace run={current} terminal={terminalRun} events={<Timeline />} />
                          ) : (
                            <>
                              <Timeline />
                              {stream.events.length === 0 && !live && (
                                <p className={styles.empty}>{terminalRun ? t('runDetail.noEvents') : t('runDetail.waitingForAgent')}</p>
                              )}
                            </>
                          )}
                        </>
                      )}
                    </div>
                  </main>

                  {view === 'conversation' && (
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
                      models={projectModels.data?.models ?? []}
                      onSend={(text, options) => terminalRun ? continueSession(text, options) : sendFollowUp(text)}
                      onFinish={doFinishSession}
                      onCancel={doCancel}
                    />
                  )}
                </div>

                <RunInspector
                  run={current}
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
          </section>
		  <Modal
			open={retryDialogOpen}
			onClose={() => !retry.isPending && setRetryDialogOpen(false)}
			title={t('runDetail.retryModel.title')}
			data-testid="retry-model-dialog"
			footer={<>
				<Button variant="ghost" onClick={() => setRetryDialogOpen(false)} disabled={retry.isPending}>{t('common.cancel')}</Button>
				<Button variant="primary" onClick={() => doRetry(retryModelId)} loading={retry.isPending} disabled={!retryModelId}>{t('runDetail.retryModel.confirm')}</Button>
			</>}
		  >
			<div className={styles.retryModelDialog}>
				<p>{t('runDetail.retryModel.description')}</p>
				<div className={styles.retryCurrentModel}>
					<span>{t('runDetail.retryModel.current')}</span>
					<strong>{current.model_name || t('runDetail.retryModel.unknown')}</strong>
				</div>
				<fieldset className={styles.retryModelOptions}>
					<legend>{t('runDetail.retryModel.choose')}</legend>
					{retryModelOptions.map((model) => (
						<label key={model.id} data-selected={retryModelId === model.id}>
							<input type="radio" name="retry-model" value={model.id} checked={retryModelId === model.id} onChange={() => setRetryModelId(model.id)} />
							<span><strong>{model.name}</strong><small>{model.model_name}</small></span>
						</label>
					))}
				</fieldset>
			</div>
		  </Modal>
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
  models,
  onSend,
  onFinish,
  onCancel,
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
  models: readonly ProjectModel[];
  onSend: (text: string, options?: ResumeSessionOptions) => void;
  onFinish: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  if (!current.session || !canAct) return null;
  if (sessionLive && !sessionAwaiting && !sessionTurnRunning) {
    return <div className={styles.sessionDock} data-testid="session-panel"><div className={styles.sessionPending}><span data-testid="session-pending-note">{current.status === 'queued' ? t('runDetail.session.sessionQueued') : t('runDetail.session.sessionStarting')}</span></div></div>;
  }
  if (sessionLive) {
    return (
      <div className={styles.sessionDock} data-testid="session-panel">
        <RunSessionComposer
          runId={current.id}
          disabled={sendPending || cancelPending}
          configurable={false}
          running={sessionTurnRunning}
          placeholder={sessionAwaiting ? t('runDetail.composer.continuePlaceholder') : t('runDetail.composer.followUpPlaceholder')}
          currentModelId={current.model_id ?? ''}
          currentPermissionMode={current.permission_mode}
          models={models}
          onSend={onSend}
          onStop={onCancel}
        />
        {failedSubmission?.kind === 'follow_up' && <FailedSubmissionNotice submission={failedSubmission} onRetry={() => onSend(failedSubmission.text)} />}
        <div className={styles.sessionActions}>
          <span data-testid="session-actions-hint">{t('runDetail.session.sessionActionsHint')}</span>
          <div className={styles.sessionActionButtons}>
            <Button type="button" variant="ghost" size="sm" onClick={onFinish} loading={finishPending} data-testid="session-finish-btn">{t('runDetail.session.finishSession')}</Button>
          </div>
        </div>
      </div>
    );
  }
  return (
    <div className={styles.sessionDock} data-testid="resume-session-panel">
      <span className={styles.resumeHint}>{t('runDetail.session.resumeHint')}</span>
      <RunSessionComposer
        runId={current.id}
        disabled={!modelConfigured || resumePending}
        configurable
        running={false}
        placeholder={t('runDetail.composer.continuePlaceholder')}
        currentModelId={current.model_id ?? ''}
        currentPermissionMode={current.permission_mode}
        models={models}
        onSend={onSend}
      />
      {failedSubmission?.kind === 'resume' && <FailedSubmissionNotice submission={failedSubmission} onRetry={() => onSend(failedSubmission.text, failedSubmission.options)} />}
    </div>
  );
}

function RunInspector({
  run,
  noChanges,
  diffState,
  diffContent,
  startedAt,
  onDiff,
  onPr,
  showPr,
}: {
  run: Run;
  noChanges: boolean;
  diffState: 'loading' | 'error' | 'ready' | 'unavailable';
  diffContent?: string;
  startedAt?: string | null;
  onDiff: () => void;
  onPr: () => void;
  showPr: boolean;
}) {
  const { t } = useTranslation();
  return (
    <aside className={styles.inspector} data-testid="run-inspector" aria-label={t('runDetail.inspector.runDetailsLabel')}>
      <ProvenanceSection provenance={run.provenance} />
      <div className={`${styles.inspectorSection} ${styles.inspectorUsage}`} data-testid="run-usage">
        <UsageSummary value={run.usage_summary} />
      </div>
      <WorkflowContractSection contract={run.execution_contract} />
      {run.kind === 'review' && <SCMGrantSection grant={run.scm_grant} />}
      <InspectorSection title={t('runDetail.inspector.runOverview')}>
        <dl className={styles.facts}>
			<InspectorFact label={t('runDetail.inspector.executionStatus')}>{currentRunStatusLabel(run.status, t)}</InspectorFact>
			<InspectorFact label={t('runDetail.inspector.deliveryStatus')}>
				<span data-testid="delivery-status">{deliveryStatusLabel(run.delivery_status, t)}</span>
			</InspectorFact>
			{run.delivery_kind && <InspectorFact label={t('runDetail.inspector.deliveryKind')}>{deliveryKindLabel(run.delivery_kind, t)}</InspectorFact>}
			{run.delivery_error && <InspectorFact label={t('runDetail.inspector.deliveryIssue')}>{run.delivery_error}</InspectorFact>}
          <InspectorFact label={t('runDetail.inspector.permission')}>{permissionModeLabel(run.permission_mode, t)}</InspectorFact>
          <InspectorFact label={t('runDetail.inspector.workspace')}>{run.k8s_job_name || t('runDetail.inspector.notReported')}</InspectorFact>
        </dl>
      </InspectorSection>
      {!run.kind || run.kind === 'agent' ? (
        <InspectorSection title={t('runDetail.inspector.changes')}>
          <p className={styles.inspectorHint}>{noChanges ? t('runDetail.inspector.noCodeChanges') : diffState === 'ready' ? t('runDetail.inspector.reviewPatch') : diffState === 'loading' ? t('runDetail.inspector.loadingChangeSummary') : diffState === 'error' ? t('runDetail.inspector.diffUnavailable') : t('runDetail.inspector.diffAfterAgent')}</p>
          {diffContent && <DiffSummary patch={diffContent} />}
          <button type="button" className={styles.inspectorAction} onClick={onDiff} data-testid="tab-diff"><ArrowSquareOut size={14} weight="regular" aria-hidden="true" /><span>{t('runDetail.inspector.viewCompleteDiff')}</span></button>
	          {showPr && <button type="button" className={styles.inspectorAction} onClick={onPr} data-testid="tab-pr"><ArrowSquareOut size={14} weight="regular" aria-hidden="true" /><span>{t('runDetail.inspector.openPrDetails')}</span></button>}
	          {run.pr_url && <a className={styles.inspectorAction} href={run.pr_url} target="_blank" rel="noreferrer" data-testid="pr-link"><span>{t(run.pr_state === 'merged' ? 'runDetail.inspector.mergedPr' : run.pr_state === 'closed' ? 'runDetail.inspector.closedPr' : run.pr_draft === true ? 'runDetail.inspector.draftPr' : run.pr_draft === false ? 'runDetail.inspector.readyPr' : 'runDetail.inspector.pullRequest')} {run.pr_number ? `#${run.pr_number}` : ''}</span><ArrowSquareOut size={14} weight="regular" aria-hidden="true" /></a>}
	          {run.pr_state === 'conflict' && <p className={styles.inspectorHint} data-testid="pr-delivery-conflict">{t('runDetail.inspector.prDeliveryConflict')}</p>}
	          {run.pr_state === 'provider_unsupported' && <p className={styles.inspectorHint} data-testid="pr-provider-unsupported">{t('runDetail.inspector.prProviderUnsupported')}</p>}
        </InspectorSection>
      ) : null}
      <InspectorSection title={t('runDetail.inspector.execution')}>
        <dl className={styles.facts}>
          <InspectorFact label={t('runDetail.inspector.started')}>{startedAt ? formatDateTime(startedAt) : t('runDetail.inspector.notStarted')}</InspectorFact>
          <InspectorFact label={t('runDetail.inspector.duration')}>{startedAt ? formatDuration(startedAt, run.finished_at) : t('runDetail.inspector.notStarted')}</InspectorFact>
          <InspectorFact label={t('runDetail.inspector.runId')}><code>{run.id}</code></InspectorFact>
          {run.retried_from && <InspectorFact label={t('runDetail.inspector.retryOf')}><Link to={`/runs/${run.retried_from}`}>{shortId(run.retried_from)}</Link></InspectorFact>}
          {run.resumed_from && <InspectorFact label={t('runDetail.inspector.resumedFrom')}><Link to={`/runs/${run.resumed_from}`} data-testid="resumed-from">{t('runDetail.inspector.resumedFromLink', { id: shortId(run.resumed_from) })}</Link></InspectorFact>}
        </dl>
        <OriginReference run={run} />
      </InspectorSection>
    </aside>
  );
}

function WorkflowContractSection({ contract }: { contract?: WorkflowContract }) {
  const { t } = useTranslation();
  if (!contract) {
    return (
      <InspectorSection title={t('runDetail.contract.title')}>
        <p className={styles.inspectorHint} data-testid="workflow-contract-unavailable">{t('runDetail.contract.legacyUnavailable')}</p>
      </InspectorSection>
    );
  }
  const output = contract.delivery.outputs[0];
  const delivery = output
    ? [output.type, output.target, output.ready_policy].filter(Boolean).join(' · ')
    : t('runDetail.inspector.unavailable');
  const verification = [contract.verification.mode, contract.verification.rules_revision].filter(Boolean).join(' · ');
  return (
    <InspectorSection title={t('runDetail.contract.title')}>
      <dl className={styles.facts} data-testid="workflow-contract">
        <InspectorFact label={t('runDetail.contract.workflow')}>{contract.workflow.name} v{contract.workflow.revision}</InspectorFact>
        <InspectorFact label={t('runDetail.contract.profile')}>{contract.profile.name} · {contract.profile.role}</InspectorFact>
        <InspectorFact label={t('runDetail.contract.trigger')}>{contract.trigger.kind}</InspectorFact>
        <InspectorFact label={t('runDetail.contract.model')}>{contract.execution.llm_selection.model_name || t('runDetail.inspector.unavailable')}</InspectorFact>
        <InspectorFact label={t('runDetail.contract.timeout')}>{formatTimeout(contract.execution.timeout_seconds)} · {contract.execution.timeout_source}</InspectorFact>
        <InspectorFact label={t('runDetail.contract.delivery')}>{delivery}</InspectorFact>
        <InspectorFact label={t('runDetail.contract.verification')}>{verification}</InspectorFact>
      </dl>
      <details className={styles.provenanceTechnical} data-testid="workflow-contract-technical">
        <summary>{t('runDetail.contract.technical')}</summary>
        <dl className={styles.facts}>
          <InspectorFact label={t('runDetail.contract.hash')}><code>{contract.hash}</code></InspectorFact>
          <InspectorFact label={t('runDetail.contract.definition')}><code>{contract.workflow.definition_hash}</code></InspectorFact>
          <InspectorFact label={t('runDetail.contract.access')}>{contract.execution.workspace_access}</InspectorFact>
          <InspectorFact label={t('runDetail.contract.requirements')}>{contract.requirements.join(', ')}</InspectorFact>
        </dl>
      </details>
    </InspectorSection>
  );
}

function ReviewDeliveryState({ run }: { run: Run }) {
  const { t } = useTranslation();
  const reviewComplete = run.review_result?.completion?.status === 'complete';
  const state = run.review_posted_at
    ? (reviewComplete ? 'posted' : 'incomplete')
    : run.review_delivery_error
      ? 'error'
    : run.review_result
      ? run.status === 'failed' || run.status === 'canceled' ? 'incomplete' : 'posting'
      : isTerminal(run.status) ? 'unavailable' : 'running';
  return (
    <span className={styles.reviewDeliveryState} data-testid="review-delivery-state" data-state={state}>
      <span aria-hidden="true" />
      {t(`runDetail.review.delivery.${state}`)}
    </span>
  );
}

function ReviewWorkspace({ run, terminal, events }: { run: Run; terminal: boolean; events: ReactNode }) {
  const { t } = useTranslation();
  const hasPlan = !!run.review_plan;
  const hasResult = !!run.review_result;

  return (
    <div className={styles.reviewWorkspace} data-testid="review-workspace">
      <div className={styles.reviewRevisionStrip} data-testid="review-revision-strip">
        <div>
          <span>{t('runDetail.review.pullRequest')}</span>
          {run.pr_url ? <a href={run.pr_url} target="_blank" rel="noreferrer">#{run.pr_number || '—'} <ArrowSquareOut size={12} aria-hidden="true" /></a> : <strong>#{run.pr_number || '—'}</strong>}
        </div>
        <div><span>{t('runDetail.review.base')}</span><code>{run.pr_base_branch || shortRevision(run.pr_base_sha || '') || '—'}</code></div>
        <div><span>{t('runDetail.review.head')}</span><code>{run.pr_head_branch || shortRevision(run.pr_head_sha || '') || '—'}</code></div>
        <div><span>{t('runDetail.review.revisions')}</span><code>{shortRevision(run.pr_base_sha || '') || '—'} → {shortRevision(run.pr_head_sha || '') || '—'}</code></div>
      </div>

      {run.review_delivery_error && (
        <section className={styles.reviewDeliveryError} data-testid="review-delivery-error" role="alert">
          <ShieldWarning size={18} weight="fill" aria-hidden="true" />
          <div><strong>{t('runDetail.review.deliveryError.title')}</strong><p>{run.review_delivery_error}</p></div>
        </section>
      )}

      {hasPlan ? <ReviewCoverageCard plan={run.review_plan} /> : !terminal ? (
        <ReviewStage
          testId="review-stage-plan"
          title={t('runDetail.review.stage.planTitle')}
          body={t('runDetail.review.stage.planBody')}
          events={events}
        />
      ) : (
        <ReviewCoverageCard />
      )}

      {hasResult ? <ReviewResultPanel result={run.review_result!} /> : hasPlan && !terminal ? (
        <ReviewStage
          testId="review-stage-analysis"
          title={t('runDetail.review.stage.analysisTitle')}
          body={t('runDetail.review.stage.analysisBody')}
          events={events}
        />
      ) : terminal ? (
        <section className={styles.reviewUnavailable} data-testid="review-legacy-unavailable" role="status">
          <strong>{t('runDetail.review.legacy.title')}</strong>
          <p>{t('runDetail.review.legacy.body')}</p>
        </section>
      ) : null}
    </div>
  );
}

function ReviewStage({ testId, title, body, events }: { testId: string; title: string; body: string; events: ReactNode }) {
  const { t } = useTranslation();
  return (
    <section className={styles.reviewStage} data-testid={testId}>
      <div className={styles.reviewStageLead}>
        <Spinner label={title} />
        <div><strong>{title}</strong><p>{body}</p></div>
      </div>
      <details className={styles.reviewStageEvents}>
        <summary>{t('runDetail.review.stage.activity')}</summary>
        {events}
      </details>
    </section>
  );
}

function ReviewResultPanel({ result }: { result: ReviewResult }) {
  const { t } = useTranslation();
  const complete = result.completion?.status === 'complete';
  const checks = result.checks ?? [];
  return (
    <section className={styles.reviewResult} data-testid="review-result">
      <header>
        <div><span>{t('runDetail.review.result.eyebrow')}</span><h2>{t('runDetail.review.result.title')}</h2></div>
        <strong>{t('runDetail.review.result.count', { count: result.findings.length })}</strong>
      </header>
      <p className={styles.reviewSummary}>{result.summary}</p>
      {!complete && (
        <div className={styles.reviewIncomplete} data-testid="review-incomplete" role="status">
          <ShieldWarning size={18} weight="fill" aria-hidden="true" />
          <div>
            <strong>{t('runDetail.review.result.incompleteTitle')}</strong>
            <p>{t('runDetail.review.result.incompleteBody')}</p>
            {!!result.completion?.reasons?.length && <code>{result.completion.reasons.join(' · ')}</code>}
          </div>
        </div>
      )}
      {result.findings.length === 0 && complete ? (
        <div className={styles.reviewNoFindings} data-testid="review-no-findings"><Check size={18} weight="bold" aria-hidden="true" /><span>{t('runDetail.review.result.noFindings')}</span></div>
      ) : result.findings.length > 0 ? (
        <ol className={styles.findingList}>
          {result.findings.map((finding, index) => (
            <li key={`${finding.path}:${finding.line}:${index}`} className={styles.findingCard} data-severity={finding.severity}>
              <div className={styles.findingHead}>
                <span className={styles.findingSeverity}>{finding.severity}</span>
                <div><h3>{finding.title}</h3><code>{finding.path}:{finding.line}{finding.end_line && finding.end_line !== finding.line ? `–${finding.end_line}` : ''}</code></div>
                <span className={styles.findingConfidence}>{t('runDetail.review.result.confidence', { confidence: finding.confidence })}</span>
              </div>
              <p>{finding.body}</p>
              {finding.suggestion && <div className={styles.findingSuggestion}><span>{t('runDetail.review.result.suggestion')}</span><code>{finding.suggestion}</code></div>}
            </li>
          ))}
        </ol>
      ) : null}
      {checks.length > 0 && (
        <details className={styles.reviewChecks}>
          <summary>{t('runDetail.review.result.checks', { count: checks.length })}</summary>
          <ul>{checks.map((check) => <li key={check}>{check}</li>)}</ul>
        </details>
      )}
    </section>
  );
}

function SCMGrantSection({ grant }: { grant?: SCMGrant }) {
  const { t } = useTranslation();
  return (
    <InspectorSection title={t('runDetail.review.grant.title')}>
      {grant ? (
        <dl className={styles.facts} data-testid="scm-grant">
          <InspectorFact label={t('runDetail.review.grant.provider')}>{grant.provider}</InspectorFact>
          <InspectorFact label={t('runDetail.review.grant.repository')}>{grant.repository}</InspectorFact>
          <InspectorFact label={t('runDetail.review.grant.installation')}><code>{grant.installation_id || '—'}</code></InspectorFact>
          <InspectorFact label={t('runDetail.review.grant.config')}>v{grant.provider_config_revision ?? '—'}</InspectorFact>
          <InspectorFact label={t('runDetail.review.grant.credential')}><code>{grant.credential_version_id || '—'}</code></InspectorFact>
          <InspectorFact label={t('runDetail.review.grant.principal')}>{grant.acting_principal_kind || '—'}</InspectorFact>
        </dl>
      ) : <p className={styles.inspectorHint} data-testid="scm-grant-unavailable">{t('runDetail.review.grant.unavailable')}</p>}
    </InspectorSection>
  );
}

function ReviewCoverageCard({ plan }: { plan?: ReviewPlan }) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<'indexed' | 'skipped'>('indexed');
  if (!plan) {
    return (
      <section className={styles.reviewCoverage} data-testid="review-coverage-unavailable" aria-label={t('runDetail.coverage.title')}>
        <header><div><span>{t('runDetail.coverage.eyebrow')}</span><h2>{t('runDetail.coverage.title')}</h2></div><strong data-coverage="unavailable">{t('runDetail.coverage.unavailable')}</strong></header>
        <p>{t('runDetail.coverage.legacyUnavailable')}</p>
      </section>
    );
  }
  const indexed = plan.files.filter((file) => file.status === 'indexed');
  const skipped = plan.files.filter((file) => file.status !== 'indexed');
  const visibleFiles = tab === 'indexed' ? indexed : skipped;
  const coverageLabel = plan.coverage === 'complete' ? t('runDetail.coverage.complete') : t('runDetail.coverage.partial');
  return (
    <section className={styles.reviewCoverage} data-testid="review-coverage" aria-label={t('runDetail.coverage.title')}>
      <header>
        <div><span>{t('runDetail.coverage.eyebrow')}</span><h2>{t('runDetail.coverage.title')}</h2></div>
        <strong data-coverage={plan.coverage}>{coverageLabel}</strong>
      </header>
      <div className={styles.coverageMetrics}>
        <div><b>{plan.indexed_files}/{plan.changed_files}</b><span>{t('runDetail.coverage.files')}</span></div>
        <div><b>{plan.indexed_hunks}/{plan.changed_hunks}</b><span>{t('runDetail.coverage.hunks')}</span></div>
        <div><b>{plan.changed_lines}</b><span>{t('runDetail.coverage.lines')}</span></div>
      </div>
      <dl className={styles.coverageRevisions}>
        <div><dt>{t('runDetail.coverage.baseHead')}</dt><dd><code>{shortRevision(plan.base_sha)} → {shortRevision(plan.head_sha)}</code></dd></div>
        <div><dt>{t('runDetail.coverage.mergeBase')}</dt><dd><code>{shortRevision(plan.merge_base_sha)}</code></dd></div>
      </dl>
      <div className={styles.coverageLedger}>
        <div className={styles.coverageTabs} role="tablist" aria-label={t('runDetail.coverage.ledger')}>
          <button id="review-coverage-indexed-tab" type="button" role="tab" aria-controls="review-coverage-panel" aria-selected={tab === 'indexed'} onClick={() => setTab('indexed')}>{t('runDetail.coverage.indexed', { count: indexed.length })}</button>
          <button id="review-coverage-skipped-tab" type="button" role="tab" aria-controls="review-coverage-panel" aria-selected={tab === 'skipped'} onClick={() => setTab('skipped')} data-testid="review-coverage-tab-skipped">{t('runDetail.coverage.skipped', { count: skipped.length })}</button>
        </div>
        <div id="review-coverage-panel" role="tabpanel" aria-labelledby={`review-coverage-${tab}-tab`}>
          {visibleFiles.length > 0 ? (
            <ul>{visibleFiles.map((file) => <li key={file.path}><code>{file.path}</code><span>{file.status === 'indexed' ? t('runDetail.coverage.fileStats', { hunks: file.hunks, lines: file.changed_lines }) : file.reason || t('runDetail.coverage.unsupported')}</span></li>)}</ul>
          ) : <p>{tab === 'skipped' ? t('runDetail.coverage.noneSkipped') : t('runDetail.coverage.noneIndexed')}</p>}
        </div>
      </div>
    </section>
  );
}

function formatTimeout(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

function shortRevision(value: string): string {
  return value.length > 10 ? value.slice(0, 10) : value;
}

function currentRunStatusLabel(status: Run['status'], t: TFunction): string {
  return t(`status.${status}`);
}

function deliveryStatusLabel(status: Run['delivery_status'], t: TFunction): string {
  if (!status) return t('runDetail.inspector.deliveryUnknown');
  return t(`runDetail.inspector.delivery.${status}`);
}

function deliveryKindLabel(kind: NonNullable<Run['delivery_kind']>, t: TFunction): string {
  return t(`runDetail.inspector.deliveryKindLabel.${kind}`);
}

function ProvenanceSection({ provenance }: { provenance?: RunProvenance }) {
  const { t } = useTranslation();
  const executedFor = provenance?.executed_for;
  const unavailable = t('runDetail.inspector.unavailable');

  return (
    <>
      <InspectorSection title={t('runDetail.inspector.identityAndSource')}>
        <dl className={`${styles.facts} ${styles.provenanceFacts}`} data-testid="run-provenance">
          <InspectorFact label={t('runDetail.inspector.requestedBy')}>
            <ActorDisplay
              actor={provenance?.requested_actor}
              qualifier={provenance?.requested_actor?.kind === 'external_actor' ? t('runDetail.inspector.externalActor') : undefined}
              testId="provenance-requested"
            />
          </InspectorFact>
          <InspectorFact label={t('runDetail.inspector.accountableTo')}>
            <ActorDisplay
              actor={provenance?.accountable_actor}
              qualifier={provenance?.precision === 'rule_owner' ? t('runDetail.inspector.ruleOwner') : undefined}
              testId="provenance-accountable"
            />
          </InspectorFact>
          <InspectorFact label={t('runDetail.inspector.triggeredFrom')}>
            <span className={styles.triggerReference} data-testid="provenance-trigger">
              <span>
                {provenance?.trigger.href ? (
                  <a href={provenance.trigger.href} target="_blank" rel="noreferrer">
                    {provenance.trigger.label} <ArrowSquareOut size={12} weight="regular" aria-hidden="true" />
                  </a>
                ) : provenance?.trigger.label ?? unavailable}
              </span>
              {provenance?.trigger.ref && <small>{provenance.trigger.ref}</small>}
            </span>
          </InspectorFact>
        </dl>
      </InspectorSection>
      <InspectorSection title={t('runDetail.inspector.executedFor')}>
        <dl className={styles.facts} data-testid="provenance-executed-for">
          <InspectorFact label={t('runDetail.inspector.repository')}>{executedFor?.repository || unavailable}</InspectorFact>
          <InspectorFact label={t('runDetail.modelLabel')}>
            <span className={styles.executedFor}>
              <span>{executedFor?.model || unavailable}</span>
              {executedFor?.model && <small>{t('runDetail.inspector.dispatchSnapshot')}</small>}
            </span>
          </InspectorFact>
        </dl>
      </InspectorSection>
      <InspectorSection title={t('runDetail.inspector.writtenBackAs')}>
        <div className={styles.botIdentity} data-testid="provenance-written-back">
          {provenance?.writeback_actor ? <span className={styles.botMark}>JC</span> : null}
          <ActorDisplay
            actor={provenance?.writeback_actor}
            qualifier={provenance?.writeback_actor ? t('runDetail.inspector.botOrApp') : undefined}
            emptyLabel={t('runDetail.inspector.notApplicable')}
            testId="provenance-writeback-actor"
          />
        </div>
      </InspectorSection>
      {provenance && (
        <section className={styles.inspectorSection}>
          <details className={styles.provenanceTechnical}>
            <summary>{t('runDetail.inspector.technicalIdentity')}</summary>
            <dl className={styles.facts}>
              <InspectorFact label={t('runDetail.inspector.runtimePrincipal')}>
                {provenance.runtime_principal?.label || unavailable}
              </InspectorFact>
              <InspectorFact label={t('runDetail.inspector.attribution')}>
                {provenancePrecisionLabel(provenance.precision, t)}
              </InspectorFact>
              <InspectorFact label={t('runDetail.inspector.source')}>
                <code>{provenance.attribution_source || unavailable}</code>
              </InspectorFact>
            </dl>
          </details>
        </section>
      )}
    </>
  );
}

function ActorDisplay({
  actor,
  qualifier,
  emptyLabel,
  testId,
}: {
  actor?: ProvenanceActorRef;
  qualifier?: string;
  emptyLabel?: string;
  testId: string;
}) {
  const { t } = useTranslation();
  return (
    <span className={styles.actorDisplay} data-testid={testId}>
      <span>{actor?.label || emptyLabel || t('runDetail.inspector.notAttributed')}</span>
      {qualifier && <small>{qualifier}</small>}
    </span>
  );
}

function provenancePrecisionLabel(precision: string, t: TFunction): string {
  const labels: Record<string, string> = {
    exact: t('runDetail.inspector.precisionExact'),
    linked_external: t('runDetail.inspector.precisionLinkedExternal'),
    rule_owner: t('runDetail.inspector.precisionRuleOwner'),
    unattributed: t('runDetail.inspector.precisionUnattributed'),
  };
  return labels[precision] ?? precision;
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
  const { t } = useTranslation();
  if (run.origin === 'webhook' && run.origin_comment_url) {
    return <a className={styles.originRef} href={run.origin_comment_url} target="_blank" rel="noreferrer" data-testid="origin-chip"><span>{t('runDetail.origin.fromPrComment')}</span><ArrowSquareOut size={14} weight="regular" aria-hidden="true" /></a>;
  }
  if (run.origin === 'schedule') return <span className={styles.originRef} data-testid="origin-chip-schedule">{t('runDetail.origin.scheduled')}</span>;
  if (run.origin === 'kanban') return <span className={styles.originRef} data-testid="origin-chip-kanban">{t('runDetail.origin.kanbanAutomation')}</span>;
  if (run.origin === 'automation') return <span className={styles.originRef} data-testid="origin-chip-automation">{t('runDetail.origin.prEventAutomation')}</span>;
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
  const { t } = useTranslation();
  return (
    <div className={styles.subview}>
      <button type="button" onClick={onBack}><ArrowLeft size={16} weight="regular" aria-hidden="true" /><span>{t('runDetail.action.conversation')}</span></button>
      {run.status !== 'succeeded' && !diff.data ? <p className={styles.empty}>{run.status === 'failed' ? t('runDetail.diff.runFailedNoDiff') : t('runDetail.diff.diffAfterArtifact')}</p>
        : noChanges ? <p className={styles.empty} data-testid="diff-no-changes">{t('runDetail.diff.noCodeChanges')}</p>
          : diff.isLoading ? <LoadingBlock label={t('runDetail.diff.loadingDiff')} />
            : diff.isError ? <ErrorBlock error={diff.error} onRetry={() => diff.refetch()} title={t('runDetail.diff.loadDiffError')} />
              : <DiffView patch={diff.data?.content ?? ''} downloadUrl={downloadUrl} downloadName={`${shortId(run.id)}.diff`} />}
    </div>
  );
}

function FailedSubmissionNotice({ submission, onRetry }: { submission: FailedSubmission; onRetry: () => void }) {
  const { t } = useTranslation();
  return (
    <div className={styles.failedSubmission} role="alert" data-testid="failed-submission">
      <strong>{t('runDetail.failedSubmission.notSent')}</strong><span>{t('runDetail.failedSubmission.draftPreserved')}</span><pre>{submission.text}</pre>
      <Button type="button" variant="secondary" size="sm" onClick={onRetry}>{t('runDetail.failedSubmission.retryUnsent')}</Button>
    </div>
  );
}

function runKindLabel(run: Run, t: TFunction): string {
  if (run.kind === 'review') return t('runDetail.kind.codeReview');
  if (run.session) return t('runDetail.kind.session');
  return run.origin && run.origin !== 'api'
    ? t('runDetail.kind.automatedTask')
    : t('runDetail.kind.manualTask');
}

function runOriginLabel(run: Run, t: TFunction): string {
  if (run.origin === 'webhook') return t('runDetail.originLabel.webhook');
  if (run.origin === 'schedule') return t('runDetail.originLabel.schedule');
  if (run.origin === 'kanban') return t('runDetail.originLabel.kanban');
  if (run.origin === 'automation') return t('runDetail.originLabel.prEvent');
  return t('runDetail.originLabel.manual');
}
