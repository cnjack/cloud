/*
 * storage.test.ts — the CEK / pairing-session stores. fake-indexeddb is not a
 * dependency, so the IndexedDB implementations are covered by the shared
 * interface contract tested here against the memory stub (the IDB store is a
 * thin IDB translation of the same logic, used in the browser only).
 */
import { describe, expect, it } from 'vitest';
import { createMemoryCekStore, createMemoryPairingSessionStore } from './storage';

describe('memory CekStore', () => {
  it('round-trips a CEK per device and bumps the version on writes', async () => {
    const store = createMemoryCekStore();
    expect(await store.get('d1')).toBeNull();
    expect(store.version('d1')).toBe(0);

    const cek = { cek: new Uint8Array(32).fill(7), keyGen: 1 };
    await store.put('d1', cek);
    expect(await store.get('d1')).toEqual(cek);
    expect(store.version('d1')).toBe(1);

    await store.delete('d1');
    expect(await store.get('d1')).toBeNull();
    expect(store.version('d1')).toBe(2);
  });

  it('keeps devices isolated and notifies subscribers', async () => {
    const store = createMemoryCekStore();
    const seen: string[] = [];
    const unsub = store.subscribe((id) => seen.push(id));

    await store.put('d1', { cek: new Uint8Array(32).fill(1), keyGen: 1 });
    await store.put('d2', { cek: new Uint8Array(32).fill(2), keyGen: 4 });
    expect((await store.get('d1'))?.keyGen).toBe(1);
    expect((await store.get('d2'))?.keyGen).toBe(4);
    expect(seen).toEqual(['d1', 'd2']);

    unsub();
    await store.delete('d1');
    expect(seen).toEqual(['d1', 'd2']);
  });
});

describe('memory PairingSessionStore', () => {
  it('round-trips an in-flight pairing keyed by device', async () => {
    const store = createMemoryPairingSessionStore();
    expect(await store.get('d1')).toBeNull();
    const session = {
      deviceId: 'd1',
      pairingId: 'p1',
      pubkey: 'b64',
      privateKeyJwk: { kty: 'EC' } as JsonWebKey,
      createdAt: 123,
    };
    await store.put(session);
    expect(await store.get('d1')).toEqual(session);
    await store.delete('d1');
    expect(await store.get('d1')).toBeNull();
  });
});
