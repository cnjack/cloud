import { GitBranch } from '@phosphor-icons/react';
import { buildProductComposerStrings } from '@jcloud/device-ui';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { RuntimeProvider } from 'jcode-ui';
import { ChatInput, type AgentMode, type ProductComposerHost } from 'jcode-ui/product';
import { normalizeState, type ChatRuntime, type RuntimeActions, type RuntimeState } from 'jcode-ui-core/runtime';
import { ApiError } from '../api/client';
import { useAccountRepositoryBranches, useStartAccountTask } from '../api/queries';
import type { AccountRepositoryTarget, ProjectModel } from '../api/types';
import { Select } from '../components/Select';
import { useToast } from '../components/Toast';
import { buildProjectModelProviders, projectModelKey, projectModelRef } from '../lib/productComposerModels';
import styles from './WorkHomePage.module.css';

const LAST_ACCOUNT_MODEL_KEY = 'jcloud.last-model.v1:';
const CLOUD_MODES: AgentMode[] = ['approval', 'plan', 'auto'];

function storedModel(accountId: string): string {
  try { return window.localStorage.getItem(LAST_ACCOUNT_MODEL_KEY + (accountId || 'session')) ?? ''; }
  catch { return ''; }
}

function rememberModel(accountId: string, modelId: string) {
  try { window.localStorage.setItem(LAST_ACCOUNT_MODEL_KEY + (accountId || 'session'), modelId); }
  catch { /* The current in-memory selection remains usable. */ }
}

class AccountTaskRuntime implements ChatRuntime {
  private state: RuntimeState = normalizeState({});
  private listeners = new Set<() => void>();
  readonly actions: RuntimeActions;

  constructor(send: (text: string) => void) {
    this.actions = {
      sendMessage: (text) => send(text),
      // The fieldset is disabled while the initial request is in flight, so
      // welcome tasks never expose a queue or a stop action they cannot honor.
      enqueueMessage: () => {},
      removeQueuedMessage: () => {},
      stop: () => {},
      resolveApproval: () => {},
      submitAskUser: () => {},
      editMessage: (_id, text) => send(text),
    };
  }

  getState = (): RuntimeState => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };
}

