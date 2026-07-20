/*
 * DeviceTimeline.tsx — renders one device session: grouped durable items
 * (groupDeviceEvents; assistant text comes from durable agent_message events)
 * followed by not-yet-superseded local finalized text blocks and the live
 * streaming bubble. Reuses runview's timeline CSS and PermissionCard so
 * the device view reads identically to the run detail view.
 */
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { renderMarkdown } from 'jcode-ui';
import { CaretRight, Check, Circle, Prohibit, Warning } from '@phosphor-icons/react';
import { PermissionCard } from '../runview';
import type { PermissionCardItem, PermissionControls } from '../runview';
import { groupDeviceEvents } from './grouping';
import type {
  DeviceApprovalItem,
  DeviceToolCardItem,
  DeviceViewEvent,
  DeviceViewItem,
} from './types';
import type { FinalizedText } from './sessionReducer';
import run from '../runview/Timeline.module.css';
import styles from './DeviceTimeline.module.css';

export interface DeviceApprovalControls {
  /** approve | approve_all | deny */
  onDecide: (approvalId: string, decision: string) => void;
  /** True greys every approval button (device offline / send in flight). */
  disabled?: boolean;
}

export function DeviceTimeline({
  events,
  finalizedText,
  streamingText,
  agentRunning,
  approvals,
}: {
  events: DeviceViewEvent[];
  finalizedText: FinalizedText[];
  streamingText: string;
  agentRunning: boolean;
  approvals?: DeviceApprovalControls;
}) {
  const { t } = useTranslation();
  const items = useMemo(() => groupDeviceEvents(events), [events]);
  const rows: React.ReactNode[] = [];

  for (let i = 0; i < items.length; i += 1) {
    const item = items[i]!;
    if (item.kind === 'tool_card') {
      const tools: DeviceToolCardItem[] = [item];
      while (i + 1 < items.length && items[i + 1]!.kind === 'tool_card') {
        tools.push(items[i + 1] as DeviceToolCardItem);
        i += 1;
      }
      rows.push(<ToolGroup key={`tools-${tools[0]!.seq}`} tools={tools} t={t} />);
      continue;
    }
    rows.push(<Row key={`${item.kind}-${item.seq}`} item={item} approvals={approvals} t={t} />);
  }

  for (const block of finalizedText) {
    rows.push(<AssistantMessage key={`local-${block.id}`} text={block.text} />);
  }
  if (streamingText) {
    rows.push(<AssistantMessage key="streaming" text={streamingText} streaming />);
  }

  return (
    <div className={run.wrap} data-testid="device-timeline">
      <div className={run.thread}>{rows}</div>
      {agentRunning && !streamingText && (
        <div className={run.pending} role="status" aria-label={t('device.session.thinkingAria')}>
          <span className={run.avatar} aria-hidden>JC</span>
          <span className={run.dots} aria-hidden><i /><i /><i /></span>
          <span>{t('device.session.thinking')}</span>
        </div>
      )}
    </div>
  );
}

function AssistantMessage({ text, streaming = false }: { text: string; streaming?: boolean }) {
  return (
    <article className={run.message} data-testid="device-message-assistant" data-streaming={streaming || undefined}>
      <div className={run.messageHead}>
        <span className={run.avatar} aria-hidden>JC</span>
        <span className={run.author}>jcode</span>
      </div>
      <div className={run.prose} dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }} />
    </article>
  );
}

function Row({
  item,
  approvals,
  t,
}: {
  item: DeviceViewItem;
  approvals?: DeviceApprovalControls;
  t: TFunction;
}) {
  if (item.kind === 'user_message') {
    return (
      <article className={`${run.message} ${run.userMessage}`} data-testid="device-message-user">
        <div className={run.messageHead}>
          <span className={`${run.avatar} ${run.userAvatar}`} aria-hidden>U</span>
          <strong>{t('device.session.you')}</strong>
          {item.source && <span className={styles.channelBadge}>{item.source}</span>}
          <time>{timeLabel(item.ts)}</time>
        </div>
        <div className={run.userBubble}>{item.content}</div>
      </article>
    );
  }

  if (item.kind === 'assistant_message') {
    return <AssistantMessage text={item.text} />;
  }

  if (item.kind === 'approval_card') {
    return (
      <div className={run.permission}>
        <ApprovalRow item={item} approvals={approvals} t={t} />
      </div>
    );
  }

  if (item.kind === 'ask_user') {
    return (
      <div className={run.event} data-kind="ask_user" data-testid="device-event">
        <span className={run.eventRule} aria-hidden />
        <span className={run.eventIcon} aria-hidden><Circle size={9} weight="fill" /></span>
        <div>
          <span>{t('device.session.askUser')}</span>
          {item.questions.map((q, i) => (
            <span key={i} className={styles.askQuestion}>{q.header ? `${q.header}: ` : ''}{q.question}</span>
          ))}
        </div>
        <time>{timeLabel(item.ts)}</time>
      </div>
    );
  }

  return (
    <div className={run.event} data-kind={item.kind} data-testid="device-event">
      <span className={run.eventRule} aria-hidden />
      <span className={run.eventIcon} aria-hidden>{eventIcon(item)}</span>
      <div>
        <span>{eventLabel(item, t)}</span>
        {item.kind === 'unknown' && <pre>{item.raw}</pre>}
      </div>
      <time>{timeLabel(item.ts)}</time>
    </div>
  );
}

