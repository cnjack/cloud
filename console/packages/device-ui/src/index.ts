/*
 * index.ts — @jcloud/device-ui public barrel.
 *
 * The shared kernel for jcode device-relay clients (console, mobile app):
 *   devicecrypto/  CEK envelope crypto + P-256 pairing + CEK stores (IndexedDB
 *                  in DOM runtimes, memory stub elsewhere — see provider.ts)
 *   api/           DeviceApi HTTP/SSE layer + transparent E2EE wrapper +
 *                  react-query hooks + the DeviceApiProvider context
 *   deviceview/    event mapping, session reducer, timeline & pairing card
 *   hooks/         pairing + session-stream state machines
 *   runview/       PermissionCard + timeline CSS shared with the console's run view
 *   components/    generic controls the device views use (Button)
 *   i18n/          resource bundles for hosts that don't ship console locales
 */

// --- devicecrypto -------------------------------------------------------------
export {
  isEnvelope,
  b64encode,
  b64decode,
  importCek,
  encryptJson,
  decryptJson,
  decryptText,
} from './devicecrypto/envelope';
export type { DeviceEnvelope } from './devicecrypto/envelope';
export {
  CEK_HKDF_INFO,
  generatePairingKeys,
  importPairingPrivateKey,
  unwrapCek,
} from './devicecrypto/pairing';
export type { DeviceWrap, PairingKeys } from './devicecrypto/pairing';
export {
  createMemoryCekStore,
  createMemoryPairingSessionStore,
  createIdbCekStore,
  createIdbPairingSessionStore,
} from './devicecrypto/storage';
export type {
  StoredCek,
  CekStore,
  PairingSession,
  PairingSessionStore,
} from './devicecrypto/storage';
export { createDeviceCrypto, sharedCekStore, sharedPairingSessions, sharedDeviceCrypto } from './devicecrypto/provider';
export type { DeviceCrypto } from './devicecrypto/provider';

// --- api ----------------------------------------------------------------------
export { ApiError, apiErrorCode } from './api/errors';
export type { TokenSource } from './api/errors';
export { createDeviceApi } from './api/devices';
export type {
  Device,
  DeviceCapabilities,
  DeviceCapabilityProject,
  DeviceCapabilityModel,
  DeviceCapabilitySlashCommand,
  ComposeAttachment,
  ComposeImage,
  SendMessageExtras,
  DeviceSessionMeta,
  DeviceSession,
  DeviceSessionEvent,
  SendMessageResult,
  CreatePairingResult,
  PairingState,
  DeviceStreamFrame,
  DeviceStreamCallbacks,
  DeviceStreamHandle,
  DeviceApi,
  DeviceApiOptions,
} from './api/devices';
export { withDeviceCrypto } from './api/encryptedDevices';
export { DeviceApiProvider, useDeviceApi } from './api/DeviceApiProvider';
export {
  dqk,
  useDevices,
  useDeviceSessions,
  useSendDeviceMessage,
  useStopDeviceSession,
  useRespondDeviceApproval,
} from './api/deviceQueries';

// --- compose (M12 shared compose panel) ---------------------------------------
// @deprecated M14: superseded by the jcode product composer (jcode-ui/product
// ChatInput + useDeviceComposer). Kept exported for downstream pinning; the
// console and mobile apps no longer render it.
export {
  DeviceCompose,
  initialComposeValue,
  composeExtras,
  COMPOSE_MAX_ATTACHMENT_BYTES,
  COMPOSE_MAX_ATTACHMENTS,
} from './compose/DeviceCompose';
export type { ComposeValue, DeviceComposeProps } from './compose/DeviceCompose';

// --- product composer (M14: jcode-ui/product over the device relay) ----------
export { useDeviceComposer } from './product/useDeviceComposer';
export type { UseDeviceComposerOptions, DeviceComposer } from './product/useDeviceComposer';
export { DeviceChatRuntime } from './product/runtime';
export type { DeviceRuntimeCallbacks } from './product/runtime';
export {
  buildProviders,
  buildSlashCommands,
  buildWorkspaceTasks,
  buildSendExtras,
  initialDeviceComposerState,
  modelKey,
} from './product/hostState';
export type { DeviceComposerState } from './product/hostState';
export { toThreadItems, localSystemItem } from './product/threadItems';
export type { DeviceItemDescriber, ToThreadItemsOptions } from './product/threadItems';
export { buildProductComposerStrings } from './product/strings';

// --- deviceview ---------------------------------------------------------------
// @deprecated M14: superseded by jcode-ui <Thread> fed by toThreadItems (the
// product/ module). Kept exported for downstream pinning; the console and
// mobile apps no longer render it.
export { DeviceTimeline } from './deviceview/DeviceTimeline';
export type { DeviceApprovalControls } from './deviceview/DeviceTimeline';
export { DevicePairingCard } from './deviceview/DevicePairingCard';
export { DevicePairingGate } from './deviceview/DevicePairingGate';
export type { DevicePairingGateProps } from './deviceview/DevicePairingGate';
export { mapDeviceEvent, applyToolResult, prettyArgs } from './deviceview/eventModel';
export { KNOWN_MESSAGE_SOURCES, channelLabelKey } from './deviceview/channels';
export { groupDeviceEvents } from './deviceview/grouping';
export {
  initialDeviceSessionState,
  reduceDeviceEvents,
  reduceDeviceDelta,
  hasSeqGap,
  mapTokenSnapshot,
} from './deviceview/sessionReducer';
export type { DeviceSessionState, FinalizedText, DeviceTokenSnapshot } from './deviceview/sessionReducer';
export { resolveOnline } from './deviceview/offline';
export type {
  DeviceViewEvent,
  DeviceViewItem,
  DeviceToolCardItem,
  DeviceApprovalItem,
  DeviceAskUserItem,
  DeviceStatusItem,
  DeviceSubagentItem,
  DeviceUnknownItem,
  UserMessageItem,
  DeviceToolStatus,
} from './deviceview/types';

// --- hooks --------------------------------------------------------------------
export { useDevicePairing } from './hooks/useDevicePairing';
export type { DevicePairingPhase, DevicePairingDeps, DevicePairing } from './hooks/useDevicePairing';
export { useDeviceSessionStream } from './hooks/useDeviceSessionStream';
export type { DeviceStreamPhase } from './hooks/useDeviceSessionStream';

// --- runview (PermissionCard + shared timeline CSS) ----------------------------
export { PermissionCard, timelineCss } from './runview';
export type { PermissionOptionView, PermissionCardItem, PermissionControls } from './runview';

// --- components ----------------------------------------------------------------
export { Button } from './components/Button';

// --- i18n ----------------------------------------------------------------------
export {
  DEVICE_UI_LOCALES,
  deviceUiResources,
  registerDeviceUiResources,
  deviceUiInterpolation,
} from './i18n';
