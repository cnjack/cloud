/*
 * hostState.ts — pure projections from DeviceCapabilities to the
 * ProductComposerHost state model (jcode-ui/product), plus send-extras
 * assembly. Kept pure (no React, no i18n) so the mapping rules are unit
 * testable; useDeviceComposer wires them to live state.
 *
 * Degradation rules (vs. the desktop host, which reads a rich model catalog):
 *   - capabilities.models carries only {provider, id, label} — no per-model
 *     reasoning/image metadata. We synthesize: tool_call=true, image_support
 *     assumed true (the device is the ultimate gate), reasoning_options
 *     derived from the device-wide capabilities.efforts list.
 *   - capabilities.slash_commands arrives only from M14+ connectors; older
 *     devices simply produce an empty slash menu (plus the local /goal entry).
 */
import type {
  ModelRef,
  ProviderInfo,
  SlashCommandInfo,
  WorkspaceTaskRef,
} from 'jcode-ui/product';
import type { DeviceCapabilities, ComposeImage, SendMessageExtras } from '../api/devices';

/** Model enable/disable + favorites are keyed "provider/model" (desktop rule). */
export function modelKey(ref: ModelRef): string {
  return `${ref.provider}/${ref.model}`;
}

/**
 * Group the flat capabilities.models list into ProviderInfo[]. `disabledKeys`
 * holds user-hidden "provider/model" keys (Manage Models dialog, local-only —
 * the relay has no model-enable API).
 */
export function buildProviders(
  capabilities: DeviceCapabilities | null | undefined,
  disabledKeys: ReadonlySet<string> = new Set(),
): ProviderInfo[] {
  const efforts = capabilities?.efforts ?? [];
  const byProvider = new Map<string, ProviderInfo>();
  for (const m of capabilities?.models ?? []) {
    let p = byProvider.get(m.provider);
    if (!p) {
      p = { id: m.provider, name: m.provider, models: [] };
      byProvider.set(m.provider, p);
    }
    p.models.push({
      id: m.id,
      name: m.label || m.id,
      tool_call: true,
      enabled: !disabledKeys.has(`${m.provider}/${m.id}`),
      // The relay reports no per-model vision flag — allow images and let the
      // device reject what the active model cannot consume.
      image_support: true,
      // Device-wide effort ladder doubles as every model's reasoning options;
      // absent → the effort control hides itself.
      reasoning: efforts.length > 0,
      reasoning_options: efforts.length > 0 ? [{ type: 'effort', values: [...efforts] }] : undefined,
    });
  }
  return [...byProvider.values()];
}

/** capabilities.slash_commands → SlashCommandInfo[] (unknown types → 'skill'). */
export function buildSlashCommands(
  capabilities: DeviceCapabilities | null | undefined,
): SlashCommandInfo[] {
  return (capabilities?.slash_commands ?? []).map((c) => ({
    slash: c.slash,
    description: c.description ?? '',
    type: c.type === 'flow' ? 'flow' : c.type === 'builtin' ? 'builtin' : 'skill',
  }));
}

/** The workspace picker derives its list from tasks[].project — one per project. */
export function buildWorkspaceTasks(
  capabilities: DeviceCapabilities | null | undefined,
): WorkspaceTaskRef[] {
  return (capabilities?.projects ?? []).map((p) => ({ uuid: p.path, project: p.path }));
}

/**
 * The compose state the product composer edits (via host actions) and the
 * runtime reads at send time. All fields are LOCAL — the relay applies them
 * per message (chat.send extras), there is no device-side switch API.
 */
export interface DeviceComposerState {
  /** Session approval mode shown in the composer. */
  mode: 'approval' | 'plan' | 'auto' | 'full_access';
  /** mode only rides the payload once the user explicitly picked one — an
   * untouched composer preserves the device default (pre-M14 behavior). */
  modeTouched: boolean;
  /** Selected model override (null = device default). */
  model: ModelRef | null;
  /** Per-"provider/model" reasoning-effort overrides. */
  effortOverrides: Record<string, string>;
  /** Next message becomes the session goal (chat.send goal_armed). */
  goalArmed: boolean;
  /** Workspace override ('' = device default project). */
  projectPath: string;
}

export function initialDeviceComposerState(): DeviceComposerState {
  return {
    mode: 'approval',
    modeTouched: false,
    model: null,
    effortOverrides: {},
    goalArmed: false,
    projectPath: '',
  };
}

/**
 * Assemble chat.send extras from the composer state, honoring the same rule
 * as M12 composeExtras: an option only rides the payload while the device
 * still advertises it. Returns undefined when nothing applies (payload stays
 * byte-identical to a plain text send).
 *
 * goal_armed is NOT handled here — a goal send carries `{goal_armed: true}`
 * alone (the connector gives it priority over every other compose field).
 */
export function buildSendExtras(
  state: DeviceComposerState,
  capabilities: DeviceCapabilities | null | undefined,
  images?: ComposeImage[],
): SendMessageExtras | undefined {
  const extras: SendMessageExtras = {};
  if (state.model) {
    const advertised = capabilities?.models?.some(
      (m) => m.provider === state.model!.provider && m.id === state.model!.model,
    );
    if (advertised) extras.model = { provider: state.model.provider, id: state.model.model };
  }
  if (extras.model) {
    const effort = state.effortOverrides[modelKey(state.model!)];
    if (effort && (capabilities?.efforts ?? []).includes(effort)) extras.effort = effort;
  }
  if (state.projectPath) {
    const advertised = capabilities?.projects?.some((p) => p.path === state.projectPath);
    if (advertised) extras.project_path = state.projectPath;
  }
  if (images && images.length > 0) extras.images = images;
  return Object.keys(extras).length > 0 ? extras : undefined;
}
