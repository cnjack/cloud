/*
 * pairing.ts — the client half of the CEK pairing handshake (docs/17 §6.3).
 *
 * The client generates a P-256 ECDH key pair and sends the public key (SPKI,
 * base64) with its pairing request. The device, on approve, replies with an
 * ECIES wrap:
 *
 *   { "ephemeral_pubkey": "<b64 SPKI>", "nonce": "<b64 12B>", "ct": "<b64>" }
 *
 * Unwrapping: ECDH(client private, device ephemeral public) → HKDF-SHA256
 * (salt = empty, info = "jcode-device-cek") → AES-256-GCM open →
 * { "cek": "<b64>", "key_gen": N }.
 */
import { b64decode, b64encode } from './envelope';

/** The device's wrap blob (opaque to the orchestrator). */
export interface DeviceWrap {
  ephemeral_pubkey: string;
  nonce: string;
  ct: string;
}

/** HKDF info string — part of the wire contract, never change. */
export const CEK_HKDF_INFO = 'jcode-device-cek';

/** A fresh pairing key pair plus its wire/serialisable forms. */
export interface PairingKeys {
  privateKey: CryptoKey;
  /** base64 SPKI — sent as `pubkey` in the pairing request. */
  pubkeyBase64: string;
  /** JWK export — persisted in IndexedDB so a pending pairing survives reloads. */
  privateKeyJwk: JsonWebKey;
}

/** generatePairingKeys mints the client's P-256 ECDH identity for one pairing. */
export async function generatePairingKeys(): Promise<PairingKeys> {
  const kp = await crypto.subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, true, [
    'deriveBits',
  ]);
  const spki = await crypto.subtle.exportKey('spki', kp.publicKey);
  const privateKeyJwk = await crypto.subtle.exportKey('jwk', kp.privateKey);
  return {
    privateKey: kp.privateKey,
    pubkeyBase64: b64encode(new Uint8Array(spki)),
    privateKeyJwk,
  };
}

/** importPairingPrivateKey restores a persisted pairing private key (JWK). */
export function importPairingPrivateKey(jwk: JsonWebKey): Promise<CryptoKey> {
  return crypto.subtle.importKey('jwk', jwk, { name: 'ECDH', namedCurve: 'P-256' }, true, [
    'deriveBits',
  ]);
}

/** deriveWrapKey is the shared ECDH → HKDF-SHA256 → AES-256-GCM derivation. */
async function deriveWrapKey(privateKey: CryptoKey, ephemeralSpki: Uint8Array): Promise<CryptoKey> {
  const ephemeral = await crypto.subtle.importKey(
    'spki',
    ephemeralSpki as BufferSource,
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    [],
  );
  const shared = await crypto.subtle.deriveBits({ name: 'ECDH', public: ephemeral }, privateKey, 256);
  const hkdfKey = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveKey']);
  return crypto.subtle.deriveKey(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info: new TextEncoder().encode(CEK_HKDF_INFO),
    },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt'],
  );
}

/** unwrapCek opens the device's wrap blob with the client's pairing private key. */
export async function unwrapCek(
  privateKey: CryptoKey,
  wrap: DeviceWrap,
): Promise<{ cek: Uint8Array; keyGen: number }> {
  const key = await deriveWrapKey(privateKey, b64decode(wrap.ephemeral_pubkey));
  const pt = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: b64decode(wrap.nonce) as BufferSource },
    key,
    b64decode(wrap.ct) as BufferSource,
  );
  const inner = JSON.parse(new TextDecoder().decode(pt)) as { cek?: unknown; key_gen?: unknown };
  if (typeof inner.cek !== 'string' || typeof inner.key_gen !== 'number') {
    throw new Error('pairing wrap: unexpected CEK payload shape');
  }
  return { cek: b64decode(inner.cek), keyGen: inner.key_gen };
}
