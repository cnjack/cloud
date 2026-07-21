import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DevicePairingCard } from './DevicePairingCard';
import type { DevicePairing } from '../hooks/useDevicePairing';

vi.mock('../hooks/useDevicePairing', () => ({
  useDevicePairing: vi.fn(),
}));

import { useDevicePairing } from '../hooks/useDevicePairing';

const mockUseDevicePairing = vi.mocked(useDevicePairing);

function pairingOf(partial: Partial<DevicePairing>): DevicePairing {
  return { phase: 'idle', pairingId: null, start: vi.fn(), starting: false, error: null, ...partial };
}

afterEach(() => vi.restoreAllMocks());

describe('DevicePairingCard (M17 desktop-first approval guidance)', () => {
  it('pending points at the desktop cloud badge, CLI demoted to a copyable fallback', () => {
    mockUseDevicePairing.mockReturnValue(pairingOf({ phase: 'pending', pairingId: 'pair-123' }));
    render(<DevicePairingCard deviceId="dev-1" />);

    // Primary guidance: approve in the desktop app (pulsing cloud badge).
    expect(screen.getByText(/desktop app/i)).toBeTruthy();
    expect(screen.getByText(/cloud badge/i)).toBeTruthy();
    // CLI is still offered, but as a labelled fallback footnote.
    expect(screen.getByText(/prefer the terminal/i)).toBeTruthy();
    expect(screen.getByTestId('pairing-cli-command').textContent).toBe('jcode cloud approve pair-123');
  });

  it('copies the CLI command to the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    mockUseDevicePairing.mockReturnValue(pairingOf({ phase: 'pending', pairingId: 'pair-123' }));
    render(<DevicePairingCard deviceId="dev-1" />);

    fireEvent.click(screen.getByTestId('pairing-copy-command'));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('jcode cloud approve pair-123'));
    await waitFor(() =>
      expect(screen.getByTestId('pairing-copy-command').getAttribute('aria-label')).toBe('Copied'),
    );
  });

  it('idle leads with the QR scan path and still offers start pairing', () => {
    const start = vi.fn();
    mockUseDevicePairing.mockReturnValue(pairingOf({ phase: 'idle', start }));
    render(<DevicePairingCard deviceId="dev-1" />);

    expect(screen.getByText(/QR code/i)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /pair this client/i }));
    expect(start).toHaveBeenCalled();
  });
});
