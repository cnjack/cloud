/*
 * eventModel.test.ts — every durable/ephemeral relay kind → its view item,
 * plus the unknown-kind and malformed-payload fallbacks. Payload fixtures
 * mirror jcode internal/handler/web.go and internal/web/*.go.
 */
import { describe, expect, it } from 'vitest';
import { mapDeviceEvent, applyToolResult, prettyArgs } from './eventModel';
import type { DeviceViewEvent } from './types';

function ev(seq: number, kind: string, data?: unknown): DeviceViewEvent {
  return { seq, ts: '2026-07-20T10:00:00Z', kind, payload: { type: kind, data } };
}

describe('mapDeviceEvent — durable kinds', () => {
  it('user_message carries content + channel source', () => {
    const item = mapDeviceEvent(ev(1, 'user_message', { content: 'fix the build', source: 'console' }));
    expect(item).toEqual({
      kind: 'user_message', seq: 1, ts: '2026-07-20T10:00:00Z',
      content: 'fix the build', source: 'console',
    });
  });

  it('agent_message maps to a finalized assistant bubble; empty text is dropped', () => {
    // Synthesized by the connector at agent_done (jcode internal/cloud/events.go).
    const item = mapDeviceEvent(ev(2, 'agent_message', { text: 'Done. **All** good.', stopped: false, error: '' }));
    expect(item).toEqual({
      kind: 'assistant_message', seq: 2, ts: '2026-07-20T10:00:00Z',
      text: 'Done. **All** good.',
    });
    expect(mapDeviceEvent(ev(3, 'agent_message', { text: '' }))).toBeNull();
    expect(mapDeviceEvent(ev(4, 'agent_message', {}))).toBeNull();
  });

  it('tool_call maps display_info and pretty-prints args, status running', () => {
    const item = mapDeviceEvent(ev(2, 'tool_call', {
      name: 'execute',
      args: '{"command":"ls"}',
      tool_call_id: 'call_1',
      display_info: { title: 'Shell', subtitle: 'ls' },
    }));
    expect(item).toMatchObject({
      kind: 'tool_card', tool: 'execute', callId: 'call_1',
      title: 'Shell', subtitle: 'ls', status: 'running',
    });
    expect((item as { args: string }).args).toContain('"command": "ls"');
  });

  it('tool_result folds into cards (null here), agent_text never lands in the log', () => {
    expect(mapDeviceEvent(ev(3, 'tool_result', { name: 'read', output: 'x' }))).toBeNull();
    expect(mapDeviceEvent(ev(3, 'agent_text', { text: 'chunk' }))).toBeNull();
    expect(mapDeviceEvent(ev(3, 'token_update', { total_tokens: 5 }))).toBeNull();
    expect(mapDeviceEvent(ev(3, 'subagent_progress', {}))).toBeNull();
  });

  it('approval_request uses `id` as the approval key (not approval_id)', () => {
    const item = mapDeviceEvent(ev(4, 'approval_request', {
      id: 'approval_1', tool_name: 'execute', tool_args: '{"command":"git push"}', tool_call_id: 'call_2', is_external: false,
    }));
    expect(item).toMatchObject({
      kind: 'approval_card', approvalId: 'approval_1',
      toolName: 'execute', toolCallId: 'call_2', status: 'pending',
    });
  });

  it('ask_user_request keeps the question list', () => {
    const item = mapDeviceEvent(ev(5, 'ask_user_request', {
      id: 'ask_1',
      questions: [{ question: 'Which file?', header: 'Target', options: [{ label: 'a' }] }],
    }));
    expect(item).toMatchObject({
      kind: 'ask_user', askId: 'ask_1',
      questions: [{ question: 'Which file?', header: 'Target' }],
    });
  });

  it('lifecycle/config events become status rows with structured fields', () => {
    expect(mapDeviceEvent(ev(6, 'agent_start'))).toMatchObject({ kind: 'status', eventKind: 'agent_start' });
    expect(mapDeviceEvent(ev(7, 'agent_done', { stopped: true }))).toMatchObject({ kind: 'status', eventKind: 'agent_done', stopped: true });
    expect(mapDeviceEvent(ev(8, 'agent_done', { error: 'boom' }))).toMatchObject({ kind: 'status', errorMessage: 'boom' });
    expect(mapDeviceEvent(ev(9, 'task_status', { status: 'running' }))).toMatchObject({ kind: 'status', status: 'running' });
    expect(mapDeviceEvent(ev(10, 'mode_changed', { mode: 'plan' }))).toMatchObject({ kind: 'status', mode: 'plan' });
    expect(mapDeviceEvent(ev(11, 'model_changed', { provider: 'openai', model: 'gpt-5' }))).toMatchObject({ kind: 'status', provider: 'openai', model: 'gpt-5' });
    expect(mapDeviceEvent(ev(12, 'goal_update', { objective: 'ship it', status: 'active' }))).toMatchObject({ kind: 'status', goalObjective: 'ship it', goalStatus: 'active' });
    // goal_update with a typed-null data (jcode ws.go note) must not crash.
    expect(mapDeviceEvent(ev(13, 'goal_update', null))).toMatchObject({ kind: 'status', eventKind: 'goal_update' });
    expect(mapDeviceEvent(ev(14, 'session_reset', {}))).toMatchObject({ kind: 'status', eventKind: 'session_reset' });
    expect(mapDeviceEvent(ev(15, 'todo_update'))).toMatchObject({ kind: 'status', eventKind: 'todo_update' });
  });

  it('subagent_event maps start and done shapes', () => {
    expect(mapDeviceEvent(ev(16, 'subagent_event', { name: 'scan', agent_type: 'explore', done: false })))
      .toMatchObject({ kind: 'subagent', name: 'scan', done: false });
    expect(mapDeviceEvent(ev(17, 'subagent_event', { name: 'scan', agent_type: 'explore', done: true, result: 'ok' })))
      .toMatchObject({ kind: 'subagent', done: true, result: 'ok' });
  });

  it('unknown kinds degrade to an unknown row with the raw payload', () => {
    const item = mapDeviceEvent(ev(18, 'mcp_changed', { servers: 2 }));
    expect(item).toMatchObject({ kind: 'unknown', eventKind: 'mcp_changed' });
    expect((item as { raw: string }).raw).toContain('"servers": 2');
  });

  it('malformed payloads never throw', () => {
    expect(mapDeviceEvent(ev(19, 'user_message', 'not-an-object'))).toMatchObject({ kind: 'user_message', content: '' });
    expect(mapDeviceEvent(ev(20, 'tool_call', null))).toMatchObject({ kind: 'tool_card', tool: 'tool', status: 'running' });
    expect(mapDeviceEvent({ seq: 21, ts: '', kind: 'approval_request', payload: {} }))
      .toMatchObject({ kind: 'approval_card', approvalId: '' });
  });
});

describe('applyToolResult', () => {
  const running = {
    kind: 'tool_card' as const, seq: 2, ts: '', tool: 'execute', args: '{}', status: 'running' as const,
  };

  it('success → succeeded with display_output preferred', () => {
    const card = applyToolResult(running, ev(3, 'tool_result', { name: 'execute', output: 'raw', display_output: 'clean' }));
    expect(card).toMatchObject({ status: 'succeeded', output: 'clean', resultSeq: 3 });
  });

  it('error → failed', () => {
    const card = applyToolResult(running, ev(3, 'tool_result', { name: 'execute', output: '', error: 'exit 1' }));
    expect(card.status).toBe('failed');
  });

  it('denied → denied (not failed)', () => {
    const card = applyToolResult(running, ev(3, 'tool_result', { name: 'execute', output: '', denied: true }));
    expect(card.status).toBe('denied');
  });
});

describe('prettyArgs', () => {
  it('pretty-prints valid JSON, passes raw text through otherwise', () => {
    expect(prettyArgs('{"a":1}')).toBe('{\n  "a": 1\n}');
    expect(prettyArgs('not json')).toBe('not json');
    expect(prettyArgs(undefined)).toBe('');
  });
});
