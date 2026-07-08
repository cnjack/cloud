/*
 * Timeline — the run's live event feed (PRD §6 "事件时间线").
 *
 * Renders the groupTimeline() projection (grouping.ts), not the raw event
 * list:
 * - text_block  → one markdown prose block per run of agent.text chunks
 * - tool_card   → one card per paired agent.tool_call/agent.tool_result
 * - tool_call/tool_result → degraded standalone rows for orphans that
 *   couldn't be paired (no call_id, or no matching counterpart)
 * - run.status/failure/artifact/git/result → compact system rows
 *
 * Live-follow behaviour: auto-scrolls to the newest event, BUT pauses
 * auto-scroll the moment the user scrolls up (so they can read history while
 * events keep streaming). A "Jump to latest" affordance resumes it.
 */
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { toTimelineItem, terminalStatusSeq } from './eventModel';
import { groupTimeline } from './grouping';
import type { GroupedTimelineItem, PermissionControls, RunViewEvent } from './types';
import { Collapsible } from './Collapsible';
import { MessageBlock } from './MessageBlock';
import { PermissionCard } from './PermissionCard';
import { StatusPill } from './StatusPill';
import { ToolCard } from './ToolCard';
import styles from './Timeline.module.css';

const SCROLL_SLACK = 48; // px from bottom still counted as "at bottom"

export function Timeline({
  events,
  live,
  permissions,
}: {
  events: RunViewEvent[];
  live: boolean;
  /**
   * F8b: host-injected controls for pending PermissionCards (decide callback +
   * optimistic/read-only state). Optional — without it the cards render inert,
   * keeping runview usable by hosts that have no approval surface.
   */
  permissions?: PermissionControls;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [pinned, setPinned] = useState(true);
  const [hasNew, setHasNew] = useState(false);
  const prevCount = useRef(events.length);

  // F7: the seq of the run's terminal status frame (undefined until the run ends).
  const terminalSeq = terminalStatusSeq(events);
  const grouped = useMemo(() => groupTimeline(events), [events]);

  // Track whether the user is at the bottom; pause auto-scroll if they scroll up.
  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const atBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight <= SCROLL_SLACK;
    setPinned(atBottom);
    if (atBottom) setHasNew(false);
  };

  // On new events: if pinned, stick to bottom; else flag "new events below".
  useLayoutEffect(() => {
    const grew = events.length > prevCount.current;
    prevCount.current = events.length;
    if (!grew) return;
    const el = scrollRef.current;
    if (!el) return;
    if (pinned) {
      el.scrollTop = el.scrollHeight;
    } else {
      setHasNew(true);
    }
  }, [events.length, pinned]);

  // Initial mount: scroll to bottom.
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const jumpToLatest = () => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    setPinned(true);
    setHasNew(false);
  };

  return (
    <div className={styles.wrap}>
      <div
        className={styles.scroll}
        ref={scrollRef}
        onScroll={onScroll}
        data-testid="event-timeline"
      >
        <ol className={styles.list}>
          {grouped.map((item) => (
            <TimelineRow
              key={item.seq}
              item={item}
              // F7: mark the terminal run.status row as the final end state so it
              // reads unambiguously regardless of failure/status micro-ordering.
              final={item.kind === 'status' && item.seq === terminalSeq}
              permissions={permissions}
            />
          ))}
        </ol>
        {live && (
          <div className={styles.liveRow} aria-hidden>
            <span className={styles.liveDot} />
            <span className={styles.liveLabel}>streaming…</span>
          </div>
        )}
      </div>

      {hasNew && !pinned && (
        <button className={styles.jump} onClick={jumpToLatest} type="button">
          ↓ New events
        </button>
      )}
    </div>
  );
}