/** jcode approvals carry no option list — the decision vocabulary is fixed. */
function ApprovalRow({
  item,
  approvals,
  t,
}: {
  item: DeviceApprovalItem;
  approvals?: DeviceApprovalControls;
  t: TFunction;
}) {
  const card: PermissionCardItem = {
    kind: 'permission_card',
    seq: item.seq,
    ts: item.ts,
    requestId: item.approvalId,
    toolCallId: item.toolCallId,
    title: item.toolArgs ? `${item.toolName} — ${firstLine(item.toolArgs)}` : item.toolName,
    options: [
      { optionId: 'approve', name: t('device.session.approve'), kind: 'allow_once' },
      { optionId: 'approve_all', name: t('device.session.approveAll'), kind: 'allow_always' },
      { optionId: 'deny', name: t('device.session.deny'), kind: 'reject_once' },
    ],
    status: 'pending',
  };
  const controls: PermissionControls = {
    onDecide: approvals ? (requestId, optionId) => approvals.onDecide(requestId, optionId) : undefined,
    disabled: approvals?.disabled,
    decided: item.status === 'answered' && item.decision ? { [item.approvalId]: item.decision } : undefined,
  };
  return <PermissionCard item={card} controls={controls} />;
}

function ToolGroup({ tools, t }: { tools: DeviceToolCardItem[]; t: TFunction }) {
  return (
    <div className={run.progress} data-testid="device-tools">
      {tools.map((tool, index) => (
        <details
          className={run.tool}
          key={`${tool.callId ?? tool.seq}`}
          data-status={tool.status}
          data-testid="device-tool"
          open={tool.status === 'failed'}
        >
          <summary>
            <span className={run.toolRail} data-last={index === tools.length - 1 || undefined} aria-hidden />
            <span className={run.toolIcon} aria-hidden>{toolIcon(tool.status)}</span>
            <strong>
              {tool.title ?? tool.tool.replaceAll('_', ' ')} <small>· {tool.subtitle ?? tool.tool}</small>
            </strong>
            <span className={run.toolStatus}>{toolStatusLabel(tool.status, t)}</span>
          </summary>
          <div className={run.toolDetails}>
            {tool.args && tool.args !== '{}' && <pre>{tool.args}</pre>}
            {tool.output && <pre data-error={tool.status === 'failed' || undefined}>{tool.output}</pre>}
          </div>
        </details>
      ))}
    </div>
  );
}

function toolIcon(status: DeviceToolCardItem['status']): React.ReactNode {
  switch (status) {
    case 'running': return <CaretRight size={13} weight="bold" />;
    case 'failed': return <Warning size={13} weight="bold" />;
    case 'denied': return <Prohibit size={13} weight="bold" />;
    default: return <Check size={13} weight="bold" />;
  }
}

function toolStatusLabel(status: DeviceToolCardItem['status'], t: TFunction): string {
  switch (status) {
    case 'succeeded': return t('run.toolStatusDone');
    case 'denied': return t('device.session.toolDenied');
    default: return t(`run.toolStatus.${status}`, { defaultValue: status });
  }
}

function eventIcon(item: DeviceViewItem): React.ReactNode {
  if (item.kind === 'status' && item.eventKind === 'agent_done') {
    return item.errorMessage ? <Warning size={15} weight="fill" /> : <Check size={15} weight="bold" />;
  }
  return <Circle size={9} weight="fill" />;
}

function eventLabel(item: DeviceViewItem, t: TFunction): string {
  switch (item.kind) {
    case 'status':
      switch (item.eventKind) {
        case 'agent_start': return t('device.session.agentStarted');
        case 'agent_done':
          if (item.errorMessage) return t('device.session.agentFailed', { message: item.errorMessage });
          if (item.stopped) return t('device.session.agentStopped');
          return t('device.session.agentDone');
        case 'task_status':
          return item.status === 'running' ? t('device.session.taskRunning') : t('device.session.taskIdle');
        case 'mode_changed': return t('device.session.modeChanged', { mode: item.mode ?? '?' });
        case 'model_changed':
          return t('device.session.modelChanged', {
            model: item.provider ? `${item.provider}/${item.model ?? ''}` : (item.model ?? '?'),
          });
        case 'session_reset': return t('device.session.sessionReset');
        case 'todo_update': return t('device.session.todoUpdated');
        case 'goal_update':
          return item.goalObjective
            ? t('device.session.goalUpdated', { objective: item.goalObjective })
            : t('device.session.goalCleared');
        default: return item.eventKind;
      }
    case 'subagent':
      if (!item.done) return t('device.session.subagentStarted', { name: item.name });
      if (item.error) return t('device.session.subagentFailed', { name: item.name });
      return t('device.session.subagentDone', { name: item.name });
    case 'unknown': return t('run.unknownEvent', { type: item.eventKind });
    default: return '';
  }
}

function firstLine(value: string): string {
  const line = value.split('\n', 1)[0] ?? value;
  return line.length > 120 ? `${line.slice(0, 120)}…` : line;
}

function timeLabel(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? ''
    : date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
