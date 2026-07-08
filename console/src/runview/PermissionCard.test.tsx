/*
 * PermissionCard.test.tsx — the F8b approval card's three states:
 *   1. pending           — title + one button per option; a click calls the
 *      host-injected onDecide(requestId, optionId)
 *   2. pending, decided  — the optimistic state (controls.decided): buttons
 *      inert, the chosen one marked, a "waiting for the agent" note
 *   3. resolved          — the chosen option's name + the resolution badge
 *      ("user" vs "timed out"), no live buttons
 * plus the viewer (read-only) rendering: buttons disabled, no onDecide firing.
 */
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { PermissionCard } from './PermissionCard';
import type { PermissionCardItem } from './types';

function card(over: Partial<PermissionCardItem> = {}): PermissionCardItem {
  return {
    kind: 'permission_card',
    seq: 7,
    ts: '2026-07-09T00:00:00Z',
    requestId: 'req-1',
    toolCallId: 'tc-1',
    title: 'Run `make deploy`',
    options: [
      { optionId: 'allow', name: 'Allow', kind: 'allow_once' },
      { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
    ],
    status: 'pending',
    ...over,
  };
}

describe('PermissionCard — pending', () => {
  it('renders the title and one active button per option; a click fires onDecide', () => {
    const onDecide = vi.fn();
    render(<PermissionCard item={card()} controls={{ onDecide }} />);

    expect(screen.getByText('Run `make deploy`')).toBeTruthy();
    const allow = screen.getByTestId('permission-option-allow') as HTMLButtonElement;
    const reject = screen.getByTestId('permission-option-reject') as HTMLButtonElement;
    expect(allow.disabled).toBe(false);
    expect(reject.disabled).toBe(false);
    expect(screen.queryByTestId('permission-waiting')).toBeNull();
    expect(screen.queryByTestId('permission-outcome')).toBeNull();

    fireEvent.click(allow);
    expect(onDecide).toHaveBeenCalledTimes(1);
    expect(onDecide).toHaveBeenCalledWith('req-1', 'allow');
  });

  it('renders inert without controls (a host with no approval surface)', () => {
    render(<PermissionCard item={card()} />);
    // Buttons exist but clicking them cannot throw (no onDecide wired).
    fireEvent.click(screen.getByTestId('permission-option-allow'));
    expect(screen.getByTestId('permission-card').getAttribute('data-status')).toBe('pending');
  });
});

describe('PermissionCard — decided (optimistic)', () => {
  it('greys every button, marks the chosen one and shows the waiting note', () => {
    const onDecide = vi.fn();
    render(
      <PermissionCard
        item={card()}
        controls={{ onDecide, decided: { 'req-1': 'allow' } }}
      />,
    );

    const allow = screen.getByTestId('permission-option-allow') as HTMLButtonElement;
    const reject = screen.getByTestId('permission-option-reject') as HTMLButtonElement;
    expect(allow.disabled).toBe(true);
    expect(reject.disabled).toBe(true);
    expect(allow.getAttribute('data-chosen')).toBeTruthy();
    expect(reject.getAttribute('data-chosen')).toBeNull();
    expect(screen.getByTestId('permission-waiting')).toBeTruthy();

    // Inert buttons can no longer decide.
    fireEvent.click(reject);
    expect(onDecide).not.toHaveBeenCalled();
  });

  it('a decision for a DIFFERENT request leaves this card active (keyed by request_id)', () => {
    render(<PermissionCard item={card()} controls={{ decided: { 'req-other': 'allow' } }} />);
    expect((screen.getByTestId('permission-option-allow') as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByTestId('permission-waiting')).toBeNull();
  });
});

describe('PermissionCard — resolved', () => {
  it('shows the chosen option name and the "user" resolution badge', () => {
    render(
      <PermissionCard
        item={card({ status: 'resolved', resolvedOptionId: 'allow', resolution: 'user' })}
      />,
    );
    expect(screen.getByTestId('permission-outcome').textContent).toContain('Allow');
    expect(screen.getByTestId('permission-resolution').textContent).toBe('user');
    // No live buttons on a resolved card.
    expect(screen.queryByTestId('permission-option-allow')).toBeNull();
  });

  it('shows the "timed out" badge and a graceful label for an empty option (Cancelled outcome)', () => {
    render(
      <PermissionCard
        item={card({ status: 'resolved', resolvedOptionId: '', resolution: 'timeout' })}
      />,
    );
    expect(screen.getByTestId('permission-resolution').textContent).toBe('timed out');
    expect(screen.getByTestId('permission-outcome').textContent).toContain('No action');
  });
});

describe('PermissionCard — viewer (read-only)', () => {
  it('disables the buttons, explains why, and never fires onDecide', () => {
    const onDecide = vi.fn();
    render(<PermissionCard item={card()} controls={{ onDecide, disabled: true }} />);

    const allow = screen.getByTestId('permission-option-allow') as HTMLButtonElement;
    expect(allow.disabled).toBe(true);
    expect(screen.getByTestId('permission-readonly')).toBeTruthy();
    fireEvent.click(allow);
    expect(onDecide).not.toHaveBeenCalled();
  });
});
