/*
 * threadItems.test.ts — M4 device event model → jcode-ui-core ThreadItem
 * mapping (M14). Covers the message/tool/approval/system projections, the
 * tool_result folding inherited from grouping.ts, and the ephemeral stream
 * tail (finalized + streaming assistant bubbles).
 */
import { describe, expect, it } from 'vitest';
import type { DeviceViewEvent } from '../deviceview/types';
import type { DeviceSessionState } from '../deviceview/sessionReducer';
import { initialDeviceSessionState } from '../deviceview/sessionReducer';
import { toThreadItems } from './threadItems';
import type { DeviceItemDescriber } from './threadItems';

function ev(seq: number, kind: string, data: unknown): DeviceViewEvent {
  return { seq, ts: '2026-07-21T10:00:00Z', kind, payload: { type: kind, data } };
}

function stateWith(events: DeviceViewEvent[], extra: Partial<DeviceSessionState> = {}): DeviceSessionState {
  return { ...initialDeviceSessionState(), events, ...extra };
}

/** Identity-ish describer: label by kind, null drops the row. */
const describeAll: DeviceItemDescriber = (item) => {
  if (item.kind === 'status' && item.eventKind === 'agent_start') return null;
  if (item.kind === 'status') return `status:${item.eventKind}`;
  if (item.kind === 'ask_user') return 'asked';
  if (item.kind === 'subagent') return `subagent:${item.name}`;
  if (item.kind === 'unknown') return `unknown:${item.eventKind}`;
  return null;
};

describe('toThreadItems', () => {
  it('maps user/agent messages with roles, source and timestamps', () => {
    const items = toThreadItems(
      stateWith([
        ev(1, 'user_message', { content: 'hi', source: 'console' }),
        ev(2, 'agent_message', { text: 'hello **there**' }),
      ]),
      { describe: describeAll },
    );
    expect(items).toHaveLength(2);
    const [user, assistant] = items;
    expect(user).toMatchObject({ kind: 'message', seq: 1 });
    expect(user!.kind === 'message' && user!.data).toMatchObject({
      role: 'user',
      content: 'hi',
      source: 'console',
      timestamp: Date.parse('2026-07-21T10:00:00Z'),
    });
    expect(assistant!.kind === 'message' && assistant!.data).toMatchObject({
      role: 'assistant',
      content: 'hello **there**',
    });
  });

  it('folds tool_result into the tool item (status/output/denied)', () => {
    const items = toThreadItems(
      stateWith([
        ev(1, 'tool_call', { name: 'bash', tool_call_id: 'c1', args: '{"cmd":"ls"}' }),
        ev(2, 'tool_result', { tool_call_id: 'c1', output: 'file.txt' }),
        ev(3, 'tool_call', { name: 'edit', tool_call_id: 'c2', args: '{}' }),
        ev(4, 'tool_result', { tool_call_id: 'c2', denied: true }),
        ev(5, 'tool_call', { name: 'write', tool_call_id: 'c3', args: '{}' }),
      ]),
      { describe: describeAll },
    );
    expect(items).toHaveLength(3);
    const [done, denied, running] = items;
    expect(done!.kind === 'tool' && done!.data).toMatchObject({
      name: 'bash',
      status: 'done',
      output: 'file.txt',
      toolCallID: 'c1',
    });
    expect(denied!.kind === 'tool' && denied!.data).toMatchObject({ status: 'done', denied: true });
    expect(running!.kind === 'tool' && running!.data).toMatchObject({ status: 'running' });
  });

  it('maps approvals to the classic boolean Approval shape', () => {
    const items = toThreadItems(
      stateWith([ev(1, 'approval_request', { id: 'a1', tool_name: 'bash', tool_args: '{}' })]),
      { describe: describeAll },
    );
    expect(items[0]).toMatchObject({
      kind: 'approval',
      data: { id: 'a1', tool_name: 'bash', resolved: false },
    });
  });

  it('routes status/ask_user/subagent/unknown rows through the describer', () => {
    const items = toThreadItems(
      stateWith([
        ev(1, 'agent_start', {}),                 // describe → null: dropped
        ev(2, 'mode_changed', { mode: 'plan' }),  // → system notice
        ev(3, 'ask_user_request', { id: 'q1', questions: [{ question: 'ok?' }] }),
        ev(4, 'subagent_event', { name: 'scout', agent_type: 'explore', done: false }),
        ev(5, 'some_future_event', { x: 1 }),
      ]),
      { describe: describeAll },
    );
    expect(items.map((i) => i.kind)).toEqual(['message', 'message', 'message', 'message']);
    const texts = items.map((i) => (i.kind === 'message' ? i.data.content : ''));
    expect(texts).toEqual(['status:mode_changed', 'asked', 'subagent:scout', 'unknown:some_future_event']);
    expect(items.every((i) => i.kind === 'message' && i.data.role === 'system')).toBe(true);
    // unknown rows surface as errors; the rest as notices.
    expect(items[3]!.kind === 'message' && items[3]!.data.level).toBe('error');
    expect(items[1]!.kind === 'message' && items[1]!.data.level).toBe('notice');
  });

  it('appends finalized and streaming assistant bubbles after durable items', () => {
    const items = toThreadItems(
      stateWith([ev(1, 'user_message', { content: 'hi', source: '' })], {
        finalizedText: [{ id: -1, text: 'partial answer' }],
        streamingText: 'still typing…',
      }),
      { describe: describeAll },
    );
    expect(items).toHaveLength(3);
    expect(items[1]!.kind === 'message' && items[1]!.data).toMatchObject({
      role: 'assistant',
      content: 'partial answer',
    });
    expect(items[2]!.kind === 'message' && items[2]!.data).toMatchObject({
      role: 'assistant',
      content: 'still typing…',
    });
    // Synthetic seqs never collide with durable event seqs.
    expect(items[1]!.seq).toBeLessThan(0);
    expect(items[2]!.seq).toBeLessThan(items[1]!.seq);
  });
});
