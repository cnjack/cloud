import { buildProductComposerStrings } from '@jcloud/device-ui';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { RuntimeProvider } from 'jcode-ui';
import { ChatInput, type AgentMode, type ProductComposerHost } from 'jcode-ui/product';
import { normalizeState, type ChatRuntime, type RuntimeActions } from 'jcode-ui-core/runtime';
import type { ProjectModel, ResumeSessionOptions } from '../api/types';
import { buildProjectModelProviders, projectModelKey, projectModelRef } from '../lib/productComposerModels';

const CLOUD_SESSION_MODES: AgentMode[] = ['approval', 'plan', 'auto'];

function cloudMode(value?: string): AgentMode {
  return value === 'plan' || value === 'auto' || value === 'approval' ? value : 'approval';
}

export function RunSessionComposer({
  runId,
  configurable,
  running,
  disabled,
  placeholder,
  currentModelId,
  currentPermissionMode,
  models,
  onSend,
  onStop,
}: {
  runId: string;
  configurable: boolean;
  running: boolean;
  disabled: boolean;
  placeholder: string;
  currentModelId: string;
  currentPermissionMode?: string;
  models: readonly ProjectModel[];
  onSend: (text: string, options?: ResumeSessionOptions) => void;
  onStop?: () => void;
}) {
  const { t } = useTranslation();
  const availableModels = useMemo(() => models.filter((model) => model.capabilities.tools), [models]);
  const [selectedModelId, setSelectedModelId] = useState('');
  const [mode, setMode] = useState<AgentMode>(() => cloudMode(currentPermissionMode));
  const [goalArmed, setGoalArmed] = useState(false);

  useEffect(() => {
    const selected = availableModels.find((model) => model.id === currentModelId) ?? availableModels[0];
    setSelectedModelId(selected?.id ?? '');
  }, [availableModels, currentModelId]);

  useEffect(() => {
    setMode(cloudMode(currentPermissionMode));
  }, [currentPermissionMode, runId]);

  const providers = useMemo(() => buildProjectModelProviders(availableModels), [availableModels]);
  const modelRefs = useMemo(() => availableModels.map(projectModelRef), [availableModels]);
  const modelByRef = useMemo(
    () => new Map(availableModels.map((model) => [projectModelKey(model), model] as const)),
    [availableModels],
  );
  const activeModel = availableModels.find((model) => model.id === selectedModelId);
  const activeModelRef = activeModel ? projectModelRef(activeModel) : { provider: '', model: '' };

  const selectModel = useCallback((provider: string, model: string) => {
    const selected = modelByRef.get(`${provider}/${model}`);
    if (selected) setSelectedModelId(selected.id);
  }, [modelByRef]);

  const sendRef = useRef<(text: string) => void>(() => {});
  const stopRef = useRef<() => void>(() => {});
  sendRef.current = (text: string) => {
    const prompt = text.trim();
    if (!prompt) return;
    const body = goalArmed ? `/goal ${prompt}` : prompt;
    if (goalArmed) setGoalArmed(false);
    onSend(body, configurable ? { model_id: selectedModelId, permission_mode: mode } : undefined);
  };
  stopRef.current = () => onStop?.();

  const runtime = useMemo<ChatRuntime>(() => {
    const state = normalizeState({ isRunning: running });
    const actions: RuntimeActions = {
      sendMessage: (text) => sendRef.current(text),
      enqueueMessage: (text) => sendRef.current(text),
      removeQueuedMessage: () => {},
      stop: () => stopRef.current(),
      resolveApproval: () => {},
      submitAskUser: () => {},
      editMessage: (_id, text) => sendRef.current(text),
    };
    return { getState: () => state, subscribe: () => () => {}, actions };
  }, [running]);

  const strings = useMemo(() => ({
    ...buildProductComposerStrings(t),
    placeholder,
    queuePlaceholder: placeholder,
    send: t('runDetail.composer.sendMessage'),
    queue: t('runDetail.composer.queue'),
    stop: t('runDetail.action.stop'),
    modelNoImages: t('runDetail.composer.attachmentsUnavailableTitle'),
    modeCeilingHint: 'Cloud sessions support approval, plan, and auto modes.',
  }), [placeholder, t]);

  const host = useMemo<ProductComposerHost>(() => ({
    providerName: activeModelRef.provider,
    modelName: activeModelRef.model,
    mode,
    allowedModes: CLOUD_SESSION_MODES,
    providers,
    favoriteModels: [],
    recentModels: modelRefs,
    imageSupport: false,
    effortOverrides: {},
    slashCommands: [],
    hasMessages: true,
    goalArmed,
    sessionId: `run:${runId}`,
    projectPath: '',
    tasks: [],
    strings,
    selectModel,
    selectMode: setMode,
    setEffort: () => {},
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
  }), [activeModelRef.model, activeModelRef.provider, goalArmed, mode, modelRefs, providers, runId, selectModel, strings]);

  return <div className="jcode-product" data-testid="run-session-composer">
    <RuntimeProvider runtime={runtime}>
      <ChatInput
        host={host}
        modelManagement={false}
        modelFavorites={false}
        modelSelection={configurable && availableModels.length > 0}
        modeSelection={configurable}
        effortSelection={false}
        sendDisabled={disabled}
      />
    </RuntimeProvider>
  </div>;
}
