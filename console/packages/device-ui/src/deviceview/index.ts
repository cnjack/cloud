/*
 * index.ts — deviceview's public API barrel (same boundary rule as runview/).
 */
export { DeviceTimeline } from './DeviceTimeline';
export type { DeviceApprovalControls } from './DeviceTimeline';
export { DevicePairingCard } from './DevicePairingCard';
export { DevicePairingGate } from './DevicePairingGate';
export type { DevicePairingGateProps } from './DevicePairingGate';
export { mapDeviceEvent, applyToolResult, prettyArgs } from './eventModel';
export { groupDeviceEvents } from './grouping';
export {
  initialDeviceSessionState,
  reduceDeviceEvents,
  reduceDeviceDelta,
  hasSeqGap,
} from './sessionReducer';
export type { DeviceSessionState, FinalizedText } from './sessionReducer';
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
} from './types';
