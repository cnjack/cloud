/*
 * grouping.test.ts — groupTimeline() coverage:
 *   - streaming text merge (pure run, interrupted run)
 *   - purity / re-projection: recomputing from scratch on a growing or
 *     out-of-order event list never leaks state between calls
 *   - call_id pairing (paired success/error, orphan call, orphan result,
 *     running/unresolved)
 *   - pass-through of status/failure/artifact/git/result rows
 */
import { describe, expect, it } from 'vitest';
import { groupTimeline } from './grouping';
import type { RunViewEvent } from './types';

function ev(seq: number, type: string, payload: Record<string, unknown> = {}): RunViewEvent {
  return { seq, ts: new Date(seq * 1000).toISOString(), type, payload };
}

describe('groupTimeline — streaming text merge', () => {
  it('merges a pure run of consecutive agent.text chunks into one text_block', () => {
    const events = [
      ev(1, 'agent.text', { text: 'Hello ' }),
      ev(2, 'agent.text', { text: 'world' }),
      ev(3, 'agent.text', { text: '!' }),
    ];
    const grouped = groupTimeline(events);
    expect(grouped).toHaveLength(1);
    expect(grouped[0]).toMatchObject({ kind: 'text_block', seq: 1, lastSeq: 3, text: 'Hello world!' });
  });

  it('breaks the text block when a tool call interrupts the stream', () => {
    const events = [
      ev(1, 'agent.text', { text: 'a' }),
      ev(2, 'agent.text', { text: 'b' }),
      ev(3, 'agent.tool_call', { tool: 'bash', call_id: 'c1', input: { cmd: 'ls' } }),
      ev(4, 'agent.text', { text: 'c' }),
      ev(5, 'agent.text', { text: 'd' }),
    ];
    const grouped = groupTimeline(events);
    expect(grouped.map((g) => g.kind)).toEqual(['text_block', 'tool_card', 'text_block']);
    expect(grouped[0]).toMatchObject({ seq: 1, lastSeq: 2, text: 'ab' });
    expect(grouped[2]).toMatchObject({ seq: 4, lastSeq: 5, text: 'cd' });
  });

  it('starts a new block after a status row interrupts (any non-text event breaks the run)', () => {
    const events = [
      ev(1, 'agent.text', { text: 'a' }),
      ev(2, 'run.status', { status: 'running' }),
      ev(3, 'agent.text', { text: 'b' }),
    ];
    const grouped = groupTimeline(events);
    expect(grouped.map((g) => g.kind)).toEqual(['text_block', 'status', 'text_block']);
    expect(grouped[0]).toMatchObject({ text: 'a' });
    expect(grouped[2]).toMatchObject({ text: 'b' });
  });
});

describe('groupTimeline — purity / re-projection', () => {
  it('re-projects correctly from an out-of-order event array (sorts by seq internally)', () => {
    const scrambled = [
      ev(3, 'agent.text', { text: 'c' }),
      ev(1, 'agent.text', { text: 'a' }),
      ev(2, 'agent.text', { text: 'b' }),
    ];
    const grouped = groupTimeline(scrambled);
    expect(grouped).toHaveLength(1);
    expect(grouped[0]).toMatchObject({ seq: 1, lastSeq: 3, text: 'abc' });
  });

  it('does not leak state between calls: an earlier projection is unaffected by a later, larger one', () => {
    const base: RunViewEvent[] = [
      ev(1, 'agent.text', { text: 'hi' }),
      ev(2, 'agent.tool_call', { tool: 'bash', call_id: 'c1', input: {} }),
    ];
    const firstProjection = groupTimeline(base);
    expect(firstProjection[1]).toMatchObject({ kind: 'tool_card', status: 'running' });

    // Simulate the next SSE frame landing: a fresh, larger array (the reducer
    // never mutates the old one), re-grouped from scratch.
    const grown = [...base, ev(3, 'agent.tool_result', { call_id: 'c1', ok: true, output: 'done' })];
    const secondProjection = groupTimeline(grown);
    expect(secondProjection[1]).toMatchObject({ kind: 'tool_card', status: 'succeeded', output: 'done' });

    // The FIRST projection's card must still read "running" — if grouping
    // shared mutable state across calls, this would have flipped too.
    expect(firstProjection[1]).toMatchObject({ kind: 'tool_card', status: 'running' });
  });
});

