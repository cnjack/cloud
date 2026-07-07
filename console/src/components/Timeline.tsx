/*
 * Timeline — the run's live event feed (PRD §6 "事件时间线").
 *
 * - agent.text        → prose block
 * - agent.tool_call   → collapsible mono block (args)
 * - agent.tool_result → collapsible mono block (output; error-tinted if is_error)
 * - run.status/failure/artifact → compact system rows
 *
 * Live-follow behaviour: auto-scrolls to the newest event, BUT pauses
 * auto-scroll the moment the user scrolls up (so they can read history while
 * events keep streaming). A "Jump to latest" affordance resumes it.
 */
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { RunEvent } from '../api/types';
import {
  toTimelineItem,
  terminalStatusSeq,
  type TimelineItem,
} from '../api/eventModel';
import { formatTime } from '../lib/format';
import { StatusBadge } from './StatusBadge';
import type { RunStatus } from '../api/types';
import styles from './Timeline.module.css';

const SCROLL_SLACK = 48; // px from bottom still counted as "at bottom"

export function Timeline({
  events,
  live,
}: {
  events: RunEvent[];
  live: boolean;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [pinned, setPinned] = useState(true);
  const [hasNew, setHasNew] = useState(false);
  const prevCount = useRef(events.length);

  // F7: the seq of the run's terminal status frame (undefined until the run ends).
  const terminalSeq = terminalStatusSeq(events);

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
          {events.map((ev) => (
            <TimelineRow
              key={ev.seq}
              item={toTimelineItem(ev)}
              // F7: mark the terminal run.status row as the final end state so it
              // reads unambiguously regardless of failure/status micro-ordering.
              final={ev.seq === terminalSeq}
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
}: {
  item: TimelineItem;
  final?: boolean;
}) {
  const time = formatTime(item.ts);

  switch (item.kind) {
    case 'text':
      return (
        <li className={styles.row} data-kind="text">
          <RowGutter time={time} marker="text" />
          <div className={styles.text}>{item.text}</div>
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
            <StatusBadge status={item.status as RunStatus} size="sm" />
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

function Collapsible({
  title,
  body,
  error,
  defaultOpen = false,
}: {
  title: React.ReactNode;
  body: string;
  error?: boolean;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const empty = !body.trim();
  return (
    <div className={styles.collapsible} data-error={error || undefined}>
      <button
        type="button"
        className={styles.collapseHead}
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        disabled={empty}
      >
        <span className={styles.chevron} data-open={open || undefined} aria-hidden>
          ▸
        </span>
        {title}
      </button>
      {open && !empty && (
        <pre className={styles.code}>
          <code>{body}</code>
        </pre>
      )}
    </div>
  );
}
