/*
 * useDeviceComposer — the cloud-side ProductComposerHost + ChatRuntime pair
 * that lets the stock jcode product composer (jcode-ui/product ChatInput) and
 * Thread run a device-relay session unchanged.
 *
 *   const { host, runtime } = useDeviceComposer({ deviceId, sessionId, device, streamState, sessionRunning })
 *   <RuntimeProvider runtime={runtime}>
 *     <Thread ... />          // session page
 *     <ChatInput host={host} />
 *   </RuntimeProvider>
 *
 * Everything the desktop gets from its RTK store + /api/* client is projected
 * here from device.capabilities + local state; everything the desktop sends
 * through /api/chat goes through the relay chat.send envelope. Per-host
 * divergences (localStorage-backed favorites/recents/disabled models, per-
 * message mode/model/effort/project overrides, no git/browse/task-stats APIs)
 * are documented in reports/M14-unified-composer.md §B.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import type { ChatImage, ThreadItem } from 'jcode-ui-core';
import type {
  AgentMode,
  ModelRef,
  ProductComposerHost,
} from 'jcode-ui/product';
import type { Device, SendMessageExtras } from '../api/devices';
import { dqk, useRespondDeviceApproval, useSendDeviceMessage, useStopDeviceSession } from '../api/deviceQueries';
import type { DeviceSessionState } from '../deviceview/sessionReducer';
import { initialDeviceSessionState } from '../deviceview/sessionReducer';
import type { DeviceViewItem } from '../deviceview/types';
import {
  buildProviders,
  buildSendExtras,
  buildSlashCommands,
  buildWorkspaceTasks,
  CLOUD_ALLOWED_MODES,
  initialDeviceComposerState,
} from './hostState';
import type { DeviceComposerState } from './hostState';
import { localSystemItem, toThreadItems } from './threadItems';
import { DeviceChatRuntime } from './runtime';
import { buildProductComposerStrings } from './strings';

export interface UseDeviceComposerOptions {
  deviceId: string;
  /** Session id, or 'new' on the welcome page (the device assigns the sid). */
  sessionId: string;
  /** The device row (capabilities drive the pickers; null while loading). */
  device?: Device | null;
  /** Live session projection from useDeviceSessionStream; absent on welcome. */
  streamState?: DeviceSessionState;
  /** session.status === 'running' (list-level backstop alongside the stream). */
  sessionRunning?: boolean;
  /** hasMessages drives the composer's welcome ↔ docked layout. */
  hasMessages?: boolean;
  /**
   * Extra error sink for send/stop/approval failures. Errors always append a
   * local system row to the runtime timeline; hosts that don't render a
   * Thread on the composer's page (the welcome page) should also route them
   * here (e.g. a toast) so failures stay visible.
   */
  onError?: (message: string) => void;
  /**
   * Fired after a message was accepted by the relay (202). Welcome pages use
   * this to track the not-yet-visible new session (pending card + auto-open
   * once it appears in the session list).
   */
  onSent?: (info: { sessionId: string; text: string; at: number }) => void;
}

export interface DeviceComposer {
  host: ProductComposerHost;
  runtime: DeviceChatRuntime;
}

// ── localStorage-backed per-device composer prefs ────────────────────────────

