/*
 * grouping.test.ts — tool_call/tool_result pairing rules: by tool_call_id,
 * id-less fallback to the oldest running card, orphan results stay visible.
 */
import { describe, expect, it } from 'vitest';
import { groupDeviceEvents } from './grouping';
import type { DeviceToolCardItem, DeviceViewEvent } from './types';

function ev(seq: number, kind: string, data?: unknown): DeviceViewEvent {
  return { seq, ts: '', kind, payload: { type: kind, data } };
}

describe('groupDeviceEvents', () => {
  it('pairs a result with its call by tool_call_id, out of order safe', () => {
    const items = groupDeviceEvents([
      ev(1, 'tool_call', { name: 'read', args: '{}', tool_call_id: 'a' }),
      ev(2, 'tool_call', { name: 'execute', args: '{}', tool_call_id: 'b' }),
      ev(3, 'tool_result', { name: 'execute', tool_call_id: 'b', output: 'done' }),
      ev(4, 'tool_result', { name: 'read', tool_call_id: 'a', error: 'nope' }),
    ]);
    expect(items).toHaveLength(2);
    const [read, exec] = items as DeviceToolCardItem[];
    expect(read).toMatchObject({ tool: 'read', status: 'failed', resultSeq: 4 });
    expect(exec).toMatchObject({ tool: 'execute', status: 'succeeded', output: 'done', resultSeq: 3 });
  });

  it('id-less result folds into the oldest still-running card', () => {
    const items = groupDeviceEvents([
      ev(1, 'tool_call', { name: 'read', args: '{}' }),
      ev(2, 'tool_call', { name: 'grep', args: '{}' }),
      ev(3, 'tool_result', { name: 'read', output: 'x' }),
    ]);
    expect(items).toHaveLength(2);
    expect((items[0] as DeviceToolCardItem).status).toBe('succeeded');
    expect((items[1] as DeviceToolCardItem).status).toBe('running');
  });

  it('orphan result (no call seen) stays visible as its own card', () => {
    const items = groupDeviceEvents([
      ev(5, 'tool_result', { name: 'execute', output: 'late' }),
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: 'tool_card', tool: 'execute', status: 'succeeded', output: 'late' });
  });

  it('non-tool events pass through in order', () => {
    const items = groupDeviceEvents([
      ev(1, 'user_message', { content: 'hi', source: 'console' }),
      ev(2, 'agent_start'),
      ev(3, 'agent_done', {}),
    ]);
    expect(items.map((i) => i.kind)).toEqual(['user_message', 'status', 'status']);
  });
});
