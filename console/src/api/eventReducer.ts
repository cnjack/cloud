/*
 * eventReducer.ts — pure reducer for a run's event timeline.
 *
 * The stream contract is "replay events with seq > after_seq, then live". On a
 * refresh/reconnect we first GET the historical events, then open the SSE
 * stream which replays from our last seq and continues live. That means events
 * CAN arrive out of order and CAN be delivered twice. This reducer is the
 * single place that guarantees:
 *
 *   1. dedupe by seq        — an event with a seq we already hold is dropped
 *   2. total ordering by seq — the visible list is always sorted ascending
 *   3. derived run status    — the latest run.status payload wins
 *
 * It is pure (no React, no I/O) so it is exhaustively unit-tested in
 * eventReducer.test.ts. The hook (useRunStream) is a thin wrapper over it.
 */
import type { RunEvent, RunStatus } from './types';

export interface EventState {
  /** Events sorted ascending by seq, deduped. */
  events: RunEvent[];
  /** Highest seq seen (0 if none) — the cursor for the next stream/replay. */
  lastSeq: number;
  /** Latest status derived from run.status events, if any. */
  derivedStatus?: RunStatus;
  /** ST-1: draft PR link, from the latest run.status frame that carries pr_url. */
  prURL?: string;
  prNumber?: number;
  /** Set of seqs held, for O(1) dedupe. Not for rendering. */
  seen: Set<number>;
}

export function initialEventState(): EventState {
  return { events: [], lastSeq: 0, seen: new Set() };
}

/**
 * Merge one or more incoming events into state. Returns a NEW state object when
 * anything changed, or the SAME reference when every incoming event was a
 * duplicate (lets React bail out of a re-render).
 */
export function reduceEvents(
  state: EventState,
  incoming: RunEvent | RunEvent[],
): EventState {
  const batch = Array.isArray(incoming) ? incoming : [incoming];

  // Filter to genuinely new, well-formed events.
  const fresh: RunEvent[] = [];
  const seenInBatch = new Set<number>();
  for (const ev of batch) {
    if (ev == null || typeof ev.seq !== 'number' || !Number.isFinite(ev.seq)) {
      continue;
    }
    if (state.seen.has(ev.seq) || seenInBatch.has(ev.seq)) continue;
    seenInBatch.add(ev.seq);
    fresh.push(ev);
  }

  if (fresh.length === 0) return state;

  // Insert while keeping the list sorted by seq. Most events arrive in order
  // (append to tail), so we fast-path that and only sort when needed. `tailSeq`
  // tracks the running tail so an out-of-order event anywhere in the batch is
  // detected (not just relative to the pre-batch tail).
  const events = state.events.slice();
  let needsSort = false;
  let tailSeq = events.length ? events[events.length - 1]!.seq : -Infinity;
  for (const ev of fresh) {
    if (ev.seq < tailSeq) needsSort = true;
    else tailSeq = ev.seq;
    events.push(ev);
  }
  if (needsSort) events.sort((a, b) => a.seq - b.seq);

  const seen = new Set(state.seen);
  let lastSeq = state.lastSeq;
  let derivedStatus = state.derivedStatus;
  let prURL = state.prURL;
  let prNumber = state.prNumber;

  for (const ev of fresh) {
    seen.add(ev.seq);
    if (ev.seq > lastSeq) lastSeq = ev.seq;
  }

  // Derive status from the run.status event with the HIGHEST seq (latest wins),
  // independent of arrival order.
  let bestStatusSeq = -1;
  // Draft-PR link (ST-1): the reconciler re-emits run.status carrying pr_url
  // once the PR is opened (after the run is terminal). Take it from the
  // highest-seq status frame that has one.
  let bestPRSeq = -1;
  for (const ev of events) {
    if (ev.type === 'run.status' && typeof ev.payload?.status === 'string') {
      if (ev.seq > bestStatusSeq) {
        bestStatusSeq = ev.seq;
        derivedStatus = ev.payload.status as RunStatus;
      }
      const url = ev.payload.pr_url;
      if (typeof url === 'string' && url !== '' && ev.seq > bestPRSeq) {
        bestPRSeq = ev.seq;
        prURL = url;
        prNumber =
          typeof ev.payload.pr_number === 'number'
            ? ev.payload.pr_number
            : prNumber;
      }
    }
  }

  return { events, lastSeq, derivedStatus, prURL, prNumber, seen };
}
