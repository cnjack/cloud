/*
 * eventModel.ts — defensively narrow one relayed jcode WS event into
 * deviceview's view model. Pure and total: every durable kind maps to an item,
 * unknown kinds degrade to an 'unknown' row with the raw payload (never throw).
 *
 * Payload reference: jcode internal/handler/web.go (WebToolCallData,
 * WebToolResultData, WebApprovalRequestData, WebDoneData, …) and
 * internal/web/{chat,engine,models,sessions}.go.
 */
import type {
  AssistantMessageItem,
  DeviceApprovalItem,
  DeviceAskUserItem,
  DeviceStatusItem,
  DeviceSubagentItem,
  DeviceToolCardItem,
  DeviceUnknownItem,
  DeviceViewEvent,
  DeviceViewItem,
  UserMessageItem,
} from './types';

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === 'object' && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : undefined;
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' ? v : undefined;
}

function bool(v: unknown): boolean | undefined {
  return typeof v === 'boolean' ? v : undefined;
}

/** Pretty-print a JSON-string field; pass the raw text through when invalid. */
export function prettyArgs(raw: string | undefined): string {
  if (!raw) return '';
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

function unknownItem(ev: DeviceViewEvent): DeviceUnknownItem {
  let raw = '';
  try {
    raw = JSON.stringify(ev.payload?.data ?? ev.payload ?? null, null, 2);
  } catch {
    raw = String(ev.payload);
  }
  return { kind: 'unknown', seq: ev.seq, ts: ev.ts, eventKind: ev.kind, raw };
}

/**
 * Map a durable session event to its view item, or null when the event is not
 * timeline-worthy on its own (tool_result folds into the tool card — see
 * grouping.ts; token-level/ephemeral kinds never reach the durable log).
 */
export function mapDeviceEvent(ev: DeviceViewEvent): DeviceViewItem | null {
  const data = asRecord(ev.payload?.data);
  switch (ev.kind) {
    case 'user_message': {
      const content = str(data?.content) ?? '';
      const source = str(data?.source) ?? '';
      const item: UserMessageItem = { kind: 'user_message', seq: ev.seq, ts: ev.ts, content, source };
      return item;
    }
    case 'agent_message': {
      // Synthesized by the connector at agent_done — the replayable full
      // assistant message. Empty text carries nothing, skip it.
      const text = str(data?.text) ?? '';
      if (!text) return null;
      const item: AssistantMessageItem = { kind: 'assistant_message', seq: ev.seq, ts: ev.ts, text };
      return item;
    }
    case 'tool_call': {
      const display = asRecord(data?.display_info);
      const item: DeviceToolCardItem = {
        kind: 'tool_card',
        seq: ev.seq,
        ts: ev.ts,
        tool: str(data?.name) ?? 'tool',
        callId: str(data?.tool_call_id),
        title: str(display?.title),
        subtitle: str(display?.subtitle),
        args: prettyArgs(str(data?.args)),
        status: 'running',
      };
      return item;
    }
    case 'tool_result':
      // Pairing happens in grouping.ts; a result alone is not a row.
      return null;
    case 'approval_request': {
      const item: DeviceApprovalItem = {
        kind: 'approval_card',
        seq: ev.seq,
        ts: ev.ts,
        approvalId: str(data?.id) ?? '',
        toolName: str(data?.tool_name) ?? 'tool',
        toolArgs: prettyArgs(str(data?.tool_args)),
        toolCallId: str(data?.tool_call_id),
        status: 'pending',
      };
      return item;
    }
    case 'ask_user_request': {
      const questions = Array.isArray(data?.questions) ? data.questions : [];
      const item: DeviceAskUserItem = {
        kind: 'ask_user',
        seq: ev.seq,
        ts: ev.ts,
        askId: str(data?.id) ?? '',
        questions: questions.map((q) => {
          const r = asRecord(q);
          return { question: str(r?.question) ?? '', header: str(r?.header) };
        }),
      };
      return item;
    }
    case 'agent_start':
    case 'session_reset':
    case 'todo_update':
      return { kind: 'status', seq: ev.seq, ts: ev.ts, eventKind: ev.kind } satisfies DeviceStatusItem;
    case 'agent_done':
      return {
        kind: 'status',
        seq: ev.seq,
        ts: ev.ts,
        eventKind: ev.kind,
        errorMessage: str(data?.error),
        stopped: bool(data?.stopped),
      } satisfies DeviceStatusItem;
    case 'task_status':
      return {
        kind: 'status',
        seq: ev.seq,
        ts: ev.ts,
        eventKind: ev.kind,
        status: str(data?.status),
      } satisfies DeviceStatusItem;
    case 'mode_changed':
      return {
        kind: 'status',
        seq: ev.seq,
        ts: ev.ts,
        eventKind: ev.kind,
        mode: str(data?.mode),
      } satisfies DeviceStatusItem;
    case 'model_changed':
      return {
        kind: 'status',
        seq: ev.seq,
        ts: ev.ts,
        eventKind: ev.kind,
        model: str(data?.model),
        provider: str(data?.provider),
      } satisfies DeviceStatusItem;
    case 'goal_update': {
      // data is the Goal object or a typed-null (jcode ws.go note).
      return {
        kind: 'status',
        seq: ev.seq,
        ts: ev.ts,
        eventKind: ev.kind,
        goalObjective: str(data?.objective),
        goalStatus: str(data?.status),
      } satisfies DeviceStatusItem;
    }
    case 'subagent_event': {
      const item: DeviceSubagentItem = {
        kind: 'subagent',
        seq: ev.seq,
        ts: ev.ts,
        name: str(data?.name) ?? 'agent',
        agentType: str(data?.agent_type) ?? '',
        done: bool(data?.done) ?? false,
        result: str(data?.result),
        error: str(data?.error),
      };
      return item;
    }
    // Ephemeral kinds should never be in the durable log; if one shows up,
    // ignore it rather than double-render the live stream.
    case 'agent_text':
    case 'token_update':
    case 'subagent_progress':
      return null;
    default:
      return unknownItem(ev);
  }
}

/** Fold a tool_result event into its tool card (pairing by tool_call_id). */
export function applyToolResult(
  card: DeviceToolCardItem,
  ev: DeviceViewEvent,
): DeviceToolCardItem {
  const data = asRecord(ev.payload?.data);
  const error = str(data?.error);
  const denied = bool(data?.denied) === true;
  const output =
    str(data?.display_output) ?? str(data?.output) ?? error ?? '';
  return {
    ...card,
    tool: str(data?.name) ?? card.tool,
    status: denied ? 'denied' : error ? 'failed' : 'succeeded',
    output,
    resultSeq: ev.seq,
  };
}