function TimelineRow({
  item,
  final = false,
  permissions,
}: {
  item: GroupedTimelineItem;
  final?: boolean;
  permissions?: PermissionControls;
}) {
  const time = formatTime(item.ts);

  switch (item.kind) {
    case 'text_block':
      return (
        <li className={styles.row} data-kind="text_block">
          <RowGutter time={time} marker="text" />
          <MessageBlock text={item.text} />
        </li>
      );

    case 'tool_card':
      return (
        <li className={styles.row} data-kind="tool_card">
          <RowGutter time={time} marker={item.isError ? 'error' : 'call'} />
          <ToolCard item={item} />
        </li>
      );

    case 'tool_call':
      return (
        <li className={styles.row} data-kind="tool_call">
          <RowGutter time={time} marker="call" />
          <Collapsible
            title={
              <>
                <span className={styles.toolVerb}>call</span>
                <span className={styles.toolName}>{item.tool}</span>
              </>
            }
            body={item.args}
          />
        </li>
      );

    case 'tool_result':
      return (
        <li className={styles.row} data-kind="tool_result">
          <RowGutter time={time} marker={item.isError ? 'error' : 'result'} />
          <Collapsible
            error={item.isError}
            defaultOpen={item.isError}
            title={
              <>
                <span className={styles.toolVerb}>{item.isError ? 'error' : 'result'}</span>
                {item.tool && <span className={styles.toolName}>{item.tool}</span>}
              </>
            }
            body={item.output}
          />
        </li>
      );

    case 'status':
      return (
        <li
          className={styles.row}
          data-kind="status"
          data-final={final || undefined}
        >
          <RowGutter time={time} marker="status" />
          <div className={styles.sysRow}>
            <span className={styles.sysLabel}>{final ? 'final status' : 'status'}</span>
            <StatusPill status={item.status} />
            {final && (
              <span className={styles.finalTag} data-testid="timeline-final">
                end of run
              </span>
            )}
          </div>
        </li>
      );

    case 'failure':
      return (
        <li className={styles.row} data-kind="failure">
          <RowGutter time={time} marker="error" />
          <div className={styles.failure}>
            <span className={styles.failReason}>{item.reason ?? 'failure'}</span>
            <span className={styles.failMsg}>{item.message}</span>
          </div>
        </li>
      );

    case 'artifact':
      return (
        <li className={styles.row} data-kind="artifact">
          <RowGutter time={time} marker="artifact" />
          <div className={styles.sysRow}>
            <span className={styles.sysLabel}>artifact ready</span>
            <code className={styles.artifactKind}>{item.artifact}</code>
          </div>
        </li>
      );

    case 'git':
      return (
        <li className={styles.row} data-kind="git">
          <RowGutter time={time} marker="artifact" />
          <div className={styles.sysRow}>
            <span className={styles.sysLabel}>pushed branch</span>
            <code className={styles.artifactKind}>{item.branch}</code>
            {item.commitSha && (
              <code className={styles.artifactKind}>@ {item.commitSha}</code>
            )}
          </div>
        </li>
      );

    case 'result':
      return (
        <li className={styles.row} data-kind="result" data-testid="timeline-result">
          <RowGutter time={time} marker="dot" />
          <div className={styles.sysRow}>
            <span className={styles.sysLabel}>result</span>
            <span className={styles.resultMsg}>{item.message}</span>
          </div>
        </li>
      );

    // D22 session: the user's follow-up prompt — a right-aligned chat bubble so
    // the timeline reads as a continuous conversation with the agent.
    case 'user_message':
      return (
        <li className={styles.row} data-kind="user_message" data-testid="timeline-user-message">
          <RowGutter time={time} marker="text" />
          <div className={styles.userMessage}>
            <span className={styles.userMessageBy}>{item.by || 'you'}</span>
            <span className={styles.userMessageText}>{item.prompt}</span>
          </div>
        </li>
      );

    // F8b: a permission request card (pending buttons / resolved outcome).
    case 'permission_card':
      return (
        <li className={styles.row} data-kind="permission_card">
          <RowGutter time={time} marker={item.status === 'pending' ? 'call' : 'status'} />
          <PermissionCard item={item} controls={permissions} />
        </li>
      );

    // F8b: an ORPHAN resolution (its request event never arrived) — a compact
    // system row so the outcome is still visible.
    case 'permission_resolved':
      return (
        <li className={styles.row} data-kind="permission_resolved">
          <RowGutter time={time} marker="status" />
          <div className={styles.sysRow}>
            <span className={styles.sysLabel}>permission</span>
            <span className={styles.resultMsg}>
              {item.resolution === 'timeout' ? 'timed out' : 'resolved'}
              {item.optionId ? ` — ${item.optionId}` : ''}
            </span>
          </div>
        </li>
      );

    // D22 session: the wind-down marker (user Finish / idle timeout).
    case 'session_finish':
      return (
        <li className={styles.row} data-kind="session_finish" data-testid="timeline-session-finish">
          <RowGutter time={time} marker="status" />
          <div className={styles.sysRow}>
            <span className={styles.sysLabel}>session</span>
            <span className={styles.resultMsg}>{item.message}</span>
          </div>
        </li>
      );

    default:
      return (
        <li className={styles.row} data-kind="unknown">
          <RowGutter time={time} marker="dot" />
          <Collapsible title={<span className={styles.toolName}>{item.type}</span>} body={item.raw} />
        </li>
      );
  }
}

function RowGutter({ time, marker }: { time: string; marker: string }) {
  return (
    <div className={styles.gutter}>
      <span className={styles.marker} data-marker={marker} aria-hidden />
      <time className={styles.time}>{time}</time>
    </div>
  );
}

// Kept local (no formatting-lib import) to avoid a dependency on the host's
// lib/format.ts — see README.md. Deliberately minimal: HH:MM:SS, same as the
// host's formatTime for the common case.
function formatTime(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

// Re-exported so a host can build its own row dispatch on top of the raw
// (ungrouped) per-event view model if it ever needs to.
export { toTimelineItem };
