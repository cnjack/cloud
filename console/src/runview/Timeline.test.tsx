import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { RuntimeProvider } from 'jcode-ui';
import type { ChatRuntime, RuntimeActions } from 'jcode-ui-core/runtime';
import { Timeline } from './Timeline';
import { toThreadItems } from './threadModel';
import type { PermissionControls, RunViewEvent } from './types';

function ev(seq: number, type: string, payload: Record<string, unknown> = {}): RunViewEvent {
  return { seq, ts: new Date(seq * 1000).toISOString(), type, payload };
}

const actions: RuntimeActions = {
  sendMessage: vi.fn(),
  enqueueMessage: vi.fn(),
  removeQueuedMessage: vi.fn(),
  stop: vi.fn(),
  resolveApproval: vi.fn(),
  submitAskUser: vi.fn(),
  editMessage: vi.fn(),
};

function renderTimeline(
  events: RunViewEvent[],
  opts: { isRunning?: boolean; permissions?: PermissionControls } = {},
) {
  const state = {
    items: toThreadItems(events),
    isRunning: opts.isRunning ?? false,
    tokenSnapshot: null,
    goal: null,
    todos: [],
    queued: [],
  };
  const runtime: ChatRuntime = {
    getState: () => state,
    subscribe: () => () => {},
    actions,
  };
  return render(
    <RuntimeProvider runtime={runtime}>
      <Timeline permissions={opts.permissions} />
    </RuntimeProvider>,
  );
}

describe('Timeline — published jcode-ui rendering', () => {
  it('renders merged assistant markdown through the package Message component', () => {
    const { container } = renderTimeline([
      ev(1, 'agent.text', { text: 'Hello ' }),
      ev(2, 'agent.text', { text: '**world**' }),
    ]);

    expect(container.querySelectorAll('.jcode-message')).toHaveLength(1);
    expect(container.querySelector('.jcode-message strong')?.textContent).toBe('world');
    expect(screen.getByText('JCODE')).toBeTruthy();
  });

  it('renders paired tools through the package ToolCallCard component', () => {
    const { container } = renderTimeline([
      ev(1, 'agent.tool_call', {
        name: 'execute',
        call_id: 'c1',
        args: { command: 'pwd' },
      }),
      ev(2, 'agent.tool_result', {
        name: 'execute',
        call_id: 'c1',
        ok: true,
        output: '/workspace',
      }),
    ]);

    expect(container.querySelectorAll('.jcode-toolcall')).toHaveLength(1);
    expect(container.textContent).toContain('execute');
  });

  it('renders lifecycle information as visible package system messages', () => {
    const { container } = renderTimeline([
      ev(1, 'run.session', { resumed: true }),
      ev(2, 'session.finish', { reason: 'idle_timeout' }),
      ev(3, 'run.status', { status: 'succeeded' }),
    ]);

    expect(container.querySelectorAll('.jcode-message[data-role="system"]')).toHaveLength(3);
    expect(container.textContent).toContain('Session resumed');
    expect(container.textContent).toContain('Session finished (idle timeout)');
    expect(container.textContent).toContain('Final status: Succeeded');
  });

  it('keeps the real Cloud author visible for multi-user follow-ups', () => {
    const { container } = renderTimeline([
      ev(1, 'user.message', { prompt: 'Please continue', by: 'Ada Lovelace' }),
    ]);

    expect(container.textContent).toContain('Ada Lovelace');
    expect(container.textContent).toContain('Please continue');
    expect(screen.queryByText('You')).toBeNull();
  });

  it('keeps an unknown event payload visibly inspectable', () => {
    const { container } = renderTimeline([
      ev(1, 'future.event', { reason: 'new contract' }),
    ]);

    expect(container.textContent).toContain('Unknown event: future.event');
    expect(container.textContent).toContain('"reason": "new contract"');
  });

  it('keeps arbitrary Cloud permission option IDs actionable', () => {
    const onDecide = vi.fn();
    renderTimeline(
      [
        ev(1, 'agent.permission_request', {
          request_id: 'req-1',
          title: 'Deploy',
          options: [
            { option_id: 'custom-allow', name: 'Proceed', kind: 'allow_once' },
            { option_id: 'custom-reject', name: 'No', kind: 'reject_once' },
          ],
        }),
      ],
      { permissions: { onDecide } },
    );

    fireEvent.click(screen.getByTestId('permission-option-custom-allow'));
    expect(onDecide).toHaveBeenCalledWith('req-1', 'custom-allow');
  });

  it('shows the package pending indicator while a turn is running', () => {
    renderTimeline([], { isRunning: true });
    expect(screen.getByLabelText('Thinking…')).toBeTruthy();
  });
});
