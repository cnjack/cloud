import {
  ArrowLeft,
  ArrowRight,
  CaretDown,
  Check,
  Cloud,
  GitBranch,
  GitPullRequest,
  Plus,
  TerminalWindow,
} from '@phosphor-icons/react';
import { useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useDevices, type Device } from '@jcloud/device-ui';
import {
  useAccountModels,
  useAccountRepositories,
  useProject,
  useProjectBoardLinks,
  useProjectModels,
  useRepositories,
  useRuns,
  useSystem,
  useUpdateService,
} from '../api/queries';
import type { AccountRepositoryTarget, Service } from '../api/types';
import { useOptionalAuth } from '../auth/AuthProvider';
import { AccountHeader } from '../components/AccountHeader';
import { RepositoryAutomationsPanel } from '../project-workspace/ProjectAutomationsPanel';
import { RunActivityList } from '../project-workspace/RunActivityList';
import { SettingsPanel } from '../project-workspace/SettingsPanel';
import { KanbanBoardModal } from '../pages/KanbanBoardModal';
import { RepositoryUsagePanel } from '../pages/RepositoryUsagePanel';
import { AccountRepositoryComposer } from './AccountRepositoryComposer';
import { useAccountNavigation } from './useAccountNavigation';
import { ConversationRail } from './ConversationRail';
import { RemoteComposer } from './RemoteComposer';
import { repositoryWorkspacePath, workspaceTab, type WorkspaceTab } from './workspaceNavigation';
import styles from './WorkHomePage.module.css';

const OPEN_CONTEXT_Z_INDEX = 'calc(var(--z-dropdown, 900) + 1)';

function repositoryKey(target: AccountRepositoryTarget): string {
  return `${target.provider}:${target.provider_repo_id}`;
}

function providerLabel(provider: string): string {
  if (provider === 'github') return 'GitHub';
  if (provider === 'gitlab') return 'GitLab';
  if (provider === 'gitea') return 'Gitea';
  return provider;
}

function matchingRepository(target: AccountRepositoryTarget | undefined, repositories: Service[]): Service | undefined {
  if (!target) return undefined;
  if (target.repository_id) {
    const exact = repositories.find((repository) => repository.id === target.repository_id);
    if (exact) return exact;
  }
  return repositories.find((repository) => repository.provider === target.provider
    && String(repository.provider_repo_id ?? '') === target.provider_repo_id);
}

function useDebouncedValue(value: string, delayMs: number): string {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timeout = window.setTimeout(() => setDebounced(value), delayMs);
    return () => window.clearTimeout(timeout);
  }, [delayMs, value]);
  return debounced;
}

