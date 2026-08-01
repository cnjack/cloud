/*
 * ProjectDetailPage — route controller for the Project workspace.
 *
 * The page intentionally owns queries and mutations, while the visual surface
 * lives in project-workspace/. This keeps a Project's service selection,
 * workspace tab, composer, activity, and settings policy from drifting into
 * unrelated generic page primitives.
 */
import { useDeferredValue, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowSquareOut, GitBranch, Plus, Trash } from '@phosphor-icons/react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  useCreateService,
  useCreateServiceRun,
  useDeleteService,
  usePluginRepositories,
  useProject,
  useProjectBoardLinks,
  useProjectModels,
  useProjectPlugins,
	useServiceBranches,
	useSystem,
  useRuns,
  useUpdateService,
} from '../api/queries';
import { useApi, useDemoMode, useRole } from '../api/ApiProvider';
import { useOptionalAuth } from '../auth/AuthProvider';
import { ApiError } from '../api/client';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { GitModeBadge } from '../components/GitModeBadge';
import { Modal } from '../components/Modal';
import { useModelGate } from '../components/ModelGate';
import { RailAccountFooter } from '../components/RailAccountFooter';
import { Select } from '../components/Select';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { LanguageToggle } from '../components/LanguageToggle';
import { ThemeToggle } from '../components/ThemeToggle';
import { useToast } from '../components/Toast';
import { Wordmark } from '../components/Wordmark';
import type { PluginRepositoryResource } from '../api/types';
import { resolveWorkspaceLocation, type WorkspaceTab } from '../project-workspace/location';
import { ProjectWorkspaceShell } from '../project-workspace/ProjectWorkspaceShell';
import { ProjectSettingsAction } from '../project-workspace/ProjectSettingsAction';
import { RunActivityList, type RunFilter } from '../project-workspace/RunActivityList';
import { SettingsPanel } from '../project-workspace/SettingsPanel';
import { TaskComposer } from '../project-workspace/TaskComposer';
import { ProjectAutomationsPanel } from '../project-workspace/ProjectAutomationsPanel';
import { serviceMark, serviceProviderLabel, serviceSource } from '../project-workspace/presentation';
import { KanbanBoardModal } from './KanbanBoardModal';
import { ProjectUsagePanel } from './ProjectUsagePanel';
import {
  ProjectSettingsPage,
  ProjectSettingsSubnav,
  resolveProjectSettingsSection,
  type ProjectSettingsSectionId,
} from './ProjectSettingsModal';
import styles from './ProjectDetailPage.module.css';

const MAX_RUN_ATTACHMENTS = 10;
const MAX_ATTACHMENT_SIZE_BYTES = 25 * 1024 * 1024;
const MAX_RUN_ATTACHMENTS_SIZE_MIB = 100;
const MAX_RUN_ATTACHMENTS_SIZE_BYTES = MAX_RUN_ATTACHMENTS_SIZE_MIB * 1024 * 1024;

