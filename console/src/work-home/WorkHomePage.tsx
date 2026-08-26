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
import { RemoteComposer } from './RemoteComposer';
import styles from './WorkHomePage.module.css';

type WorkspaceTab = 'tasks' | 'board' | 'reviews' | 'automations' | 'usage' | 'settings';
type ContextKind = 'repository' | 'remote';

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
  const [activeTarget, setActiveTarget] = useState<AccountRepositoryTarget>();
  const [contextKind, setContextKind] = useState<ContextKind>('repository');
  const [selectedDeviceId, setSelectedDeviceId] = useState('');
  const [contextOpen, setContextOpen] = useState(false);
  const [remotePicker, setRemotePicker] = useState(false);
  const [repositoryQuery, setRepositoryQuery] = useState('');
  const [tab, setTab] = useState<WorkspaceTab>((searchParams.get('tab') as WorkspaceTab) || 'tasks');

  const normalizedRepositoryQuery = repositoryQuery.trim();
  const debouncedRepositoryQuery = useDebouncedValue(normalizedRepositoryQuery, 250);
  const searchCatalog = useAccountRepositories(
    debouncedRepositoryQuery,
    20,
    contextOpen && !remotePicker && debouncedRepositoryQuery.length > 0,
  );
  const targets = catalog.data?.repositories ?? [];
  const pickerTargets = debouncedRepositoryQuery
    ? searchCatalog.data?.repositories ?? []
    : targets;
  const repositorySearchLoading = normalizedRepositoryQuery !== debouncedRepositoryQuery
    || (debouncedRepositoryQuery.length > 0 && searchCatalog.isPending);
  const repositorySearchError = debouncedRepositoryQuery.length > 0 && searchCatalog.isError;
  const unavailableSource = catalog.data?.sources.find((source) => source.status === 'unavailable'
    && (targets.length === 0 || source.provider === activeTarget?.provider));
  const activeRepository = matchingRepository(activeTarget, repositories.data ?? []);
  const selectedDevice = devices.data?.find((device) => device.id === selectedDeviceId);
  const onlineDevices = (devices.data ?? []).filter((device) => device.online);
  const accountId = auth?.me?.user.id ?? '';

  useEffect(() => {
    const requestedDevice = searchParams.get('remote');
    if (!requestedDevice || !devices.data) return;
    const device = devices.data.find((candidate) => candidate.id === requestedDevice);
    if (!device) return;
    setContextKind('remote');
    setSelectedDeviceId(device.id);
  }, [devices.data, searchParams]);

  useEffect(() => {
    setActiveTarget((current) => {
      if (targets.length === 0) {
        return catalog.data?.sources.some((source) => source.status === 'unavailable') ? undefined : current;
      }
      if (current) {
        return targets.find((target) => repositoryKey(target) === repositoryKey(current)) ?? current;
      }
      const requested = targets.find((target) => target.repository_id === searchParams.get('repository'));
      return requested ?? targets.find((target) => target.execution_available !== false) ?? targets[0];
    });
  }, [catalog.data?.sources, searchParams, targets]);

  useEffect(() => {
    const close = (event: MouseEvent) => {
      const node = event.target as Node;
      if (!menuRef.current?.contains(node)) setContextOpen(false);
    };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, []);

  const selectRepository = (target: AccountRepositoryTarget) => {
    setContextKind('repository');
    setActiveTarget(target);
    setSelectedDeviceId('');
    setContextOpen(false);
    setRemotePicker(false);
    setRepositoryQuery('');
    const next = new URLSearchParams(searchParams);
    next.delete('remote');
    if (target.repository_id) next.set('repository', target.repository_id); else next.delete('repository');
    next.set('tab', tab);
    setSearchParams(next, { replace: true });
  };

  const selectDevice = (device: Device) => {
    setContextKind('remote');
    setSelectedDeviceId(device.id);
    setContextOpen(false);
    setRemotePicker(false);
    setRepositoryQuery('');
    const next = new URLSearchParams(searchParams);
    next.delete('repository');
    next.set('remote', device.id);
    setSearchParams(next, { replace: true });
  };

  return (
    <div className={styles.page} data-testid="work-home">
      <AccountHeader />

      <main className={styles.main}>
        <section className={styles.hero}>
          <span className={styles.eyebrow}>{t('repositories.composerEyebrow')}</span>
          <h1>{t('repositories.composerTitle')}</h1>
          <p>{contextKind === 'remote' ? 'Continue on an online jcode device with the same conversation UI.' : t('repositories.composerDescription')}</p>
        </section>

        {contextKind === 'repository' && catalog.isPending && !catalog.data ? <WorkHomeSkeleton /> : contextKind === 'remote' && selectedDevice ? (
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
        ) : activeTarget ? (
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
            onTabChange={(nextTab) => { setTab(nextTab); const next = new URLSearchParams(searchParams); next.set('tab', nextTab); setSearchParams(next, { replace: true }); }}
          />
        )}
      </main>
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
  return <div className={[styles.context, open ? styles.contextOpen : ''].filter(Boolean).join(' ')} ref={menuRef}>
    <button type="button" className={styles.contextButton} onClick={onToggle} aria-expanded={open} aria-label={`Repository or Remote context ${selectedDevice?.name ?? activeTarget?.full_name ?? ''}`}>
      <span className={styles.contextMark}>{selectedDevice ? <TerminalWindow size={15} /> : <GitBranch size={15} />}</span>
      <strong>{selectedDevice?.name ?? activeTarget?.full_name ?? 'Repository'}</strong><CaretDown size={12} />
    </button>
    {open && <div className={styles.contextMenu}>
      {remotePicker ? <>
        <header className={styles.menuHead}><button type="button" onClick={onRemoteBack} aria-label="Back to repositories"><ArrowLeft size={15} /></button><span><strong>Remote connection</strong><small>Choose an online jcode device</small></span></header>
        <div className={styles.menuGroup}><span className={styles.menuLabel}>Online devices</span>{devices.map((device) => <button type="button" className={styles.menuOption} key={device.id} onClick={() => onSelectDevice(device)}><span className={styles.contextMark}><TerminalWindow size={15} /></span><span><strong>{device.name}</strong><small>{device.platform || 'jcode device'} · online</small></span><span className={styles.onlineDot} /></button>)}</div>
        <footer className={styles.menuFooter}><Link to="/devices/guide"><span className={styles.contextMark}><Plus size={15} /></span><span><strong>Connect new device</strong><small>Run jcode login and complete encrypted pairing</small></span><ArrowRight size={15} /></Link></footer>
      </> : <>
        <div className={styles.menuSearch}><input type="search" aria-label="Search repositories" placeholder="Search repositories…" value={query} onChange={(event) => onQueryChange(event.target.value)} /></div>
        <div className={styles.menuGroup}><span className={styles.menuLabel}>Git repositories</span>{searchLoading ? <RepositorySearchSkeleton /> : searchError ? <span className={styles.menuEmpty}>Repository search failed. Change the query or try again.</span> : <>{targets.map((target) => <button type="button" className={styles.menuOption} key={repositoryKey(target)} onClick={() => onSelectRepository(target)}><span className={styles.contextMark}><GitBranch size={15} /></span><span><strong>{target.full_name}</strong><small>{providerLabel(target.provider)} · {target.default_branch}</small></span>{repositoryKey(target) === (activeTarget ? repositoryKey(activeTarget) : '') && <Check size={14} />}</button>)}{targets.length === 0 && <span className={styles.menuEmpty}>No repositories match “{query}”.</span>}</>}</div>
        <footer className={styles.menuFooter}><button type="button" onClick={onRemoteOpen} aria-label={`Remote connection ${devices.length} online`}><span className={styles.contextMark}><TerminalWindow size={15} /></span><span><strong>Remote connection</strong><small>{devices.length} jcode devices online</small></span><ArrowRight size={15} /></button></footer>
      </>}
    </div>}
  </div>;
}

function RepositorySearchSkeleton() {
  return <div className={styles.menuSkeleton} data-testid="repository-search-skeleton" role="status" aria-label="Searching repositories">
    {[0, 1, 2, 3].map((item) => <div className={styles.menuSkeletonRow} key={item}><span className={styles.skeletonMark} /><span><i /><i /></span></div>)}
  </div>;
}

function WorkHomeSkeleton() {
  return <div className={styles.workHomeSkeleton} data-testid="work-home-skeleton" role="status" aria-label="Loading repositories">
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
  const tabs: Array<[WorkspaceTab, string]> = [['tasks', 'Tasks'], ['board', 'Board'], ['reviews', 'Reviews'], ['automations', 'Automations'], ['usage', 'Usage'], ['settings', 'Settings']];

  return <section className={styles.workspace} aria-label={`${target.full_name} Repository workspace`}>
    <header className={styles.workspaceHead}><span className={styles.workspaceIdentity}><span className={styles.contextMark}><GitBranch size={15} /></span><strong>{target.full_name}</strong><small>{providerLabel(target.provider)} · {target.default_branch}</small></span><span className={styles.execution}><Cloud size={14} />Cloud runner</span></header>
    <nav className={styles.tabs} role="tablist" aria-label="Repository workspace sections">{tabs.map(([id, label]) => <button key={id} type="button" role="tab" aria-selected={tab === id} className={tab === id ? styles.activeTab : ''} onClick={() => onTabChange(id)}>{label}</button>)}</nav>
    <div className={styles.panel} role="tabpanel">
      {!repository && <div className={styles.workspaceNotice}><strong>Repository workspace is ready on first use</strong><span>Start the first task to materialize task history, Board configuration, reviews, usage, and settings for this Repository.</span></div>}
      {tab === 'tasks' && <RunActivityList runs={scopedRuns} isLoading={!!repository && runs.isLoading} error={runs.isError ? runs.error : undefined} onRetry={() => void runs.refetch()} filter="all" onFilterChange={() => {}} canRun showFilters={false} emptyTitle="No tasks in this Repository" emptyDescription="Describe a task above to start the first conversation." />}
      {tab === 'board' && <section className={styles.boardPanel}><header className={styles.panelHead}><span><h2>Board</h2><p>Connect an existing Board and let Cards start this Repository's agent.</p></span></header>{repository ? <KanbanBoardModal projectId={projectId} serviceId={repository.id} links={links} canManage canRun embedded /> : <div className={styles.emptyPanel}>Start the first task before connecting an existing JType Board.</div>}</section>}
      {tab === 'reviews' && <section><header className={styles.panelHead}><span><h2>Code reviews</h2><p>Independent review runs for this Repository.</p></span><Link className={styles.primaryLink} to={`/code-reviews?repository=${encodeURIComponent(repository?.id ?? '')}`}><GitPullRequest size={14} />Create code review</Link></header><RunActivityList runs={reviews} isLoading={!!repository && runs.isLoading} error={runs.isError ? runs.error : undefined} onRetry={() => void runs.refetch()} filter="reviews" onFilterChange={() => {}} canRun showFilters={false} emptyTitle="No code reviews yet" emptyDescription="Create a review without mixing it into implementation tasks." /></section>}
      {tab === 'automations' && (repository ? <RepositoryAutomationsPanel projectId={projectId} repository={repository} canManage={(project.data?.role ?? 'owner') === 'owner'} /> : <div className={styles.emptyPanel}>Start the first task before configuring Repository automations.</div>)}
      {tab === 'usage' && (repository ? <RepositoryUsagePanel repositoryId={repository.id} /> : <div className={styles.emptyPanel}>Usage will appear after this Repository runs its first task.</div>)}
      {tab === 'settings' && (repository ? <SettingsPanel service={repository} models={projectModels.data?.models ?? []} modelState={projectModels.isError ? 'unverified' : projectModels.isLoading ? 'loading' : 'ready'} updating={updateService.isPending} onDefaultModelChange={(id) => updateService.mutate({ serviceId: repository.id, input: { default_model_id: id } })} onPRReadyPolicyChange={(policy) => updateService.mutate({ serviceId: repository.id, input: { pr_ready_policy: policy } })} runnerProfiles={system.data?.runner.profiles ?? []} onRunnerProfileChange={(profile) => updateService.mutate({ serviceId: repository.id, input: { runner_profile: profile } })} onRetryModels={() => void projectModels.refetch()} /> : <div className={styles.emptyPanel}>Repository-specific settings become available after first use. Account model access stays in Personal settings.</div>)}
    </div>
  </section>;
}
