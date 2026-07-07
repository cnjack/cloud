import { describe, expect, it } from 'vitest';
import { terminalStatusSeq, toTimelineItem } from './eventModel';
import type { RunEvent } from './types';

function ev(seq: number, type: string, payload: Record<string, unknown> = {}): RunEvent {
  return { seq, ts: new Date(seq * 1000).toISOString(), type, payload };
}

describe('terminalStatusSeq (F7 — terminal end-state marker)', () => {
  it('returns undefined while the run has no terminal status yet', () => {
    const events = [
      ev(1, 'run.status', { status: 'queued' }),
      ev(2, 'run.status', { status: 'scheduling' }),
      ev(3, 'run.status', { status: 'running' }),
    ];
    expect(terminalStatusSeq(events)).toBeUndefined();
  });

  it('marks the terminal run.status(failed) as the final row', () => {
    const events = [
      ev(1, 'run.status', { status: 'running' }),
      ev(2, 'agent.tool_result', { tool: 'git.clone', ok: false, exit_code: 128 }),
      ev(3, 'run.failure', { reason: 'clone_failed', message: 'not found' }),
      ev(4, 'run.status', { status: 'failed' }),
    ];
    expect(terminalStatusSeq(events)).toBe(4);
  });

  it('picks the terminal status even when the failure frame comes AFTER it (fast-fail interleave)', () => {
    // The load-bearing F7 case: the terminal run.status is emitted, then the red
    // failure block lands at a higher seq. The end-state marker must still be the
    // terminal status row (seq 3), not the failure block — so reading
    // top-to-bottom lands on the true end state.
    const events = [
      ev(1, 'run.status', { status: 'running' }),
      ev(2, 'agent.tool_result', { tool: 'git.clone', ok: false }),
      ev(3, 'run.status', { status: 'failed' }),
      ev(4, 'run.failure', { reason: 'clone_failed', message: 'not found' }),
    ];
    expect(terminalStatusSeq(events)).toBe(3);
  });

  it('takes the HIGHEST-seq terminal status so a duplicate never doubles the marker', () => {
    // A re-emitted terminal status (e.g. the ST-1 pr_url re-emit) must not create
    // a second "final" row — only the highest seq wins.
    const events = [
      ev(3, 'run.status', { status: 'succeeded' }),
      ev(4, 'run.artifact', { kind: 'diff' }),
      ev(5, 'run.status', { status: 'succeeded', pr_url: 'https://g/o/r/pulls/1' }),
    ];
    expect(terminalStatusSeq(events)).toBe(5);
  });

  it('ignores non-terminal status frames after a terminal one is impossible by seq, but does not regress on lower seqs', () => {
    // A late lower-seq non-terminal status cannot outrank the terminal one.
    const events = [
      ev(2, 'run.status', { status: 'running' }),
      ev(5, 'run.status', { status: 'canceled' }),
      ev(3, 'run.status', { status: 'scheduling' }),
    ];
    expect(terminalStatusSeq(events)).toBe(5);
  });

  it('toTimelineItem still maps a terminal status frame to a status item', () => {
    const item = toTimelineItem(ev(4, 'run.status', { status: 'failed' }));
    expect(item.kind).toBe('status');
    if (item.kind === 'status') expect(item.status).toBe('failed');
  });
});
