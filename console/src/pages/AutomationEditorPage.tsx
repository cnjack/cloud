import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { ArrowLeft, Warning } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { Disclosure, DisclosureButton, DisclosurePanel } from '@headlessui/react';
import { Button } from '../components/Button';
import { ErrorBlock, LoadingBlock } from '../components/States';
import {
  useCreateProjectAutomation,
  useProject,
  useProjectAutomation,
  useProjectModels,
  useProviderCapabilities,
  useUpdateProjectAutomation,
} from '../api/queries';
import type {
  AutomationTriggerKind,
  CreateProjectAutomationInput,
  NormalizedScmAction,
  ProjectAutomationSpec,
  ProviderKind,
  ScmAutomationAction,
} from '../api/types';
import { ApiError, apiErrorCode } from '../api/client';
import styles from './AutomationEditorPage.module.css';

const ALL_ACTIONS: readonly NormalizedScmAction[] = [
  'push.updated',
  'pull_request.opened', 'pull_request.reopened', 'pull_request.synchronized',
  'pull_request.ready', 'pull_request.closed', 'pull_request.merged',
  'review.approved', 'review.changes_requested', 'review.commented',
  'review.dismissed', 'review.approval_removed',
  'comment.created',
  'issue.opened', 'issue.reopened', 'issue.updated', 'issue.closed',
  'check.completed',
  'tag.created', 'tag.deleted',
  'release.published', 'release.updated', 'release.deleted',
];

// Keep the creation surface focused on the workflows people configure most
// often. The complete normalized event contract remains available on demand,
// and an existing low-frequency selection always keeps that section visible.
const COMMON_ACTIONS = new Set<NormalizedScmAction>([
  'push.updated',
  'pull_request.opened',
  'pull_request.synchronized',
  'pull_request.ready',
  'pull_request.merged',
  'review.approved',
  'comment.created',
  'issue.opened',
  'issue.updated',
  'check.completed',
]);

const GITHUB_ACTIONS = new Set<NormalizedScmAction>(ALL_ACTIONS.filter((action) =>
  action !== 'review.approval_removed'
));
const GITLAB_ACTIONS = new Set<NormalizedScmAction>(ALL_ACTIONS.filter((action) =>
  action !== 'pull_request.ready' &&
  action !== 'review.changes_requested' &&
  action !== 'review.dismissed',
));
const GITEA_ACTIONS = new Set<NormalizedScmAction>(ALL_ACTIONS.filter((action) =>
  action !== 'pull_request.ready' &&
  action !== 'review.dismissed' &&
  action !== 'review.approval_removed',
));

function actionParts(action: NormalizedScmAction): ScmAutomationAction {
  const dot = action.indexOf('.');
  return { event_family: action.slice(0, dot), action: action.slice(dot + 1) };
}

function actionName(action: ScmAutomationAction): NormalizedScmAction {
  return `${action.event_family}.${action.action}` as NormalizedScmAction;
}

function supportedActions(provider: string | undefined): ReadonlySet<NormalizedScmAction> {
  if (provider === 'github') return GITHUB_ACTIONS;
  if (provider === 'gitlab') return GITLAB_ACTIONS;
  if (provider === 'gitea') return GITEA_ACTIONS;
  return new Set();
}

