/*
 * types.ts — runview's own event/view-model contract.
 *
 * Deliberately independent of the console's `api/types.ts`: this module is the
 * seed of a future standalone package (shared with the jcode desktop/web
 * clients), so it must not assume anything about how the host app fetches or
 * stores runs — only the shape of the ACP-ish event stream it renders.
 * `RunViewEvent`/`RunViewEventPayload` are structurally compatible with the
 * console's `RunEvent`/`RunEventPayload` (api/types.ts), so the host can pass
 * those objects straight through with no adapter layer.
 */

export interface RunViewEventPayload {
  [key: string]: unknown;
}

export interface RunViewEvent {
  seq: number;
  ts: string;
  type: string;
  payload: RunViewEventPayload;
}

/* ---- per-event view models (ungrouped, one row per wire event) ----------- */

export interface TextItem {
  seq: number;
  ts: string;
  kind: 'text';
  text: string;
}

export interface ToolCallItem {
  seq: number;
  ts: string;
  kind: 'tool_call';
  tool: string;
  callId?: string;
  args: string; // pretty-printed
}

export interface ToolResultItem {
  seq: number;
  ts: string;
  kind: 'tool_result';
  tool?: string;
  callId?: string;
  output: string;
  isError: boolean;
}

export interface StatusItem {
  seq: number;
  ts: string;
  kind: 'status';
  status: string;
}

export interface FailureItem {
  seq: number;
  ts: string;
  kind: 'failure';
  reason?: string;
  message: string;
}

export interface ArtifactItem {
  seq: number;
  ts: string;
  kind: 'artifact';
  artifact: string;
}

export interface GitItem {
  seq: number;
  ts: string;
  kind: 'git';
  branch: string;
  commitSha?: string;
}

/** run.result { outcome: "no_changes" } — a successful run that produced no diff. */
export interface ResultItem {
  seq: number;
  ts: string;
  kind: 'result';
  outcome: string;
  message: string;
}

export interface UnknownItem {
  seq: number;
  ts: string;
  kind: 'unknown';
  type: string;
  raw: string;
}

export type TimelineItem =
  | TextItem
  | ToolCallItem
  | ToolResultItem
  | StatusItem
  | FailureItem
  | ArtifactItem
  | GitItem
  | ResultItem
  | UnknownItem;

/* ---- grouped (rendering) projection --------------------------------------
 * What groupTimeline() produces: the same information, collapsed into the
 * blocks a human actually wants to read (see grouping.ts for the rules). */

/** A run of consecutive `agent.text` chunks merged into one prose block. */
export interface TextBlockItem {
  kind: 'text_block';
  /** Anchor seq = the seq of the FIRST chunk merged into this block (stable React key). */
  seq: number;
  /** ts of the first chunk. */
  ts: string;
  /** seq of the most recently merged chunk — lets a host show a "still streaming" cue. */
  lastSeq: number;
  text: string;
}

export type ToolCardStatus = 'running' | 'succeeded' | 'failed';

/** An `agent.tool_call` paired with its `agent.tool_result` by call_id. */
export interface ToolCardItem {
  kind: 'tool_card';
  /** Anchor seq = the call's seq. */
  seq: number;
  /** ts of the call. */
  ts: string;
  tool: string;
  callId: string;
  status: ToolCardStatus;
  args: string; // pretty-printed
  output?: string; // pretty-printed; absent while status === 'running'
  isError: boolean;
  callSeq: number;
  resultSeq?: number;
  resultTs?: string;
}

/**
 * The rendering projection: text_block/tool_card are new grouped shapes;
 * everything else passes through as its ungrouped TimelineItem shape
 * (including ToolCallItem/ToolResultItem for orphan events that could never
 * be paired — see grouping.ts's "graceful degradation" rule).
 */
export type GroupedTimelineItem =
  | TextBlockItem
  | ToolCardItem
  | ToolCallItem
  | ToolResultItem
  | StatusItem
  | FailureItem
  | ArtifactItem
  | GitItem
  | ResultItem
  | UnknownItem;