describe('groupTimeline — call_id pairing', () => {
  it('pairs a call/result into one succeeded card', () => {
    const events = [
      ev(1, 'agent.tool_call', { tool: 'bash', call_id: 'c1', input: { cmd: 'ls' } }),
      ev(2, 'agent.tool_result', { call_id: 'c1', ok: true, output: 'file1\nfile2' }),
    ];
    const grouped = groupTimeline(events);
    expect(grouped).toHaveLength(1);
    expect(grouped[0]).toMatchObject({
      kind: 'tool_card',
      seq: 1,
      tool: 'bash',
      callId: 'c1',
      status: 'succeeded',
      isError: false,
      output: 'file1\nfile2',
      resultSeq: 2,
    });
  });

  it('pairs a call/result into a failed card when the result is an error', () => {
    const events = [
      ev(1, 'agent.tool_call', { tool: 'bash', call_id: 'c1', input: { cmd: 'boom' } }),
      ev(2, 'agent.tool_result', { call_id: 'c1', ok: false, exit_code: 1, output: 'no such file' }),
    ];
    const grouped = groupTimeline(events);
    expect(grouped[0]).toMatchObject({ kind: 'tool_card', status: 'failed', isError: true });
  });

  it('leaves a card in "running" status when its result has not arrived yet', () => {
    const events = [ev(1, 'agent.tool_call', { tool: 'bash', call_id: 'c1', input: {} })];
    const grouped = groupTimeline(events);
    expect(grouped[0]).toMatchObject({ kind: 'tool_card', status: 'running' });
    expect((grouped[0] as { output?: string }).output).toBeUndefined();
  });

  it('degrades a tool_call with no call_id to a standalone row (can never pair)', () => {
    const events = [ev(1, 'agent.tool_call', { tool: 'bash', input: {} })];
    const grouped = groupTimeline(events);
    expect(grouped[0]?.kind).toBe('tool_call');
  });

  it('degrades an orphan tool_result (no matching open call) to a standalone row', () => {
    const events = [ev(1, 'agent.tool_result', { call_id: 'unknown-call', ok: true, output: 'x' })];
    const grouped = groupTimeline(events);
    expect(grouped[0]?.kind).toBe('tool_result');
    expect(grouped[0]).toMatchObject({ isError: false, output: 'x' });
  });

  it('degrades a tool_result with no call_id at all to a standalone row', () => {
    const events = [ev(1, 'agent.tool_result', { ok: false, output: 'fail' })];
    const grouped = groupTimeline(events);
    expect(grouped[0]?.kind).toBe('tool_result');
    expect(grouped[0]).toMatchObject({ isError: true });
  });
});

describe('groupTimeline — pass-through rows', () => {
  it('passes status/failure/artifact/git/result events through unchanged', () => {
    const events = [
      ev(1, 'run.status', { status: 'running' }),
      ev(2, 'run.artifact', { kind: 'diff' }),
      ev(3, 'run.git', { branch: 'agent/run-1', commit_sha: 'abc123' }),
      ev(4, 'run.result', { outcome: 'no_changes' }),
      ev(5, 'run.failure', { reason: 'agent_error', message: 'oops' }),
    ];
    const grouped = groupTimeline(events);
    expect(grouped.map((g) => g.kind)).toEqual(['status', 'artifact', 'git', 'result', 'failure']);
    expect(grouped[3]).toMatchObject({ outcome: 'no_changes', message: 'No code changes' });
  });
});