export function ProjectDetailPage() {
  const { t } = useTranslation();
  const { projectId = '' } = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const toast = useToast();
  const auth = useOptionalAuth();
  const appRole = useRole();
  const demo = useDemoMode();
  const api = useApi();

  const project = useProject(projectId);
  const runs = useRuns(projectId);
  const createServiceRun = useCreateServiceRun(projectId);
  const createService = useCreateService(projectId);
  const updateService = useUpdateService(projectId);
  const deleteService = useDeleteService(projectId);

  const [prompt, setPrompt] = useState('');
  const [promptError, setPromptError] = useState<string>();
  const [selectedModel, setSelectedModel] = useState('');
  const [selectedBranch, setSelectedBranch] = useState('');
  const [modelEffort, setModelEffort] = useState<'auto' | 'low' | 'medium' | 'high'>('auto');
  const [goalMode, setGoalMode] = useState(false);
  const [attachments, setAttachments] = useState<File[]>([]);
  const [attachmentError, setAttachmentError] = useState<string>();
  const [attachmentsUploading, setAttachmentsUploading] = useState(false);
  const [askApproval, setAskApproval] = useState(false);
  const [runFilter, setRunFilter] = useState<RunFilter>('all');
  const [kanbanOpen, setKanbanOpen] = useState(false);
  const [kanbanCardPath, setKanbanCardPath] = useState('');
  const [repoQuery, setRepoQuery] = useState('');
  const [pickerInstallationId, setPickerInstallationId] = useState('');
  const deferredQuery = useDeferredValue(repoQuery);

  const p = project.data;
  // Some clients update the project cache by mutating its services array. Do
  // not memoize this derived route state by object identity: a newly added
  // service must immediately become a selectable execution target.
  const services = p?.services ?? [];
  const role = p?.role ?? 'owner';
  const canRun = role !== 'viewer';
  const canManage = role === 'owner';
  const workspaceLocation = resolveWorkspaceLocation(services, searchParams, canManage);
  const activeServiceId = workspaceLocation.serviceId;
  const activeService = services.find((service) => service.id === activeServiceId);
  const serviceBranches = useServiceBranches(activeServiceId, canRun && !!activeService);
  const workspaceTab = workspaceLocation.tab;
  const addOpen = searchParams.get('add') === 'service';
  const projectSettingsOpen = canManage && searchParams.get('view') === 'project-settings';
  const projectSettingsSection = resolveProjectSettingsSection(searchParams.get('settings'), canManage);

  useEffect(() => {
    if (searchParams.get('kanban') !== '1' || !activeService) return;
    setKanbanCardPath(searchParams.get('card') ?? '');
    setKanbanOpen(true);
    const next = new URLSearchParams(searchParams);
    next.delete('kanban');
    next.delete('card');
    setSearchParams(next, { replace: true });
  }, [activeService, searchParams, setSearchParams]);

  // A project switch must not retain a previous project's draft/model/form state.
  useEffect(() => {
    setPrompt('');
    setPromptError(undefined);
    setSelectedModel('');
    setSelectedBranch('');
    setModelEffort('auto');
    setGoalMode(false);
    setAttachments([]);
    setAttachmentError(undefined);
    setAskApproval(false);
    setRunFilter('all');
    setRepoQuery('');
    setPickerInstallationId('');
  }, [projectId]);

  // Services are independently bound repositories. Switching services must
  // never retain a branch from the preceding repository; start from its stored
  // default and let branch discovery refine the selectable list.
  useEffect(() => {
    setSelectedBranch(activeService?.default_branch ?? '');
    setAttachments([]);
    setAttachmentError(undefined);
  }, [activeService?.id, activeService?.default_branch]);

	// Only offer branches confirmed by the repository Plugin. If its stored
	// default branch has disappeared upstream, choose the provider's marked
	// default (or first returned ref) instead of submitting a stale guess.
	useEffect(() => {
		const branches = serviceBranches.data;
		if (!branches || branches.length === 0) return;
		if (branches.some((branch) => branch.name === selectedBranch)) return;
		setSelectedBranch(branches.find((branch) => branch.default)?.name ?? branches[0]!.name);
	}, [selectedBranch, serviceBranches.data]);

  // The Project URL is the source of truth for its durable navigation state.
  useEffect(() => {
    if (!p) return;
    const requestedSettingsSection = searchParams.get('settings');
    const settingsNeedsNormalization =
      projectSettingsOpen
      && requestedSettingsSection !== null
      && requestedSettingsSection !== projectSettingsSection;
    if (!workspaceLocation.needsNormalization && !settingsNeedsNormalization) return;
    const next = new URLSearchParams(searchParams);
    if (workspaceLocation.needsNormalization) {
      next.set('service', workspaceLocation.serviceId);
      next.set('tab', workspaceLocation.tab);
    }
    if (settingsNeedsNormalization) next.delete('settings');
    setSearchParams(next, { replace: true });
  }, [p, projectSettingsOpen, projectSettingsSection, searchParams, setSearchParams, workspaceLocation]);

  const pluginsQuery = useProjectPlugins(projectId, !!p && canRun);
  // Viewers cannot open Kanban manually, but may follow an Automation output
  // deep link into the same board with the host enforcing read-only access.
  const boardLinks = useProjectBoardLinks(projectId, !!p);
  const activeBoardLinks = (boardLinks.data ?? []).filter((link) => link.service_id === activeService?.id);
  const jtypePluginEnabled = (pluginsQuery.data ?? []).some((plugin) =>
    plugin.provider === 'jtype' && plugin.status === 'enabled' && !!plugin.workspace_id);
  const canOpenKanban = !!activeService && canRun && (jtypePluginEnabled || activeBoardLinks.length > 0);
  const hasServiceHeaderActions = canOpenKanban ||
    activeService?.repo_kind === 'provider' || (canManage && !!activeService);
  const availableGitPlugins = useMemo(
    () => (pluginsQuery.data ?? []).filter((plugin) =>
      plugin.id &&
      plugin.status === 'enabled' &&
      (plugin.provider === 'github' || plugin.provider === 'gitlab' || plugin.provider === 'gitea')),
    [pluginsQuery.data],
  );
  const effectiveInstallationId = pickerInstallationId || availableGitPlugins[0]?.id || '';
  const selectedPlugin = availableGitPlugins.find((plugin) => plugin.id === effectiveInstallationId);
  const canAddRepo = canRun && availableGitPlugins.length > 0;
  const needsGitPlugin = canRun && pluginsQuery.isSuccess && availableGitPlugins.length === 0;
  const repoList = usePluginRepositories(
    projectId,
    effectiveInstallationId,
    deferredQuery,
    addOpen && !!effectiveInstallationId,
  );

  const modelGate = useModelGate(projectId, canRun && services.length > 0);
  const projectModels = useProjectModels(projectId, canRun && services.length > 0);
	const system = useSystem(canManage);
  const grantedModels = projectModels.data?.models ?? [];
  const modelPolicyState = projectModels.isError
    ? 'unverified'
    : projectModels.isLoading
      ? 'loading'
      : 'ready';
  const effectiveSelectedModel = grantedModels.some((model) => model.id === selectedModel)
    ? selectedModel
    : '';
  const effectiveModel = grantedModels.find((model) =>
    model.id === (effectiveSelectedModel || activeService?.default_model_id || p?.default_model_id))
    ?? (grantedModels.length === 1 ? grantedModels[0] : undefined);
  const effortEnabled = effectiveModel?.capabilities.reasoning === true;

  useEffect(() => {
    if (!effortEnabled) setModelEffort('auto');
  }, [effortEnabled]);

  const scopedRuns = useMemo(() => {
    const allRuns = runs.data ?? [];
    // Old rows without service_id are project history, not invisible data.
    if (!activeServiceId) return allRuns;
    return allRuns.filter((run) => !run.service_id || run.service_id === activeServiceId);
  }, [activeServiceId, runs.data]);
  const visibleRuns = useMemo(() => {
    if (runFilter === 'sessions') return scopedRuns.filter((run) => run.session);
    if (runFilter === 'reviews') return scopedRuns.filter((run) => run.kind === 'review');
    return scopedRuns;
  }, [runFilter, scopedRuns]);

  const setWorkspaceTab = (tab: WorkspaceTab) => {
    const next = new URLSearchParams(searchParams);
    next.delete('view');
    next.delete('settings');
    if (activeServiceId) next.set('service', activeServiceId);
    next.set('tab', tab);
    if (tab !== 'tasks') next.delete('add');
    setSearchParams(next);
  };

  const setProjectSettingsOpen = (open: boolean) => {
    const next = new URLSearchParams(searchParams);
    if (open) next.set('view', 'project-settings');
    else {
      next.delete('view');
      next.delete('settings');
    }
    setSearchParams(next);
  };

  const setProjectSettingsSection = (section: ProjectSettingsSectionId) => {
    const next = new URLSearchParams(searchParams);
    next.set('view', 'project-settings');
    if (section === 'general') next.delete('settings');
    else next.set('settings', section);
    setSearchParams(next);
  };

  const selectService = (serviceId: string) => {
    const next = new URLSearchParams(searchParams);
    next.delete('view');
    next.delete('settings');
    next.set('service', serviceId);
    next.set('tab', workspaceTab);
    setSearchParams(next);
    if (workspaceTab === 'tasks') setRunFilter('all');
  };

  const openAddService = () => {
    const next = new URLSearchParams(searchParams);
    next.set('add', 'service');
    setSearchParams(next);
  };

  const closeAddService = () => {
    if (createService.isPending) return;
    const next = new URLSearchParams(searchParams);
    next.delete('add');
    setSearchParams(next, { replace: true });
  };

  const submit = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!modelGate.configured || !activeServiceId) return;
    if (!prompt.trim()) {
      setPromptError(t('projectDetail.promptRequired'));
      return;
    }
    setPromptError(undefined);
    setAttachmentError(undefined);
    const baseBranch = selectedBranch || activeService?.default_branch;
    void (async () => {
      setAttachmentsUploading(true);
      let attachmentStageIDs: string[] = [];
      try {
        const staged = [];
        for (const file of attachments) {
          staged.push(await api.uploadRunAttachment(activeServiceId, file));
        }
        attachmentStageIDs = staged.map((intent) => intent.stage.id);
      } catch (error) {
        setAttachmentError(error instanceof ApiError ? error.message : t('taskComposer.attachmentUploadFailed'));
        setAttachmentsUploading(false);
        return;
      }
      setAttachmentsUploading(false);
      createServiceRun.mutate(
      {
        serviceId: activeServiceId,
        input: {
          prompt: prompt.trim(),
          ...(baseBranch ? { base_branch: baseBranch } : {}),
          ...(effectiveSelectedModel ? { model_id: effectiveSelectedModel } : {}),
          ...(modelEffort !== 'auto' ? { model_effort: modelEffort } : {}),
          ...(goalMode ? { goal_mode: true } : {}),
          ...(attachmentStageIDs.length ? { attachment_stage_ids: attachmentStageIDs } : {}),
          session: true,
          ...(askApproval ? { permission_mode: 'approval' as const } : {}),
        },
      },
      {
        onSuccess: (run) => {
          setPrompt('');
          setAttachments([]);
          setGoalMode(false);
          setModelEffort('auto');
          toast.push({ kind: 'success', message: t('projectDetail.sessionStarted') });
          navigate(`/runs/${run.id}`);
        },
        onError: (error) => {
          toast.push({
            kind: 'error',
            message: error instanceof ApiError ? error.message : t('projectDetail.startRunFailed'),
          });
        },
      },
      );
    })();
  };

  const addAttachments = (files: File[]) => {
    setAttachmentError(undefined);
    const room = MAX_RUN_ATTACHMENTS - attachments.length;
    if (files.length > room) {
      setAttachmentError(t('taskComposer.attachmentTooMany', { max: MAX_RUN_ATTACHMENTS }));
      return;
    }
    const invalid = files.find((file) => file.size <= 0 || file.size > MAX_ATTACHMENT_SIZE_BYTES);
    if (invalid) {
      setAttachmentError(t('taskComposer.attachmentTooLarge', { name: invalid.name }));
      return;
    }
    const selectedBytes = attachments.reduce((total, file) => total + file.size, 0);
    const addedBytes = files.reduce((total, file) => total + file.size, 0);
    if (selectedBytes + addedBytes > MAX_RUN_ATTACHMENTS_SIZE_BYTES) {
      setAttachmentError(t('taskComposer.attachmentTotalTooLarge', { max: MAX_RUN_ATTACHMENTS_SIZE_MIB }));
      return;
    }
    setAttachments((current) => [...current, ...files]);
  };

  const updateDefaultModel = (modelId: string) => {
    if (!activeService) return;
    updateService.mutate(
      { serviceId: activeService.id, input: { default_model_id: modelId } },
      {
        onSuccess: () => toast.push({ kind: 'success', message: t('projectDetail.defaultModelUpdated') }),
        onError: (error) =>
          toast.push({
            kind: 'error',
            message: error instanceof ApiError ? error.message : t('projectDetail.defaultModelFailed'),
          }),
      },
    );
  };

  const updatePRReadyPolicy = (policy: 'always_draft' | 'lifecycle_aware') => {
    if (!activeService) return;
    updateService.mutate(
      { serviceId: activeService.id, input: { pr_ready_policy: policy } },
      {
        onSuccess: () => toast.push({ kind: 'success', message: t('projectDetail.deliveryPolicyUpdated') }),
        onError: (error) =>
          toast.push({
            kind: 'error',
            message: error instanceof ApiError ? error.message : t('projectDetail.deliveryPolicyFailed'),
          }),
      },
    );
  };

  const updateRunnerProfile = (runnerProfile: string) => {
    if (!activeService) return;
    updateService.mutate(
      { serviceId: activeService.id, input: { runner_profile: runnerProfile } },
      {
        onSuccess: () => toast.push({ kind: 'success', message: t('projectDetail.runnerProfileUpdated') }),
        onError: (error) => toast.push({
          kind: 'error',
          message: error instanceof ApiError ? error.message : t('projectDetail.runnerProfileFailed'),
        }),
      },
    );
  };

  const removeActiveService = () => {
    if (!activeService) return;
    if (!window.confirm(t('projectDetail.deleteServiceConfirm', { name: activeService.name }))) return;
    deleteService.mutate(activeService.id, {
      onSuccess: () => {
        const next = new URLSearchParams(searchParams);
        const firstRemaining = services.find((service) => service.id !== activeService.id);
        if (firstRemaining) next.set('service', firstRemaining.id);
        else next.delete('service');
        next.set('tab', 'tasks');
        setSearchParams(next, { replace: true });
        toast.push({ kind: 'success', message: t('projectDetail.serviceDeleted', { name: activeService.name }) });
      },
      onError: (error) => toast.push({
        kind: 'error',
        message: error instanceof ApiError ? error.message : t('projectDetail.deleteServiceFailed'),
      }),
    });
  };

  const pickRepo = (repo: PluginRepositoryResource) => {
    const name = repo.full_name.split('/').pop() || repo.full_name;
    createService.mutate({
      name,
      installation_id: effectiveInstallationId,
      provider_repo_id: String(repo.id),
      git_mode: 'draft_pr',
    }, {
      onSuccess: () => {
        toast.push({ kind: 'success', message: t('projectDetail.repoAdded', { name: repo.full_name }) });
        closeAddService();
        setRepoQuery('');
      },
      onError: (error) =>
        toast.push({
          kind: 'error',
          message: error instanceof ApiError ? error.message : t('projectDetail.addRepoFailed'),
        }),
    });
  };

  if (project.isLoading) return <LoadingBlock label={t('projectDetail.loadingProject')} />;
  if (project.isError || !p) {
    return (
      <ErrorBlock
        error={project.error}
        onRetry={() => project.refetch()}
        title={t('projectDetail.loadFailedTitle')}
      />
    );
  }

  return (
    <>
      <ProjectWorkspaceShell
        mode={projectSettingsOpen ? 'settings' : 'workspace'}
        workspaceChrome={projectSettingsOpen || services.length > 0}
        projectName={p.name}
        services={services}
        activeServiceId={activeServiceId}
        activeTab={workspaceTab}
        canManage={canManage}
        onSelectService={selectService}
        onSelectTab={setWorkspaceTab}
        railTop={
          <>
            <Wordmark />
            <Link to="/" className={styles.workspaceProjectsLink}>
              {t('projectDetail.projects')}
            </Link>
          </>
        }
        railFooter={
          <RailAccountFooter
            demo={demo}
            me={auth?.me ?? null}
            providers={auth?.providers ?? []}
            role={appRole}
            onSignOut={auth && !demo ? auth.logout : undefined}
            testId="project-rail-footer"
          />
        }
        railAction={
          canRun && services.length > 0 ? (
            <button
              type="button"
              className={styles.railAddService}
              onClick={openAddService}
              data-testid="add-repo-trigger"
            >
              <Plus size={16} weight="regular" aria-hidden="true" />
              <span>{t('projectDetail.addService')}</span>
            </button>
          ) : undefined
        }
        projectAction={
          canManage ? (
            <ProjectSettingsAction
              onClick={() => setProjectSettingsOpen(true)}
              active={projectSettingsOpen}
            />
          ) : undefined
        }
        mobileActions={
          canRun && services.length > 0 ? (
            <button type="button" className={styles.mobileAddService} onClick={openAddService}>
              <Plus size={16} weight="regular" aria-hidden="true" />
              <span>{t('common.add')}</span>
            </button>
          ) : undefined
        }
        utility={
          <>
            <nav className={styles.workspaceBreadcrumbs} aria-label={t('projectDetail.breadcrumb')}>
              <Link to="/">{t('projectDetail.projects')}</Link>
              <span aria-hidden>/</span>
              {projectSettingsOpen ? (
                <button
                  type="button"
                  className={styles.workspaceBreadcrumbBack}
                  onClick={() => setProjectSettingsOpen(false)}
                  data-testid="project-settings-back"
                  aria-label={t('projectDetail.backToWorkspace')}
                >
                  <span>{p.name}</span>
                </button>
              ) : <span>{p.name}</span>}
              {projectSettingsOpen && (
                <>
                  <span aria-hidden>/</span>
                  <span>{t('projectDetail.projectSettings')}</span>
                </>
              )}
            </nav>
            <div className={styles.workspaceUtilityActions} data-testid="project-utility-actions">
              {demo && <span className={styles.workspaceDemoTag}>{t('projectDetail.demoTag')}</span>}
              <LanguageToggle />
              <ThemeToggle />
            </div>
          </>
        }
        subnav={
          projectSettingsOpen ? (
            <ProjectSettingsSubnav
              canManage={canRun}
              activeSection={projectSettingsSection}
              onSelect={setProjectSettingsSection}
            />
          ) : undefined
        }
        scrollResetKey={
          projectSettingsOpen ? `${p.id}:project-settings:${projectSettingsSection}` : undefined
        }
        header={
          <div className={styles.workspaceServiceHeader}>
            <div className={styles.workspaceServiceIdentity}>
              {activeService && (
                <span className={styles.workspaceServiceMark} aria-hidden>
                  {serviceMark(activeService)}
                </span>
              )}
              <div className={styles.workspaceServiceCopy}>
                <span className={styles.workspaceServiceEyebrow}>
                  {activeService ? serviceProviderLabel(activeService) : t('projectDetail.projectWorkspace')}
                </span>
                <h1>{activeService?.name ?? p.name}</h1>
                <div className={styles.workspaceRepoMeta}>
                  {activeService ? (
                    <>
                      <code>{serviceSource(activeService)}</code>
                      <span>{activeService.default_branch}</span>
                      <GitModeBadge
                        gitMode={activeService.git_mode}
                        providerRepo={activeService.repo_owner_name}
                      />
                    </>
                  ) : (
                    <span>{t('projectDetail.noRepositoriesYet')}</span>
                  )}
                </div>
              </div>
            </div>
            {hasServiceHeaderActions && (
              <div className={styles.workspaceServiceActions} data-testid="workspace-service-actions">
                {canOpenKanban && (
                  <button
                    type="button"
                    className={styles.workspaceServiceAction}
                    onClick={() => setKanbanOpen(true)}
                    data-testid="project-kanban-btn"
                  >
                    {t('projectDetail.kanban')}
                  </button>
                )}
                {activeService && activeService.repo_kind === 'provider' && (
                  activeService.repo_html_url ? (
                    <a
                      className={styles.workspaceServiceAction}
                      href={activeService.repo_html_url}
                      target="_blank"
                      rel="noopener noreferrer"
                      aria-label={t('projectDetail.openProvider', { provider: serviceProviderLabel(activeService) })}
                    >
                      <ArrowSquareOut size={16} aria-hidden="true" />
                      <span>{t('projectDetail.openProvider', { provider: serviceProviderLabel(activeService) })}</span>
                    </a>
                  ) : (
                    <button
                      type="button"
                      className={styles.workspaceServiceAction}
                      disabled
                      title={t('projectDetail.repoUrlUnresolved')}
                    >{t('projectDetail.repositoryUnavailable')}</button>
                  )
                )}
                {canManage && activeService && (
                  <button
                    type="button"
                    className={styles.workspaceServiceAction}
                    onClick={removeActiveService}
                    disabled={deleteService.isPending}
                    aria-label={t('projectDetail.deleteServiceAria', { name: activeService.name })}
                    data-testid="delete-service"
                  >
                    <Trash size={16} aria-hidden="true" />
                    <span>{t('projectDetail.deleteService')}</span>
                  </button>
                )}
              </div>
            )}
          </div>
        }
      >
        {projectSettingsOpen ? (
          <ProjectSettingsPage
            project={p}
            onDeleted={() => navigate('/')}
            activeSection={projectSettingsSection}
          />
        ) : workspaceTab === 'tasks' && (
          <>
            {services.length === 0 && (
              <section className={styles.firstServiceEmpty} data-testid="no-repo-empty">
                <span className={styles.firstServiceIcon} aria-hidden="true">
                  <GitBranch size={22} weight="regular" />
                </span>
                <span className={styles.firstServiceEyebrow}>{t('projectDetail.firstServiceEyebrow')}</span>
                <h2>{t('projectDetail.addRepoEmptyTitle')}</h2>
                <p>
                  {canManage
                    ? t('projectDetail.addRepoEmptyManage')
                    : t('projectDetail.addRepoEmptyMember')}
                </p>
                {canRun && (
                  <Button data-testid="empty-add-service" onClick={openAddService}>
                    <Plus size={14} aria-hidden="true" />
                    {t('projectDetail.firstServiceAction')}
                  </Button>
                )}
              </section>
            )}

            {canRun && activeService && (
              <TaskComposer
                service={activeService}
                notice={modelGate.notice}
                configured={modelGate.configured && !serviceBranches.isError}
                prompt={prompt}
                promptError={promptError}
                onPromptChange={setPrompt}
                models={grantedModels}
                selectedModel={effectiveSelectedModel}
                effectiveDefaultModelName={effectiveModel?.name}
                onSelectedModelChange={(modelID) => {
                  setSelectedModel(modelID);
                  setModelEffort('auto');
                }}
                effortEnabled={effortEnabled}
                modelEffort={modelEffort}
                onModelEffortChange={setModelEffort}
                goalMode={goalMode}
                onGoalModeChange={setGoalMode}
                attachments={attachments}
                attachmentError={attachmentError}
                onAttachmentsAdd={addAttachments}
                onAttachmentRemove={(index) => setAttachments((current) => current.filter((_, itemIndex) => itemIndex !== index))}
                branches={serviceBranches.data ?? []}
                branchesLoading={serviceBranches.isLoading}
                branchesError={serviceBranches.isError}
                selectedBranch={selectedBranch}
                onSelectedBranchChange={setSelectedBranch}
                askApproval={askApproval}
                onAskApprovalChange={setAskApproval}
                onSubmit={submit}
                busy={attachmentsUploading || createServiceRun.isPending}
              />
            )}

            {services.length > 0 && (
              <RunActivityList
                runs={visibleRuns}
                isLoading={runs.isLoading}
                error={runs.isError ? runs.error : undefined}
                onRetry={() => void runs.refetch()}
                filter={runFilter}
                onFilterChange={setRunFilter}
                canRun={canRun}
              />
            )}

            {needsGitPlugin && !addOpen && services.length > 0 && (
              <p className={styles.addRepoNeedsIntegration} data-testid="add-repo-needs-plugin">
                {t('projectDetail.memberNeedsIntegration')}
              </p>
            )}
          </>
        )}

        {!projectSettingsOpen && workspaceTab === 'automations' && (
          <section className={styles.automationWorkspace}>
            <ProjectAutomationsPanel
              projectId={projectId}
              services={services}
              canManage={canManage}
              initialServiceId={activeService?.id}
            />
          </section>
        )}

        {!projectSettingsOpen && workspaceTab === 'usage' && (
          <ProjectUsagePanel projectId={projectId} />
        )}

        {!projectSettingsOpen && workspaceTab === 'settings' && canManage && (
          <SettingsPanel
            service={activeService}
            models={grantedModels}
            modelState={modelPolicyState}
            updating={updateService.isPending}
            onDefaultModelChange={updateDefaultModel}
            onPRReadyPolicyChange={updatePRReadyPolicy}
            runnerProfiles={system.data?.runner.profiles ?? []}
            onRunnerProfileChange={updateRunnerProfile}
            onRetryModels={() => void projectModels.refetch()}
          />
        )}
      </ProjectWorkspaceShell>

      <Modal
        open={canRun && addOpen}
        onClose={closeAddService}
        title={t('projectDetail.addService')}
        data-testid="add-service-dialog"
        footer={
          <Button
            type="button"
            variant="ghost"
            onClick={closeAddService}
            disabled={createService.isPending}
          >
            {t('common.cancel')}
          </Button>
        }
      >
        <div className={styles.addServiceDialogBody}>
          {services.length === 0 && (
            <p className={styles.addServiceDialogDescription}>
              {t('projectDetail.firstServiceSetupDescription')}
            </p>
          )}
          {canAddRepo ? (
            <div className={styles.repoPicker} data-testid="repo-picker">
              {availableGitPlugins.length > 0 && (
                <label className={styles.pickerHint}>
                  {t('plugins.projectPlugin')}
                  <Select
                    value={effectiveInstallationId}
                    onChange={setPickerInstallationId}
                    data-testid="repo-source-select"
                    className={styles.repoSourceSelect}
                    options={availableGitPlugins.map((plugin) => ({
                      value: plugin.id!,
                      label: `${plugin.provider === 'github' ? 'GitHub' : plugin.provider === 'gitlab' ? 'GitLab' : 'Gitea'} · ${plugin.external_account ?? plugin.external_account_id ?? plugin.provider}`,
                    }))}
                  />
                </label>
              )}
              <TextField
                label={t('projectDetail.pickRepository')}
                placeholder={t('projectDetail.searchRepositories')}
                value={repoQuery}
                onChange={(event) => setRepoQuery(event.target.value)}
                data-testid="repo-picker-search"
                autoComplete="off"
              />
              {repoList.isLoading ? (
                <p className={styles.pickerHint}>{t('projectDetail.loadingRepositories')}</p>
              ) : repoList.isError ? (
                <p className={styles.pickerHint} data-testid="repo-picker-error">
                  {repoList.error instanceof ApiError
                    ? repoList.error.message
                    : t('projectDetail.listReposFailed', { source: selectedPlugin?.provider ?? 'Plugin' })}
                </p>
              ) : repoList.data && repoList.data.length === 0 ? (
                <p className={styles.pickerHint}>{t('projectDetail.noRepositoriesMatch')}</p>
              ) : (
                <ul className={styles.pickerList}>
                  {(repoList.data ?? []).map((repo) => (
                    <li key={repo.id}>
                      <button
                        type="button"
                        className={styles.pickerItem}
                        onClick={() => pickRepo(repo)}
                        disabled={createService.isPending}
                        data-testid="repo-pick"
                        data-repo={repo.full_name}
                      >
                        <span className={styles.pickerRepoName}>
                          {repo.full_name}
                          {repo.private && <span className={styles.pickerPrivate}>{t('projectDetail.private')}</span>}
                        </span>
                        {repo.description && <span className={styles.pickerRepoDesc}>{repo.description}</span>}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          ) : (
            <div className={styles.addRepoNeedsIntegration} data-testid="add-repo-needs-plugin">
              <p>{t('projectDetail.memberNeedsIntegration')}</p>
              {canManage && (
                <Link to={`/projects/${encodeURIComponent(projectId)}?view=project-settings&settings=plugins`}>
                  {t('projectDetail.configureProjectPlugins')}
                </Link>
              )}
            </div>
          )}
        </div>
      </Modal>

      {kanbanOpen && activeService && (
        <KanbanBoardModal
          projectId={projectId}
          serviceId={activeService.id}
          links={activeBoardLinks}
          initialCardPath={kanbanCardPath || undefined}
          canManage={canRun}
          onClose={() => {
            setKanbanOpen(false);
            setKanbanCardPath('');
          }}
        />
      )}

    </>
  );
}
