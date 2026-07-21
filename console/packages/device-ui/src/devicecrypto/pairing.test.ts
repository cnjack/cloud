/*
 * pairing.test.ts — the client half of the P-256 ECIES pairing (docs/17 §6.3):
 * key generation/export, and unwrapCek against a simulated device wrap that
 * follows the wire contract (ECDH → HKDF-SHA256 info "jcode-device-cek" →
 * AES-256-GCM).
 */
import { describe, expect, it } from 'vitest';
import { deviceWrapCek } from '../test/deviceWrap';
import { b64decode } from './envelope';
import { generatePairingKeys, importPairingPrivateKey, unwrapCek } from './pairing';

describe('generatePairingKeys', () => {
  it('exports a P-256 SPKI pubkey and a re-importable JWK private key', async () => {
    const keys = await generatePairingKeys();
    // SPKI of a P-256 key is 91 bytes.
    expect(b64decode(keys.pubkeyBase64)).toHaveLength(91);
    expect(keys.privateKeyJwk.kty).toBe('EC');
    expect(keys.privateKeyJwk.crv).toBe('P-256');
    const reimported = await importPairingPrivateKey(keys.privateKeyJwk);
    expect(reimported.type).toBe('private');
  });
});

describe('unwrapCek', () => {
  it('opens a device wrap produced under the wire contract', async () => {
    const keys = await generatePairingKeys();
    const cek = crypto.getRandomValues(new Uint8Array(32));
    const wrap = await deviceWrapCek(keys.pubkeyBase64, cek, 2);

    const { cek: got, keyGen } = await unwrapCek(keys.privateKey, wrap);
    expect(Array.from(got)).toEqual(Array.from(cek));
    expect(keyGen).toBe(2);
  });

  it('works with the persisted (JWK round-tripped) private key', async () => {
    const keys = await generatePairingKeys();
    const cek = crypto.getRandomValues(new Uint8Array(32));
    const wrap = await deviceWrapCek(keys.pubkeyBase64, cek, 1);

    const restored = await importPairingPrivateKey(JSON.parse(JSON.stringify(keys.privateKeyJwk)));
    const { cek: got } = await unwrapCek(restored, wrap);
    expect(Array.from(got)).toEqual(Array.from(cek));
  });

  it('rejects a wrap made for a different client key', async () => {
    const alice = await generatePairingKeys();
    const bob = await generatePairingKeys();
    const wrap = await deviceWrapCek(alice.pubkeyBase64, crypto.getRandomValues(new Uint8Array(32)), 1);
    await expect(unwrapCek(bob.privateKey, wrap)).rejects.toThrow();
  });
});
