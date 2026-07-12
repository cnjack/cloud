import { renderMarkdown } from 'jcode-ui';
import { ArrowSquareOut, CaretRight, Check, Circle, File, Warning } from '@phosphor-icons/react';
import { groupTimeline } from './grouping';
import { terminalStatusSeq } from './eventModel';
import { PermissionCard } from './PermissionCard';
import type {
  GroupedTimelineItem,
  PermissionControls,
  RunViewEvent,
  ToolCardItem,
} from './types';
import styles from './Timeline.module.css';

/** Semantic conversation renderer for the Cloud task-detail surface. */
export function Timeline({
  events,
  isRunning = false,
  permissions,
}: {
  events: RunViewEvent[];
  isRunning?: boolean;
  permissions?: PermissionControls;
}) {
  const items = groupTimeline(events);
  const finalStatus = terminalStatusSeq(events);
  const rows: React.ReactNode[] = [];

  for (let index = 0; index < items.length; index += 1) {
    const item = items[index]!;
    if (isTool(item)) {
      const tools: ToolCardItem[] = [item];
      while (index + 1 < items.length && isTool(items[index + 1]!)) {
        tools.push(items[index + 1] as ToolCardItem);
        index += 1;
      }
      rows.push(<ToolProgress key={`tools-${tools[0]!.seq}`} tools={tools} />);
      continue;
    }
    rows.push(
      <TimelineRow
        key={`${item.kind}-${item.seq}`}
        item={item}
        finalStatus={finalStatus}
        permissions={permissions}
      />,
    );
  }

  return (
    <div className={styles.wrap} data-testid="event-timeline">
      <div className={styles.thread}>{rows}</div>
      {isRunning && (
        <div className={styles.pending} role="status" aria-label="Thinking…">
          <span className={styles.avatar} aria-hidden>JC</span>
          <span className={styles.dots} aria-hidden><i /><i /><i /></span>
          <span>Thinking</span>
        </div>
      )}
    </div>
  );
}

function isTool(item: GroupedTimelineItem): item is ToolCardItem {
  return item.kind === 'tool_card';
}

function TimelineRow({
  item,
  finalStatus,
  permissions,
}: {
  item: GroupedTimelineItem;
  finalStatus?: number;
  permissions?: PermissionControls;
}) {
  if (item.kind === 'text_block') {
    return (
      <article className={styles.message} data-testid="thread-message-assistant">
        <div className={styles.messageHead}>
          <span className={styles.avatar} aria-hidden>JC</span>
          <span className={styles.author}>jcode</span>
          <time>{timeLabel(item.ts)}</time>
        </div>
        <div className={styles.prose} dangerouslySetInnerHTML={{ __html: renderMarkdown(item.text) }} />
      </article>
    );
  }

  if (item.kind === 'user_message') {
    return (
      <article className={`${styles.message} ${styles.userMessage}`} data-testid="thread-message-user">
        <div className={styles.messageHead}>
          <span className={`${styles.avatar} ${styles.userAvatar}`} aria-hidden>U</span>
          <strong>{item.by || 'Project member'}</strong>
          <time>{timeLabel(item.ts)}</time>
        </div>
        <div className={styles.userBubble}>{item.prompt}</div>
      </article>
    );
  }

  if (item.kind === 'permission_card') {
    return <div className={styles.permission}><PermissionCard item={item} controls={permissions} /></div>;
  }

  return (
    <div
      className={styles.event}
      data-kind={item.kind}
      data-final={item.kind === 'status' && item.seq === finalStatus || undefined}
      data-testid="thread-event"
    >
      <span className={styles.eventRule} aria-hidden />
      <span className={styles.eventIcon} aria-hidden>{eventIcon(item)}</span>
      <div>
        <span>{eventLabel(item, finalStatus)}</span>
        {item.kind === 'unknown' && <pre>{item.raw}</pre>}
      </div>
      <time>{timeLabel(item.ts)}</time>
    </div>
  );
}

function ToolProgress({ tools }: { tools: ToolCardItem[] }) {
  return (
    <div className={styles.progress} data-testid="thread-progress">
      {tools.map((tool, index) => (
        <details className={styles.tool} key={tool.callId} data-status={tool.status} data-testid="thread-tool" open={tool.status === 'failed'}>
          <summary>
            <span className={styles.toolRail} aria-hidden>{index === tools.length - 1 ? '└' : '├'}</span>
            <span className={styles.toolIcon} aria-hidden>{tool.status === 'running' ? <CaretRight size={13} weight="bold" /> : tool.status === 'failed' ? <Warning size={13} weight="bold" /> : <Check size={13} weight="bold" />}</span>
            <strong>{toolTitle(tool)} <small>· {tool.tool}</small></strong>
            <span className={styles.toolStatus}>{tool.status === 'succeeded' ? 'Done' : tool.status}</span>
          </summary>
          <div className={styles.toolDetails}>
            {tool.args && tool.args !== '{}' && <pre>{tool.args}</pre>}
            {tool.output && <pre data-error={tool.isError || undefined}>{tool.output}</pre>}
          </div>
        </details>
      ))}
    </div>
  );
}

function toolTitle(tool: ToolCardItem): string {
  const labels: Record<string, string> = {
    read: 'Read project context',
    edit: 'Edit files',
    execute: 'Run command',
    search: 'Search workspace',
  };
  return labels[tool.tool] ?? tool.tool.replaceAll('_', ' ');
}

function eventIcon(item: GroupedTimelineItem): React.ReactNode {
  if (item.kind === 'failure') return <Warning size={15} weight="fill" />;
  if (item.kind === 'git') return <ArrowSquareOut size={15} weight="regular" />;
  if (item.kind === 'artifact') return <File size={15} weight="regular" />;
  if (item.kind === 'status') return item.status === 'succeeded' ? <Check size={15} weight="bold" /> : <Circle size={9} weight="fill" />;
  return <Circle size={9} weight="fill" />;
}

function eventLabel(item: GroupedTimelineItem, finalStatus?: number): string {
  switch (item.kind) {
    case 'status': {
      const status = sentence(item.status);
      return item.seq === finalStatus ? `Final status: ${status}` : `Status: ${status}`;
    }
    case 'failure': return item.reason ? `${item.message} · ${item.reason}` : item.message;
    case 'artifact': return `Artifact ready: ${item.artifact}`;
    case 'git': return item.commitSha ? `Pushed ${item.branch} at ${item.commitSha}` : `Pushed ${item.branch}`;
    case 'result': return item.message;
    case 'session_info':
    case 'session_finish': return item.message;
    case 'permission_resolved': return `Permission ${item.resolution === 'timeout' ? 'timed out' : 'resolved'}${item.optionId ? ` · ${item.optionId}` : ''}`;
    case 'unknown': return `Unknown event: ${item.type}`;
    case 'tool_call': return `${toolName(item.tool)} started`;
    case 'tool_result': return item.isError ? `${toolName(item.tool ?? 'tool')} failed` : `${toolName(item.tool ?? 'tool')} completed`;
    default: return '';
  }
}

function toolName(value: string): string {
  return value.replaceAll('_', ' ');
}

function sentence(value: string): string {
  const normalized = value.replaceAll('_', ' ');
  return normalized ? normalized[0]!.toUpperCase() + normalized.slice(1) : 'Unknown';
}

function timeLabel(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? ''
    : date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
