/*
 * DevicePairingGate.test.tsx — the M13 pairing gate: e2ee devices render the
 * pairing guide instead of the session surfaces until the client holds the
 * CEK; a completed pairing (CEK written to the store) unlocks without a
 * reload; non-e2ee (gray-rollout) devices are never gated.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { DeviceApiProvider } from '../api/DeviceApiProvider';
import type { Device, DeviceApi } from '../api/devices';
import { createDeviceCrypto } from '../devicecrypto/provider';
import { createMemoryCekStore } from '../devicecrypto/storage';
import { DevicePairingGate } from './DevicePairingGate';

const CEK_RAW = crypto.getRandomValues(new Uint8Array(32));

function makeDevice(overrides: Partial<Device> = {}): Device {
  return { id: 'dev-gate', name: 'gate device', online: true, ...overrides };
}

function makeRig() {
  const store = createMemoryCekStore();
  return { store, crypto: createDeviceCrypto(store) };
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <DeviceApiProvider api={{} as DeviceApi}>{children}</DeviceApiProvider>
    </QueryClientProvider>
  );
}

const CHILDREN = <div data-testid="gated-surface">session surface</div>;

describe('DevicePairingGate', () => {
  it('gates an e2ee device when the client holds no CEK', async () => {
    const rig = makeRig();
    render(
      <DevicePairingGate device={makeDevice({ e2ee: true })} crypto={rig.crypto}>
        {CHILDREN}
      </DevicePairingGate>,
      { wrapper },
    );
    await waitFor(() => expect(screen.getByTestId('device-pairing-gate')).toBeTruthy());
    expect(screen.queryByTestId('gated-surface')).toBeNull();
    // The guide explains why the surface is hidden and offers pairing. The
    // card mounts after the pairing hook's first effect — on slow runners
    // (CI) it may lag the gate by a tick.
    expect(screen.getByText('Pairing required')).toBeTruthy();
    await waitFor(() => expect(screen.getByTestId('device-pairing-card')).toBeTruthy());
  });

  it('renders children for an e2ee device once the CEK is stored', async () => {
    const rig = makeRig();
    await rig.store.put('dev-gate', { cek: CEK_RAW, keyGen: 1 });
    render(
      <DevicePairingGate device={makeDevice({ e2ee: true })} crypto={rig.crypto}>
        {CHILDREN}
      </DevicePairingGate>,
      { wrapper },
    );
    await waitFor(() => expect(screen.getByTestId('gated-surface')).toBeTruthy());
    expect(screen.queryByTestId('device-pairing-gate')).toBeNull();
  });

  it('never gates a non-e2ee (gray-rollout) device — no CEK lookup', async () => {
    const rig = makeRig();
    // A crypto whose lookups blow up proves the pass-through never asks.
    const crypto = {
      ...rig.crypto,
      getKey: async () => {
        throw new Error('must not be called');
      },
    };
    render(
      <DevicePairingGate device={makeDevice()} crypto={crypto}>
        {CHILDREN}
      </DevicePairingGate>,
      { wrapper },
    );
    expect(screen.getByTestId('gated-surface')).toBeTruthy();
    expect(screen.queryByTestId('device-pairing-gate')).toBeNull();
  });

  it('passes through while the device is still loading (undefined)', () => {
    const rig = makeRig();
    render(
      <DevicePairingGate device={undefined} crypto={rig.crypto}>
        {CHILDREN}
      </DevicePairingGate>,
      { wrapper },
    );
    expect(screen.getByTestId('gated-surface')).toBeTruthy();
  });

  it('unlocks automatically when the CEK lands in the store (pairing completed)', async () => {
    const rig = makeRig();
    render(
      <DevicePairingGate device={makeDevice({ e2ee: true })} crypto={rig.crypto}>
        {CHILDREN}
      </DevicePairingGate>,
      { wrapper },
    );
    await waitFor(() => expect(screen.getByTestId('device-pairing-gate')).toBeTruthy());

    // The pairing flow writes the unwrapped CEK into the store; the version
    // bump + subscription re-resolves the gate without a reload.
    await act(async () => {
      await rig.store.put('dev-gate', { cek: CEK_RAW, keyGen: 1 });
    });
    await waitFor(() => expect(screen.getByTestId('gated-surface')).toBeTruthy());
    expect(screen.queryByTestId('device-pairing-gate')).toBeNull();
  });
});
