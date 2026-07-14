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
import { ArrowSquareOut, Plus } from '@phosphor-icons/react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  useCreateService,
  useCreateServiceRun,
  useIntegrationRepos,
  useIntegrations,
  useProject,
  useProjectBoardLinks,
  useProjectModels,
  useProviderRepos,
  useRuns,
  useUpdateService,
} from '../api/queries';
import { useDemoMode, useRole } from '../api/ApiProvider';
import { useOptionalAuth } from '../auth/AuthProvider';
import { ApiError } from '../api/client';
import { Button } from '../components/Button';
import { Card } from '../components/Card';
import { EmptyState } from '../components/EmptyState';
import { TextField } from '../components/Field';
import { GitModeBadge } from '../components/GitModeBadge';
import { GitModeToggle } from '../components/GitModeToggle';
import { IdentityChip } from '../components/IdentityChip';
import { useModelGate } from '../components/ModelGate';
import { Select } from '../components/Select';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { LanguageToggle } from '../components/LanguageToggle';
import { ThemeToggle } from '../components/ThemeToggle';
import { useToast } from '../components/Toast';
import { Wordmark } from '../components/Wordmark';
import { providerForRepoUrl } from '../lib/repo';
import type { GitMode, GitProvider, ProviderRepo } from '../api/types';
import { resolveWorkspaceLocation, type WorkspaceTab } from '../project-workspace/location';
import { ProjectWorkspaceShell } from '../project-workspace/ProjectWorkspaceShell';
import { ProjectSettingsAction } from '../project-workspace/ProjectSettingsAction';
import { RunActivityList, type RunFilter } from '../project-workspace/RunActivityList';
import { SettingsPanel } from '../project-workspace/SettingsPanel';
import { TaskComposer } from '../project-workspace/TaskComposer';
import { AutomationsPanel } from '../project-workspace/AutomationsPanel';
import { serviceMark, serviceProviderLabel, serviceSource } from '../project-workspace/presentation';
import { KanbanBoardModal } from './KanbanBoardModal';
import { ProjectSettingsPage } from './ProjectSettingsModal';
import styles from './ProjectDetailPage.module.css';

