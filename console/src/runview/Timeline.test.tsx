/*
 * Timeline.test.tsx — rendering coverage on top of the pure grouping/eventModel
 * tests: merged text renders as markdown prose (not one row per chunk), a
 * paired tool call/result renders as one card with args collapsed/output
 * visible, and a run.result row surfaces the no_changes message.
 */
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Timeline } from './Timeline';
import type { RunViewEvent } from './types';

function ev(seq: number, type: string, payload: Record<string, unknown> = {}): RunViewEvent {
  return { seq, ts: new Date(seq * 1000).toISOString(), type, payload };
}

describe('Timeline — streaming text merge renders one prose block', () => {
  it('renders consecutive agent.text chunks as a single markdown block, not one row per chunk', () => {
    const events = [
      ev(1, 'agent.text', { text: 'Hello ' }),
      ev(2, 'agent.text', { text: '**world**' }),
      ev(3, 'agent.text', { text: '!' }),
    ];
    const { container } = render(<Timeline events={events} live={false} />);

    // Exactly one row for the whole merged message — not three.
    expect(container.querySelectorAll('[data-kind="text_block"]')).toHaveLength(1);
    // Markdown rendered (the merged **world** became a real <strong>), and the
    // chunk boundary produced no stray whitespace/row split.
    expect(container.querySelector('strong')?.textContent).toBe('world');
    expect(container.textContent).toContain('Hello');
    expect(container.textContent).toContain('world');
  });
});

describe('Timeline — tool call/result pairing', () => {
  it('renders a paired call/result as one card with the tool name and status', () => {
    const events = [
      ev(1, 'agent.tool_call', { tool: 'bash', call_id: 'c1', input: { cmd: 'ls' } }),
      ev(2, 'agent.tool_result', { call_id: 'c1', ok: true, output: 'file1' }),
    ];
    const { container } = render(<Timeline events={events} live={false} />);

    expect(container.querySelectorAll('[data-kind="tool_card"]')).toHaveLength(1);
    expect(screen.getByText('bash')).toBeTruthy();
    expect(screen.getByText('Succeeded')).toBeTruthy();
    // The output preview is visible without expanding.
    expect(screen.getByText('file1')).toBeTruthy();
  });

  it('degrades an orphan tool_result to the standalone collapsible row', () => {
    const events = [ev(1, 'agent.tool_result', { call_id: 'ghost', ok: false, output: 'nope' })];
    const { container } = render(<Timeline events={events} live={false} />);
    expect(container.querySelectorAll('[data-kind="tool_result"]')).toHaveLength(1);
    expect(container.querySelectorAll('[data-kind="tool_card"]')).toHaveLength(0);
  });
});

describe('Timeline — run.result (D18/D26)', () => {
  it('shows "No code changes" for a run.result(no_changes) event', () => {
    const events = [
      ev(1, 'run.status', { status: 'running' }),
      ev(2, 'run.result', { outcome: 'no_changes' }),
      ev(3, 'run.status', { status: 'succeeded' }),
    ];
    render(<Timeline events={events} live={false} />);
    expect(screen.getByTestId('timeline-result').textContent).toContain('No code changes');
  });
});

describe('Timeline — terminal row', () => {
  it('marks the terminal run.status row as the final end state', () => {
    const events = [
      ev(1, 'run.status', { status: 'running' }),
      ev(2, 'run.status', { status: 'succeeded' }),
    ];
    render(<Timeline events={events} live={false} />);
    expect(screen.getByTestId('timeline-final')).toBeTruthy();
  });
});

describe('Timeline — session events (D22)', () => {
  it('renders a user.message event as a user chat bubble with author + prompt', () => {
    const events = [
      ev(1, 'agent.text', { text: 'First answer.' }),
      ev(2, 'user.message', { prompt: 'Now add tests please', by: 'Ada' }),
      ev(3, 'agent.text', { text: 'Adding tests…' }),
    ];
    const { container } = render(<Timeline events={events} live={false} />);

    const bubble = screen.getByTestId('timeline-user-message');
    expect(bubble.textContent).toContain('Ada');
    expect(bubble.textContent).toContain('Now add tests please');
    // The user message BREAKS the text merge: two separate agent prose blocks.
    expect(container.querySelectorAll('[data-kind="text_block"]')).toHaveLength(2);
  });

  it('falls back to "you" when the author is absent (service principal)', () => {
    render(<Timeline events={[ev(1, 'user.message', { prompt: 'hi' })]} live={false} />);
    expect(screen.getByTestId('timeline-user-message').textContent).toContain('you');
  });

  it('renders session.finish as a compact system row with the idle-timeout reason', () => {
    render(
      <Timeline
        events={[ev(1, 'session.finish', { reason: 'idle_timeout' })]}
        live={false}
      />,
    );
    expect(screen.getByTestId('timeline-session-finish').textContent).toContain(
      'Session finished (idle timeout)',
    );
  });

  it('renders an awaiting_input status frame with the Awaiting input pill', () => {
    render(<Timeline events={[ev(1, 'run.status', { status: 'awaiting_input' })]} live={false} />);
    expect(screen.getByText('Awaiting input')).toBeTruthy();
  });
});

describe('Timeline — run.session (F9b / D23 ①②)', () => {
  it('renders a low-key system row (not an unknown block) for a resumed session', () => {
    const { container } = render(
      <Timeline
        events={[ev(1, 'run.session', { acp_session_id: 'acp-1', resumed: true })]}
        live={false}
      />,
    );
    expect(screen.getByTestId('timeline-session-info').textContent).toContain('Session resumed');
    // Not degraded to the unknown-event fallback.
    expect(container.querySelectorAll('[data-kind="unknown"]')).toHaveLength(0);
  });

  it('renders "Session established" for a fresh session', () => {
    render(
      <Timeline
        events={[ev(1, 'run.session', { acp_session_id: 'acp-1', resumed: false })]}
        live={false}
      />,
    );
    expect(screen.getByTestId('timeline-session-info').textContent).toContain('Session established');
  });
});