describe('groupTimeline — permission request/resolved pairing (F8b)', () => {
  const options = [
    { option_id: 'allow', name: 'Allow', kind: 'allow_once' },
    { option_id: 'reject', name: 'Reject', kind: 'reject_once' },
  ];

  it('renders a pending permission_card for an unresolved request', () => {
    const grouped = groupTimeline([
      ev(1, 'agent.permission_request', {
        request_id: 'req-1',
        tool_call_id: 'tc-1',
        title: 'Run `make deploy`',
        options,
      }),
    ]);
    expect(grouped).toHaveLength(1);
    expect(grouped[0]).toMatchObject({
      kind: 'permission_card',
      status: 'pending',
      requestId: 'req-1',
      title: 'Run `make deploy`',
    });
    expect((grouped[0] as { options: unknown[] }).options).toHaveLength(2);
  });

  it('pairs request and resolved by request_id ACROSS interleaved events (no adjacency)', () => {
    // The request event is delivered synchronously and may precede the
    // tool_call it references; other events interleave freely before the
    // resolution arrives. Pairing must key off request_id alone.
    const grouped = groupTimeline([
      ev(1, 'agent.permission_request', { request_id: 'req-1', title: 'Edit README', options }),
      ev(2, 'agent.tool_call', { tool: 'edit_file', call_id: 'c9', args: {} }),
      ev(3, 'agent.text', { text: 'thinking…' }),
      ev(4, 'agent.tool_result', { call_id: 'c9', ok: true, output: 'done' }),
      ev(5, 'agent.permission_resolved', { request_id: 'req-1', option_id: 'allow', resolution: 'user' }),
    ]);
    const card = grouped.find((g) => g.kind === 'permission_card');
    expect(card).toMatchObject({
      status: 'resolved',
      resolvedOptionId: 'allow',
      resolution: 'user',
      resolvedSeq: 5,
    });
    // The resolution row itself is folded into the card, not a second row.
    expect(grouped.filter((g) => g.kind === 'permission_resolved')).toHaveLength(0);
  });

  it('a duplicate request event (at-least-once delivery) does not spawn a second card or reset a resolved one', () => {
    const grouped = groupTimeline([
      ev(1, 'agent.permission_request', { request_id: 'req-1', title: 'T', options }),
      ev(2, 'agent.permission_resolved', { request_id: 'req-1', option_id: 'reject', resolution: 'timeout' }),
      ev(3, 'agent.permission_request', { request_id: 'req-1', title: 'T', options }),
    ]);
    const cards = grouped.filter((g) => g.kind === 'permission_card');
    expect(cards).toHaveLength(1);
    expect(cards[0]).toMatchObject({ status: 'resolved', resolution: 'timeout' });
  });

  it('an ORPHAN resolved (request never arrived) degrades to a standalone row', () => {
    const grouped = groupTimeline([
      ev(1, 'agent.permission_resolved', { request_id: 'req-ghost', option_id: '', resolution: 'timeout' }),
    ]);
    expect(grouped).toHaveLength(1);
    expect(grouped[0]).toMatchObject({ kind: 'permission_resolved', requestId: 'req-ghost' });
  });

  it('two concurrent requests resolve independently by their own request_id', () => {
    const grouped = groupTimeline([
      ev(1, 'agent.permission_request', { request_id: 'req-a', title: 'A', options }),
      ev(2, 'agent.permission_request', { request_id: 'req-b', title: 'B', options }),
      ev(3, 'agent.permission_resolved', { request_id: 'req-b', option_id: 'reject', resolution: 'timeout' }),
    ]);
    const cards = grouped.filter((g) => g.kind === 'permission_card');
    expect(cards).toHaveLength(2);
    expect(cards[0]).toMatchObject({ requestId: 'req-a', status: 'pending' });
    expect(cards[1]).toMatchObject({ requestId: 'req-b', status: 'resolved' });
  });
});
