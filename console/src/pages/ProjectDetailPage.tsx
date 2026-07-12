/*
 * ProjectDetailPage — the composer + run list (J1-S4, J2, J3; multitenant §5).
 *
 * Dumb UX: a project with a single repository never shows the word "service" —
 * the composer just dispatches a run. Once a project has more than one repo
 * (added via the low-key "+ Add repository" affordance below the runs), the
 * composer grows a repository selector and runs are dispatched against the chosen
 * service.
 *
 * Role gating (blueprint §2, backend-enforced 403s are the source of truth; this
 * is UX): a viewer sees no composer and no Settings; only an owner (or
 * cluster-admin) can change settings / add a repository / manage members.
 */
import { useDeferredValue, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  useProject,
  useRuns,
  useCreateServiceRun,
  useCreateService,
  useProviderRepos,
  useProjectModels,
  useUpdateService,
  useIntegrations,
  useIntegrationRepos,
  useProjectBoardLinks,
} from '../api/queries';
import { useOptionalAuth } from '../auth/AuthProvider';
import { useModelGate } from '../components/ModelGate';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { Select } from '../components/Select';
import { GitModeToggle } from '../components/GitModeToggle';
import { StatusBadge } from '../components/StatusBadge';
import { EmptyState } from '../components/EmptyState';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { useToast } from '../components/Toast';
import { GitModeBadge } from '../components/GitModeBadge';
import { ProjectSettingsModal } from './ProjectSettingsModal';
import { KanbanBoardModal } from './KanbanBoardModal';
import { SchedulesPanel } from './SchedulesPanel';
import { ApiError } from '../api/client';
import { providerForRepoUrl } from '../lib/repo';
import { shortId, summarize, timeAgo } from '../lib/format';
import type { GitMode, GitProvider, ProviderRepo, Run, Service } from '../api/types';
import styles from './ProjectDetailPage.module.css';

/** A human label for a service in the repository selector. */
function serviceLabel(svc: Service): string {
  const repo =
    svc.repo_kind === 'provider' ? svc.repo_owner_name : svc.raw_repo_url;
  return svc.name === 'default' ? repo || svc.name : `${svc.name} · ${repo ?? ''}`;
}

type WorkspaceTab = 'tasks' | 'automations';
type RunFilter = 'all' | 'sessions' | 'reviews';
const WORKSPACE_TABS: readonly WorkspaceTab[] = ['tasks', 'automations'];

function serviceMark(service: Service): string {
  if (service.repo_kind === 'raw') return 'PATH';
  switch (service.provider?.toLowerCase()) {
    case 'gitea':
      return 'GT';
    case 'github':
      return 'GH';
    case 'gitlab':
      return 'GL';
    default:
      return 'GIT';
  }
}

function serviceSource(service: Service): string {
  return service.repo_kind === 'provider'
    ? service.repo_owner_name || service.name
    : service.raw_repo_url || service.name;
}

function serviceProviderLabel(service: Service): string {
  if (service.repo_kind === 'raw') return 'Path / remote URL';
  const provider = service.provider?.toLowerCase();
  if (provider === 'gitea') return 'Gitea';
  if (provider === 'github') return 'GitHub';
  if (provider === 'gitlab') return 'GitLab';
  return 'Git repository';
}

function runKindLabel(run: Run): string {
  if (run.kind === 'review') return 'Review';
  if (run.origin === 'schedule') return 'Schedule';
  if (run.origin === 'kanban') return 'Kanban';
  if (run.origin === 'webhook') return 'Webhook';
  return run.session ? 'Session' : 'Task';
}

