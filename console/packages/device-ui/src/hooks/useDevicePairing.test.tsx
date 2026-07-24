/*
 * useDevicePairing.test.tsx — the pairing state machine: idle → pending →
 * (approved ⇒ CEK unwrapped + stored ⇒ ready) / denied / expired, with the
 * device side simulated under the wire contract (deviceWrapCek).
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { DeviceApiProvider } from '../api/DeviceApiProvider';
import type { DeviceApi } from '../api/devices';
import { importCek, decryptText, encryptJson } from '../devicecrypto/envelope';
import { createDeviceCrypto } from '../devicecrypto/provider';
import { createMemoryCekStore, createMemoryPairingSessionStore } from '../devicecrypto/storage';
import { deviceWrapCek } from '../test/deviceWrap';
import { useDevicePairing } from './useDevicePairing';

const DEVICE = 'dev-1';
const CEK_RAW = crypto.getRandomValues(new Uint8Array(32));

interface Rig {
  api: DeviceApi;
  cekStore: ReturnType<typeof createMemoryCekStore>;
  sessions: ReturnType<typeof createMemoryPairingSessionStore>;
  crypto: ReturnType<typeof createDeviceCrypto>;
  capturedPubkey: () => string;
  approve: () => void;
  deny: () => void;
  reset: () => void;
  createCalls: number;
}

function makeRig(): Rig {
  const cekStore = createMemoryCekStore();
  const sessions = createMemoryPairingSessionStore();
  const crypto = createDeviceCrypto(cekStore);
  let pubkey = '';
  let decision: 'pending' | 'approved' | 'denied' = 'pending';
  const rig: Rig = {
    api: {
      createPairing: async (_d: string, req: { label: string; kty: string; pubkey: string }) => {
        rig.createCalls += 1;
        expect(req.kty).toBe('P-256');
        expect(req.pubkey).toBeTruthy();
        pubkey = req.pubkey;
        return { pairing_id: 'pair-1', status: 'pending' };
      },
      getPairing: async () => {
        if (decision === 'approved') {
          return { status: 'approved', wrap: await deviceWrapCek(pubkey, CEK_RAW, 3) };
        }
        return { status: decision } as never;
      },
    } as unknown as DeviceApi,
    cekStore,
    sessions,
    crypto,
    capturedPubkey: () => pubkey,
    approve: () => {
      decision = 'approved';
    },
    deny: () => {
      decision = 'denied';
    },
    /** Back to pending — the device has not decided the NEW pairing yet. */
    reset: () => {
      decision = 'pending';
    },
    createCalls: 0,
  };
  return rig;
}

function wrapperFor(rig: Rig) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <DeviceApiProvider api={rig.api} crypto={rig.crypto}>{children}</DeviceApiProvider>
      </QueryClientProvider>
    );
  };
}

function renderPairing(rig: Rig) {
  return renderHook(
    () =>
      useDevicePairing(DEVICE, {
        sessions: rig.sessions,
        pollMs: 5,
      }),
    { wrapper: wrapperFor(rig) },
  );
}

describe('useDevicePairing', () => {
  it('is ready immediately when the CEK is already stored', async () => {
    const rig = makeRig();
    await rig.cekStore.put(DEVICE, { cek: CEK_RAW, keyGen: 1 });
    const { result } = renderPairing(rig);
    await waitFor(() => expect(result.current.phase).toBe('ready'));
  });

  it('runs idle → pending → approved ⇒ ready, unwrapping and storing the CEK', async () => {
    const rig = makeRig();
    const { result } = renderPairing(rig);
    await waitFor(() => expect(result.current.phase).toBe('idle'));

    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe('pending'));
    expect(result.current.pairingId).toBe('pair-1');
    expect(rig.createCalls).toBe(1);
    // The in-flight pairing (incl. the private key) is persisted.
    expect((await rig.sessions.get(DEVICE))?.pairingId).toBe('pair-1');

    act(() => rig.approve());
    // CI-only flake surfaced phase 'error' here with no diagnosable detail —
    // dump the underlying error on timeout so the next failure is
    // self-explaining in the CI log.
    try {
      await waitFor(() => expect(result.current.phase).toBe('ready'));
    } catch (err) {
      console.error(
        '[useDevicePairing.test] ready never came; phase=',
        result.current.phase,
        'error=',
        result.current.error,
      );
      throw err;
    }
    expect(result.current.pairingId).toBeNull();

    // The unwrapped CEK landed in the store with the device's key generation,
    // and actually decrypts content sealed under it.
    const stored = await rig.cekStore.get(DEVICE);
    expect(stored).not.toBeNull();
    expect(Array.from(stored!.cek)).toEqual(Array.from(CEK_RAW));
    expect(stored!.keyGen).toBe(3);
    expect(await rig.sessions.get(DEVICE)).toBeNull();

    const key = await importCek(stored!.cek);
    const env = await encryptJson(key, 3, { title: 'paired' });
    expect(await decryptText(key, env)).toBe(JSON.stringify({ title: 'paired' }));
  });

  it('keeps slow approval polls single-flight while unwrapping the CEK', async () => {
    const rig = makeRig();
    const getPairing = rig.api.getPairing.bind(rig.api);
    let concurrent = 0;
    let maxConcurrent = 0;
    rig.api.getPairing = async (...args) => {
      concurrent += 1;
      maxConcurrent = Math.max(maxConcurrent, concurrent);
      try {
        await new Promise((resolve) => setTimeout(resolve, 20));
        return await getPairing(...args);
      } finally {
        concurrent -= 1;
      }
    };

    const { result } = renderPairing(rig);
    await waitFor(() => expect(result.current.phase).toBe('idle'));
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe('pending'));
    act(() => rig.approve());
    await waitFor(() => expect(result.current.phase).toBe('ready'));

    expect(maxConcurrent).toBe(1);
  });

  it('surfaces a denial and can start over', async () => {
    const rig = makeRig();
    const { result } = renderPairing(rig);
    await waitFor(() => expect(result.current.phase).toBe('idle'));

    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe('pending'));
    act(() => rig.deny());
    await waitFor(() => expect(result.current.phase).toBe('denied'));
    expect(await rig.sessions.get(DEVICE)).toBeNull();

    rig.reset(); // the fresh pairing has no decision yet
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe('pending'));
    expect(rig.createCalls).toBe(2);
  });

  it('resumes a persisted pending pairing (reload mid-flight)', async () => {
    const rig = makeRig();
    const first = renderPairing(rig);
    await waitFor(() => expect(first.result.current.phase).toBe('idle'));
    act(() => first.result.current.start());
    await waitFor(() => expect(first.result.current.phase).toBe('pending'));
    first.unmount();

    // A fresh hook (the "reload") finds the persisted pairing and resumes
    // polling without re-POSTing.
    const second = renderPairing(rig);
    await waitFor(() => expect(second.result.current.phase).toBe('pending'));
    expect(second.result.current.pairingId).toBe('pair-1');
    expect(rig.createCalls).toBe(1);

    act(() => rig.approve());
    await waitFor(() => expect(second.result.current.phase).toBe('ready'));
    second.unmount();
  });
});