export function ProjectDetailPage() {
  const { t } = useTranslation();
  const { projectId = '' } = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const toast = useToast();
  const auth = useOptionalAuth();
  const appRole = useRole();
  const demo = useDemoMode();

  const project = useProject(projectId);
  const runs = useRuns(projectId);
  const createServiceRun = useCreateServiceRun(projectId);
  const createService = useCreateService(projectId);
  const updateService = useUpdateService(projectId);

  const [prompt, setPrompt] = useState('');
  const [promptError, setPromptError] = useState<string>();
  const [selectedModel, setSelectedModel] = useState('');
  const [askApproval, setAskApproval] = useState(false);
  const [runFilter, setRunFilter] = useState<RunFilter>('all');
  const [kanbanOpen, setKanbanOpen] = useState(false);
  const [scheduleCreateOpen, setScheduleCreateOpen] = useState(false);

  const [addOpen, setAddOpen] = useState(false);
  const [repoName, setRepoName] = useState('');
  const [repoUrl, setRepoUrl] = useState('');
  const [repoMode, setRepoMode] = useState<GitMode>('readonly');
  const [repoErr, setRepoErr] = useState<{ name?: string; url?: string }>({});
  const [pickerProvider, setPickerProvider] = useState('gitea');
  const [repoQuery, setRepoQuery] = useState('');
  const [pickerIntegrationId, setPickerIntegrationId] = useState('');
  const deferredQuery = useDeferredValue(repoQuery);

  const p = project.data;
  // Some clients update the project cache by mutating its services array. Do
  // not memoize this derived route state by object identity: a newly added
  // service must immediately become a selectable execution target.
  const services = p?.services ?? [];
  const role = p?.role ?? 'owner';
  const canRun = role !== 'viewer';
  const canManage = role === 'owner';
  const multiService = services.length > 1;
  const workspaceLocation = resolveWorkspaceLocation(services, searchParams, canManage);
  const activeServiceId = workspaceLocation.serviceId;
  const activeService = services.find((service) => service.id === activeServiceId);
  const workspaceTab = workspaceLocation.tab;
  const projectSettingsOpen = canManage && searchParams.get('view') === 'project-settings';
  const automationOAuthReturnTo = (() => {
    const params = new URLSearchParams();
    if (activeServiceId) params.set('service', activeServiceId);
    params.set('tab', 'automations');
    params.set('automation', 'review-new');
    return `/projects/${encodeURIComponent(projectId)}?${params.toString()}`;
  })();

  // A project switch must not retain a previous project's draft/model/form state.
  useEffect(() => {
    setPrompt('');
    setPromptError(undefined);
    setSelectedModel('');
    setAskApproval(false);
    setRunFilter('all');
    setScheduleCreateOpen(false);
    setAddOpen(false);
    setRepoName('');
    setRepoUrl('');
    setRepoMode('readonly');
    setRepoErr({});
    setRepoQuery('');
    setPickerIntegrationId('');
  }, [projectId]);

  // The Project URL is the source of truth for its durable navigation state.
  useEffect(() => {
    if (!p || !workspaceLocation.needsNormalization) return;
    const next = new URLSearchParams(searchParams);
    next.set('service', workspaceLocation.serviceId);
    next.set('tab', workspaceLocation.tab);
    setSearchParams(next, { replace: true });
  }, [p, searchParams, setSearchParams, workspaceLocation]);

  const boardLinks = useProjectBoardLinks(projectId, !!p && canRun);
  const hasBoardLinks = (boardLinks.data?.length ?? 0) > 0;
  const boardLinksUnavailable = canRun && boardLinks.isError;

  const integrationsQuery = useIntegrations(projectId, !!p && canRun);
  const availableIntegrations = useMemo(
    () => integrationsQuery.data ?? [],
    [integrationsQuery.data],
  );
  const effectiveIntegrationId =
    pickerIntegrationId || (!canManage ? availableIntegrations[0]?.id ?? '' : '');
  const integrationMode = effectiveIntegrationId !== '';
  const canAddRepo = canManage || (canRun && availableIntegrations.length > 0);
  const memberNeedsIntegration =
    canRun && !canManage && integrationsQuery.isSuccess && availableIntegrations.length === 0;
  const pickerProviders = useMemo(() => {
    const providerIds = (auth?.providers ?? []).map((provider) => provider.id);
    return providerIds.includes('gitea') ? providerIds : ['gitea', ...providerIds];
  }, [auth]);
  const providerRepos = useProviderRepos(
    pickerProvider,
    deferredQuery,
    addOpen && !integrationMode && canManage,
  );
  const integrationRepos = useIntegrationRepos(
    projectId,
    effectiveIntegrationId,
    deferredQuery,
    addOpen && integrationMode,
  );
  const repoList = integrationMode ? integrationRepos : providerRepos;

  const modelGate = useModelGate(projectId, canRun && services.length > 0);
  const projectModels = useProjectModels(projectId, canRun && services.length > 0);
  const grantedModels = projectModels.data?.models ?? [];
  const modelPolicyState = projectModels.isError
    ? 'unverified'
    : projectModels.isLoading
      ? 'loading'
      : 'ready';
  const effectiveSelectedModel = grantedModels.some((model) => model.id === selectedModel)
    ? selectedModel
    : '';

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
    if (activeServiceId) next.set('service', activeServiceId);
    next.set('tab', tab);
    setSearchParams(next);
  };

  const setProjectSettingsOpen = (open: boolean) => {
    const next = new URLSearchParams(searchParams);
    if (open) next.set('view', 'project-settings');
    else next.delete('view');
    setSearchParams(next);
  };

  const selectService = (serviceId: string) => {
    const next = new URLSearchParams(searchParams);
    next.delete('view');
    next.set('service', serviceId);
    next.set('tab', workspaceTab);
    setSearchParams(next);
    if (workspaceTab === 'tasks') setRunFilter('all');
  };

  const openAddService = () => {
    setWorkspaceTab('tasks');
    setAddOpen(true);
  };

  const submit = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!modelGate.configured || !activeServiceId) return;
    if (!prompt.trim()) {
      setPromptError(t('projectDetail.promptRequired'));
      return;
    }
    setPromptError(undefined);
    createServiceRun.mutate(
      {
        serviceId: activeServiceId,
        input: {
          prompt: prompt.trim(),
          ...(effectiveSelectedModel ? { model_id: effectiveSelectedModel } : {}),
          session: true,
          ...(askApproval ? { permission_mode: 'approval' as const } : {}),
        },
      },
      {
        onSuccess: (run) => {
          setPrompt('');
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

  const pickRepo = (repo: ProviderRepo) => {
    const name = repo.full_name.split('/').pop() || repo.full_name;
    const input = integrationMode
      ? {
          name,
          owner_name: repo.full_name,
          integration_id: effectiveIntegrationId,
          default_branch: repo.default_branch || 'main',
          git_mode: 'draft_pr' as GitMode,
          provider_repo_id: repo.id,
        }
      : {
          name,
          provider: pickerProvider as GitProvider,
          owner_name: repo.full_name,
          default_branch: repo.default_branch || 'main',
          git_mode: 'draft_pr' as GitMode,
          provider_repo_id: repo.id,
        };
    createService.mutate(input, {
      onSuccess: () => {
        toast.push({ kind: 'success', message: t('projectDetail.repoAdded', { name: repo.full_name }) });
        setAddOpen(false);
        setRepoQuery('');
      },
      onError: (error) =>
        toast.push({
          kind: 'error',
          message: error instanceof ApiError ? error.message : t('projectDetail.addRepoFailed'),
        }),
    });
  };

  const submitRepo = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const errors: { name?: string; url?: string } = {};
    if (!repoName.trim()) errors.name = t('projectDetail.nameRequired');
    if (!repoUrl.trim()) errors.url = t('projectDetail.repoUrlRequired');
    else if (repoMode === 'draft_pr' && providerForRepoUrl(repoUrl) === null) {
      errors.url = t('projectDetail.draftPrNeedsProviderUrl');
    }
    setRepoErr(errors);
    if (Object.keys(errors).length > 0) return;

    createService.mutate(
      { name: repoName.trim(), repo_url: repoUrl.trim(), git_mode: repoMode },
      {
        onSuccess: () => {
          toast.push({ kind: 'success', message: t('projectDetail.repoAdded', { name: repoName.trim() }) });
          setAddOpen(false);
          setRepoName('');
          setRepoUrl('');
          setRepoMode('readonly');
          setRepoErr({});
        },
        onError: (error) =>
          toast.push({
            kind: 'error',
            message: error instanceof ApiError ? error.message : t('projectDetail.addRepoFailed'),
          }),
      },
    );
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
          <div className={styles.workspaceFooterRow}>
            {appRole === 'cluster-admin' ? (
              <Link to="/system" className={styles.workspaceClusterLink}>
                {t('projectDetail.cluster')}
              </Link>
            ) : (
              <span className={styles.workspaceFooterLabel}>{t('projectDetail.projectWorkspace')}</span>
            )}
            <ThemeToggle />
          </div>
        }
        railAction={
          canAddRepo ? (
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
          <>
            {canAddRepo && (
              <button type="button" className={styles.mobileAddService} onClick={openAddService}>
                <Plus size={16} weight="regular" aria-hidden="true" />
                <span>{t('common.add')}</span>
              </button>
            )}
            {appRole === 'cluster-admin' && (
              <Link to="/system" className={styles.mobileClusterLink}>
                {t('projectDetail.cluster')}
              </Link>
            )}
            <ThemeToggle />
          </>
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
            <div className={styles.workspaceUtilityActions}>
              {hasBoardLinks && (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setKanbanOpen(true)}
                  data-testid="project-kanban-btn"
                >
                  {t('projectDetail.kanban')}
                </Button>
              )}
              {boardLinksUnavailable && !hasBoardLinks && (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => void boardLinks.refetch()}
                  disabled={boardLinks.isFetching}
                  title={t('projectDetail.kanbanRetryTitle')}
                  data-testid="project-kanban-retry"
                >
                  {boardLinks.isFetching ? t('projectDetail.loadingKanban') : t('projectDetail.kanbanUnavailableRetry')}
                </Button>
              )}
              {demo && <span className={styles.workspaceDemoTag}>{t('projectDetail.demoTag')}</span>}
              <LanguageToggle />
              <IdentityChip
                me={auth?.me ?? null}
                providers={auth?.providers ?? []}
                role={appRole}
                onSignOut={auth && !demo ? auth.logout : undefined}
              />
            </div>
          </>
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
                  {multiService && (
                    <span className={styles.workspaceRepoCount} data-testid="repo-count">
                      {t('projectDetail.repositoriesCount', { count: services.length })}
                    </span>
                  )}
                </div>
              </div>
            </div>
            {activeService && activeService.repo_kind === 'provider' && (
              activeService.repo_html_url ? (
                <a
                  className={styles.workspaceRepoAction}
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
                  className={styles.workspaceRepoAction}
                  disabled
                  title={t('projectDetail.repoUrlUnresolved')}
                >{t('projectDetail.repositoryUnavailable')}</button>
              )
            )}
          </div>
        }
      >
        {projectSettingsOpen ? (
          <ProjectSettingsPage
            project={p}
            onDeleted={() => navigate('/')}
          />
        ) : workspaceTab === 'tasks' && (
          <>
            {canRun && services.length === 0 && (
              <EmptyState
                data-testid="no-repo-empty"
                title={t('projectDetail.addRepoEmptyTitle')}
                description={
                  canManage
                    ? t('projectDetail.addRepoEmptyManage')
                    : t('projectDetail.addRepoEmptyMember')
                }
              />
            )}

            {canRun && activeService && (
              <TaskComposer
                service={activeService}
                notice={modelGate.notice}
                configured={modelGate.configured}
                prompt={prompt}
                promptError={promptError}
                onPromptChange={setPrompt}
                models={grantedModels}
                selectedModel={effectiveSelectedModel}
                onSelectedModelChange={setSelectedModel}
                askApproval={askApproval}
                onAskApprovalChange={setAskApproval}
                onSubmit={submit}
                busy={createServiceRun.isPending}
              />
            )}

            <RunActivityList
              runs={visibleRuns}
              isLoading={runs.isLoading}
              error={runs.isError ? runs.error : undefined}
              onRetry={() => void runs.refetch()}
              filter={runFilter}
              onFilterChange={setRunFilter}
              canRun={canRun}
            />

            {canAddRepo && addOpen && (
              <section className={styles.addRepo} aria-label={t('projectDetail.addService')}>
                <Card className={styles.addRepoCard}>
                  <div className={styles.repoPicker} data-testid="repo-picker">
                    {(availableIntegrations.length > 0 || canManage) && (
                      <label className={styles.pickerHint}>
                        {t('projectDetail.source')}
                        <Select
                          value={effectiveIntegrationId}
                          onChange={setPickerIntegrationId}
                          data-testid="repo-source-select"
                          className={styles.repoSourceSelect}
                          options={[
                            ...(canManage ? [{ value: '', label: t('projectDetail.directCredential') }] : []),
                            ...availableIntegrations.map((integration) => ({
                              value: integration.id,
                              label: `${integration.name} · ${integration.provider} · @${integration.bot_username}`,
                            })),
                          ]}
                        />
                      </label>
                    )}
                    {!integrationMode && pickerProviders.length > 1 && (
                      <div className={styles.pickerTabs} role="tablist" aria-label={t('projectDetail.gitProvider')}>
                        {pickerProviders.map((provider) => (
                          <button
                            key={provider}
                            type="button"
                            role="tab"
                            aria-selected={pickerProvider === provider}
                            className={styles.pickerTab}
                            data-active={pickerProvider === provider || undefined}
                            onClick={() => setPickerProvider(provider)}
                          >
                            {provider}
                          </button>
                        ))}
                      </div>
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
                          : t('projectDetail.listReposFailed', { source: integrationMode ? t('projectDetail.integrationSource') : pickerProvider })}
                        {!integrationMode && t('projectDetail.addRepoByUrlInstead')}
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
                    {!integrationMode && canManage && <div className={styles.pickerDivider}>{t('projectDetail.orAddByUrl')}</div>}
                  </div>

                  {!integrationMode && canManage && (
                    <form onSubmit={submitRepo} noValidate className={styles.addRepoForm}>
                      <TextField
                        label={t('projectDetail.repositoryName')}
                        required
                        placeholder="frontend"
                        value={repoName}
                        onChange={(event) => setRepoName(event.target.value)}
                        error={repoErr.name}
                        data-testid="add-repo-name"
                        autoComplete="off"
                      />
                      <TextField
                        label={t('projectDetail.repositoryUrl')}
                        required
                        placeholder="https://github.com/acme/frontend"
                        value={repoUrl}
                        onChange={(event) => setRepoUrl(event.target.value)}
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
                          {t('common.cancel')}
                        </Button>
                        <Button
                          type="submit"
                          variant="primary"
                          size="sm"
                          loading={createService.isPending}
                          data-testid="add-repo-submit"
                        >
                          {t('projectDetail.addRepository')}
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
                        {t('common.cancel')}
                      </Button>
                    </div>
                  )}
                </Card>
              </section>
            )}

            {memberNeedsIntegration && (
              <p className={styles.addRepoNeedsIntegration} data-testid="add-repo-needs-integration">
                {t('projectDetail.memberNeedsIntegration')}
              </p>
            )}
          </>
        )}

        {!projectSettingsOpen && workspaceTab === 'automations' && (
          <section className={styles.automationWorkspace}>
            {activeService && canRun ? (
              <AutomationsPanel
                service={activeService}
                canManage={canManage}
                models={grantedModels}
                scheduleCreateOpen={scheduleCreateOpen}
                onScheduleCreateOpenChange={setScheduleCreateOpen}
                me={auth?.me ?? null}
                providers={auth?.providers ?? []}
                oauthReturnTo={automationOAuthReturnTo}
                initialEditorOpen={searchParams.get('automation') === 'review-new'}
              />
            ) : activeService ? (
              <EmptyState
                title={t('projectDetail.automationsMembersTitle')}
                description={t('projectDetail.automationsMembersDesc')}
              />
            ) : (
              <EmptyState
                title={t('projectDetail.automationsNeedServiceTitle')}
                description={t('projectDetail.automationsNeedServiceDesc')}
              />
            )}
          </section>
        )}

        {!projectSettingsOpen && workspaceTab === 'settings' && canManage && (
          <SettingsPanel
            service={activeService}
            models={grantedModels}
            modelState={modelPolicyState}
            updating={updateService.isPending}
            onDefaultModelChange={updateDefaultModel}
            onRetryModels={() => void projectModels.refetch()}
          />
        )}
      </ProjectWorkspaceShell>

      {kanbanOpen && hasBoardLinks && (
        <KanbanBoardModal
          projectId={projectId}
          links={boardLinks.data!}
          onClose={() => setKanbanOpen(false)}
        />
      )}
    </>
  );
}
