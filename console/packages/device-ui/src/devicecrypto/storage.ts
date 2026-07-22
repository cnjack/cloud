/*
 * storage.ts — client-side persistence for the device E2EE material
 * (docs/17 §6.3): the unwrapped CEK per device, and any in-flight pairing
 * session (so a pending approval survives a page reload).
 *
 * The real store is IndexedDB (db `jcode-device-e2ee`, object stores `ceks`
 * and `pairings`); tests and non-DOM runtimes use the in-memory stub behind
 * the same interfaces.
 */

export interface StoredCek {
  /** Raw 32-byte CEK. */
  cek: Uint8Array;
  keyGen: number;
  /** Durable approved-client identity used for rekey/revoke checks. */
  pairingId?: string;
  /** Kept so a later desktop rekey wrap can be opened without re-pairing. */
  privateKeyJwk?: JsonWebKey;
}

export interface CekStore {
  get(deviceId: string): Promise<StoredCek | null>;
  put(deviceId: string, cek: StoredCek): Promise<void>;
  delete(deviceId: string): Promise<void>;
  /** Monotonic per-device version bumped by put/delete — lets key caches invalidate. */
  version(deviceId: string): number;
  /** Subscribe to writes (returns the unsubscribe). */
  subscribe(cb: (deviceId: string) => void): () => void;
}

/** A pairing in flight: the private key stays client-side until it resolves. */
export interface PairingSession {
  deviceId: string;
  pairingId: string;
  pubkey: string;
  privateKeyJwk: JsonWebKey;
  createdAt: number;
}

export interface PairingSessionStore {
  get(deviceId: string): Promise<PairingSession | null>;
  put(session: PairingSession): Promise<void>;
  delete(deviceId: string): Promise<void>;
}

// --- in-memory stub (tests, non-DOM runtimes) ------------------------------------

export function createMemoryCekStore(): CekStore {
  const data = new Map<string, StoredCek>();
  const versions = new Map<string, number>();
  const subs = new Set<(deviceId: string) => void>();
  const bump = (deviceId: string) => {
    versions.set(deviceId, (versions.get(deviceId) ?? 0) + 1);
    for (const cb of subs) cb(deviceId);
  };
  return {
    get: async (deviceId) => data.get(deviceId) ?? null,
    put: async (deviceId, cek) => {
      data.set(deviceId, cek);
      bump(deviceId);
    },
    delete: async (deviceId) => {
      data.delete(deviceId);
      bump(deviceId);
    },
    version: (deviceId) => versions.get(deviceId) ?? 0,
    subscribe: (cb) => {
      subs.add(cb);
      return () => subs.delete(cb);
    },
  };
}

export function createMemoryPairingSessionStore(): PairingSessionStore {
  const data = new Map<string, PairingSession>();
  return {
    get: async (deviceId) => data.get(deviceId) ?? null,
    put: async (session) => {
      data.set(session.deviceId, session);
    },
    delete: async (deviceId) => {
      data.delete(deviceId);
    },
  };
}

// --- IndexedDB ------------------------------------------------------------------

const DB_NAME = 'jcode-device-e2ee';
const CEK_STORE = 'ceks';
const PAIRING_STORE = 'pairings';

function openDb(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      req.result.createObjectStore(CEK_STORE);
      req.result.createObjectStore(PAIRING_STORE);
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function idbReq<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

async function idbGet<T>(store: string, key: string): Promise<T | null> {
  const db = await openDb();
  try {
    const tx = db.transaction(store, 'readonly');
    const v = await idbReq<T | undefined>(tx.objectStore(store).get(key) as IDBRequest<T | undefined>);
    return v ?? null;
  } finally {
    db.close();
  }
}

async function idbPut(store: string, key: string, value: unknown): Promise<void> {
  const db = await openDb();
  try {
    const tx = db.transaction(store, 'readwrite');
    await idbReq(tx.objectStore(store).put(value, key));
  } finally {
    db.close();
  }
}

async function idbDelete(store: string, key: string): Promise<void> {
  const db = await openDb();
  try {
    const tx = db.transaction(store, 'readwrite');
    await idbReq(tx.objectStore(store).delete(key));
  } finally {
    db.close();
  }
}

interface StoredCekRecord {
  cek: number[];
  keyGen: number;
  pairingId?: string;
  privateKeyJwk?: JsonWebKey;
}

/**
 * createIdbCekStore persists CEKs in IndexedDB (serialized as byte arrays —
 * structured-clone friendly). Version/subscribe tracking is in-memory: the
 * console is a single-tab writer, so cross-tab invalidation is out of scope.
 */
export function createIdbCekStore(): CekStore {
  const versions = new Map<string, number>();
  const subs = new Set<(deviceId: string) => void>();
  const bump = (deviceId: string) => {
    versions.set(deviceId, (versions.get(deviceId) ?? 0) + 1);
    for (const cb of subs) cb(deviceId);
  };
  return {
    get: async (deviceId) => {
      const rec = await idbGet<StoredCekRecord>(CEK_STORE, deviceId);
      return rec ? {
        cek: new Uint8Array(rec.cek),
        keyGen: rec.keyGen,
        pairingId: rec.pairingId,
        privateKeyJwk: rec.privateKeyJwk,
      } : null;
    },
    put: async (deviceId, cek) => {
      const rec: StoredCekRecord = {
        cek: Array.from(cek.cek),
        keyGen: cek.keyGen,
        pairingId: cek.pairingId,
        privateKeyJwk: cek.privateKeyJwk,
      };
      await idbPut(CEK_STORE, deviceId, rec);
      bump(deviceId);
    },
    delete: async (deviceId) => {
      await idbDelete(CEK_STORE, deviceId);
      bump(deviceId);
    },
    version: (deviceId) => versions.get(deviceId) ?? 0,
    subscribe: (cb) => {
      subs.add(cb);
      return () => subs.delete(cb);
    },
  };
}

export function createIdbPairingSessionStore(): PairingSessionStore {
  return {
    get: (deviceId) => idbGet<PairingSession>(PAIRING_STORE, deviceId),
    put: (session) => idbPut(PAIRING_STORE, session.deviceId, session),
    delete: (deviceId) => idbDelete(PAIRING_STORE, deviceId),
  };
}
