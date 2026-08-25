import {
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  CaretDown,
  Check,
  Cloud,
  GitBranch,
  GitPullRequest,
  Plus,
  TerminalWindow,
} from '@phosphor-icons/react';
import { useEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from 'react';
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
  useStartAccountTask,
  useSystem,
  useUpdateService,
} from '../api/queries';
import type { AccountRepositoryTarget, Service } from '../api/types';
import { ApiError } from '../api/client';
import { useOptionalAuth } from '../auth/AuthProvider';
import { Button } from '../components/Button';
import { AccountHeader } from '../components/AccountHeader';
import { Select } from '../components/Select';
import { useToast } from '../components/Toast';
import { RepositoryAutomationsPanel } from '../project-workspace/ProjectAutomationsPanel';
import { RunActivityList } from '../project-workspace/RunActivityList';
import { SettingsPanel } from '../project-workspace/SettingsPanel';
import { KanbanBoardModal } from '../pages/KanbanBoardModal';
import { RepositoryUsagePanel } from '../pages/RepositoryUsagePanel';
import { RemoteComposer } from './RemoteComposer';
import styles from './WorkHomePage.module.css';

type WorkspaceTab = 'tasks' | 'board' | 'reviews' | 'automations' | 'usage' | 'settings';
type ContextKind = 'repository' | 'remote';
const LAST_ACCOUNT_MODEL_KEY = 'jcloud.last-model.v1:';

function repositoryKey(target: AccountRepositoryTarget): string {
  return `${target.provider}:${target.provider_repo_id}`;
}

function storedModel(accountId: string): string {
  try { return window.localStorage.getItem(LAST_ACCOUNT_MODEL_KEY + (accountId || 'session')) ?? ''; }
  catch { return ''; }
}

