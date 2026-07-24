import {
  createDeviceCrypto,
  createIdbCekStore,
  type CekStore,
  type StoredCek,
} from '@jcloud/device-ui';
import { isNativeRuntime, secureDelete, secureGet, secureSet } from './secureStorage';

const PREFIX = 'device.cek.';
const legacyStore = createIdbCekStore();
const versions = new Map<string, number>();
const subscribers = new Set<(deviceId: string) => void>();

function bump(deviceId: string) {
  versions.set(deviceId, (versions.get(deviceId) ?? 0) + 1);
  for (const subscriber of subscribers) subscriber(deviceId);
}

function serialize(value: StoredCek): string {
  return JSON.stringify({
    cek: Array.from(value.cek),
    keyGen: value.keyGen,
    pairingId: value.pairingId,
    privateKeyJwk: value.privateKeyJwk,
  });
}

function deserialize(raw: string): StoredCek | null {
  try {
    const value = JSON.parse(raw) as {
      cek?: unknown;
      keyGen?: unknown;
      pairingId?: unknown;
      privateKeyJwk?: unknown;
    };
    if (!Array.isArray(value.cek) || typeof value.keyGen !== 'number') return null;
    return {
      cek: new Uint8Array(value.cek as number[]),
      keyGen: value.keyGen,
      pairingId: typeof value.pairingId === 'string' ? value.pairingId : undefined,
      privateKeyJwk: (value.privateKeyJwk as JsonWebKey | undefined) ?? undefined,
    };
  } catch {
    return null;
  }
}

/** Android Keystore / iOS Keychain backed CEK store with one-time IndexedDB migration. */
export const mobileCekStore: CekStore = {
  get: async (deviceId) => {
    if (!isNativeRuntime()) return legacyStore.get(deviceId);
    const key = PREFIX + deviceId;
    const stored = await secureGet(key);
    if (stored) {
      const value = deserialize(stored);
      if (value) return value;
      await secureDelete(key);
    }

    const legacy = await legacyStore.get(deviceId);
    if (!legacy) return null;
    await secureSet(key, serialize(legacy));
    await legacyStore.delete(deviceId);
    return legacy;
  },
  put: async (deviceId, value) => {
    if (isNativeRuntime()) {
      await secureSet(PREFIX + deviceId, serialize(value));
      await legacyStore.delete(deviceId);
    } else {
      await legacyStore.put(deviceId, value);
    }
    bump(deviceId);
  },
  delete: async (deviceId) => {
    if (isNativeRuntime()) await secureDelete(PREFIX + deviceId);
    await legacyStore.delete(deviceId);
    bump(deviceId);
  },
  version: (deviceId) => versions.get(deviceId) ?? 0,
  subscribe: (subscriber) => {
    subscribers.add(subscriber);
    return () => subscribers.delete(subscriber);
  },
};

export const mobileDeviceCrypto = createDeviceCrypto(mobileCekStore);

