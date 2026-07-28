import {
  ActivityGroupCard,
  ExploringGroupCard,
  Message,
  ToolBatchGroupCard,
  ToolCallCard,
  TurnChangesCard,
  appendTurnChangeSummaries,
  groupActivityTimeline,
  isActivityItem,
  isApprovalItem,
  isBatchItem,
  isExploringItem,
  isMessageItem,
  isToolItem,
  isTurnChangesItem,
  type ThreadItem,
} from 'jcode-ui';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { PermissionCard } from '@jcloud/device-ui';
import { timelineCss as styles } from '@jcloud/device-ui';
import { toThreadItems, type CloudApproval, type CloudMessage } from './threadModel';
import type { PermissionControls, RunViewEvent } from './types';

/**
 * Cloud's event adapter around the canonical jcode-ui conversation components.
 *
 * Cloud keeps one host-specific branch: ACP permissions must echo an arbitrary
 * option_id, while jcode-ui's stock approval banner currently exposes a boolean
 * allow/deny contract. Messages, Markdown, tool grouping and every tool body
 * otherwise come directly from jcode-ui.
 */
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
  const items = useMemo(
    () => appendTurnChangeSummaries(groupActivityTimeline(toThreadItems(events)), { isRunning }),
    [events, isRunning],
  );

  return (
    <div className={styles.wrap} data-testid="event-timeline">
      <div className={styles.thread}>
        {items.map((item) => (
          <JcodeThreadItem
            key={`${item.kind}-${item.seq}`}
            item={item}
            isRunning={isRunning}
            permissions={permissions}
          />
        ))}
      </div>
      {isRunning && (
        <div
          className="jcode-pending jcode-chat-col"
          data-jcode-ui=""
          role="status"
          aria-live="polite"
          aria-label={t('run.thinkingAria')}
        >
          <div className="jcode-pending__inner jcode-gutter">
            <span className="jcode-pending__ring" aria-hidden="true"><span className="jcode-pending-ring" /></span>
            <span className="jcode-pending__label">{t('run.thinking')}</span>
          </div>
        </div>
      )}
    </div>
  );
}

function JcodeThreadItem({
  item,
  isRunning,
  permissions,
}: {
  item: ThreadItem;
  isRunning: boolean;
  permissions?: PermissionControls;
}) {
  if (isMessageItem(item)) {
    const message = item.data as CloudMessage;
    const testId = message.role === 'system' ? 'thread-message-system' : `thread-message-${message.role}`;
    return (
      <div data-testid={testId} data-event-kind={message.role === 'system' ? 'lifecycle' : undefined}>
        <Message
          message={message}
          canEdit={message.role === 'user' && !isRunning}
          slots={message.author ? {
            header: () => (
              <div className="mb-2 flex items-center gap-2.5">
                <div
                  className="jcode-msg-avatar flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-[10px] font-bold"
                  style={{ background: 'var(--jcode-color-foreground)', color: 'var(--jcode-color-surface)' }}
                  aria-hidden
                >
                  U
                </div>
                <span className="text-[11px] font-semibold tracking-wide">{message.author}</span>
              </div>
            ),
          } : undefined}
        />
      </div>
    );
  }
  if (isActivityItem(item)) {
    return <div className="jcode-chat-col"><ActivityGroupCard group={item.data} className="jcode-gutter" /></div>;
  }
  if (isToolItem(item)) {
    return <div className="jcode-chat-col"><ToolCallCard tool={item.data} className="jcode-gutter" /></div>;
  }
  if (isApprovalItem(item)) {
    const approval = item.data as CloudApproval;
    return <div className={styles.permission}><PermissionCard item={approval.permission} controls={permissions} /></div>;
  }
  if (isTurnChangesItem(item)) {
    return <div className="jcode-chat-col"><TurnChangesCard summary={item.data} className="jcode-gutter" /></div>;
  }
  // Legacy kinds are not produced by this adapter, but remain lossless if a
  // future jcode-ui grouping policy emits them.
  if (isExploringItem(item)) {
    return <div className="jcode-chat-col"><ExploringGroupCard group={item.data} className="jcode-gutter" /></div>;
  }
  if (isBatchItem(item)) {
    return <div className="jcode-chat-col"><ToolBatchGroupCard group={item.data} className="jcode-gutter" /></div>;
  }
  return null;
}
