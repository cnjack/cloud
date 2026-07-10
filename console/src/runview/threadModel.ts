import type { Approval, Message, ThreadItem, ToolCall } from 'jcode-ui-core';
import { groupTimeline } from './grouping';
import { terminalStatusSeq } from './eventModel';
import type {
  GroupedTimelineItem,
  PermissionCardItem,
  RunViewEvent,
} from './types';

/**
 * The published approval component exposes a fixed boolean allow/deny contract,
 * while Cloud must echo the exact option_id offered by ACP. Keep the package's
 * standard approval discriminant so the headless Thread can carry it, and retain
 * the lossless Cloud card for the host renderer.
 */
export interface CloudApproval extends Approval {
  permission: PermissionCardItem;
}

export interface CloudMessage extends Message {
  /** Real Cloud author for a multi-user follow-up. */
  author?: string;
}

export function toThreadItems(events: RunViewEvent[]): ThreadItem[] {
  const finalStatusSeq = terminalStatusSeq(events);
  return groupTimeline(events).map((item) => toThreadItem(item, finalStatusSeq));
}

function toThreadItem(item: GroupedTimelineItem, finalStatusSeq?: number): ThreadItem {
  const timestamp = parseTimestamp(item.ts);

  switch (item.kind) {
    case 'text_block':
      return message(item.seq, 'assistant', item.text, timestamp);

    case 'user_message':
      return {
        kind: 'message',
        seq: item.seq,
        data: {
          id: `user-${item.seq}`,
          role: 'user',
          content: item.prompt,
          timestamp,
          source: item.by || undefined,
          author: item.by || undefined,
        } as CloudMessage,
      };

    case 'tool_card':
      return tool(item.seq, {
        id: `tool-${item.callId}`,
        toolCallID: item.callId,
        name: item.tool,
        args: item.args,
        output: item.output,
        displayOutput: item.output,
        error: item.isError ? item.output || 'Tool failed' : undefined,
        status:
          item.status === 'running'
            ? 'running'
            : item.status === 'failed'
              ? 'error'
              : 'done',
        timestamp,
      });

    case 'tool_call':
      return tool(item.seq, {
        id: `tool-call-${item.seq}`,
        toolCallID: item.callId,
        name: item.tool,
        args: item.args,
        status: 'running',
        timestamp,
      });

    case 'tool_result':
      return tool(item.seq, {
        id: `tool-result-${item.seq}`,
        toolCallID: item.callId,
        name: item.tool || 'tool',
        args: '{}',
        output: item.output,
        displayOutput: item.output,
        error: item.isError ? item.output || 'Tool failed' : undefined,
        status: item.isError ? 'error' : 'done',
        timestamp,
      });

    case 'permission_card': {
      const chosen = item.options.find((option) => option.optionId === item.resolvedOptionId);
      const approval: CloudApproval = {
        id: item.requestId,
        tool_name: item.title,
        tool_args: JSON.stringify({ options: item.options }),
        is_external: false,
        resolved: item.status === 'resolved',
        approved: chosen?.kind.startsWith('allow') ?? false,
        permission: item,
      };
      return { kind: 'approval', seq: item.seq, data: approval };
    }

    case 'status':
      return systemMessage(
        item.seq,
        item.seq === finalStatusSeq
          ? `Final status: ${formatStatus(item.status)} — end of run`
          : `Status: ${formatStatus(item.status)}`,
        timestamp,
      );

    case 'failure':
      return systemMessage(item.seq, item.message, timestamp, {
        level: 'error',
        detail: item.reason,
      });

    case 'artifact':
      return systemMessage(item.seq, `Artifact ready: ${item.artifact}`, timestamp);

    case 'git':
      return systemMessage(
        item.seq,
        item.commitSha
          ? `Pushed branch \`${item.branch}\` at \`${item.commitSha}\``
          : `Pushed branch \`${item.branch}\``,
        timestamp,
      );

    case 'result':
      return systemMessage(item.seq, `Result: ${item.message}`, timestamp);

    case 'permission_resolved':
      return systemMessage(
        item.seq,
        `Permission ${item.resolution === 'timeout' ? 'timed out' : 'resolved'}${
          item.optionId ? ` — ${item.optionId}` : ''
        }`,
        timestamp,
      );

    case 'session_info':
    case 'session_finish':
      return systemMessage(item.seq, item.message, timestamp);

    case 'unknown':
      return systemMessage(
        item.seq,
        `Unknown event: ${item.type}\n\n\`\`\`json\n${item.raw}\n\`\`\``,
        timestamp,
      );
  }
}

function message(
  seq: number,
  role: 'assistant',
  content: string,
  timestamp: number,
): ThreadItem {
  return {
    kind: 'message',
    seq,
    data: { id: `${role}-${seq}`, role, content, timestamp },
  };
}

function systemMessage(
  seq: number,
  content: string,
  timestamp: number,
  extra: { level?: 'error' | 'notice'; detail?: string } = {},
): ThreadItem {
  return {
    kind: 'message',
    seq,
    data: {
      id: `system-${seq}`,
      role: 'system',
      content,
      timestamp,
      level: 'notice',
      ...extra,
    },
  };
}

function tool(seq: number, data: ToolCall): ThreadItem {
  return { kind: 'tool', seq, data };
}

function parseTimestamp(ts: string): number {
  const parsed = Date.parse(ts);
  return Number.isFinite(parsed) ? parsed : 0;
}

function formatStatus(status: string): string {
  if (!status) return 'Unknown';
  const normalized = status.replaceAll('_', ' ');
  return normalized[0]!.toUpperCase() + normalized.slice(1);
}
