import { describe, expect, it } from 'vitest';
import {
  initialEventState,
  reduceEvents,
  type EventState,
} from './eventReducer';
import type { RunEvent } from './types';

function ev(seq: number, type = 'agent.text', payload = {}): RunEvent {
  const ms = Number.isFinite(seq) ? seq * 1000 : 0;
  return { seq, ts: new Date(ms).toISOString(), type, payload };
}

function seqs(state: EventState): number[] {
  return state.events.map((e) => e.seq);
}

describe('reduceEvents', () => {
  it('appends in-order events and tracks lastSeq', () => {
    let s = initialEventState();
    s = reduceEvents(s, [ev(1), ev(2), ev(3)]);
    expect(seqs(s)).toEqual([1, 2, 3]);
    expect(s.lastSeq).toBe(3);
  });

  it('dedupes events by seq (replay overlap then live)', () => {
    // Simulate: replay 1..3 via GET, then SSE replays 2..5 (overlap 2,3).
    let s = initialEventState();
    s = reduceEvents(s, [ev(1), ev(2), ev(3)]);
    s = reduceEvents(s, [ev(2), ev(3), ev(4), ev(5)]);
    expect(seqs(s)).toEqual([1, 2, 3, 4, 5]);
    expect(s.lastSeq).toBe(5);
  });

  it('sorts out-of-order arrivals by seq', () => {
    let s = initialEventState();
    s = reduceEvents(s, [ev(3), ev(1), ev(2)]);
    expect(seqs(s)).toEqual([1, 2, 3]);

    // A late-arriving earlier event still slots into place.
    s = reduceEvents(s, ev(0));
    expect(seqs(s)).toEqual([0, 1, 2, 3]);
  });

  it('drops duplicates within a single batch', () => {
    let s = initialEventState();
    s = reduceEvents(s, [ev(1), ev(1), ev(2), ev(2)]);
    expect(seqs(s)).toEqual([1, 2]);
  });

  it('returns the SAME reference when every event is a duplicate', () => {
    let s = initialEventState();
    s = reduceEvents(s, [ev(1), ev(2)]);
    const same = reduceEvents(s, [ev(1), ev(2)]);
    expect(same).toBe(s); // lets React bail out of re-render
  });

  it('returns a NEW reference when something changed', () => {
    let s = initialEventState();
    const next = reduceEvents(s, ev(1));
    expect(next).not.toBe(s);
  });

  it('ignores malformed events (missing/NaN seq)', () => {
    let s = initialEventState();
    // @ts-expect-error intentionally malformed
    s = reduceEvents(s, [{ type: 'agent.text', payload: {} }, ev(1)]);
    expect(seqs(s)).toEqual([1]);
    s = reduceEvents(s, ev(Number.NaN));
    expect(seqs(s)).toEqual([1]);
  });

  it('derives status from the highest-seq run.status regardless of arrival order', () => {
    let s = initialEventState();
    s = reduceEvents(s, [
      ev(1, 'run.status', { status: 'queued' }),
      ev(3, 'run.status', { status: 'running' }),
    ]);
    expect(s.derivedStatus).toBe('running');

    // A late lower-seq status must NOT override the newer one.
    s = reduceEvents(s, ev(2, 'run.status', { status: 'scheduling' }));
    expect(s.derivedStatus).toBe('running');

    // A newer status does win.
    s = reduceEvents(s, ev(4, 'run.status', { status: 'succeeded' }));
    expect(s.derivedStatus).toBe('succeeded');
  });

  it('leaves derivedStatus undefined when no run.status events seen', () => {
    let s = initialEventState();
    s = reduceEvents(s, [ev(1), ev(2, 'agent.tool_call')]);
    expect(s.derivedStatus).toBeUndefined();
  });

  it('derives the draft-PR link from the run.status frame carrying pr_url (ST-1)', () => {
    let s = initialEventState();
    // Terminal succeeded first (no PR yet).
    s = reduceEvents(s, ev(4, 'run.status', { status: 'succeeded' }));
    expect(s.prURL).toBeUndefined();
    // The reconciler re-emits run.status with pr_url once the PR is opened.
    s = reduceEvents(
      s,
      ev(5, 'run.status', {
        status: 'succeeded',
        pr_url: 'https://gitea.local/o/r/pulls/42',
        pr_number: 42,
      }),
    );
    expect(s.prURL).toBe('https://gitea.local/o/r/pulls/42');
    expect(s.prNumber).toBe(42);
    expect(s.derivedStatus).toBe('succeeded');
  });
});
