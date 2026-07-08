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
