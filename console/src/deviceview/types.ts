/*
 * types.ts — deviceview's own event/view-model contract.
 *
 * Mirrors runview's boundary: this module only knows the shape of the jcode
 * WS event stream relayed through the device API (api/devices.ts), never how
 * the host fetches it. Payload field names follow jcode's internal/web/ws.go
 * and internal/handler/web.go (the envelope is { type, data, task_id? }).
 */

/** One durable session event as the mapping layer sees it. */
export interface DeviceViewEvent {
  seq: number;
  ts: string;
  kind: string;
  payload: { type?: string; data?: unknown; [key: string]: unknown };
}

/** user_message { content, source } — a prompt from an external channel. */
export interface UserMessageItem {
  kind: 'user_message';
  seq: number;
  ts: string;
  content: string;
  /** Channel label: "console" | "mobile" | "wechat" | … ('' = unknown). */
  source: string;
}

/**
 * agent_message { text, error?, stopped? } — the full assistant message.
 * Never emitted by jcode itself: the device connector synthesizes it at
 * agent_done from the accumulated agent_text deltas (jcode
 * internal/cloud/events.go), which is what makes assistant text replayable.
 */
export interface AssistantMessageItem {
  kind: 'assistant_message';
  seq: number;
  ts: string;
  text: string;
}

export type DeviceToolStatus = 'running' | 'succeeded' | 'failed' | 'denied';

/** tool_call paired with its tool_result by tool_call_id (see grouping.ts). */
export interface DeviceToolCardItem {
  kind: 'tool_card';
  /** Anchor seq = the call's seq (stable React key). */
  seq: number;
  ts: string;
  tool: string;
  callId?: string;
  /** display_info.title/subtitle when the device relayed them. */
  title?: string;
  subtitle?: string;
  /** Pretty-printed args (raw string when not parseable JSON). */
  args: string;
  status: DeviceToolStatus;
  output?: string;
  resultSeq?: number;
}

/**
 * approval_request { id, tool_name, tool_args, tool_call_id, is_external }.
 * jcode has no approval_resolved event on the relay: the card is 'pending'
 * until the host answers it (optimistic 'answered'), then the paired
 * tool_result (denied or not) closes the loop.
 */
export interface DeviceApprovalItem {
  kind: 'approval_card';
  seq: number;
  ts: string;
  approvalId: string;
  toolName: string;
  toolArgs: string;
  toolCallId?: string;
  status: 'pending' | 'answered';
  /** The decision the host submitted (answered only). */
  decision?: string;
}

/** ask_user_request { id, questions } — rendered read-only (no relay answer API). */
export interface DeviceAskUserItem {
  kind: 'ask_user';
  seq: number;
  ts: string;
  askId: string;
  questions: { question: string; header?: string }[];
}

/** Lifecycle/config rows: agent_start/agent_done/task_status/mode/model/session_reset/goal/todo. */
export interface DeviceStatusItem {
  kind: 'status';
  seq: number;
  ts: string;
  /** The relay event kind, e.g. 'agent_done' — the label is derived at render time. */
  eventKind: string;
  /** Structured fields the renderer localises from (never pre-baked strings). */
  status?: string;
  mode?: string;
  model?: string;
  provider?: string;
  errorMessage?: string;
  stopped?: boolean;
  goalObjective?: string;
  goalStatus?: string;
}

/** subagent_event { name, agent_type, done, result?, error? }. */
export interface DeviceSubagentItem {
  kind: 'subagent';
  seq: number;
  ts: string;
  name: string;
  agentType: string;
  done: boolean;
  result?: string;
  error?: string;
}

export interface DeviceUnknownItem {
  kind: 'unknown';
  seq: number;
  ts: string;
  eventKind: string;
  raw: string;
}

export type DeviceViewItem =
  | UserMessageItem
  | AssistantMessageItem
  | DeviceToolCardItem
  | DeviceApprovalItem
  | DeviceAskUserItem
  | DeviceStatusItem
  | DeviceSubagentItem
  | DeviceUnknownItem;
