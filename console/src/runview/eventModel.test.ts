import { describe, expect, it } from 'vitest';
import { terminalStatusSeq, toTimelineItem } from './eventModel';
import type { RunViewEvent } from './types';

function ev(seq: number, type: string, payload: Record<string, unknown> = {}): RunViewEvent {
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

describe('toTimelineItem — run.result (D18/D26 no_changes contract)', () => {
  it('maps outcome "no_changes" to an informational "No code changes" row', () => {
    const item = toTimelineItem(ev(7, 'run.result', { outcome: 'no_changes' }));
    expect(item.kind).toBe('result');
    if (item.kind === 'result') {
      expect(item.outcome).toBe('no_changes');
      expect(item.message).toBe('No code changes');
    }
  });

  it('tolerates a missing/unknown outcome rather than throwing', () => {
    const missing = toTimelineItem(ev(8, 'run.result', {}));
    expect(missing.kind).toBe('result');
    if (missing.kind === 'result') {
      expect(missing.outcome).toBe('');
      expect(missing.message).toBe('Result');
    }

    const other = toTimelineItem(ev(9, 'run.result', { outcome: 'something_else' }));
    if (other.kind === 'result') expect(other.message).toBe('something_else');
  });
});

describe('toTimelineItem — payload type tolerance', () => {
  it('never throws on a missing/empty payload for any known event type', () => {
    const types = [
      'agent.text',
      'agent.tool_call',
      'agent.tool_result',
      'run.status',
      'run.failure',
      'run.artifact',
      'run.git',
      'run.result',
      'totally.unknown.type',
    ];
    for (const type of types) {
      expect(() => toTimelineItem({ seq: 1, ts: '', type, payload: {} })).not.toThrow();
    }
  });

  it('falls back to "unknown" for an unrecognized event type, preserving the raw payload', () => {
    const item = toTimelineItem(ev(1, 'some.future.event', { a: 1, b: 'x' }));
    expect(item.kind).toBe('unknown');
    if (item.kind === 'unknown') {
      expect(item.type).toBe('some.future.event');
      expect(item.raw).toContain('"a": 1');
    }
  });
});

describe('toTimelineItem — session events (D22)', () => {
  it('maps user.message to a user_message item carrying prompt + author', () => {
    const item = toTimelineItem(ev(5, 'user.message', { prompt: 'next step', by: 'Ada' }));
    expect(item).toMatchObject({ kind: 'user_message', prompt: 'next step', by: 'Ada' });
  });

  it('tolerates a user.message with no author (service principal)', () => {
    const item = toTimelineItem(ev(5, 'user.message', { prompt: 'go' }));
    expect(item).toMatchObject({ kind: 'user_message', prompt: 'go', by: '' });
  });

  it('maps session.finish reasons to distinct human messages', () => {
    expect(toTimelineItem(ev(6, 'session.finish', { reason: 'user' }))).toMatchObject({
      kind: 'session_finish',
      reason: 'user',
      message: 'Session finished',
    });
    expect(toTimelineItem(ev(7, 'session.finish', { reason: 'idle_timeout' }))).toMatchObject({
      kind: 'session_finish',
      reason: 'idle_timeout',
      message: 'Session finished (idle timeout)',
    });
  });

  it('awaiting_input is NOT a terminal status for the end-of-run marker', () => {
    const events = [
      ev(1, 'run.status', { status: 'running' }),
      ev(2, 'run.status', { status: 'awaiting_input' }),
    ];
    expect(terminalStatusSeq(events)).toBeUndefined();
  });
});

describe('toTimelineItem — permission events (F8b)', () => {
  it('narrows agent.permission_request incl. defensive option filtering', () => {
    const item = toTimelineItem(
      ev(9, 'agent.permission_request', {
        request_id: 'req-1',
        tool_call_id: 'tc-1',
        title: 'Run `make deploy`',
        options: [
          { option_id: 'allow', name: 'Allow', kind: 'allow_once' },
          { name: 'no-id: dropped' }, // an option without option_id can never be decided
          'garbage',
        ],
      }),
    );
    expect(item).toMatchObject({
      kind: 'permission_request',
      requestId: 'req-1',
      toolCallId: 'tc-1',
      title: 'Run `make deploy`',
    });
    expect((item as { options: unknown[] }).options).toEqual([
      { optionId: 'allow', name: 'Allow', kind: 'allow_once' },
    ]);
  });

  it('narrows agent.permission_resolved and tolerates an empty option_id (Cancelled outcome)', () => {
    expect(
      toTimelineItem(
        ev(10, 'agent.permission_resolved', { request_id: 'req-1', option_id: '', resolution: 'timeout' }),
      ),
    ).toMatchObject({
      kind: 'permission_resolved',
      requestId: 'req-1',
      optionId: '',
      resolution: 'timeout',
    });
  });
});
