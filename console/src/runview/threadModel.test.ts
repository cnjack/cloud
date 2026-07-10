import { describe, expect, it } from 'vitest';
import type { ThreadItem } from 'jcode-ui-core';
import { toThreadItems, type CloudApproval } from './threadModel';
import type { RunViewEvent } from './types';

function ev(seq: number, type: string, payload: Record<string, unknown> = {}): RunViewEvent {
  return { seq, ts: new Date(seq * 1000).toISOString(), type, payload };
}

function dataOf<T extends ThreadItem['kind']>(items: ThreadItem[], kind: T) {
  return items.filter((item) => item.kind === kind).map((item) => item.data);
}

describe('toThreadItems — jcode-ui projection', () => {
  it('merges streamed assistant chunks and preserves user follow-ups as messages', () => {
    const items = toThreadItems([
      ev(1, 'agent.text', { text: 'Hello ' }),
      ev(2, 'agent.text', { text: '**world**' }),
      ev(3, 'user.message', { prompt: 'continue', by: 'Ada' }),
    ]);

    expect(dataOf(items, 'message')).toMatchObject([
      { id: 'assistant-1', role: 'assistant', content: 'Hello **world**' },
      { id: 'user-3', role: 'user', content: 'continue', source: 'Ada', author: 'Ada' },
    ]);
  });

  it('pairs tool calls/results into the package ToolCall contract', () => {
    const items = toThreadItems([
      ev(1, 'agent.tool_call', { tool: 'execute', call_id: 'c1', args: { command: 'pwd' } }),
      ev(2, 'agent.tool_result', { call_id: 'c1', ok: false, exit_code: 1, output: 'boom' }),
    ]);

    expect(dataOf(items, 'tool')).toMatchObject([
      {
        id: 'tool-c1',
        toolCallID: 'c1',
        name: 'execute',
        status: 'error',
        output: 'boom',
        error: 'boom',
      },
    ]);
  });

  it('keeps Cloud permission options losslessly on an approval thread item', () => {
    const items = toThreadItems([
      ev(1, 'agent.permission_request', {
        request_id: 'req-1',
        title: 'Run `make deploy`',
        options: [
          { option_id: 'allow-once-id', name: 'Allow once', kind: 'allow_once' },
          { option_id: 'reject-id', name: 'Reject', kind: 'reject_once' },
        ],
      }),
      ev(2, 'agent.permission_resolved', {
        request_id: 'req-1',
        option_id: 'reject-id',
        resolution: 'timeout',
      }),
    ]);

    const approval = dataOf(items, 'approval')[0] as CloudApproval;
    expect(approval).toMatchObject({ id: 'req-1', resolved: true, approved: false });
    expect(approval.permission).toMatchObject({
      requestId: 'req-1',
      status: 'resolved',
      resolvedOptionId: 'reject-id',
      resolution: 'timeout',
    });
    expect(approval.permission.options).toEqual([
      { optionId: 'allow-once-id', name: 'Allow once', kind: 'allow_once' },
      { optionId: 'reject-id', name: 'Reject', kind: 'reject_once' },
    ]);
  });

  it('renders lifecycle events and unknown events as visible system messages', () => {
    const items = toThreadItems([
      ev(1, 'run.status', { status: 'running' }),
      ev(2, 'run.git', { branch: 'agent/run-1', commit_sha: 'abc123' }),
      ev(3, 'future.event', { answer: 42 }),
      ev(4, 'run.status', { status: 'failed' }),
    ]);

    expect(dataOf(items, 'message')).toMatchObject([
      { role: 'system', content: 'Status: Running' },
      { role: 'system', content: 'Pushed branch `agent/run-1` at `abc123`' },
      {
        role: 'system',
        content: 'Unknown event: future.event\n\n```json\n{\n  "answer": 42\n}\n```',
      },
      { role: 'system', content: 'Final status: Failed — end of run' },
    ]);
  });

  it('uses the runner production name field for specialized tool dispatch', () => {
    const items = toThreadItems([
      ev(1, 'agent.tool_call', { name: 'execute', call_id: 'c-real', args: { command: 'pwd' } }),
      ev(2, 'agent.tool_result', {
        name: 'Run command',
        call_id: 'c-real',
        output: '/workspace',
      }),
    ]);

    expect(dataOf(items, 'tool')).toMatchObject([
      { name: 'execute', toolCallID: 'c-real', status: 'done' },
    ]);
  });
});