export function WorkHomePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const auth = useOptionalAuth();
  const catalog = useAccountRepositories('', 12);
  const models = useAccountModels();
  const repositories = useRepositories();
  const devices = useDevices();
  const menuRef = useRef<HTMLDivElement>(null);
  const [chosenTarget, setChosenTarget] = useState<AccountRepositoryTarget>();
  const selectedDeviceId = searchParams.get('remote') || '';
  const contextKind = selectedDeviceId ? 'remote' : 'repository';
  const [contextOpen, setContextOpen] = useState(searchParams.get('picker') === '1');
  const [remotePicker, setRemotePicker] = useState(false);
  const [repositoryQuery, setRepositoryQuery] = useState('');
  const tab = workspaceTab(searchParams.get('tab'));
  const [conversationRailCollapsed, setConversationRailCollapsed] = useState(false);

  const normalizedRepositoryQuery = repositoryQuery.trim();
  const debouncedRepositoryQuery = useDebouncedValue(normalizedRepositoryQuery, 250);
  const searchCatalog = useAccountRepositories(
    debouncedRepositoryQuery,
    20,
    contextOpen && !remotePicker && debouncedRepositoryQuery.length > 0,
  );
  const targets = catalog.data?.repositories ?? [];
  const retainedChoice = targets.length === 0 && catalog.data?.sources.some((source) => source.status === 'unavailable')
    ? undefined : chosenTarget;
  const requestedRepositoryId = searchParams.get('repository') || '';
  const requestedTargetKey = searchParams.get('target') || '';
  const requestedTargetName = searchParams.get('repositoryName') || '';
  const hasRequestedTarget = !!(requestedRepositoryId || requestedTargetKey);
  const requestedRepository = repositories.data?.find((repository) => repository.id === requestedRepositoryId);
  const matchesRequested = (target: AccountRepositoryTarget) => hasRequestedTarget && (
    (!requestedRepositoryId && repositoryKey(target) === requestedTargetKey) ||
    target.repository_id === requestedRepositoryId || (requestedRepository != null
      && target.provider === requestedRepository.provider
      && target.provider_repo_id === String(requestedRepository.provider_repo_id))
  );
  const requestedFromCatalog = targets.find(matchesRequested);
  const requestedFromChoice = retainedChoice && matchesRequested(retainedChoice) ? retainedChoice : undefined;
  const requestedName = requestedRepository?.repo_owner_name || requestedTargetName;
  const lookupRequested = !!requestedName && !catalog.isPending && !requestedFromCatalog && !requestedFromChoice;
  const requestedCatalog = useAccountRepositories(requestedName, 20, lookupRequested);
  // The route wins over retained component state, including browser history.
  // Resolve older repositories outside the bounded first catalog page by name,
  // then require the stable provider id to match before rendering their workspace.
  const activeTarget = hasRequestedTarget
    ? requestedFromCatalog ?? requestedFromChoice ?? requestedCatalog.data?.repositories.find(matchesRequested)
    : targets.find((target) => retainedChoice && repositoryKey(target) === repositoryKey(retainedChoice))
      ?? retainedChoice ?? targets.find((target) => target.execution_available !== false) ?? targets[0];
  const requestedPending = hasRequestedTarget && (repositories.isPending
    || (!activeTarget && (catalog.isPending || (lookupRequested && requestedCatalog.isPending))));
  const requestedUnavailable = hasRequestedTarget && !activeTarget && !requestedPending;
  const pickerTargets = debouncedRepositoryQuery
    ? searchCatalog.data?.repositories ?? []
    : targets;
  const repositorySearchLoading = normalizedRepositoryQuery !== debouncedRepositoryQuery
    || (debouncedRepositoryQuery.length > 0 && searchCatalog.isPending);
  const repositorySearchError = debouncedRepositoryQuery.length > 0 && searchCatalog.isError;
  const unavailableSource = catalog.data?.sources.find((source) => source.status === 'unavailable'
    && (targets.length === 0 || source.provider === activeTarget?.provider));
  const activeRepository = matchingRepository(activeTarget, repositories.data ?? []);
  const navigation = useAccountNavigation();
  const selectedDevice = devices.data?.find((device) => device.id === selectedDeviceId);
  const onlineDevices = (devices.data ?? []).filter((device) => device.online);
  const accountId = auth?.me?.user.id ?? '';

  useEffect(() => {
    const close = (event: MouseEvent) => {
      const node = event.target as Node;
      if (!menuRef.current?.contains(node)) setContextOpen(false);
    };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, []);

  useEffect(() => { if (searchParams.get('picker') === '1') setContextOpen(true); }, [searchParams]);

  const selectRepository = (target: AccountRepositoryTarget) => {
    setChosenTarget(target);
    setContextOpen(false);
    setRemotePicker(false);
    setRepositoryQuery('');
    const next = new URLSearchParams(searchParams);
    next.delete('remote');
    next.delete('picker');
    const repositoryId = target.repository_id ?? matchingRepository(target, repositories.data ?? [])?.id;
    if (repositoryId) { next.set('repository', repositoryId); next.delete('target'); next.delete('repositoryName'); }
    else { next.delete('repository'); next.set('target', repositoryKey(target)); next.set('repositoryName', target.full_name); }
    next.set('tab', tab);
    if (next.toString() !== searchParams.toString()) setSearchParams(next);
  };

  const selectDevice = (device: Device) => {
    setContextOpen(false);
    setRemotePicker(false);
    setRepositoryQuery('');
    navigate(`/devices/${encodeURIComponent(device.id)}`);
  };

  const sectionLabels: Record<WorkspaceTab, string> = {
    tasks: t('repositories.workspaceTasks'), board: t('repositories.workspaceBoard'),
    reviews: t('repositories.workspaceReviews'), automations: t('repositories.workspaceAutomations'),
    usage: t('repositories.workspaceUsage'), settings: t('repositories.workspaceSettings'),
  };
  const workspaceName = contextKind === 'remote'
    ? selectedDevice?.name
    : activeTarget?.full_name ?? requestedRepository?.repo_owner_name;

  return (
    <div className={styles.page} data-testid="work-home" data-rail-collapsed={conversationRailCollapsed || undefined}>
      <ConversationRail
        {...navigation}
        collapsed={conversationRailCollapsed}
        onCollapsedChange={setConversationRailCollapsed}
        activeRepositoryId={contextKind === 'repository' ? activeRepository?.id ?? requestedRepositoryId : undefined}
      />

      <section className={styles.surface} data-testid="work-home-surface">
        <AccountHeader
          sectionTitle={contextKind === 'remote' ? t('repositories.remoteConnection') : workspaceName ? sectionLabels[tab] : t('repositories.conversationRailWorkHome')}
          workspace={workspaceName ? { name: workspaceName, kind: contextKind,
            href: contextKind === 'repository' && activeRepository ? repositoryWorkspacePath(activeRepository.id) : undefined } : undefined}
        />

        <main className={styles.main}>
          <div className={styles.mainInner} data-workspace-tab={contextKind === 'repository' ? tab : undefined}>
        <section className={styles.hero}>
          <span className={styles.eyebrow}>{t('repositories.composerEyebrow')}</span>
          <h1>{t('cloudRefresh.homeTitle')}</h1>
          <p>{contextKind === 'remote' ? t('repositories.remoteComposerDescription') : t('cloudRefresh.homeDescription')}</p>
        </section>

        {contextKind === 'repository' && ((catalog.isPending && !catalog.data) || requestedPending) ? <WorkHomeSkeleton /> : contextKind === 'remote' && selectedDevice ? (
          <section className={styles.remoteSurface}>
            <RemoteComposer device={selectedDevice} contextHeader={<ContextPicker
                activeTarget={activeTarget}
                selectedDevice={selectedDevice}
                targets={pickerTargets}
                devices={onlineDevices}
                open={contextOpen}
                remotePicker={remotePicker}
                query={repositoryQuery}
                searchLoading={repositorySearchLoading}
                searchError={repositorySearchError}
                menuRef={menuRef}
                onToggle={() => setContextOpen((open) => !open)}
                onQueryChange={setRepositoryQuery}
                onRemoteOpen={() => { setRepositoryQuery(''); onlineDevices.length === 0 ? navigate('/devices/guide') : setRemotePicker(true); }}
                onRemoteBack={() => setRemotePicker(false)}
                onSelectRepository={selectRepository}
                onSelectDevice={selectDevice}
              />} />
          </section>
        ) : contextKind === 'repository' && activeTarget ? (
          <AccountRepositoryComposer
            target={activeTarget}
            models={models.data ?? []}
            modelsLoading={models.isLoading}
            accountId={accountId}
            contextPicker={<ContextPicker
              activeTarget={activeTarget}
              targets={pickerTargets}
              devices={onlineDevices}
              open={contextOpen}
              remotePicker={remotePicker}
              query={repositoryQuery}
              searchLoading={repositorySearchLoading}
              searchError={repositorySearchError}
              menuRef={menuRef}
              onToggle={() => setContextOpen((open) => !open)}
              onQueryChange={setRepositoryQuery}
              onRemoteOpen={() => { setRepositoryQuery(''); onlineDevices.length === 0 ? navigate('/devices/guide') : setRemotePicker(true); }}
              onRemoteBack={() => setRemotePicker(false)}
              onSelectRepository={selectRepository}
              onSelectDevice={selectDevice}
            />}
          />
        ) : null}

        {contextKind === 'repository' && !activeTarget && !catalog.isPending && <ContextPicker
          targets={pickerTargets} devices={onlineDevices} open={contextOpen} remotePicker={remotePicker}
          query={repositoryQuery} searchLoading={repositorySearchLoading} searchError={repositorySearchError}
          menuRef={menuRef} onToggle={() => setContextOpen(open => !open)} onQueryChange={setRepositoryQuery}
          onRemoteOpen={() => { onlineDevices.length ? setRemotePicker(true) : navigate('/devices/guide'); }}
          onRemoteBack={() => setRemotePicker(false)} onSelectRepository={selectRepository} onSelectDevice={selectDevice}
        />}

        {contextKind === 'repository' && requestedUnavailable && <div className={styles.blocker} role="alert" data-testid="workspace-target-unavailable">
          <span>{t('repositories.navigationUnavailable')}</span>
          <Link className={styles.blockerAction} to="/account/settings?section=connections">{t('repositories.reviewGitAccountAccess')}</Link>
          <Link className={styles.blockerAction} to="/repositories">{t('repositories.chooseRepository')}</Link>
        </div>}
        {contextKind === 'remote' && !selectedDevice && !devices.isLoading && <div className={styles.blocker} role="alert">
          <span>{t('repositories.remoteUnavailable')}</span>
          <Link className={styles.blockerAction} to="/devices/guide">{t('repositories.connectNewDevice')}</Link>
        </div>}

        {contextKind === 'repository' && (catalog.isError || unavailableSource || (!catalog.isLoading && targets.length === 0) || (!models.isLoading && !models.data?.length) || activeTarget?.execution_available === false) && (
          <div className={styles.blocker} role="status">
            <span>{catalog.isError
              ? t('repositories.accountLoadError')
              : unavailableSource?.message
                ?? (targets.length === 0
                  ? t('repositories.linkAccount')
                  : !models.data?.length
                    ? t('repositories.authorizeModel')
                    : activeTarget?.execution_error)}</span>
            {(catalog.isError || unavailableSource || targets.length === 0 || activeTarget?.execution_available === false) && (
              <Link className={styles.blockerAction} to="/account/settings?section=connections">{t('repositories.reviewGitAccountAccess')}</Link>
            )}
            {!models.isLoading && !models.data?.length && (
              <Link className={styles.blockerAction} to="/account/settings?section=models">{t('repositories.reviewModelAccess')}</Link>
            )}
          </div>
        )}

        {contextKind === 'repository' && activeTarget && (
          <RepositoryWorkspace
            target={activeTarget}
            repository={activeRepository}
            tab={tab}
            onTabChange={(nextTab) => { if (nextTab === tab) return; const next = new URLSearchParams(searchParams); next.set('tab', nextTab); setSearchParams(next); }}
          />
        )}
          </div>
        </main>
      </section>
    </div>
  );
}

