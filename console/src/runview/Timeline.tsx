import { renderMarkdown } from 'jcode-ui';
import { ArrowSquareOut, CaretRight, Check, Circle, File, Warning } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { groupTimeline } from './grouping';
import { terminalStatusSeq } from './eventModel';
import { PermissionCard } from '@jcloud/device-ui';
import type {
  GroupedTimelineItem,
  PermissionControls,
  RunViewEvent,
  ToolCardItem,
} from './types';
import { timelineCss as styles } from '@jcloud/device-ui';

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
  const { t } = useTranslation();
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
      rows.push(<ToolProgress key={`tools-${tools[0]!.seq}`} tools={tools} t={t} />);
      continue;
    }
    rows.push(
      <TimelineRow
        key={`${item.kind}-${item.seq}`}
        item={item}
        finalStatus={finalStatus}
        permissions={permissions}
        t={t}
      />,
    );
  }

  return (
    <div className={styles.wrap} data-testid="event-timeline">
      <div className={styles.thread}>{rows}</div>
      {isRunning && (
        <div className={styles.pending} role="status" aria-label={t('run.thinkingAria')}>
          <span className={styles.avatar} aria-hidden>JC</span>
          <span className={styles.dots} aria-hidden><i /><i /><i /></span>
          <span>{t('run.thinking')}</span>
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
  t,
}: {
  item: GroupedTimelineItem;
  finalStatus?: number;
  permissions?: PermissionControls;
  t: TFunction;
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
          <strong>{item.by || t('run.projectMember')}</strong>
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
        <span>{eventLabel(item, finalStatus, t)}</span>
        {item.kind === 'unknown' && <pre>{item.raw}</pre>}
      </div>
      <time>{timeLabel(item.ts)}</time>
    </div>
  );
}

function ToolProgress({ tools, t }: { tools: ToolCardItem[]; t: TFunction }) {
  return (
    <div className={styles.progress} data-testid="thread-progress">
      {tools.map((tool, index) => (
        <details className={styles.tool} key={tool.callId} data-status={tool.status} data-testid="thread-tool" open={tool.status === 'failed'}>
          <summary>
            <span
              className={styles.toolRail}
              data-last={index === tools.length - 1 || undefined}
              data-testid="thread-tool-rail"
              aria-hidden
            />
            <span className={styles.toolIcon} aria-hidden>{tool.status === 'running' ? <CaretRight size={13} weight="bold" /> : tool.status === 'failed' ? <Warning size={13} weight="bold" /> : <Check size={13} weight="bold" />}</span>
            <strong>{toolTitle(tool, t)} <small>· {tool.tool}</small></strong>
            <span className={styles.toolStatus}>{tool.status === 'succeeded' ? t('run.toolStatusDone') : toolStatusLabel(tool.status, t)}</span>
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

function toolTitle(tool: ToolCardItem, t: TFunction): string {
  const known = new Set(['read', 'edit', 'execute', 'search']);
  return known.has(tool.tool) ? t(`run.tool.${tool.tool}`) : tool.tool.replaceAll('_', ' ');
}

/** Localised label for a non-succeeded tool status; unknown tokens pass through. */
function toolStatusLabel(status: string, t: TFunction): string {
  const known = new Set(['running', 'failed', 'queued', 'canceled']);
  return known.has(status) ? t(`run.toolStatus.${status}`) : status;
}

function eventIcon(item: GroupedTimelineItem): React.ReactNode {
  if (item.kind === 'failure') return <Warning size={15} weight="fill" />;
  if (item.kind === 'git') return <ArrowSquareOut size={15} weight="regular" />;
  if (item.kind === 'artifact') return <File size={15} weight="regular" />;
  if (item.kind === 'status') return item.status === 'succeeded' ? <Check size={15} weight="bold" /> : <Circle size={9} weight="fill" />;
  return <Circle size={9} weight="fill" />;
}

function eventLabel(item: GroupedTimelineItem, finalStatus: number | undefined, t: TFunction): string {
  switch (item.kind) {
    case 'status': {
      const status = statusName(item.status, t);
      return item.seq === finalStatus ? t('run.finalStatus', { status }) : t('run.status', { status });
    }
    // failure.message is backend-provided text (dynamic) — rendered as-is.
    case 'failure': return item.reason ? `${item.message} · ${item.reason}` : item.message;
    case 'artifact': return t('run.artifactReady', { artifact: item.artifact });
    case 'git': return item.commitSha ? t('run.pushedAt', { branch: item.branch, sha: item.commitSha }) : t('run.pushed', { branch: item.branch });
    // Re-derive the deterministic result/session copy from the structured
    // fields so it localises (eventModel stays English-decoupled).
    case 'result': return item.outcome === 'no_changes' ? t('run.result.noChanges') : (item.outcome || t('run.result.generic'));
    case 'session_info': return item.resumed ? t('run.session.resumed') : t('run.session.established');
    case 'session_finish': return item.reason === 'idle_timeout' ? t('run.session.finishedIdle') : t('run.session.finished');
    case 'permission_resolved': {
      const base = item.resolution === 'timeout' ? t('run.permissionTimedOut') : t('run.permissionResolved');
      return item.optionId ? `${base} · ${item.optionId}` : base;
    }
    case 'unknown': return t('run.unknownEvent', { type: item.type });
    case 'tool_call': return t('run.toolStarted', { tool: toolName(item.tool) });
    case 'tool_result': return item.isError ? t('run.toolFailed', { tool: toolName(item.tool ?? 'tool') }) : t('run.toolCompleted', { tool: toolName(item.tool ?? 'tool') });
    default: return '';
  }
}

function toolName(value: string): string {
  return value.replaceAll('_', ' ');
}

/** Localised run-status name; unknown tokens are sentence-cased as before. */
function statusName(value: string, t: TFunction): string {
  const known = new Set(['queued', 'scheduling', 'running', 'awaiting_input', 'succeeded', 'failed', 'canceled', 'blocked']);
  if (known.has(value)) return t(`run.statusName.${value}`);
  const normalized = value.replaceAll('_', ' ');
  return normalized ? normalized[0]!.toUpperCase() + normalized.slice(1) : t('run.unknownStatus');
}

function timeLabel(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? ''
    : date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
