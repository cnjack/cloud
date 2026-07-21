/*
 * threadItems.ts — adapt the device session view (M4 event model) to
 * jcode-ui-core ThreadItems, so the shared jcode-ui <Thread> renders device
 * sessions exactly like the desktop timeline.
 *
 * Mapping (DeviceViewItem → ThreadItem):
 *   user_message      → message (role user, `source` drives the channel tint)
 *   assistant_message → message (role assistant; markdown/mermaid via Thread)
 *   tool_card         → tool (status/denied/output folded by grouping.ts)
 *   approval_card     → approval (classic boolean: approve / approve_all / deny)
 *   status / ask_user / subagent / unknown → system `message`, text from the
 *                       host-supplied `describe` localizer (null = skip row);
 *                       the relay has no ask_user answer API, so those stay
 *                       read-only notes just like the old DeviceTimeline.
 * Ephemeral stream (agent_text deltas finalized locally / still streaming)
 * appends as trailing assistant messages, mirroring DeviceTimeline's bubbles.
 */
import type { Approval, Message, ThreadItem, ToolCall } from 'jcode-ui-core';
import { groupDeviceEvents } from '../deviceview/grouping';
import type {
  DeviceApprovalItem,
  DeviceToolCardItem,
  DeviceViewItem,
} from '../deviceview/types';
import type { DeviceSessionState } from '../deviceview/sessionReducer';

function ts(iso: string): number {
  const t = Date.parse(iso);
  return Number.isFinite(t) ? t : 0;
}

function toToolCall(item: DeviceToolCardItem): ToolCall {
  return {
    id: `tool_${item.seq}`,
    toolCallID: item.callId,
    name: item.tool,
    args: item.args,
    output: item.output,
    error: item.status === 'failed' ? item.output : undefined,
    status:
      item.status === 'running' ? 'running' : item.status === 'failed' ? 'error' : 'done',
    denied: item.status === 'denied' || undefined,
    timestamp: ts(item.ts),
    displayInfo:
      item.title || item.subtitle
        ? { title: item.title ?? item.tool, subtitle: item.subtitle }
        : undefined,
  };
}

function toApproval(item: DeviceApprovalItem): Approval {
  const resolved = item.status === 'answered';
  return {
    id: item.approvalId,
    tool_name: item.toolName,
    tool_args: item.toolArgs,
    tool_call_id: item.toolCallId,
    is_external: false,
    resolved,
    approved: resolved ? item.decision !== 'deny' : undefined,
  };
}

/**
 * Localize a non-core view item (status/ask_user/subagent/unknown) to a
 * system-message line; return null to drop the row entirely (e.g. noisy
 * lifecycle events the desktop also hides).
 */
export type DeviceItemDescriber = (item: DeviceViewItem) => string | null;

export interface ToThreadItemsOptions {
  describe: DeviceItemDescriber;
}

/** Synthetic seqs for ephemeral/local rows — never collide with event seqs. */
const STREAMING_SEQ = -1_000_000;

export function toThreadItems(
  state: Pick<DeviceSessionState, 'events' | 'finalizedText' | 'streamingText'>,
  options: ToThreadItemsOptions,
): ThreadItem[] {
  const items: ThreadItem[] = [];
  for (const view of groupDeviceEvents(state.events)) {
    switch (view.kind) {
      case 'user_message': {
        const message: Message = {
          id: `user_${view.seq}`,
          role: 'user',
          content: view.content,
          source: view.source || undefined,
          timestamp: ts(view.ts),
        };
        items.push({ kind: 'message', data: message, seq: view.seq });
        break;
      }
      case 'assistant_message': {
        const message: Message = {
          id: `assistant_${view.seq}`,
          role: 'assistant',
          content: view.text,
          timestamp: ts(view.ts),
        };
        items.push({ kind: 'message', data: message, seq: view.seq });
        break;
      }
      case 'tool_card':
        items.push({ kind: 'tool', data: toToolCall(view), seq: view.seq });
        break;
      case 'approval_card':
        items.push({ kind: 'approval', data: toApproval(view), seq: view.seq });
        break;
      default: {
        const text = options.describe(view);
        if (text) {
          const message: Message = {
            id: `sys_${view.seq}`,
            role: 'system',
            level: view.kind === 'unknown' ? 'error' : 'notice',
            content: text,
            timestamp: ts(view.ts),
          };
          items.push({ kind: 'message', data: message, seq: view.seq });
        }
      }
    }
  }
  // Locally-finalized stream bubbles (agent_done before the durable
  // agent_message lands) then the still-streaming tail.
  for (const fin of state.finalizedText) {
    const message: Message = {
      id: `fin_${fin.id}`,
      role: 'assistant',
      content: fin.text,
      timestamp: 0,
    };
    items.push({ kind: 'message', data: message, seq: STREAMING_SEQ + fin.id });
  }
  if (state.streamingText) {
    const message: Message = {
      id: 'streaming',
      role: 'assistant',
      content: state.streamingText,
      timestamp: 0,
    };
    items.push({ kind: 'message', data: message, seq: STREAMING_SEQ * 2 });
  }
  return items;
}

/** Append a local-only system note (e.g. a failed send) after the stream items. */
export function localSystemItem(id: string, content: string, seq: number): ThreadItem {
  const message: Message = {
    id,
    role: 'system',
    level: 'error',
    content,
    timestamp: Date.now(),
  };
  return { kind: 'message', data: message, seq };
}