function readJson<T>(key: string, fallback: T): T {
  try {
    const raw = globalThis.localStorage?.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function writeJson(key: string, value: unknown): void {
  try {
    globalThis.localStorage?.setItem(key, JSON.stringify(value));
  } catch {
    /* private mode etc. — prefs just don't persist */
  }
}

const EMPTY_STREAM = initialDeviceSessionState();
let localSeq = -2_000_000;

export function useDeviceComposer(options: UseDeviceComposerOptions): DeviceComposer {
  const { deviceId, sessionId, device, streamState, sessionRunning, hasMessages, onError, onSent } = options;
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const capabilities = device?.capabilities ?? null;
  const send = useSendDeviceMessage(deviceId);
  const stop = useStopDeviceSession(deviceId);
  const respondApproval = useRespondDeviceApproval(deviceId, sessionId);

  // ── Composer state (local; applied per message via chat.send extras) ──────
  const [compose, setCompose] = useState<DeviceComposerState>(() => ({
    ...initialDeviceComposerState(),
    effortOverrides: readJson(`jcloud:composer:effort:${deviceId}`, {}),
  }));
  const [favorites, setFavorites] = useState<string[]>(() =>
    readJson(`jcloud:composer:favorites:${deviceId}`, []),
  );
  const [recents, setRecents] = useState<ModelRef[]>(() =>
    readJson(`jcloud:composer:recents:${deviceId}`, []),
  );
  const [disabledModels, setDisabledModels] = useState<ReadonlySet<string>>(
    () => new Set(readJson<string[]>(`jcloud:composer:disabled:${deviceId}`, [])),
  );

  const composeRef = useRef(compose);
  composeRef.current = compose;

  // ── Send pipeline (runtime action → relay chat.send) ──────────────────────
  const [localItems, setLocalItems] = useState<ThreadItem[]>([]);
  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;
  const onSentRef = useRef(onSent);
  onSentRef.current = onSent;
  const appendLocalError = useCallback((content: string) => {
    setLocalItems((prev) => [...prev, localSystemItem(`local_${localSeq}`, content, localSeq--)]);
    onErrorRef.current?.(content);
  }, []);

  const sendRef = useRef<(text: string, images?: ChatImage[]) => void>(() => {});
  sendRef.current = (text, images) => {
    const state = composeRef.current;
    let mode: string | undefined;
    let extras: SendMessageExtras | undefined;
    if (state.goalArmed) {
      // Goal takes priority over every other compose field (relay contract):
      // the text IS the objective, sent as {text, goal_armed: true}.
      extras = { goal_armed: true };
      setCompose((c) => ({ ...c, goalArmed: false }));
    } else {
      extras = buildSendExtras(state, capabilities, images);
      mode = state.modeTouched ? state.mode : undefined;
    }
    send.mutate(
      { sessionId, text, ...(mode ? { mode } : {}), ...(extras ? { extras } : {}) },
      {
        onSuccess: () => {
          onSentRef.current?.({ sessionId, text, at: Date.now() });
        },
        onError: (error) => {
          appendLocalError(
            t('device.session.sendFailed', {
              message: error instanceof Error ? error.message : String(error),
            }),
          );
        },
      },
    );
  };

  const stopRef = useRef<() => void>(() => {});
  stopRef.current = () => {
    stop.mutate(sessionId, {
      onError: (error) => {
        appendLocalError(
          t('device.session.sendFailed', {
            message: error instanceof Error ? error.message : String(error),
          }),
        );
      },
    });
  };

  const approvalRef = useRef<(approvalId: string, decision: string) => void>(() => {});
  approvalRef.current = (approvalId, decision) => {
    respondApproval.mutate(
      { approvalId, decision },
      {
        onError: (error) => {
          appendLocalError(
            t('device.session.sendFailed', {
              message: error instanceof Error ? error.message : String(error),
            }),
          );
        },
      },
    );
  };

  const runtime = useMemo(
    () =>
      new DeviceChatRuntime({
        send: (text, images) => sendRef.current(text, images),
        stop: () => stopRef.current(),
        respondApproval: (id, decision) => approvalRef.current(id, decision),
      }),
    [],
  );

  // ── Runtime state projection ───────────────────────────────────────────────
  const describe = useCallback(
    (item: DeviceViewItem): string | null => {
      switch (item.kind) {
        case 'status':
          switch (item.eventKind) {
            case 'agent_done':
              if (item.errorMessage) return t('device.session.agentFailed', { message: item.errorMessage });
              if (item.stopped) return t('device.session.agentStopped');
              return null; // quiet turn end — the desktop shows nothing either
            case 'mode_changed':
              return item.mode ? t('device.session.modeChanged', { mode: item.mode }) : null;
            case 'model_changed':
              return item.model ? t('device.session.modelChanged', { model: item.model }) : null;
            case 'session_reset':
              return t('device.session.sessionReset');
            case 'goal_update':
              return item.goalObjective
                ? t('device.session.goalUpdated', { objective: item.goalObjective })
                : t('device.session.goalCleared');
            default:
              return null; // agent_start / task_status / todo_update: noise
          }
        case 'ask_user': {
          const questions = item.questions.map((q) => q.question).filter(Boolean).join('\n');
          return `${t('device.session.askUser')}${questions ? `\n${questions}` : ''}`;
        }
        case 'subagent':
          if (!item.done) return t('device.session.subagentStarted', { name: item.name });
          return item.error
            ? t('device.session.subagentFailed', { name: item.name })
            : t('device.session.subagentDone', { name: item.name });
        case 'unknown':
          return `${t('run.unknownEvent')}: ${item.eventKind}`;
        default:
          return null;
      }
    },
    [t],
  );

  const effectiveStream = streamState ?? EMPTY_STREAM;
  const items = useMemo(
    () => [...toThreadItems(effectiveStream, { describe }), ...localItems],
    [effectiveStream, describe, localItems],
  );
  const isRunning = effectiveStream.agentRunning || sessionRunning === true;
  const tokenSnapshot = effectiveStream.tokenSnapshot;

  useEffect(() => {
    runtime.setState({ items, isRunning, tokenSnapshot });
  }, [runtime, items, isRunning, tokenSnapshot]);

  // Type-ahead queue drain: turn end → send the oldest queued message.
  const wasRunning = useRef(false);
  useEffect(() => {
    if (wasRunning.current && !isRunning) {
      const head = runtime.shiftQueue();
      if (head) sendRef.current(head.text, head.images);
    }
    wasRunning.current = isRunning;
  }, [runtime, isRunning]);

  // ── Host state projections ─────────────────────────────────────────────────
  const providers = useMemo(() => buildProviders(capabilities, disabledModels), [capabilities, disabledModels]);
  const slashCommands = useMemo(() => buildSlashCommands(capabilities), [capabilities]);
  const tasks = useMemo(() => buildWorkspaceTasks(capabilities), [capabilities]);
  const strings = useMemo(() => buildProductComposerStrings(t), [t]);

  // ── Host actions ───────────────────────────────────────────────────────────
  const selectModel = useCallback(
    (provider: string, model: string) => {
      setCompose((c) => ({ ...c, model: { provider, model } }));
      setRecents((prev) => {
        const next = [{ provider, model }, ...prev.filter((r) => r.provider !== provider || r.model !== model)].slice(0, 8);
        writeJson(`jcloud:composer:recents:${deviceId}`, next);
        return next;
      });
    },
    [deviceId],
  );

  const selectMode = useCallback((mode: AgentMode) => {
    // M20: the composer dropdown already hides full_access; this guard keeps
    // the ceiling even if a caller bypasses the picker. The device connector
    // is the protocol-level enforcement (ack mode_not_allowed_for_cloud).
    if (!CLOUD_ALLOWED_MODES.includes(mode)) return;
    setCompose((c) => ({ ...c, mode, modeTouched: true }));
  }, []);

  const setEffort = useCallback(
    (provider: string, model: string, effort: string) => {
      setCompose((c) => {
        const effortOverrides = { ...c.effortOverrides };
        const key = `${provider}/${model}`;
        if (effort) effortOverrides[key] = effort;
        else delete effortOverrides[key];
        writeJson(`jcloud:composer:effort:${deviceId}`, effortOverrides);
        return { ...c, effortOverrides };
      });
    },
    [deviceId],
  );

  const toggleFavorite = useCallback(
    (provider: string, model: string) => {
      const key = `${provider}/${model}`;
      setFavorites((prev) => {
        const next = prev.includes(key) ? prev.filter((f) => f !== key) : [...prev, key];
        writeJson(`jcloud:composer:favorites:${deviceId}`, next);
        return next;
      });
    },
    [deviceId],
  );

  const setModelEnabled = useCallback(
    (provider: string, model: string, enabled: boolean) => {
      const key = `${provider}/${model}`;
      setDisabledModels((prev) => {
        const next = new Set(prev);
        if (enabled) next.delete(key);
        else next.add(key);
        writeJson(`jcloud:composer:disabled:${deviceId}`, [...next]);
        return next;
      });
    },
    [deviceId],
  );

  const refreshModels = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: dqk.devices });
  }, [queryClient]);

  const setGoalArmed = useCallback((armed: boolean) => {
    setCompose((c) => ({ ...c, goalArmed: armed }));
  }, []);

  const switchWorkspace = useCallback(async (path: string) => {
    // No device-side project-switch API: the choice rides the next message's
    // project_path extra. (Same-path "new session" desktop semantics N/A.)
    setCompose((c) => ({ ...c, projectPath: path }));
  }, []);

  const host = useMemo<ProductComposerHost>(
    () => ({
      providerName: compose.model?.provider ?? '',
      modelName: compose.model?.model ?? '',
      mode: compose.mode,
      // M20: cloud sessions are capped at auto — full_access is not offered.
      allowedModes: CLOUD_ALLOWED_MODES,
      providers,
      favoriteModels: favorites,
      recentModels: recents,
      // The relay reports no per-model vision flag: allow images whenever the
      // device advertises a model catalog (the device gates what the active
      // model cannot consume); no capabilities → no image affordance.
      imageSupport: (capabilities?.models?.length ?? 0) > 0,
      effortOverrides: compose.effortOverrides,
      slashCommands,
      hasMessages: hasMessages === true,
      goalArmed: compose.goalArmed,
      sessionId,
      projectPath: compose.projectPath,
      tasks,
      strings,

      selectModel,
      selectMode,
      setEffort,
      toggleFavorite,
      setModelEnabled,
      refreshModels,

      setGoalArmed,
      // No per-task stats endpoint on the relay: the context popup falls back
      // to its four basic rows (from tokenSnapshot) — same as desktop failure.
      fetchTaskStats: async () => null,

      // The device's filesystem is unreachable from the cloud: nothing is
      // "missing" client-side, and the folder browser lists no entries (the
      // path input still accepts a device-side path → project_path extra).
      validateWorkspacePaths: async () => [],
      browseFolders: async (path?: string) => ({ current: path ?? '', folders: [] }),
      switchWorkspace,
      // pickFolder / openRemoteConnect intentionally omitted (fail-visible:
      // the picker's native-picker and remote-wizard entries hide themselves).

      // No git API on the relay: empty result → BranchPicker renders null.
      fetchBranches: async () => ({ current: '', branches: [] }),
      checkoutBranch: async () => ({ branch: '' }),

      // GoalBanner is not mounted on cloud (no goal edit/clear relay API);
      // these exist only to satisfy the interface.
      setGoal: async (objective: string) => ({ objective, status: 'active' as const }),
      clearGoal: async () => {},
    }),
    [
      compose,
      providers,
      favorites,
      recents,
      capabilities,
      slashCommands,
      hasMessages,
      sessionId,
      tasks,
      strings,
      selectModel,
      selectMode,
      setEffort,
      toggleFavorite,
      setModelEnabled,
      refreshModels,
      setGoalArmed,
      switchWorkspace,
    ],
  );

  return { host, runtime };
}
