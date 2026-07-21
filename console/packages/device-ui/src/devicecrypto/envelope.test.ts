/*
 * envelope.test.ts — the AES-256-GCM wire envelope (docs/17 §6.2): the
 * detection rule, an encrypt/decrypt roundtrip, and the cross-implementation
 * test vector shared with the jcode device side
 * (jcode-cloud-relay/shared/test-vectors.json) — the console MUST open the
 * device's envelope to exactly the recorded plaintext.
 */
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  b64decode,
  decryptJson,
  decryptText,
  encryptJson,
  importCek,
  isEnvelope,
  type DeviceEnvelope,
} from './envelope';

describe('isEnvelope', () => {
  it('detects the enc marker; everything else is gray plaintext', () => {
    expect(isEnvelope({ enc: 'aes-256-gcm', key_gen: 1, nonce: 'AA==', ct: 'AA==' })).toBe(true);
    expect(isEnvelope({ enc: 'xchacha20poly1305' })).toBe(true); // marker is what matters
    expect(isEnvelope({ title: 'plain meta' })).toBe(false);
    expect(isEnvelope({ enc: 42 })).toBe(false);
    expect(isEnvelope(null)).toBe(false);
    expect(isEnvelope('enc')).toBe(false);
    expect(isEnvelope(undefined)).toBe(false);
  });
});

describe('encrypt/decrypt roundtrip', () => {
  it('seals JSON and opens it back', async () => {
    const cek = crypto.getRandomValues(new Uint8Array(32));
    const key = await importCek(cek);
    const value = { title: 's', nested: { n: 1 } };
    const env = await encryptJson(key, 3, value);
    expect(env.enc).toBe('aes-256-gcm');
    expect(env.key_gen).toBe(3);
    expect(b64decode(env.nonce)).toHaveLength(12);
    expect(await decryptJson(key, env)).toEqual(value);
  });

  it('rejects a wrong key (GCM auth)', async () => {
    const key = await importCek(crypto.getRandomValues(new Uint8Array(32)));
    const wrong = await importCek(crypto.getRandomValues(new Uint8Array(32)));
    const env = await encryptJson(key, 1, 'secret');
    await expect(decryptText(wrong, env)).rejects.toThrow();
  });
});

describe('cross-implementation test vectors', () => {
  // Canonical copy lives IN THE REPO (same dir as this test) so CI checkouts
  // are self-contained; the workspace-level file at
  // jcode-cloud-relay/shared/test-vectors.json is the dev-time cross-repo
  // source of truth and, when present, is preferred so both sides verify the
  // freshest vectors.
  const vectorPath = [
    '../jcode-cloud-relay/shared/test-vectors.json',
    '../../jcode-cloud-relay/shared/test-vectors.json',
    '../../../jcode-cloud-relay/shared/test-vectors.json',
    // packages/device-ui cwd (the suite also runs from the package root).
    '../../../../jcode-cloud-relay/shared/test-vectors.json',
  ]
    .map((p) => resolve(process.cwd(), p))
    .find((p) => existsSync(p));
  const vectorsJson = vectorPath
    ? readFileSync(vectorPath, 'utf8')
    : readFileSync(new URL('./test-vectors.json', import.meta.url), 'utf8');
  const { vectors } = JSON.parse(vectorsJson) as {
    vectors: Array<{ origin: string; cek_b64: string; plaintext: string; envelope: DeviceEnvelope }>;
  };

  for (const vector of vectors) {
    it(`opens the ${vector.origin} envelope to exactly the recorded plaintext`, async () => {
      const key = await importCek(b64decode(vector.cek_b64));
      expect(isEnvelope(vector.envelope)).toBe(true);
      expect(vector.envelope.enc).toBe('aes-256-gcm');
      expect(await decryptText(key, vector.envelope)).toBe(vector.plaintext);
    });
  }
});