function ContextPicker({
  activeTarget, selectedDevice, targets, devices, open, remotePicker, menuRef,
  query, searchLoading, searchError,
  onToggle, onQueryChange, onRemoteOpen, onRemoteBack, onSelectRepository, onSelectDevice,
}: {
  activeTarget?: AccountRepositoryTarget;
  selectedDevice?: Device;
  targets: AccountRepositoryTarget[];
  devices: Device[];
  open: boolean;
  remotePicker: boolean;
  query: string;
  searchLoading: boolean;
  searchError: boolean;
  menuRef: RefObject<HTMLDivElement>;
  onToggle: () => void;
  onQueryChange: (query: string) => void;
  onRemoteOpen: () => void;
  onRemoteBack: () => void;
  onSelectRepository: (target: AccountRepositoryTarget) => void;
  onSelectDevice: (device: Device) => void;
}) {
  const { t } = useTranslation();
  return <div className={styles.context} ref={menuRef} style={open ? { zIndex: OPEN_CONTEXT_Z_INDEX } : undefined}>
    <button type="button" className={styles.contextButton} onClick={onToggle} aria-expanded={open} aria-label={t('repositories.contextAria', { context: selectedDevice?.name ?? activeTarget?.full_name ?? '' })}>
      <span className={styles.contextMark}>{selectedDevice ? <TerminalWindow size={15} /> : <GitBranch size={15} />}</span>
      <strong>{selectedDevice?.name ?? activeTarget?.full_name ?? t('repositories.repositoryFallback')}</strong><CaretDown size={12} />
    </button>
    {open && <div className={styles.contextMenu}>
      {remotePicker ? <>
        <header className={styles.menuHead}><button type="button" onClick={onRemoteBack} aria-label={t('repositories.remoteBack')}><ArrowLeft size={15} /></button><span><strong>{t('repositories.remoteConnection')}</strong><small>{t('repositories.remoteChooseDevice')}</small></span></header>
        <div className={styles.menuGroup}><span className={styles.menuLabel}>{t('repositories.onlineDevices')}</span>{devices.map((device) => <button type="button" className={styles.menuOption} key={device.id} onClick={() => onSelectDevice(device)}><span className={styles.contextMark}><TerminalWindow size={15} /></span><span><strong>{device.name}</strong><small>{device.platform || t('repositories.jcodeDevice')} · {t('repositories.online')}</small></span><span className={styles.onlineDot} /></button>)}</div>
        <footer className={styles.menuFooter}><Link to="/devices/guide"><span className={styles.contextMark}><Plus size={15} /></span><span><strong>{t('repositories.connectNewDevice')}</strong><small>{t('repositories.connectNewDeviceDescription')}</small></span><ArrowRight size={15} /></Link></footer>
      </> : <>
        <div className={styles.menuSearch}><input type="search" aria-label={t('repositories.pickerSearchAria')} placeholder={t('repositories.pickerSearchPlaceholder')} value={query} onChange={(event) => onQueryChange(event.target.value)} /></div>
        <div className={styles.menuGroup}><span className={styles.menuLabel}>{t('repositories.gitRepositories')}</span>{searchLoading ? <RepositorySearchSkeleton /> : searchError ? <span className={styles.menuEmpty}>{t('repositories.pickerSearchError')}</span> : <>{targets.map((target) => <button type="button" className={styles.menuOption} key={repositoryKey(target)} onClick={() => onSelectRepository(target)}><span className={styles.contextMark}><GitBranch size={15} /></span><span><strong>{target.full_name}</strong><small>{providerLabel(target.provider)} · {target.default_branch}</small></span>{repositoryKey(target) === (activeTarget ? repositoryKey(activeTarget) : '') && <Check size={14} />}</button>)}{targets.length === 0 && <span className={styles.menuEmpty}>{t('repositories.pickerNoMatch', { query })}</span>}</>}</div>
        <footer className={styles.menuFooter}><button type="button" onClick={onRemoteOpen} aria-label={t('repositories.remoteConnectionAria', { count: devices.length })}><span className={styles.contextMark}><TerminalWindow size={15} /></span><span><strong>{t('repositories.remoteConnection')}</strong><small>{t('repositories.remoteOnlineCount', { count: devices.length })}</small></span><ArrowRight size={15} /></button></footer>
      </>}
    </div>}
  </div>;
}

