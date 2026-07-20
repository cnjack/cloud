/*
 * provider.ts — the DeviceCrypto key source the API layer decrypts with.
 *
 * createDeviceCrypto caches imported CryptoKeys per device and invalidates on
 * the CekStore's version counter (bumped by every put/delete), so a CEK that
 * arrives mid-session (pairing completes) is picked up on the next lookup
 * without a reload.
 */
import { importCek } from './envelope';
import {
  createIdbCekStore,
  createIdbPairingSessionStore,
  createMemoryCekStore,
  createMemoryPairingSessionStore,
  type CekStore,
  type PairingSessionStore,
} from './storage';

export interface DeviceCrypto {
  /** The device's imported CEK, or null when this client has not paired yet. */
  getKey(deviceId: string): Promise<CryptoKey | null>;
  /** The current CEK generation, or null when unpaired. */
  getKeyGen(deviceId: string): Promise<number | null>;
  /** The underlying store (the pairing flow writes the unwrapped CEK here). */
  readonly store: CekStore;
}

export function createDeviceCrypto(store: CekStore): DeviceCrypto {
  const cache = new Map<string, { version: number; key: CryptoKey | null; keyGen: number | null }>();

  async function lookup(deviceId: string) {
    const version = store.version(deviceId);
    const hit = cache.get(deviceId);
    if (hit && hit.version === version) return hit;
    const stored = await store.get(deviceId);
    const entry = {
      version,
      key: stored ? await importCek(stored.cek) : null,
      keyGen: stored?.keyGen ?? null,
    };
    cache.set(deviceId, entry);
    return entry;
  }

  return {
    getKey: async (deviceId) => (await lookup(deviceId)).key,
    getKeyGen: async (deviceId) => (await lookup(deviceId)).keyGen,
    store,
  };
}

// --- shared runtime singletons ---------------------------------------------------
// The app's single crypto/store instances: DeviceApiProvider decrypts with
// them and useDevicePairing writes the freshly-paired CEK into them, so a
// completed pairing takes effect on the very next fetch/frame.

/** IndexedDB in the browser; the memory stub elsewhere (tests, SSR-less tools). */
const hasIdb = typeof indexedDB !== 'undefined';

export const sharedCekStore: CekStore = hasIdb ? createIdbCekStore() : createMemoryCekStore();

export const sharedPairingSessions: PairingSessionStore = hasIdb
  ? createIdbPairingSessionStore()
  : createMemoryPairingSessionStore();

export const sharedDeviceCrypto: DeviceCrypto = createDeviceCrypto(sharedCekStore);
