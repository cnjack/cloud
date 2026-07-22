import { fireEvent, render, screen } from '@testing-library/react';
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
  it('pending points at the Desktop cloud settings without QR or CLI fallbacks', () => {
    mockUseDevicePairing.mockReturnValue(pairingOf({ phase: 'pending', pairingId: 'pair-123' }));
    render(<DevicePairingCard deviceId="dev-1" />);

    expect(screen.getByText(/desktop/i)).toBeTruthy();
    expect(screen.getByText(/settings.*cloud/i)).toBeTruthy();
    expect(screen.queryByText(/QR code/i)).toBeNull();
    expect(screen.queryByText(/jcode cloud approve/i)).toBeNull();
  });

  it('idle starts a pairing request for Desktop review', () => {
    const start = vi.fn();
    mockUseDevicePairing.mockReturnValue(pairingOf({ phase: 'idle', start }));
    render(<DevicePairingCard deviceId="dev-1" />);

    expect(screen.getByText(/desktop/i)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /pair this client/i }));
    expect(start).toHaveBeenCalled();
  });
});