export function ProjectDetailPage() {
  const { projectId = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();

  const project = useProject(projectId);
  const runs = useRuns(projectId);
  const createServiceRun = useCreateServiceRun(projectId);
  const createService = useCreateService(projectId);

  const [prompt, setPrompt] = useState('');
  const [promptError, setPromptError] = useState<string>();
  const [settingsOpen, setSettingsOpen] = useState(false);
  // D31: the embedded kanban board modal.
  const [kanbanOpen, setKanbanOpen] = useState(false);
  const [workspaceTab, setWorkspaceTab] = useState<WorkspaceTab>('tasks');
  const [runFilter, setRunFilter] = useState<RunFilter>('all');
  const workspaceScrollRef = useRef<HTMLDivElement>(null);
  const [selectedService, setSelectedService] = useState<string>('');
  // D21: the composer's per-run model pick ("" => resolve via service default /
  // the project's sole grant).
  const [selectedModel, setSelectedModel] = useState<string>('');
  // F8b: ask before agent actions (permission_mode=approval). This interactive
  // composer is session-only (every run is multi-turn), so the choice is always
  // offered; default OFF (full access).
  const [askApproval, setAskApproval] = useState(false);

  // Add-repository inline form.
  const [addOpen, setAddOpen] = useState(false);
  const [repoName, setRepoName] = useState('');
  const [repoUrl, setRepoUrl] = useState('');
  const [repoMode, setRepoMode] = useState<GitMode>('readonly');
  const [repoErr, setRepoErr] = useState<{ name?: string; url?: string }>({});

  // Drone-style repo picker (the primary add path; manual URL is the fallback).
  const auth = useOptionalAuth();
  const pickerProviders = useMemo(() => {
    const ids = (auth?.providers ?? []).map((p) => p.id);
    // gitea always gets a tab: the orchestrator can list via its PAT fallback
    // even when OAuth login isn't configured (console-token deployments).
    return ids.includes('gitea') ? ids : ['gitea', ...ids];
  }, [auth]);
  const [pickerProvider, setPickerProvider] = useState('gitea');
  const [repoQuery, setRepoQuery] = useState('');
  const deferredQuery = useDeferredValue(repoQuery);
  // Source of the picker: '' = Direct (owner's own credential); an id = that
  // integration's bot. A member (no Direct option) defaults to the first integration.
  const [pickerIntegrationId, setPickerIntegrationId] = useState('');

  const p = project.data;
  const services = useMemo(() => p?.services ?? [], [p]);
  const multiService = services.length > 1;
  const role = p?.role ?? 'owner';
  const canRun = role !== 'viewer';
  const canManage = role === 'owner';

  // D31: the reduced, member+ board-link list gates the "Kanban" header button.
  // Loaded once the project (and role) is known, for member+ only — a viewer's
  // 403 yields an empty list → no button (owner-only useProjectKanbanLinks would
  // both 403 members and leak credential posture, so it can't gate this).
  const boardLinks = useProjectBoardLinks(projectId, !!p && canRun);
  const hasBoardLinks = (boardLinks.data?.length ?? 0) > 0;
  // A member+ board-link query failing must not look identical to "this project
  // has no boards". The retry action exposes the real unavailable state without
  // leaking owner-only token/configuration information.
  const boardLinksUnavailable = canRun && boardLinks.isError;

  // Integrations (D19 / F5): a member can add a repo off an existing integration
  // (the integration's bot lists the repos). Loaded EAGERLY for any member+ once
  // the project (and thus the role) is known — NOT gated on the add-repo card
  // being open: the member's only entry button is itself gated on this data (it
  // appears once an integration exists), so gating the query on the open card
  // would deadlock the member path (query never enabled → button never renders).
  const integrationsQuery = useIntegrations(projectId, !!p && canRun);
  const availableIntegrations = useMemo(
    () => integrationsQuery.data ?? [],
    [integrationsQuery.data],
  );

  // Repo-add source (D19 / F5). An owner can add directly (their credential) OR via
  // an integration; a member can only add via an integration. effectiveIntegrationId
  // resolves the picker source: for a member, default to the first integration.
  const effectiveIntegrationId =
    pickerIntegrationId || (!canManage ? availableIntegrations[0]?.id ?? '' : '');
  const integrationMode = effectiveIntegrationId !== '';
  // The add-repo entry shows for an owner always, and for a member once the
  // project has at least one integration to add through.
  const canAddRepo = canManage || (canRun && availableIntegrations.length > 0);
  // Fail-visible empty state (D19): a member with NO integration cannot add a
  // repository — say so instead of silently hiding the affordance. Only once the
  // list has actually loaded (no flash while fetching).
  const memberNeedsIntegration =
    canRun && !canManage && integrationsQuery.isSuccess && availableIntegrations.length === 0;
  // Direct provider picker (owner-only path); integration picker uses the bot token.
  const providerRepos = useProviderRepos(pickerProvider, deferredQuery, addOpen && !integrationMode && canManage);
  const integrationRepos = useIntegrationRepos(
    projectId,
    effectiveIntegrationId,
    deferredQuery,
    addOpen && integrationMode,
  );
  const repoList = integrationMode ? integrationRepos : providerRepos;

  // Fail-visible (Feature A): a run cannot start without an LLM configured, so
  // the composer disables itself and explains why rather than letting the user
  // dispatch a run that would 409. The query only runs where the composer is
  // actually rendered (member+ with at least one repo) — a viewer / empty
  // project never polls (same enabled convention as useProviderRepos above).
  const modelGate = useModelGate(projectId, canRun && services.length > 0);

  // D21: the models this project is granted (composer pick options + the service
  // default editor). Only fetched where the composer is actually rendered.
  const projectModels = useProjectModels(projectId, canRun && services.length > 0);
  const grantedModels = projectModels.data?.models ?? [];
  // A Project route change reuses this component. Just like services, never
  // carry a model choice into a Project that has not granted that model.
  const effectiveSelectedModel = grantedModels.some((model) => model.id === selectedModel)
    ? selectedModel
    : '';
  const updateService = useUpdateService(projectId);

  // Default the composer's service selection to the 'default' (or first) service.
  // React Router reuses this page instance when the Project URL changes. A
  // service selected in the previous Project is never a valid execution target
  // here — ignore stale state until it belongs to the current service list.
  const selectedServiceIsCurrent = services.some((service) => service.id === selectedService);
  const activeServiceId =
    (selectedServiceIsCurrent ? selectedService : '') ||
    services.find((s) => s.name === 'default')?.id ||
    services[0]?.id ||
    '';
  const activeService = services.find((s) => s.id === activeServiceId);
  const scopedRuns = useMemo(() => {
    const allRuns = runs.data ?? [];
    // Older API rows can lack service_id. Keep them visible rather than silently
    // discarding project history when a project gains a second service.
    if (!activeServiceId) return allRuns;
    return allRuns.filter((run) => !run.service_id || run.service_id === activeServiceId);
  }, [activeServiceId, runs.data]);
  const visibleRuns = useMemo(() => {
    if (runFilter === 'sessions') return scopedRuns.filter((run) => run.session);
    if (runFilter === 'reviews') return scopedRuns.filter((run) => run.kind === 'review');
    return scopedRuns;
  }, [runFilter, scopedRuns]);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!modelGate.configured) return; // gate: no LLM configured (also 409'd by the API)
    if (!prompt.trim()) {
      setPromptError('Describe the task for the agent.');
      return;
    }
    setPromptError(undefined);
    // Runs are always service-scoped; the selector only shows for multi-repo
    // projects, otherwise the sole service is used implicitly.
    if (!activeServiceId) return;
    createServiceRun.mutate(
      {
        serviceId: activeServiceId,
        input: {
          prompt: prompt.trim(),
          ...(effectiveSelectedModel ? { model_id: effectiveSelectedModel } : {}),
          // The interactive composer is session-only: every run started here is a
          // multi-turn session (headless single-shot lives on the cron/webhook
          // automation paths, not this UI).
          session: true,
          // F8b: approval mode rides WITH the session (full access = omitted).
          ...(askApproval ? { permission_mode: 'approval' as const } : {}),
        },
      },
      {
        onSuccess: (run: { id: string }) => {
          setPrompt('');
          toast.push({ kind: 'success', message: 'Session started.' });
          navigate(`/runs/${run.id}`);
        },
        onError: (err: unknown) => {
          const msg = err instanceof ApiError ? err.message : 'Failed to start run.';
          toast.push({ kind: 'error', message: msg });
        },
      },
    );
  };

  // One click on a picked repo attaches it with sensible defaults: the repo's
  // own name + default branch, draft_pr (the closed loop provider repos exist
  // for), and the numeric provider_repo_id as its rename-proof identity.
  const pickRepo = (r: ProviderRepo) => {
    const name = r.full_name.split('/').pop() || r.full_name;
    // Integration mode (D19 / F5): bind the service to the integration (its
    // provider is authoritative; a member may do this). Direct mode is the legacy
    // owner picker keyed by the selected provider tab.
    const input = integrationMode
      ? {
          name,
          owner_name: r.full_name,
          integration_id: effectiveIntegrationId,
          default_branch: r.default_branch || 'main',
          git_mode: 'draft_pr' as GitMode,
          provider_repo_id: r.id,
        }
      : {
          name,
          provider: pickerProvider as GitProvider,
          owner_name: r.full_name,
          default_branch: r.default_branch || 'main',
          git_mode: 'draft_pr' as GitMode,
          provider_repo_id: r.id,
        };
    createService.mutate(
      input,
      {
        onSuccess: () => {
          toast.push({ kind: 'success', message: `Repository “${r.full_name}” added.` });
          setAddOpen(false);
          setRepoQuery('');
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Failed to add repository.',
          }),
      },
    );
  };

  const submitRepo = (e: React.FormEvent) => {
    e.preventDefault();
    const errs: typeof repoErr = {};
    if (!repoName.trim()) errs.name = 'Name is required.';
    if (!repoUrl.trim()) errs.url = 'Repository URL is required.';
    else if (repoMode === 'draft_pr' && providerForRepoUrl(repoUrl) === null)
      errs.url = 'Draft PR needs a provider repository URL.';
    setRepoErr(errs);
    if (Object.keys(errs).length) return;
    createService.mutate(
      { name: repoName.trim(), repo_url: repoUrl.trim(), git_mode: repoMode },
      {
        onSuccess: () => {
          toast.push({ kind: 'success', message: `Repository “${repoName.trim()}” added.` });
          setAddOpen(false);
          setRepoName('');
          setRepoUrl('');
          setRepoMode('readonly');
          setRepoErr({});
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Failed to add repository.',
          }),
      },
    );
  };

  if (project.isLoading) return <LoadingBlock label="Loading project…" />;
  if (project.isError)
    return (
      <ErrorBlock
        error={project.error}
        onRetry={() => project.refetch()}
        title="Couldn't load project"
      />
    );

  const runBusy = createServiceRun.isPending;
  const selectWorkspaceTab = (next: WorkspaceTab) => {
    if (next !== workspaceTab && workspaceScrollRef.current) {
      // The surface has one intentional internal scrollbar. Reset it when the
      // section changes so Automations never opens halfway down a prior task list.
      workspaceScrollRef.current.scrollTop = 0;
    }
    setWorkspaceTab(next);
    window.requestAnimationFrame(() => {
      document.getElementById(`workspace-tab-${next}`)?.focus();
    });
  };
  const onWorkspaceTabsKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const index = WORKSPACE_TABS.indexOf(workspaceTab);
    const nextIndex =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? WORKSPACE_TABS.length - 1
          : (index + (event.key === 'ArrowRight' ? 1 : -1) + WORKSPACE_TABS.length) %
            WORKSPACE_TABS.length;
    const next = WORKSPACE_TABS[nextIndex];
    if (next) selectWorkspaceTab(next);
  };

  return (
    <div className={styles.page}>
      <aside className={styles.serviceRail} aria-label="Project services">
        <div className={styles.railProject}>
          <span className={styles.eyebrow}>Project</span>
          <strong>{p!.name}</strong>
          <small>
            {services.length === 1 ? '1 repository' : `${services.length} repositories`}
          </small>
        </div>
        <div className={styles.railSectionHead}>
          <span>Services</span>
          <span>{services.length}</span>
        </div>
        {services.length > 0 ? (
          <div className={styles.serviceList} role="group" aria-label="Services">
            {services.map((service) => {
              const selected = service.id === activeServiceId;
              return (
                <button
                  key={service.id}
                  type="button"
                  className={styles.serviceRailItem}
                  data-active={selected || undefined}
                  aria-pressed={selected}
                  data-testid={`service-rail-${service.id}`}
                  onClick={() => {
                    setSelectedService(service.id);
                    // Changing a service should not pull someone out of
                    // Automations: schedules and provider-event posture are
                    // precisely what they are comparing across services.
                    if (workspaceTab === 'tasks') setRunFilter('all');
                  }}
                >
                  <span className={styles.serviceMark} aria-hidden>
                    {serviceMark(service)}
                  </span>
                  <span className={styles.serviceRailCopy}>
                    <strong>{service.name}</strong>
                    <small>{serviceProviderLabel(service)} · {service.default_branch}</small>
                  </span>
                </button>
              );
            })}
          </div>
        ) : (
          <p className={styles.railEmpty}>No service connected yet.</p>
        )}
      </aside>

      <section className={styles.workspaceSurface} aria-label={`${p!.name} workspace`}>
      <nav className={styles.crumbs} aria-label="Breadcrumb">
        <Link to="/" className={styles.crumbLink}>
          Projects
        </Link>
        <span className={styles.crumbSep}>/</span>
        <span className={styles.crumbCurrent}>{p!.name}</span>
      </nav>

      <header className={styles.header}>
        <div className={styles.serviceHeading}>
          {activeService && (
            <span className={styles.serviceHeaderMark} aria-hidden>
              {serviceMark(activeService)}
            </span>
          )}
          <div>
          <span className={styles.eyebrow}>
            {activeService ? serviceProviderLabel(activeService) : 'Project workspace'}
          </span>
          <h1 className={styles.title}>{activeService?.name ?? p!.name}</h1>
          <div className={styles.repoRow}>
            {activeService ? (
              <>
                <code className={styles.repo}>{serviceSource(activeService)}</code>
                <span className={styles.branch}>{activeService.default_branch}</span>
                <GitModeBadge
                  gitMode={activeService.git_mode}
                  providerRepo={activeService.repo_owner_name}
                />
              </>
            ) : (
              <span className={styles.branch}>No repositories yet</span>
            )}
            {multiService && (
              <span className={styles.repoCount} data-testid="repo-count">
                {services.length} repositories
              </span>
            )}
          </div>
          </div>
        </div>
        <div className={styles.headerActions}>
          {hasBoardLinks && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setKanbanOpen(true)}
              data-testid="project-kanban-btn"
            >
              Kanban
            </Button>
          )}
          {boardLinksUnavailable && !hasBoardLinks && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void boardLinks.refetch()}
              disabled={boardLinks.isFetching}
            title="Kanban links could not be loaded. Retry."
              data-testid="project-kanban-retry"
            >
              {boardLinks.isFetching ? 'Loading Kanban…' : 'Kanban unavailable · Retry'}
            </Button>
          )}
          {canManage && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setSettingsOpen(true)}
              data-testid="project-settings-btn"
            >
              Settings
            </Button>
          )}
        </div>
      </header>

      <div
        className={styles.workspaceTabs}
        role="tablist"
        aria-label="Project workspace sections"
        onKeyDown={onWorkspaceTabsKeyDown}
      >
        <button
          id="workspace-tab-tasks"
          type="button"
          role="tab"
          aria-selected={workspaceTab === 'tasks'}
          aria-controls="workspace-panel-tasks"
          tabIndex={workspaceTab === 'tasks' ? 0 : -1}
          className={styles.workspaceTab}
          data-active={workspaceTab === 'tasks' || undefined}
          onClick={() => selectWorkspaceTab('tasks')}
        >
          Tasks
        </button>
        <button
          id="workspace-tab-automations"
          type="button"
          role="tab"
          aria-selected={workspaceTab === 'automations'}
          aria-controls="workspace-panel-automations"
          tabIndex={workspaceTab === 'automations' ? 0 : -1}
          className={styles.workspaceTab}
          data-active={workspaceTab === 'automations' || undefined}
          onClick={() => selectWorkspaceTab('automations')}
        >
          Automations
        </button>
      </div>

      <div
        ref={workspaceScrollRef}
        className={styles.workspaceScroll}
        data-testid="project-workspace-scroll"
      >
        <div
          id="workspace-panel-tasks"
          role="tabpanel"
          aria-labelledby="workspace-tab-tasks"
          hidden={workspaceTab !== 'tasks'}
          className={styles.workspacePanel}
        >
        {workspaceTab === 'tasks' && (
          <>

      {canRun && services.length === 0 && (
        <EmptyState
          data-testid="no-repo-empty"
          title="Add a repository to get started"
          description={
            canManage
              ? 'Attach a git repository below — runs are dispatched against a repository.'
              : 'A project owner needs to attach a repository before runs can be dispatched.'
          }
        />
      )}

      {canRun && services.length > 0 && (
        <Card className={styles.composer}>
          <div className={styles.composerIntro}>
            <div>
              <span className={styles.eyebrow}>New task</span>
              <h2>Start a session in {activeService?.name}</h2>
            </div>
            <span className={styles.composerHint}>Runs in an isolated workspace.</span>
          </div>
          {modelGate.notice && (
            <div className={styles.composerNotice}>{modelGate.notice}</div>
          )}
          <form onSubmit={submit} noValidate>
            {multiService && (
              <div className={styles.serviceRow}>
                <label className={styles.serviceLabel} htmlFor="composer-service">
                  Repository
                </label>
                <Select
                  id="composer-service"
                  className={styles.serviceSelect}
                  value={activeServiceId}
                  onChange={setSelectedService}
                  options={services.map((s) => ({ value: s.id, label: serviceLabel(s) }))}
                  data-testid="composer-service-select"
                />
              </div>
            )}

            {/* D21: the owner-only "repository default model" editor. The per-run
                model pick lives on the composer bar below; this sets the fallback
                the resolution chain uses when a run omits its own pick. Only
                meaningful when the project has granted models. */}
            {grantedModels.length > 0 && canManage && activeService && (
              <div className={styles.serviceRow}>
                <label className={styles.serviceLabel} htmlFor="composer-default-model">
                  Default model
                </label>
                <Select
                  id="composer-default-model"
                  className={styles.serviceSelect}
                  aria-label="Default model for this repository"
                  value={activeService.default_model_id ?? ''}
                  data-testid="service-default-model-select"
                  onChange={(value) =>
                    updateService.mutate(
                      { serviceId: activeService.id, input: { default_model_id: value } },
                      {
                        onSuccess: () =>
                          toast.push({ kind: 'success', message: 'Default model updated.' }),
                        onError: (err) =>
                          toast.push({
                            kind: 'error',
                            message: err instanceof ApiError ? err.message : 'Could not set the default model.',
                          }),
                      },
                    )
                  }
                  options={[
                    { value: '', label: 'No default' },
                    ...grantedModels.map((m) => ({ value: m.id, label: `Default: ${m.name}` })),
                  ]}
                />
              </div>
            )}

            {/* Chat-style composer: a message box up top, a bar of pills and the
                Send action below. Every run started here is a session (D22). */}
            <div className={styles.composerBox}>
              <textarea
                className={styles.composerInput}
                aria-label="Message"
                aria-invalid={!!promptError}
                required
                placeholder="Send a message to start a session…"
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                data-testid="run-input"
                rows={3}
                disabled={!modelGate.configured}
              />
              {/* Validation error sits directly under the message, inside the box. */}
              {promptError && <p className={styles.composerError}>{promptError}</p>}
              <div className={styles.composerBar}>
                {/* D21: per-run model pick. Only shown when the project has granted
                    models (empty => env fallback, nothing to pick). "Service
                    default" lets the resolution chain decide. */}
                {grantedModels.length > 0 && (
                  <Select
                    className={styles.composerPill}
                    aria-label="Model"
                    value={effectiveSelectedModel}
                    onChange={setSelectedModel}
                    disabled={!modelGate.configured}
                    data-testid="composer-model-select"
                    options={[
                      { value: '', label: 'Service default' },
                      ...grantedModels.map((m) => ({ value: m.id, label: m.name })),
                    ]}
                  />
                )}
                {/* F8b: permission mode for the session. Full access auto-approves;
                    approval pauses the agent before actions that need permission. */}
                <Select
                  className={styles.composerPill}
                  aria-label="Permission mode"
                  title="Full access auto-approves the agent; Ask before actions pauses it for your approval in the timeline."
                  value={askApproval ? 'approval' : ''}
                  onChange={(value) => setAskApproval(value === 'approval')}
                  disabled={!modelGate.configured}
                  data-testid="composer-approval-toggle"
                  options={[
                    { value: '', label: 'Full access' },
                    { value: 'approval', label: 'Ask before actions' },
                  ]}
                />
                <Button
                  type="submit"
                  variant="primary"
                  size="sm"
                  className={styles.composerSend}
                  loading={runBusy}
                  disabled={!modelGate.configured}
                  data-testid="run-submit"
                >
                  Send
                </Button>
              </div>
            </div>
          </form>
        </Card>
      )}

      <section className={styles.runsSection}>
        <div className={styles.runsHead}>
          <div>
            <span className={styles.eyebrow}>Activity</span>
            <h2 className={styles.sectionTitle}>Recent tasks</h2>
          </div>
          <div className={styles.runFilters} aria-label="Filter recent tasks">
            {([
              ['all', 'All'],
              ['sessions', 'Sessions'],
              ['reviews', 'Reviews'],
            ] as const).map(([value, label]) => (
              <button
                key={value}
                type="button"
                className={styles.runFilter}
                aria-pressed={runFilter === value}
                onClick={() => setRunFilter(value)}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
        {runs.isLoading ? (
          <LoadingBlock label="Loading tasks…" />
        ) : runs.isError ? (
          <ErrorBlock error={runs.error} onRetry={() => runs.refetch()} title="Couldn't load tasks" />
        ) : visibleRuns.length === 0 ? (
          <EmptyState
            data-testid="runs-empty"
            title={runFilter === 'all' ? 'No tasks yet' : `No ${runFilter} for this service`}
            description={
              runFilter === 'all'
                ? canRun
                  ? 'Dispatch your first task using the composer above.'
                  : 'No tasks have been dispatched in this project yet.'
                : 'Try another filter or choose a different service.'
            }
          />
        ) : (
          <div className={styles.tableWrap} role="table" data-testid="runs-table">
            <div className={styles.tableHead} role="rowgroup">
              <div className={styles.tableHeadRow} role="row">
                <span role="columnheader">Run</span>
                <span role="columnheader">Task</span>
                <span role="columnheader">Status</span>
                <span role="columnheader">Created</span>
              </div>
            </div>
            <ul className={styles.rows} role="rowgroup">
              {visibleRuns.map((run) => (
                <li key={run.id} role="presentation">
                  <Link
                    to={`/runs/${run.id}`}
                    className={styles.row}
                    role="row"
                    data-testid="run-row"
                    data-run-id={run.id}
                    data-status={run.status}
                  >
                    <code className={styles.runId} role="cell">
                      {shortId(run.id)}
                    </code>
                    <span className={styles.prompt} role="cell">
                      <span className={styles.runKind}>{runKindLabel(run)}</span>
                      {summarize(run.prompt)}
                      {run.retried_from && (
                        <span className={styles.retryTag} title="Retry of an earlier run">
                          retry
                        </span>
                      )}
                    </span>
                    <span role="cell">
                      <StatusBadge status={run.status} size="sm" />
                    </span>
                    <span className={styles.created} role="cell">
                      {timeAgo(run.created_at)}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        )}

        {/* Low-key "add another repository" affordance — turns the project into a
            multi-repo project and reveals the composer's repository selector. An
            owner can add directly or via an integration; a member can add via a
            project integration (D19 / F5). */}
        {canAddRepo && (
          <div className={styles.addRepo}>
            {addOpen ? (
              <Card className={styles.addRepoCard}>
                {/* Drone-style picker: search what your credential (or an
                    integration's bot) can see and one-click attach. */}
                <div className={styles.repoPicker} data-testid="repo-picker">
                  {/* Source selector (D19 / F5): Direct (owner) and/or integrations. */}
                  {(availableIntegrations.length > 0 || canManage) && (
                    <label className={styles.pickerHint} style={{ display: 'block' }}>
                      Source
                      <Select
                        value={effectiveIntegrationId}
                        onChange={setPickerIntegrationId}
                        data-testid="repo-source-select"
                        style={{ display: 'flex', width: '100%', marginTop: 4 }}
                        options={[
                          ...(canManage ? [{ value: '', label: 'Direct (your credential)' }] : []),
                          ...availableIntegrations.map((i) => ({
                            value: i.id,
                            label: `${i.name} · ${i.provider} · @${i.bot_username}`,
                          })),
                        ]}
                      />
                    </label>
                  )}
                  {!integrationMode && pickerProviders.length > 1 && (
                    <div className={styles.pickerTabs} role="tablist">
                      {pickerProviders.map((id) => (
                        <button
                          key={id}
                          type="button"
                          role="tab"
                          aria-selected={pickerProvider === id}
                          className={styles.pickerTab}
                          data-active={pickerProvider === id || undefined}
                          onClick={() => setPickerProvider(id)}
                        >
                          {id}
                        </button>
                      ))}
                    </div>
                  )}
                  <TextField
                    label="Pick a repository"
                    placeholder="Search repositories…"
                    value={repoQuery}
                    onChange={(e) => setRepoQuery(e.target.value)}
                    data-testid="repo-picker-search"
                    autoComplete="off"
                  />
                  {repoList.isLoading ? (
                    <p className={styles.pickerHint}>Loading repositories…</p>
                  ) : repoList.isError ? (
                    <p className={styles.pickerHint} data-testid="repo-picker-error">
                      {repoList.error instanceof ApiError
                        ? repoList.error.message
                        : `Couldn't list ${integrationMode ? 'integration' : pickerProvider} repositories.`}
                      {!integrationMode && ' Add the repository by URL below instead.'}
                    </p>
                  ) : repoList.data && repoList.data.length === 0 ? (
                    <p className={styles.pickerHint}>No repositories match.</p>
                  ) : (
                    <ul className={styles.pickerList}>
                      {(repoList.data ?? []).map((r) => (
                        <li key={r.id}>
                          <button
                            type="button"
                            className={styles.pickerItem}
                            onClick={() => pickRepo(r)}
                            disabled={createService.isPending}
                            data-testid="repo-pick"
                            data-repo={r.full_name}
                          >
                            <span className={styles.pickerRepoName}>
                              {r.full_name}
                              {r.private && <span className={styles.pickerPrivate}>private</span>}
                            </span>
                            {r.description && (
                              <span className={styles.pickerRepoDesc}>{r.description}</span>
                            )}
                          </button>
                        </li>
                      ))}
                    </ul>
                  )}
                  {/* Manual URL entry is the owner-only Direct fallback. */}
                  {!integrationMode && canManage && (
                    <div className={styles.pickerDivider}>or add by URL</div>
                  )}
                </div>
                {!integrationMode && canManage && (
                <form onSubmit={submitRepo} noValidate className={styles.addRepoForm}>
                  <TextField
                    label="Repository name"
                    required
                    placeholder="frontend"
                    value={repoName}
                    onChange={(e) => setRepoName(e.target.value)}
                    error={repoErr.name}
                    data-testid="add-repo-name"
                    autoComplete="off"
                  />
                  <TextField
                    label="Repository URL"
                    required
                    placeholder="https://github.com/acme/frontend"
                    value={repoUrl}
                    onChange={(e) => setRepoUrl(e.target.value)}
                    error={repoErr.url}
                    data-testid="add-repo-url"
                    autoComplete="off"
                  />
                  <GitModeToggle value={repoMode} onChange={setRepoMode} />
                  <div className={styles.addRepoActions}>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => setAddOpen(false)}
                      disabled={createService.isPending}
                    >
                      Cancel
                    </Button>
                    <Button
                      type="submit"
                      variant="primary"
                      size="sm"
                      loading={createService.isPending}
                      data-testid="add-repo-submit"
                    >
                      Add repository
                    </Button>
                  </div>
                </form>
                )}
                {integrationMode && (
                  <div className={styles.addRepoActions}>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => setAddOpen(false)}
                      disabled={createService.isPending}
                    >
                      Cancel
                    </Button>
                  </div>
                )}
              </Card>
            ) : (
              <button
                type="button"
                className={styles.addRepoTrigger}
                onClick={() => setAddOpen(true)}
                data-testid="add-repo-trigger"
              >
                + Add repository
              </button>
            )}
          </div>
        )}

        {/* Fail-visible empty state (D19 / F5): a member with no integration has
            no way to add a repository — explain the path instead of silently
            hiding the affordance. */}
        {memberNeedsIntegration && (
          <div className={styles.addRepo}>
            <p className={styles.pickerHint} data-testid="add-repo-needs-integration">
              Adding a repository needs a git integration — ask a project owner to
              connect one under Project settings → Integrations.
            </p>
          </div>
        )}
      </section>

          </>
        )}
        </div>

        <section
          id="workspace-panel-automations"
          role="tabpanel"
          aria-labelledby="workspace-tab-automations"
          hidden={workspaceTab !== 'automations'}
          className={styles.workspacePanel}
        >
        {workspaceTab === 'automations' && (
          <>
          <div className={styles.automationHead}>
            <span className={styles.eyebrow}>Service automation</span>
            <h2>Schedules and provider events</h2>
            <p>Automations always run against the selected service.</p>
          </div>

          {canRun && activeService ? (
            <>
              <section className={styles.automationCapability} aria-label="Provider webhook capability">
                <span className={styles.capabilityLabel}>Provider event reviews · status unavailable</span>
                <h3>
                  {activeService.repo_kind === 'provider'
                    ? `${serviceProviderLabel(activeService)} review webhook cannot be verified here`
                    : 'Provider events need a provider-backed service'}
                </h3>
                <p>
                  {activeService.repo_kind === 'provider'
                    ? 'A provider-backed service is eligible for @jcode review events, but this API does not expose webhook registration or delivery health. Do not assume the webhook is active; verify deployment credentials and provider setup with a cluster administrator.'
                    : 'This service is addressed by a path or URL, so it cannot receive PR review webhooks.'}
                </p>
              </section>
              <SchedulesPanel service={activeService} canManage={canManage} />
            </>
          ) : (
            <EmptyState
              title="Automations need a service"
              description="Connect a repository first; schedules and provider events are scoped to a service."
            />
          )}
          </>
        )}
        </section>
      </div>

      </section>

      <ProjectSettingsModal
        open={settingsOpen}
        project={p!}
        onClose={() => setSettingsOpen(false)}
        onDeleted={() => {
          setSettingsOpen(false);
          navigate('/');
        }}
      />

      {kanbanOpen && hasBoardLinks && (
        <KanbanBoardModal
          projectId={projectId}
          links={boardLinks.data!}
          onClose={() => setKanbanOpen(false)}
        />
      )}
    </div>
  );
}
