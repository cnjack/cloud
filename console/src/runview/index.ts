/*
 * index.ts — runview's public API. See README.md for the module boundary this
 * barrel exists to enforce: everything a host needs to render a run's event
 * timeline is exported here; nothing else in this directory is meant to be
 * imported directly from outside runview/.
 */

export { Timeline } from './Timeline';
export { PermissionCard } from './PermissionCard';
export { groupTimeline } from './grouping';
export { toTimelineItem, terminalStatusSeq } from './eventModel';
export { toThreadItems } from './threadModel';
export type { CloudApproval, CloudMessage } from './threadModel';

export type {
  RunViewEvent,
  RunViewEventPayload,
  TimelineItem,
  TextItem,
  ToolCallItem,
  ToolResultItem,
  StatusItem,
  FailureItem,
  ArtifactItem,
  GitItem,
  ResultItem,
  UserMessageItem,
  SessionFinishItem,
  PermissionRequestItem,
  PermissionResolvedItem,
  PermissionOptionView,
  PermissionCardItem,
  PermissionControls,
  UnknownItem,
  GroupedTimelineItem,
  TextBlockItem,
  ToolCardItem,
  ToolCardStatus,
} from './types';
