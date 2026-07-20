/*
 * DevicesPage.test.tsx — the platform and E2EE lock badges on device cards:
 * known platforms translate, unknown/empty hide; pubkey ⇒ lock badge with the
 * E2EE tooltip, key generation noted once rotated past generation 1.
 */
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import type { Device } from '@jcloud/device-ui';
import { DevicesPage } from './DevicesPage';

const mocks = vi.hoisted(() => ({ devices: [] as Device[] }));

vi.mock('@jcloud/device-ui', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@jcloud/device-ui')>()),
  useDevices: () => ({
    data: mocks.devices,
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  }),
}));

function device(overrides: Partial<Device>): Device {
  return { id: 'd1', name: 'dev', online: true, ...overrides };
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/devices']}>
      <DevicesPage />
    </MemoryRouter>,
  );
}

describe('DevicesPage — platform badge', () => {
  it('translates known platforms and hides unknown or missing ones', () => {
    mocks.devices = [
      device({ id: 'd1', name: 'mac', platform: 'desktop' }),
      device({ id: 'd2', name: 'box', platform: 'cli' }),
      device({ id: 'd3', name: 'mystery', platform: 'toaster' }),
      device({ id: 'd4', name: 'plain' }),
    ];
    renderPage();
    expect(screen.getByText('Desktop')).toBeTruthy();
    expect(screen.getByText('CLI')).toBeTruthy();
    // Unknown platform renders no badge (and never leaks the raw value).
    expect(screen.queryByText('toaster')).toBeNull();
    expect(screen.getAllByTestId('device-card')).toHaveLength(4);
  });
});

describe('DevicesPage — E2EE lock badge', () => {
  it('shows the lock badge with the E2EE tooltip when pubkey is set', () => {
    mocks.devices = [
      device({ id: 'd1', name: 'enc', pubkey: 'cHVia2V5', key_gen: 1 }),
      device({ id: 'd2', name: 'rotated', pubkey: 'cHVia2V5', key_gen: 3 }),
      device({ id: 'd3', name: 'plain' }),
    ];
    renderPage();
    expect(screen.getByTitle('End-to-end encrypted')).toBeTruthy();
    expect(screen.getByTitle('End-to-end encrypted · key generation 3')).toBeTruthy();
    // Two encrypted devices ⇒ two lock badges; the pubkey-less one has none.
    expect(screen.getAllByTitle(/End-to-end encrypted/)).toHaveLength(2);
  });

  it('treats key_gen 0/1 as the first generation (no generation note)', () => {
    mocks.devices = [device({ id: 'd1', name: 'enc', pubkey: 'cHVia2V5', key_gen: 0 })];
    renderPage();
    expect(screen.getByTitle('End-to-end encrypted')).toBeTruthy();
    expect(screen.queryByTitle(/key generation/)).toBeNull();
  });
});