function RepositorySearchSkeleton() {
  const { t } = useTranslation();
  return <div className={styles.menuSkeleton} data-testid="repository-search-skeleton" role="status" aria-label={t('repositories.searchingRepositories')}>
    {[0, 1, 2, 3].map((item) => <div className={styles.menuSkeletonRow} key={item}><span className={styles.skeletonMark} /><span><i /><i /></span></div>)}
  </div>;
}

function WorkHomeSkeleton() {
  const { t } = useTranslation();
  return <div className={styles.workHomeSkeleton} data-testid="work-home-skeleton" role="status" aria-label={t('repositories.accountLoading')}>
    <div className={styles.skeletonComposer}><span className={styles.skeletonPill} /><span className={styles.skeletonPrompt} /><span className={styles.skeletonControls} /></div>
    <div className={styles.skeletonWorkspace}><div className={styles.skeletonIdentity} /><div className={styles.skeletonTabs}>{[0, 1, 2, 3, 4].map((item) => <span key={item} />)}</div><div className={styles.skeletonPanel}>{[0, 1, 2].map((item) => <span key={item} />)}</div></div>
  </div>;
}

function RepositoryWorkspace({ target, repository, tab, onTabChange }: {
  target: AccountRepositoryTarget;
  repository?: Service;
  tab: WorkspaceTab;
  onTabChange: (tab: WorkspaceTab) => void;
}) {
  const { t } = useTranslation();
  const projectId = repository?.project_id ?? '';
  const project = useProject(projectId);
  const runs = useRuns(projectId);
  const boardLinks = useProjectBoardLinks(projectId, !!projectId);
  const projectModels = useProjectModels(projectId, !!projectId);
  const updateService = useUpdateService(projectId);
  const system = useSystem(!!projectId);
  const scopedRuns = useMemo(() => (runs.data ?? []).filter((run) => !repository || run.service_id === repository.id), [repository, runs.data]);
  const reviews = scopedRuns.filter((run) => run.kind === 'review');
  const links = (boardLinks.data ?? []).filter((link) => !repository || link.service_id === repository.id);
  const tabs: Array<[WorkspaceTab, string]> = [
    ['tasks', t('repositories.workspaceTasks')],
    ['board', t('repositories.workspaceBoard')],
    ['reviews', t('repositories.workspaceReviews')],
    ['automations', t('repositories.workspaceAutomations')],
    ['usage', t('repositories.workspaceUsage')],
    ['settings', t('repositories.workspaceSettings')],
  ];

  return <section className={styles.workspace} aria-label={t('repositories.workspaceAria', { repositoryName: target.full_name })}>
    <header className={styles.workspaceHead}><span className={styles.workspaceIdentity}><span className={styles.contextMark}><GitBranch size={15} /></span><strong>{target.full_name}</strong><small>{providerLabel(target.provider)} · {target.default_branch}</small></span><span className={styles.execution}><Cloud size={14} />{t('repositories.cloudRunner')}</span></header>
    <nav className={styles.tabs} role="tablist" aria-label={t('repositories.tabsAria')}>{tabs.map(([id, label]) => <button key={id} type="button" role="tab" aria-selected={tab === id} className={tab === id ? styles.activeTab : ''} onClick={() => onTabChange(id)}>{label}</button>)}</nav>
    <div className={styles.panel} role="tabpanel">
      {tab === 'tasks' && <RunActivityList runs={scopedRuns} isLoading={!!repository && runs.isLoading} error={runs.isError ? runs.error : undefined} onRetry={() => void runs.refetch()} filter="all" onFilterChange={() => {}} canRun showFilters={false} emptyTitle={t('repositories.noTasksTitle')} emptyDescription={t('repositories.noTasksDescription')} />}
      {tab === 'board' && <section className={styles.boardPanel}><header className={styles.panelHead}><span><h2>{t('repositories.workspaceBoard')}</h2><p>{t('repositories.boardDescription')}</p></span></header>{repository ? <KanbanBoardModal projectId={projectId} serviceId={repository.id} links={links} canManage canRun embedded /> : <RepositoryBoardDefault />}</section>}
      {tab === 'reviews' && <section><header className={styles.panelHead}><span><h2>{t('repositories.codeReviews')}</h2><p>{t('repositories.reviewsDescription')}</p></span><Link className={styles.primaryLink} to={repository ? `/code-reviews?repository=${encodeURIComponent(repository.id)}` : '/code-reviews'}><GitPullRequest size={14} />{t('codeReviewsPage.createAction')}</Link></header><RunActivityList runs={reviews} isLoading={!!repository && runs.isLoading} error={runs.isError ? runs.error : undefined} onRetry={() => void runs.refetch()} filter="reviews" onFilterChange={() => {}} canRun showFilters={false} emptyTitle={t('codeReviewsPage.emptyTitle')} emptyDescription={t('repositories.reviewsEmptyDescription')} /></section>}
      {tab === 'automations' && <RepositoryAutomationsPanel projectId={projectId} repository={repository} canManage={!!repository && (project.data?.role ?? 'owner') === 'owner'} />}
      {tab === 'usage' && <RepositoryUsagePanel repositoryId={repository?.id} />}
      {tab === 'settings' && <SettingsPanel service={repository} preview={!repository ? { name: target.full_name, source: target.full_name, provider: providerLabel(target.provider), defaultBranch: target.default_branch, gitMode: 'draft_pr' } : undefined} models={projectModels.data?.models ?? []} modelState={projectModels.isError ? 'unverified' : projectModels.isLoading ? 'loading' : 'ready'} updating={updateService.isPending} onDefaultModelChange={(id) => repository && updateService.mutate({ serviceId: repository.id, input: { default_model_id: id } })} onPRReadyPolicyChange={(policy) => repository && updateService.mutate({ serviceId: repository.id, input: { pr_ready_policy: policy } })} runnerProfiles={system.data?.runner.profiles ?? []} onRunnerProfileChange={(profile) => repository && updateService.mutate({ serviceId: repository.id, input: { runner_profile: profile } })} onRetryModels={() => void projectModels.refetch()} />}
    </div>
  </section>;
}

function RepositoryBoardDefault() {
  const { t } = useTranslation();
  return <div className={styles.boardDefault} data-testid="repository-board-default">
    <div className={styles.boardDefaultState}>
      <strong>{t('repositories.boardNotConnected')}</strong>
      <span>{t('repositories.boardNotConnectedDescription')}</span>
    </div>
    <div className={styles.boardDefaultColumns} aria-label={t('repositories.boardWorkflowAria')}>
      <article><span>{t('repositories.boardBacklog')}</span><small>{t('repositories.boardBacklogDescription')}</small></article>
      <span aria-hidden="true">→</span>
      <article><span>{t('repositories.boardAgentQueue')}</span><small>{t('repositories.boardAgentQueueDescription')}</small></article>
      <span aria-hidden="true">→</span>
      <article><span>{t('repositories.boardDoneOptional')}</span><small>{t('repositories.boardDoneDescription')}</small></article>
    </div>
  </div>;
}
