/*
 * deviceWrap.ts — test-only simulation of the DEVICE half of the pairing
 * handshake (docs/17 §6.3): given the client's P-256 pubkey (base64 SPKI),
 * produce the ECIES wrap blob a jcode device would upload on approve —
 * ECDH(ephemeral, client pubkey) → HKDF-SHA256(salt empty, info
 * "jcode-device-cek") → AES-256-GCM seal of {cek, key_gen}. Mirrors the jcode
 * implementation so the console's unwrap path is exercised against the real
 * contract, not against itself.
 */
import { b64decode, b64encode } from '../devicecrypto/envelope';
import { CEK_HKDF_INFO, type DeviceWrap } from '../devicecrypto/pairing';

export async function deviceWrapCek(
  clientPubkeyBase64: string,
  cek: Uint8Array,
  keyGen: number,
): Promise<DeviceWrap> {
  const clientPub = await crypto.subtle.importKey(
    'spki',
    b64decode(clientPubkeyBase64) as BufferSource,
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    [],
  );
  const ephemeral = await crypto.subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, true, [
    'deriveBits',
  ]);
  const shared = await crypto.subtle.deriveBits({ name: 'ECDH', public: clientPub }, ephemeral.privateKey, 256);
  const hkdfKey = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveKey']);
  const wrapKey = await crypto.subtle.deriveKey(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info: new TextEncoder().encode(CEK_HKDF_INFO),
    },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt'],
  );
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const ct = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv: nonce },
    wrapKey,
    new TextEncoder().encode(JSON.stringify({ cek: b64encode(cek), key_gen: keyGen })),
  );
  const ephemeralSpki = await crypto.subtle.exportKey('spki', ephemeral.publicKey);
  return {
    ephemeral_pubkey: b64encode(new Uint8Array(ephemeralSpki)),
    nonce: b64encode(nonce),
    ct: b64encode(new Uint8Array(ct)),
  };
}