function rememberModel(accountId: string, modelId: string) {
  try { window.localStorage.setItem(LAST_ACCOUNT_MODEL_KEY + (accountId || 'session'), modelId); }
  catch { /* The current in-memory selection remains usable. */ }
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

export function WorkHomePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const toast = useToast();
  const auth = useOptionalAuth();
  const catalog = useAccountRepositories();
  const models = useAccountModels();
  const repositories = useRepositories();
  const devices = useDevices();
  const startTask = useStartAccountTask();
  const menuRef = useRef<HTMLDivElement>(null);
  const [prompt, setPrompt] = useState('');
  const [selectedRepository, setSelectedRepository] = useState(searchParams.get('repository') ?? '');
  const [selectedModel, setSelectedModel] = useState('');
  const [contextKind, setContextKind] = useState<ContextKind>('repository');
  const [selectedDeviceId, setSelectedDeviceId] = useState('');
  const [contextOpen, setContextOpen] = useState(false);
  const [remotePicker, setRemotePicker] = useState(false);
  const [tab, setTab] = useState<WorkspaceTab>((searchParams.get('tab') as WorkspaceTab) || 'tasks');

  const targets = catalog.data?.repositories ?? [];
  const activeTarget = targets.find((target) => repositoryKey(target) === selectedRepository);
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
    if (activeTarget || targets.length === 0) return;
    const requested = targets.find((target) => target.repository_id === searchParams.get('repository'));
    const first = requested ?? targets.find((target) => target.execution_available !== false) ?? targets[0];
    if (first) setSelectedRepository(repositoryKey(first));
  }, [activeTarget, searchParams, targets]);

  useEffect(() => {
    if (!models.data?.length) { setSelectedModel(''); return; }
    const remembered = storedModel(accountId);
    const fallback = models.data[0]?.id ?? '';
    setSelectedModel(models.data.some((model) => model.id === remembered) ? remembered : fallback);
  }, [accountId, models.data]);

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
    setSelectedRepository(repositoryKey(target));
    setSelectedDeviceId('');
    setContextOpen(false);
    setRemotePicker(false);
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
    const next = new URLSearchParams(searchParams);
    next.delete('repository');
    next.set('remote', device.id);
    setSearchParams(next, { replace: true });
  };

  const canStart = contextKind === 'repository' && !!prompt.trim() && !!activeTarget
    && activeTarget.execution_available !== false && !!selectedModel && !startTask.isPending;

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!canStart || !activeTarget) return;
    rememberModel(accountId, selectedModel);
    startTask.mutate({
      provider: activeTarget.provider,
      provider_repo_id: activeTarget.provider_repo_id,
      prompt: prompt.trim(),
      base_branch: activeTarget.default_branch,
      model_id: selectedModel,
      session: true,
    }, {
      onSuccess: ({ run }) => navigate(`/runs/${encodeURIComponent(run.id)}`),
      onError: (error) => toast.push({ kind: 'error', message: error instanceof ApiError ? error.message : t('repositories.startFailed') }),
    });
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

        {contextKind === 'remote' && selectedDevice ? (
          <section className={styles.remoteSurface}>
            <ContextPicker
              activeTarget={activeTarget}
              selectedDevice={selectedDevice}
              targets={targets}
              devices={onlineDevices}
              open={contextOpen}
              remotePicker={remotePicker}
              menuRef={menuRef}
              onToggle={() => setContextOpen((open) => !open)}
              onRemoteOpen={() => onlineDevices.length === 0 ? navigate('/devices/guide') : setRemotePicker(true)}
              onRemoteBack={() => setRemotePicker(false)}
              onSelectRepository={selectRepository}
              onSelectDevice={selectDevice}
            />
            <RemoteComposer device={selectedDevice} />
          </section>
        ) : (
          <form className={styles.composer} onSubmit={submit} noValidate>
            <ContextPicker
              activeTarget={activeTarget}
              targets={targets}
              devices={onlineDevices}
              open={contextOpen}
              remotePicker={remotePicker}
              menuRef={menuRef}
              onToggle={() => setContextOpen((open) => !open)}
              onRemoteOpen={() => onlineDevices.length === 0 ? navigate('/devices/guide') : setRemotePicker(true)}
              onRemoteBack={() => setRemotePicker(false)}
              onSelectRepository={selectRepository}
              onSelectDevice={selectDevice}
            />
            <textarea aria-label={t('repositories.taskAria')} placeholder={t('repositories.taskPlaceholder')} value={prompt} onChange={(event) => setPrompt(event.target.value)} rows={3} />
            <div className={styles.composerBar}>
              <div className={styles.composerTools}><button type="button" className={styles.iconButton} aria-label="Add attachment"><Plus size={18} /></button><span className={styles.branch}><GitBranch size={15} />{activeTarget?.default_branch ?? '—'}</span></div>
              <div className={styles.composerTools}>
                <Select aria-label={t('taskComposer.modelAria')} value={selectedModel} onChange={(id) => { setSelectedModel(id); rememberModel(accountId, id); }} disabled={models.isLoading || !models.data?.length} placeholder={t('repositories.modelRequired')} options={(models.data ?? []).map((model) => ({ value: model.id, label: model.name }))} />
                <Button type="submit" variant="primary" className={styles.sendButton} disabled={!canStart} loading={startTask.isPending} aria-label={t('repositories.startTask')}><ArrowUp size={19} weight="bold" /></Button>
              </div>
            </div>
          </form>
        )}

        {contextKind === 'repository' && (catalog.isError || (!catalog.isLoading && targets.length === 0) || (!models.isLoading && !models.data?.length) || activeTarget?.execution_available === false) && (
          <div className={styles.blocker} role="status">{catalog.isError ? t('repositories.accountLoadError') : targets.length === 0 ? t('repositories.linkAccount') : !models.data?.length ? t('repositories.authorizeModel') : activeTarget?.execution_error}</div>
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
  onToggle, onRemoteOpen, onRemoteBack, onSelectRepository, onSelectDevice,
}: {
  activeTarget?: AccountRepositoryTarget;
  selectedDevice?: Device;
  targets: AccountRepositoryTarget[];
  devices: Device[];
  open: boolean;
  remotePicker: boolean;
  menuRef: RefObject<HTMLDivElement>;
  onToggle: () => void;
  onRemoteOpen: () => void;
  onRemoteBack: () => void;
  onSelectRepository: (target: AccountRepositoryTarget) => void;
  onSelectDevice: (device: Device) => void;
}) {
  const [query, setQuery] = useState('');
  const visibleTargets = targets.filter((target) => target.full_name.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));
  return <div className={styles.context} ref={menuRef}>
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
        <div className={styles.menuSearch}><input type="search" aria-label="Search repositories" placeholder="Search repositories…" value={query} onChange={(event) => setQuery(event.target.value)} /></div>
        <div className={styles.menuGroup}><span className={styles.menuLabel}>Git repositories</span>{visibleTargets.map((target) => <button type="button" className={styles.menuOption} key={repositoryKey(target)} onClick={() => onSelectRepository(target)}><span className={styles.contextMark}><GitBranch size={15} /></span><span><strong>{target.full_name}</strong><small>{providerLabel(target.provider)} · {target.default_branch}</small></span>{target === activeTarget && <Check size={14} />}</button>)}{visibleTargets.length === 0 && <span className={styles.menuEmpty}>No repositories match “{query}”.</span>}</div>
        <footer className={styles.menuFooter}><button type="button" onClick={onRemoteOpen} aria-label={`Remote connection ${devices.length} online`}><span className={styles.contextMark}><TerminalWindow size={15} /></span><span><strong>Remote connection</strong><small>{devices.length} jcode devices online</small></span><ArrowRight size={15} /></button></footer>
      </>}
    </div>}
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
