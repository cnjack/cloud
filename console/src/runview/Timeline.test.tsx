import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { RuntimeProvider } from 'jcode-ui';
import type { ChatRuntime, RuntimeActions } from 'jcode-ui-core/runtime';
import { Timeline } from './Timeline';
import { toThreadItems } from './threadModel';
import type { PermissionControls, RunViewEvent } from './types';

function ev(seq: number, type: string, payload: Record<string, unknown> = {}): RunViewEvent {
  return { seq, ts: new Date(seq * 1000).toISOString(), type, payload };
}

function renderTimeline(
  events: RunViewEvent[],
  opts: { isRunning?: boolean; permissions?: PermissionControls } = {},
) {
  const actions: RuntimeActions = {
    sendMessage: vi.fn(),
    enqueueMessage: vi.fn(),
    removeQueuedMessage: vi.fn(),
    stop: vi.fn(),
    resolveApproval: vi.fn(),
    resolveApprovalOption: opts.permissions?.onDecide,
    submitAskUser: vi.fn(),
    editMessage: vi.fn(),
  };
  const state = {
    items: toThreadItems(events, {
      decided: opts.permissions?.decided,
      disabled: opts.permissions?.disabled,
    }),
    isRunning: opts.isRunning ?? false,
    tokenSnapshot: null,
    goal: null,
    todos: [],
    queued: [],
    connection: 'connected' as const,
  };
  const runtime: ChatRuntime = {
    getState: () => state,
    subscribe: () => () => {},
    actions,
  };
  return render(
    <RuntimeProvider runtime={runtime}>
      <Timeline />
    </RuntimeProvider>,
  );
}

describe('Timeline — task conversation rendering', () => {
  it('renders merged assistant markdown as one jcode conversation message', () => {
    const { container } = renderTimeline([
      ev(1, 'agent.text', { text: 'Hello ' }),
      ev(2, 'agent.text', { text: '**world**' }),
    ]);

    expect(screen.getAllByTestId('thread-message-assistant')).toHaveLength(1);
    expect(container.querySelector('[data-testid="thread-message-assistant"] strong')?.textContent).toBe('world');
    expect(screen.getByText('JCODE')).toBeTruthy();
  });

  it('keeps fenced code chrome scoped and copies from the header action', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    });

    const { container } = renderTimeline([
      ev(1, 'agent.text', { text: '```sh\npnpm test\n```' }),
    ]);

    const message = container.querySelector<HTMLElement>('[data-testid="thread-message-assistant"][data-jcode-ui]');
    const prose = message?.querySelector<HTMLElement>('.jcode-prose');
    expect(message?.getAttribute('data-jcode-ui')).toBe('');
    expect(prose?.classList.contains('jcode-prose')).toBe(true);
    const copy = screen.getByRole('button', { name: 'Copy code' });
    expect(copy.parentElement?.classList.contains('jcode-codeblock__bar')).toBe(true);

    fireEvent.click(copy);

    await waitFor(() => expect(writeText).toHaveBeenCalledWith('pnpm test'));
    expect(copy.textContent).toBe('Copied');
  });

  it('renders paired tools through the jcode-ui activity group and registry', () => {
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
      ev(3, 'agent.tool_call', {
        name: 'read',
        call_id: 'c2',
        args: { path: 'README.md' },
      }),
      ev(4, 'agent.tool_result', {
        name: 'read',
        call_id: 'c2',
        ok: true,
        output: '# Project',
      }),
    ]);

    const group = screen.getByTestId('activity-group');
    expect(group.getAttribute('data-jcode-ui')).toBe('');
    fireEvent.click(group.querySelector('button')!);
    expect(screen.getByTestId('activity-rows')).toBeTruthy();
    fireEvent.click(container.querySelector<HTMLElement>('[data-tool-name="execute"] button')!);
    expect(container.querySelector('.jcode-terminal__out')?.textContent).toContain('/workspace');
    fireEvent.click(container.querySelector<HTMLElement>('[data-tool-name="read"] button')!);
    expect(container.textContent).toContain('# Project');
  });

  it('renders lifecycle information through jcode-ui system messages', () => {
    const { container } = renderTimeline([
      ev(1, 'run.session', { resumed: true }),
      ev(2, 'session.finish', { reason: 'idle_timeout' }),
      ev(3, 'run.status', { status: 'succeeded' }),
    ]);

    expect(container.querySelectorAll('[data-testid="thread-message-system"]')).toHaveLength(3);
    expect(container.querySelectorAll('[data-testid="thread-message-system"][data-jcode-ui]')).toHaveLength(3);
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
    expect(screen.getByTestId('thread-message-user')).toBeTruthy();
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

    fireEvent.click(screen.getByRole('button', { name: 'Proceed' }));
    expect(onDecide).toHaveBeenCalledWith('req-1', 'custom-allow');
  });

  it('shows a compact pending indicator while a turn is running', () => {
    renderTimeline([], { isRunning: true });
    expect(screen.getByLabelText('Thinking…')).toBeTruthy();
  });
});
