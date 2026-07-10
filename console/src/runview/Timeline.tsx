import { Thread as HeadlessThread } from 'jcode-ui-core/primitives';
import { isApprovalItem, isMessageItem, isToolItem } from 'jcode-ui-core';
import type { ThreadItem } from 'jcode-ui-core';
import { Message, ToolCallCard, renderMarkdown } from 'jcode-ui';
import { PermissionCard } from './PermissionCard';
import type { PermissionControls } from './types';
import type { CloudApproval, CloudMessage } from './threadModel';
import styles from './Timeline.module.css';

/**
 * Cloud's host renderer for the published jcode-ui conversation primitives.
 * Messages and tools use the package verbatim. Permission requests retain the
 * Cloud renderer because ACP offers arbitrary option IDs, while jcode-ui 0.1.1's
 * ApprovalBanner only exposes a fixed allow/deny boolean contract.
 */
export function Timeline({ permissions }: { permissions?: PermissionControls }) {
  return (
    <div className={styles.wrap} data-testid="event-timeline">
      <HeadlessThread
        virtualize={false}
        className={`${styles.thread} jcode-thread messages-feather`}
        overscanBottom={24}
        renderItem={(item) => <ThreadRow item={item} permissions={permissions} />}
        renderPending={() => (
          <div className="jcode-pending jcode-chat-col" role="status" aria-label="Thinking…">
            <div className="jcode-pending__inner jcode-gutter">
              <span className="jcode-pending__dots" aria-hidden="true">
                <span className="jcode-pending-dot" />
                <span className="jcode-pending-dot" />
                <span className="jcode-pending-dot" />
              </span>
              <span className="jcode-pending__label">Thinking</span>
            </div>
          </div>
        )}
      />
    </div>
  );
}

function ThreadRow({
  item,
  permissions,
}: {
  item: ThreadItem;
  permissions?: PermissionControls;
}) {
  if (isMessageItem(item)) {
    const message = item.data as CloudMessage;
    if (message.role === 'user' && message.author) {
      return <AttributedUserMessage message={message} />;
    }
    // Cloud has no edit-and-replay endpoint. Never expose the package's edit
    // affordance with a no-op action (fail-visible product rule).
    return <Message message={item.data} canEdit={false} />;
  }
  if (isToolItem(item)) {
    return (
      <div className="jcode-chat-col">
        <ToolCallCard tool={item.data} className="jcode-gutter" />
      </div>
    );
  }
  if (isApprovalItem(item)) {
    const approval = item.data as CloudApproval;
    return (
      <div className="jcode-chat-col">
        <div className="jcode-gutter">
          <PermissionCard item={approval.permission} controls={permissions} />
        </div>
      </div>
    );
  }
  return null;
}

/**
 * jcode-ui@0.1.1 hard-codes every generic user heading to "You". Cloud sessions
 * are multi-user, so keep this narrow host row until the package exposes a
 * general author label. Markdown still uses the package's sanitized pipeline
 * and the row uses its published layout/style classes.
 */
function AttributedUserMessage({ message }: { message: CloudMessage }) {
  return (
    <div className="jcode-message jcode-chat-col py-3" data-role="user" data-source={message.source}>
      <div className="mb-2 flex items-center gap-2.5">
        <div
          className="jcode-msg-avatar flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-[10px] font-bold"
          style={{ background: 'var(--color-foreground)', color: 'var(--color-surface)' }}
          aria-hidden="true"
        >
          U
        </div>
        <span
          className="text-[11px] font-semibold tracking-wide"
          style={{ color: 'var(--color-foreground)' }}
        >
          {message.author}
        </span>
      </div>
      <div
        className="jcode-prose jcode-selectable jcode-gutter max-w-none break-words"
        dangerouslySetInnerHTML={{ __html: renderMarkdown(message.content) }}
      />
    </div>
  );
}