export function AccountRepositoryComposer({
  target,
  models,
  modelsLoading,
  accountId,
  contextPicker,
}: {
  target: AccountRepositoryTarget;
  models: ProjectModel[];
  modelsLoading: boolean;
  accountId: string;
  contextPicker: ReactNode;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const startTask = useStartAccountTask();
  const branches = useAccountRepositoryBranches(target.provider, target.provider_repo_id);
  const [selectedBranch, setSelectedBranch] = useState(target.default_branch);
  const [selectedModelId, setSelectedModelId] = useState('');
  const [mode, setMode] = useState<AgentMode>('approval');
  const [effortOverrides, setEffortOverrides] = useState<Record<string, string>>({});
  const [goalArmed, setGoalArmed] = useState(false);
  const composerRef = useRef<HTMLDivElement>(null);

  const providers = useMemo(() => buildProjectModelProviders(models), [models]);
  const modelRefs = useMemo(() => models.map(projectModelRef), [models]);
  const modelByRef = useMemo(() => new Map(models.map((model) => {
    return [projectModelKey(model), model] as const;
  })), [models]);
  const activeModel = models.find((model) => model.id === selectedModelId);
  const activeModelRef = activeModel ? projectModelRef(activeModel) : { provider: '', model: '' };

  useEffect(() => {
    setSelectedBranch(target.default_branch);
  }, [target.default_branch, target.provider, target.provider_repo_id]);

  useEffect(() => {
    const options = branches.data ?? [];
    if (options.length === 0 || options.some((branch) => branch.name === selectedBranch)) return;
    setSelectedBranch(options.find((branch) => branch.default)?.name ?? options[0]!.name);
  }, [branches.data, selectedBranch]);

  useEffect(() => {
    if (models.length === 0) {
      setSelectedModelId('');
      return;
    }
    const remembered = storedModel(accountId);
    const selected = models.find((model) => model.id === remembered && model.capabilities.tools)
      ?? models.find((model) => model.capabilities.tools);
    setSelectedModelId(selected?.id ?? '');
  }, [accountId, models]);

  useEffect(() => {
    // jcode-ui changes the goal placeholder dynamically, but the task input
    // still needs one stable accessible name in every mode.
    composerRef.current?.querySelector('textarea')?.setAttribute('aria-label', t('repositories.taskAria'));
  }, [goalArmed, t]);

  const sendRef = useRef<(text: string) => void>(() => {});
  const runtime = useMemo(() => new AccountTaskRuntime((text) => sendRef.current(text)), []);

  sendRef.current = (text: string) => {
    const prompt = text.trim();
    if (!prompt || !selectedModelId || !selectedBranch || branches.isLoading || branches.isError) return;
    const effort = effortOverrides[`${activeModelRef.provider}/${activeModelRef.model}`];
    rememberModel(accountId, selectedModelId);
    startTask.mutate({
      provider: target.provider,
      provider_repo_id: target.provider_repo_id,
      prompt,
      base_branch: selectedBranch,
      model_id: selectedModelId,
      session: true,
      permission_mode: mode,
      model_effort: (effort || 'auto') as 'auto' | 'low' | 'medium' | 'high',
      goal_mode: goalArmed,
    }, {
      onSuccess: ({ run }) => {
        setGoalArmed(false);
        navigate(`/runs/${encodeURIComponent(run.id)}`);
      },
      onError: (error) => toast.push({
        kind: 'error',
        message: error instanceof ApiError ? error.message : t('repositories.startFailed'),
      }),
    });
  };

  const selectModel = useCallback((provider: string, model: string) => {
    const selected = modelByRef.get(`${provider}/${model}`);
    if (!selected) return;
    setSelectedModelId(selected.id);
    rememberModel(accountId, selected.id);
  }, [accountId, modelByRef]);

  const setEffort = useCallback((provider: string, model: string, effort: string) => {
    setEffortOverrides((current) => {
      const next = { ...current };
      if (effort) next[`${provider}/${model}`] = effort;
      else delete next[`${provider}/${model}`];
      return next;
    });
  }, []);

  const strings = useMemo(() => ({
    ...buildProductComposerStrings(t),
    placeholder: t('repositories.taskAria'),
    send: t('repositories.startTask'),
    modelNoImages: t('repositories.attachmentsAfterConversation'),
    modeCeilingHint: t('repositories.modeCeilingHint'),
  }), [t]);

  const host = useMemo<ProductComposerHost>(() => ({
    providerName: activeModelRef.provider,
    modelName: activeModelRef.model,
    mode,
    allowedModes: CLOUD_MODES,
    providers,
    favoriteModels: [],
    recentModels: modelRefs,
    imageSupport: false,
    effortOverrides,
    slashCommands: [],
    hasMessages: false,
    goalArmed,
    sessionId: `account:${target.provider}:${target.provider_repo_id}`,
    projectPath: target.full_name,
    tasks: [],
    strings,
    selectModel,
    selectMode: setMode,
    setEffort,
    toggleFavorite: async () => {},
    setModelEnabled: async () => {},
    refreshModels: async () => {},
    setGoalArmed,
    fetchTaskStats: async () => null,
    validateWorkspacePaths: async () => [],
    browseFolders: async () => ({ current: '', folders: [] }),
    switchWorkspace: async () => {},
    fetchBranches: async () => ({ current: '', branches: [] }),
    checkoutBranch: async () => ({ branch: '' }),
    setGoal: async (objective: string) => ({ objective, status: 'active' as const }),
    clearGoal: async () => {},
  }), [
    activeModelRef.model, activeModelRef.provider, effortOverrides, goalArmed, mode,
    modelRefs, providers, selectModel, setEffort, strings, target,
  ]);

  const blocked = modelsLoading || !selectedModelId || branches.isLoading || branches.isError
    || !selectedBranch || target.execution_available === false || startTask.isPending;
  const branchOptions = (branches.data ?? []).map((branch) => ({
    value: branch.name,
    // Keep the always-visible composer row as compact as jcode Desktop. The
    // provider remains the authority for default/protection rules at dispatch.
    label: branch.name,
  }));

  return <div ref={composerRef} className={`${styles.accountComposer} jcode-product`} data-testid="account-repository-composer">
    <RuntimeProvider runtime={runtime}>
      <div className={styles.composerContextHeader}>
        {contextPicker}
        <span className={styles.branchPickerIcon}><GitBranch size={14} /></span>
        <Select
          aria-label={t('repositories.baseBranchAria', { branch: selectedBranch || t('repositories.unavailable') })}
          className={styles.branchPicker}
          value={selectedBranch}
          onChange={setSelectedBranch}
          options={branchOptions}
          disabled={branches.isLoading || branches.isError || branchOptions.length === 0}
          placeholder={branches.isLoading ? t('repositories.loadingBranches') : t('repositories.branchUnavailable')}
        />
      </div>
      <fieldset disabled={blocked} aria-busy={startTask.isPending}>
        <ChatInput host={host} pickerPlacement="bottom" elevated />
      </fieldset>
    </RuntimeProvider>
    {startTask.isPending && <div className={styles.composerPending} role="status">{t('repositories.startingRepositoryTask')}</div>}
    {branches.isError && <div className={styles.composerIssue} role="alert">
      <span>{branches.error instanceof ApiError ? branches.error.message : t('repositories.branchesUnavailable')}</span>
      <Link to="/account/settings?section=connections">{t('repositories.reviewGitAccountAccess')}</Link>
    </div>}
    {!modelsLoading && models.length > 0 && !selectedModelId && <div className={styles.composerIssue} role="alert">
      <span>{t('repositories.noToolModel')}</span>
      <Link to="/account/settings?section=models">{t('repositories.reviewModelAccess')}</Link>
    </div>}
  </div>;
}