export function AutomationEditorPage() {
  const { t } = useTranslation();
  const { projectId = '', automationId = '' } = useParams();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const editing = !!automationId;
  const project = useProject(projectId);
  const existing = useProjectAutomation(projectId, automationId, editing);
  const projectModels = useProjectModels(projectId);
  const create = useCreateProjectAutomation(projectId);
  const update = useUpdateProjectAutomation(projectId);

  const services = project.data?.services ?? [];
  const initialService = searchParams.get('service') ?? '';
  const [name, setName] = useState('');
  const [serviceId, setServiceId] = useState(initialService);
  const [kind, setKind] = useState<Exclude<AutomationTriggerKind, 'kanban'>>('scm');
  const [prompt, setPrompt] = useState('');
  const [modelId, setModelId] = useState('');
  const [modelEffort, setModelEffort] = useState<'auto' | 'low' | 'medium' | 'high'>('auto');
  const [enabled, setEnabled] = useState(true);
  const [ignoreJcode, setIgnoreJcode] = useState(true);
  const [branch, setBranch] = useState('');
  const [pathPattern, setPathPattern] = useState('');
  const [conclusion, setConclusion] = useState('');
  const [actions, setActions] = useState<NormalizedScmAction[]>(['push.updated']);
  const [showMoreActions, setShowMoreActions] = useState(false);
  const [cronExpr, setCronExpr] = useState('0 9 * * 1-5');
  const [formError, setFormError] = useState('');

  const selectedService = services.find((service) => service.id === serviceId);
  const models = projectModels.data?.models ?? [];
  const selectedModel = models.find((model) => model.id === modelId);
  const effortEnabled = selectedModel?.capabilities.reasoning === true;
  const scmProvider = (
    selectedService?.provider === 'github' ||
    selectedService?.provider === 'gitlab' ||
    selectedService?.provider === 'gitea'
  ) ? selectedService.provider as ProviderKind : 'github';
  const capabilities = useProviderCapabilities(scmProvider, kind === 'scm' && !!selectedService);
  const supported = useMemo(() => {
    if (!capabilities.data) return supportedActions(selectedService?.provider);
    return new Set<NormalizedScmAction>(capabilities.data.capabilities.flatMap((group) =>
      group.actions.map((action) => `${group.family}.${action}` as NormalizedScmAction)));
  }, [capabilities.data, selectedService?.provider]);
  const pathFilterAllowed = actions.length > 0 && actions.every((action) => action === 'push.updated');
  const conclusionFilterAllowed = actions.length > 0 && actions.every((action) => action === 'check.completed');
  const branchFilterAllowed = actions.length > 0 && actions.every((action) =>
    action.startsWith('push.') || action.startsWith('pull_request.') || action.startsWith('check.'));
  const canEdit = project.data?.role !== 'viewer';
  const moreActionsOpen = showMoreActions || actions.some((action) => !COMMON_ACTIONS.has(action));

  useEffect(() => {
    if (!serviceId && initialService) setServiceId(initialService);
  }, [initialService, serviceId]);

  useEffect(() => {
    if (editing || modelId || !selectedService || !models.length) return;
    const preferred = models.find((model) => model.id === selectedService.default_model_id)
      ?? (models.length === 1 ? models[0] : undefined);
    if (preferred) setModelId(preferred.id);
  }, [editing, modelId, models, selectedService]);

  useEffect(() => {
    if (!effortEnabled) setModelEffort('auto');
  }, [effortEnabled]);

  useEffect(() => {
    const spec = existing.data;
    if (!spec) return;
    hydrate(spec);
  // Hydration must only rerun when the loaded server snapshot changes.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [existing.data]);

  function hydrate(spec: ProjectAutomationSpec) {
    const automation = spec.automation;
    setName(automation.name);
    setServiceId(automation.service_id);
    setKind(automation.trigger_kind === 'cron' ? 'cron' : 'scm');
    setPrompt(automation.prompt_template);
    setModelId(automation.model_id ?? '');
    setModelEffort(automation.model_effort ?? 'auto');
    setEnabled(automation.enabled);
    setIgnoreJcode(automation.ignore_jcode);
    setBranch(spec.scm?.branch ?? '');
    setPathPattern(spec.scm?.path_pattern ?? '');
    setConclusion(spec.scm?.conclusion ?? '');
    setActions((spec.actions ?? []).map(actionName));
    setCronExpr(spec.cron?.cron_expr ?? '0 9 * * 1-5');
  }

  function toggleAction(action: NormalizedScmAction) {
    setActions((current) => {
      const next = current.includes(action) ? current.filter((item) => item !== action) : [...current, action];
      if (!next.length || !next.every((item) => item === 'push.updated')) setPathPattern('');
      if (!next.length || !next.every((item) => item === 'check.completed')) setConclusion('');
      if (!next.length || !next.every((item) =>
        item.startsWith('push.') || item.startsWith('pull_request.') || item.startsWith('check.'))) setBranch('');
      return next;
    });
  }

  function submit(event: React.FormEvent) {
    event.preventDefault();
    setFormError('');
    if (!canEdit) {
      setFormError(t('automationEditor.validation.noPermission'));
      return;
    }
    if (!name.trim() || !serviceId || !prompt.trim() || !modelId) {
      setFormError(t('automationEditor.validation.required'));
      return;
    }
    if (kind === 'scm' && (!supported.size || !actions.some((action) => supported.has(action)))) {
      setFormError(t('automationEditor.validation.eventRequired'));
      return;
    }
    if (kind === 'cron' && !cronExpr.trim()) {
      setFormError(t('automationEditor.validation.cronRequired'));
      return;
    }
    const input: CreateProjectAutomationInput = {
      service_id: serviceId,
      name: name.trim(),
      prompt_template: prompt.trim(),
      model_id: modelId,
      model_effort: modelEffort,
      enabled,
      ignore_jcode: ignoreJcode,
      ...(kind === 'scm' ? {
        scm: {
          branch: branch.trim(),
          path_pattern: pathPattern.trim(),
          conclusion: conclusion.trim(),
          actions: actions.filter((action) => supported.has(action)).map(actionParts),
        },
      } : {}),
      ...(kind === 'cron' ? { cron: { cron_expr: cronExpr.trim() } } : {}),
    };
    const onSuccess = () => navigate(`/projects/${encodeURIComponent(projectId)}?tab=automations&service=${encodeURIComponent(serviceId)}`);
    if (editing) update.mutate({ automationId, input }, { onSuccess });
    else create.mutate(input, { onSuccess });
  }

  if (project.isLoading || projectModels.isLoading || (editing && existing.isLoading)) {
    return <LoadingBlock label={t('automationEditor.loading')} />;
  }
  if (project.isError || projectModels.isError || (editing && existing.isError)) {
    return <ErrorBlock error={project.error ?? projectModels.error ?? existing.error} title={t('automationEditor.loadError')} />;
  }

  return (
    <main className={styles.page} data-testid="automation-editor-page">
      <Link className={styles.back} to={`/projects/${encodeURIComponent(projectId)}?tab=automations${serviceId ? `&service=${encodeURIComponent(serviceId)}` : ''}`}>
        <ArrowLeft size={16} aria-hidden />
        {t('projectAutomations.title')}
      </Link>
      <header>
        <p>{t('automationEditor.eyebrow')}</p>
        <h1>{editing ? t('automationEditor.editTitle') : t('automationEditor.createTitle')}</h1>
        <span>{t('automationEditor.subtitle')}</span>
      </header>
      {!canEdit && <div className={styles.warning}><Warning size={18} />{t('automationEditor.viewerWarning')}</div>}
      <form onSubmit={submit} className={styles.form}>
        <section>
          <h2>{t('automationEditor.task')}</h2>
          <div className={styles.grid}>
            <label>{t('automationEditor.name')}<input value={name} onChange={(event) => setName(event.target.value)} required /></label>
            <div className={styles.fixedService}>
              <span>{t('automationEditor.service')}</span>
              <strong>{selectedService?.name ?? t('automationEditor.serviceUnavailable')}</strong>
              {selectedService?.repo_owner_name && <small>{selectedService.repo_owner_name}</small>}
            </div>
            <label>{t('automationEditor.model')}
              <select aria-label={t('automationEditor.model')} value={modelId} onChange={(event) => {
                setModelId(event.target.value);
                setModelEffort('auto');
              }} required>
                <option value="">{t('automationEditor.selectModel')}</option>
                {models.map((model) => <option value={model.id} key={model.id}>{model.name} · {model.model_name}</option>)}
              </select>
            </label>
            {effortEnabled && (
              <label>{t('automationEditor.effort')}
                <select aria-label={t('automationEditor.effort')} value={modelEffort} onChange={(event) => setModelEffort(event.target.value as typeof modelEffort)}>
                  <option value="auto">{t('automationEditor.effortAuto')}</option>
                  <option value="low">{t('automationEditor.effortLow')}</option>
                  <option value="medium">{t('automationEditor.effortMedium')}</option>
                  <option value="high">{t('automationEditor.effortHigh')}</option>
                </select>
                <small>{t('automationEditor.effortHint')}</small>
              </label>
            )}
          </div>
          <label>{t('automationEditor.promptTemplate')}<textarea rows={6} value={prompt} onChange={(event) => setPrompt(event.target.value)} required /></label>
          <div className={styles.inline}>
            <label><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />{t('automationEditor.enabled')}</label>
            <label><input type="checkbox" checked={ignoreJcode} onChange={(event) => setIgnoreJcode(event.target.checked)} />{t('automationEditor.ignoreJcode')}</label>
          </div>
        </section>

        <section>
          <h2>{t('automationEditor.trigger')}</h2>
          <div className={styles.triggerTabs}>
            {(['scm', 'cron'] as const).map((value) => (
              <button key={value} type="button" aria-pressed={kind === value} onClick={() => setKind(value)}>
                {value === 'scm' ? t('automationEditor.scm') : t('automationEditor.cron')}
              </button>
            ))}
          </div>

          {kind === 'scm' && (
            <div className={styles.triggerBody}>
              {!supported.size && <div className={styles.warning}><Warning size={18} />{t('automationEditor.selectGitService')}</div>}
              {capabilities.isError && <ErrorBlock error={capabilities.error} title={t('automationEditor.capabilitiesLoadError')} />}
              <div className={styles.grid}>
                <label>{t('automationEditor.branchFilter')}<input aria-label={t('automationEditor.branchFilter')} value={branch} disabled={!branchFilterAllowed} onChange={(event) => setBranch(event.target.value)} placeholder={t('automationEditor.branchPlaceholder')} /><small>{t('automationEditor.branchHint')}</small></label>
                <label>{t('automationEditor.pathPattern')}<input aria-label={t('automationEditor.pathPattern')} value={pathPattern} disabled={!pathFilterAllowed} onChange={(event) => setPathPattern(event.target.value)} placeholder="src/**" /><small>{t('automationEditor.pathHint')}</small></label>
                <label>{t('automationEditor.conclusion')}<input aria-label={t('automationEditor.conclusion')} value={conclusion} disabled={!conclusionFilterAllowed} onChange={(event) => setConclusion(event.target.value)} placeholder="success" /><small>{t('automationEditor.conclusionHint')}</small></label>
              </div>
              <fieldset className={styles.actionGrid}>
                <legend>{t('automationEditor.events')}</legend>
                {ALL_ACTIONS.filter((action) => COMMON_ACTIONS.has(action)).map((action) => {
                  const available = supported.has(action);
                  return (
                    <label key={action} data-disabled={!available}>
                      <input
                        type="checkbox"
                        checked={actions.includes(action) && available}
                        disabled={!available}
                        onChange={() => toggleAction(action)}
                      />
                      <span>{action}</span>
                      {!available && (
                        <small>
                          {t('automationEditor.eventUnavailable', {
                            provider: selectedService?.provider ?? t('automationEditor.thisProvider'),
                            version: capabilities.data?.minimum_version
                              ? ` ${capabilities.data.minimum_version}+`
                              : '',
                          })}
                        </small>
                      )}
                    </label>
                  );
                })}
              </fieldset>
              <Disclosure as="div" className={styles.moreEvents} defaultOpen={moreActionsOpen} key={moreActionsOpen ? 'more-open' : 'more-closed'}>
                {({ open }) => <>
                  <DisclosureButton onClick={() => {
                    if (!actions.some((action) => !COMMON_ACTIONS.has(action))) setShowMoreActions(!open);
                  }}>
                    {t('automationEditor.moreEvents')}
                  </DisclosureButton>
                  <DisclosurePanel>
                    <fieldset className={styles.actionGrid}>
                  <legend className={styles.srOnly}>{t('automationEditor.moreEvents')}</legend>
                  {ALL_ACTIONS.filter((action) => !COMMON_ACTIONS.has(action)).map((action) => {
                    const available = supported.has(action);
                    return (
                      <label key={action} data-disabled={!available}>
                        <input
                          type="checkbox"
                          checked={actions.includes(action) && available}
                          disabled={!available}
                          onChange={() => toggleAction(action)}
                        />
                        <span>{action}</span>
                        {!available && (
                          <small>
                            {t('automationEditor.eventUnavailable', {
                              provider: selectedService?.provider ?? t('automationEditor.thisProvider'),
                              version: capabilities.data?.minimum_version
                                ? ` ${capabilities.data.minimum_version}+`
                                : '',
                            })}
                          </small>
                        )}
                      </label>
                    );
                  })}
                    </fieldset>
                  </DisclosurePanel>
                </>}
              </Disclosure>
            </div>
          )}

          {kind === 'cron' && (
            <div className={styles.triggerBody}>
              <label>{t('automationEditor.cronExpression')}<input value={cronExpr} onChange={(event) => setCronExpr(event.target.value)} placeholder="0 9 * * 1-5" /></label>
              <small>{t('automationEditor.cronHint')}</small>
            </div>
          )}
        </section>
        {(formError || create.error || update.error) && (
          <p className={styles.error} role="alert">
            {formError || automationMutationError(create.error ?? update.error, t)}
          </p>
        )}
        <footer>
          <Button type="button" variant="ghost" onClick={() => navigate(-1)}>{t('common.cancel')}</Button>
          <Button type="submit" loading={create.isPending || update.isPending} disabled={!canEdit}>
            {editing ? t('automationEditor.save') : t('automationEditor.create')}
          </Button>
        </footer>
      </form>
    </main>
  );
}

function automationMutationError(error: unknown, t: (key: string) => string): string {
  if (!error) return '';
  if (error instanceof ApiError) {
    const code = apiErrorCode(error);
    if (code === 'automation_overlap') return t('automationEditor.apiError.overlap');
    if (code === 'webhook_reconcile_failed') return t('automationEditor.apiError.webhook');
    if (code === 'plugin_unavailable') return t('automationEditor.apiError.pluginUnavailable');
    if (code === 'model_not_granted' || code === 'model_unavailable') return t('automationEditor.apiError.modelUnavailable');
    if (code === 'model_not_selected' || code === 'model_not_configured') return t('automationEditor.apiError.modelRequired');
    if (code === 'model_effort_unsupported') return t('automationEditor.apiError.effortUnsupported');
  }
  return t('automationEditor.apiError.generic');
}
