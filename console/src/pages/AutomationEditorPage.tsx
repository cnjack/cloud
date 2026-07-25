import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { ArrowLeft, Warning } from '@phosphor-icons/react';
import { Button } from '../components/Button';
import { ErrorBlock, LoadingBlock } from '../components/States';
import {
  useCreateProjectAutomation,
  usePluginBoards,
  usePluginWorkspaces,
  useProject,
  useProjectAutomation,
  useProjectPlugins,
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

const GITHUB_ACTIONS = new Set(ALL_ACTIONS);
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
  const { projectId = '', automationId = '' } = useParams();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const editing = !!automationId;
  const project = useProject(projectId);
  const plugins = useProjectPlugins(projectId);
  const existing = useProjectAutomation(projectId, automationId, editing);
  const create = useCreateProjectAutomation(projectId);
  const update = useUpdateProjectAutomation(projectId);

  const services = project.data?.services ?? [];
  const initialService = searchParams.get('service') ?? services[0]?.id ?? '';
  const [name, setName] = useState('');
  const [serviceId, setServiceId] = useState(initialService);
  const [kind, setKind] = useState<AutomationTriggerKind>('scm');
  const [prompt, setPrompt] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [ignoreJcode, setIgnoreJcode] = useState(true);
  const [branch, setBranch] = useState('');
  const [pathPattern, setPathPattern] = useState('');
  const [conclusion, setConclusion] = useState('');
  const [actions, setActions] = useState<NormalizedScmAction[]>(['push.updated']);
  const [cronExpr, setCronExpr] = useState('0 9 * * 1-5');
  const [installationId, setInstallationId] = useState('');
  const [workspaceId, setWorkspaceId] = useState('');
  const [boardRef, setBoardRef] = useState('');
  const [triggerColumn, setTriggerColumn] = useState('');
  const [doneColumn, setDoneColumn] = useState('');
  const [formError, setFormError] = useState('');

  const jtypeInstallations = (plugins.data ?? []).filter((plugin) =>
    plugin.provider === 'jtype' && plugin.status === 'enabled' && plugin.id);
  const selectedJTypeInstallation = jtypeInstallations.find((plugin) => plugin.id === installationId);
  const workspaces = usePluginWorkspaces(projectId, installationId, kind === 'kanban');
  const boards = usePluginBoards(projectId, installationId, workspaceId, kind === 'kanban');
  const selectedService = services.find((service) => service.id === serviceId);
  const selectedBoard = boards.data?.find((board) => board.ref === boardRef);
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

  useEffect(() => {
    if (!serviceId && initialService) setServiceId(initialService);
  }, [initialService, serviceId]);

  useEffect(() => {
    const spec = existing.data;
    if (!spec) return;
    hydrate(spec);
  // Hydration must only rerun when the loaded server snapshot changes.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [existing.data]);

  useEffect(() => {
    if (kind !== 'kanban' || installationId || !jtypeInstallations[0]?.id) return;
    setInstallationId(jtypeInstallations[0].id);
  }, [installationId, jtypeInstallations, kind]);

  useEffect(() => {
    if (kind !== 'kanban') return;
    const boundWorkspace = selectedJTypeInstallation?.workspace_id ?? '';
    if (workspaceId !== boundWorkspace) {
      setWorkspaceId(boundWorkspace);
      setBoardRef('');
      setTriggerColumn('');
      setDoneColumn('');
    }
  }, [kind, selectedJTypeInstallation?.workspace_id, workspaceId]);

  function hydrate(spec: ProjectAutomationSpec) {
    const automation = spec.automation;
    setName(automation.name);
    setServiceId(automation.service_id);
    setKind(automation.trigger_kind);
    setPrompt(automation.prompt_template);
    setEnabled(automation.enabled);
    setIgnoreJcode(automation.ignore_jcode);
    setBranch(spec.scm?.branch ?? '');
    setPathPattern(spec.scm?.path_pattern ?? '');
    setConclusion(spec.scm?.conclusion ?? '');
    setActions((spec.actions ?? []).map(actionName));
    setCronExpr(spec.cron?.cron_expr ?? '0 9 * * 1-5');
    setInstallationId(spec.kanban?.installation_id ?? '');
    setBoardRef(spec.kanban?.board_ref ?? '');
    setTriggerColumn(spec.kanban?.trigger_column ?? '');
    setDoneColumn(spec.kanban?.done_column ?? '');
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
      setFormError('You do not have permission to edit Automations.');
      return;
    }
    if (!name.trim() || !serviceId || !prompt.trim()) {
      setFormError('Name, Service, and task Prompt are required.');
      return;
    }
    if (kind === 'scm' && (!supported.size || !actions.some((action) => supported.has(action)))) {
      setFormError('Select at least one event supported by this Service provider.');
      return;
    }
    if (kind === 'kanban' && (!installationId || !boardRef || !triggerColumn)) {
      setFormError('Select a JType workspace, board, and trigger column.');
      return;
    }
    if (kind === 'cron' && !cronExpr.trim()) {
      setFormError('Cron expression is required.');
      return;
    }
    const input: CreateProjectAutomationInput = {
      service_id: serviceId,
      name: name.trim(),
      prompt_template: prompt.trim(),
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
      ...(kind === 'kanban' ? {
        kanban: {
          installation_id: installationId,
          board_ref: boardRef,
          trigger_column: triggerColumn,
          done_column: doneColumn,
        },
      } : {}),
      ...(kind === 'cron' ? { cron: { cron_expr: cronExpr.trim() } } : {}),
    };
    const onSuccess = () => navigate(`/projects/${encodeURIComponent(projectId)}?tab=automations&service=${encodeURIComponent(serviceId)}`);
    if (editing) update.mutate({ automationId, input }, { onSuccess });
    else create.mutate(input, { onSuccess });
  }

  if (project.isLoading || plugins.isLoading || (editing && existing.isLoading)) {
    return <LoadingBlock label="Loading Automation…" />;
  }
  if (project.isError || plugins.isError || (editing && existing.isError)) {
    return <ErrorBlock error={project.error ?? plugins.error ?? existing.error} title="Could not load Automation" />;
  }

  return (
    <main className={styles.page} data-testid="automation-editor-page">
      <Link className={styles.back} to={`/projects/${encodeURIComponent(projectId)}?tab=automations`}>
        <ArrowLeft size={16} aria-hidden />
        Automations
      </Link>
      <header>
        <p>Project Automation</p>
        <h1>{editing ? 'Edit Automation' : 'Create Automation'}</h1>
        <span>Bind one strongly typed trigger to one Service.</span>
      </header>
      {!canEdit && <div className={styles.warning}><Warning size={18} />Viewers can inspect but cannot save Automations.</div>}
      <form onSubmit={submit} className={styles.form}>
        <section>
          <h2>Task</h2>
          <div className={styles.grid}>
            <label>Name<input value={name} onChange={(event) => setName(event.target.value)} required /></label>
            <label>Service
              <select value={serviceId} onChange={(event) => {
                setServiceId(event.target.value);
                setActions([]);
              }} required>
                <option value="">Select Service…</option>
                {services.map((service) => <option value={service.id} key={service.id}>{service.name}</option>)}
              </select>
            </label>
          </div>
          <label>Prompt template<textarea rows={6} value={prompt} onChange={(event) => setPrompt(event.target.value)} required /></label>
          <div className={styles.inline}>
            <label><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />Enabled</label>
            <label><input type="checkbox" checked={ignoreJcode} onChange={(event) => setIgnoreJcode(event.target.checked)} />Ignore events created by jcode</label>
          </div>
        </section>

        <section>
          <h2>Trigger</h2>
          <div className={styles.triggerTabs}>
            {(['scm', 'kanban', 'cron'] as const).map((value) => (
              <button key={value} type="button" aria-pressed={kind === value} onClick={() => setKind(value)}>
                {value === 'scm' ? 'SCM' : value === 'kanban' ? 'JType Kanban' : 'Cron'}
              </button>
            ))}
          </div>

          {kind === 'scm' && (
            <div className={styles.triggerBody}>
              {!supported.size && <div className={styles.warning}><Warning size={18} />Select a GitHub, GitLab, or Gitea Service.</div>}
              {capabilities.isError && <ErrorBlock error={capabilities.error} title="Provider capabilities could not be loaded" />}
              <div className={styles.grid}>
                <label>Branch filter<input aria-label="Branch filter" value={branch} disabled={!branchFilterAllowed} onChange={(event) => setBranch(event.target.value)} placeholder="main or feature/*" /><small>Available for branch-carrying Push, PR, and Check events.</small></label>
                <label>Path pattern<input aria-label="Path pattern" value={pathPattern} disabled={!pathFilterAllowed} onChange={(event) => setPathPattern(event.target.value)} placeholder="src/**" /><small>Available only when every selected event is push.updated.</small></label>
                <label>CI conclusion<input aria-label="CI conclusion" value={conclusion} disabled={!conclusionFilterAllowed} onChange={(event) => setConclusion(event.target.value)} placeholder="success" /><small>Available only when every selected event is check.completed.</small></label>
              </div>
              <fieldset className={styles.actionGrid}>
                <legend>Events</legend>
                {ALL_ACTIONS.map((action) => {
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
                          Not supported by {selectedService?.provider ?? 'this provider'}
                          {capabilities.data?.minimum_version ? ` ${capabilities.data.minimum_version}+` : ''}.
                        </small>
                      )}
                    </label>
                  );
                })}
              </fieldset>
            </div>
          )}

          {kind === 'kanban' && (
            <div className={styles.triggerBody}>
              {!jtypeInstallations.length && <div className={styles.warning}><Warning size={18} />Enable the JType Kanban Plugin before creating this trigger.</div>}
              <div className={styles.grid}>
                <label>JType connection
                  <select value={installationId} onChange={(event) => {
                    setInstallationId(event.target.value);
                    setBoardRef('');
                  }}>
                    <option value="">Select connection…</option>
                    {jtypeInstallations.map((plugin) => <option key={plugin.id} value={plugin.id}>{plugin.external_account ?? plugin.workspace_id ?? 'JType'}</option>)}
                  </select>
                </label>
                <label>Workspace
                  <select value={workspaceId} disabled>
                    <option value="">Select workspace…</option>
                    {(workspaces.data ?? []).map((workspace) => <option key={workspace.id} value={workspace.id}>{workspace.name}</option>)}
                  </select>
                  <small>The workspace is fixed by this Project Plugin. Change it on the Plugin detail page.</small>
                </label>
                <label>Board
                  <select value={boardRef} onChange={(event) => {
                    setBoardRef(event.target.value);
                    setTriggerColumn('');
                    setDoneColumn('');
                  }} disabled={!workspaceId || boards.isLoading}>
                    <option value="">Select board…</option>
                    {(boards.data ?? []).map((board) => <option key={board.id} value={board.ref}>{board.title}</option>)}
                  </select>
                </label>
                <label>Trigger column
                  <select value={triggerColumn} onChange={(event) => setTriggerColumn(event.target.value)} disabled={!selectedBoard}>
                    <option value="">Select column…</option>
                    {(selectedBoard?.columns ?? []).map((column) => <option key={column.key} value={column.key}>{column.name}</option>)}
                  </select>
                </label>
                <label>Completion column (optional)
                  <select value={doneColumn} onChange={(event) => setDoneColumn(event.target.value)} disabled={!selectedBoard}>
                    <option value="">None</option>
                    {(selectedBoard?.columns ?? []).map((column) => <option key={column.key} value={column.key}>{column.name}</option>)}
                  </select>
                </label>
              </div>
              {(workspaces.isError || boards.isError) && <ErrorBlock error={workspaces.error ?? boards.error} title="JType resources could not be listed" />}
            </div>
          )}

          {kind === 'cron' && (
            <div className={styles.triggerBody}>
              <label>Cron expression<input value={cronExpr} onChange={(event) => setCronExpr(event.target.value)} placeholder="0 9 * * 1-5" /></label>
              <small>Five-field cron expression. The minimum interval is enforced by the server.</small>
            </div>
          )}
        </section>
        {(formError || create.error || update.error) && (
          <p className={styles.error} role="alert">
            {formError || (create.error as Error | null)?.message || (update.error as Error | null)?.message}
          </p>
        )}
        <footer>
          <Button type="button" variant="ghost" onClick={() => navigate(-1)}>Cancel</Button>
          <Button type="submit" loading={create.isPending || update.isPending} disabled={!canEdit}>
            {editing ? 'Save Automation' : 'Create Automation'}
          </Button>
        </footer>
      </form>
    </main>
  );
}
