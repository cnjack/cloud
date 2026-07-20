/*
 * envelope.ts — the M5 E2EE wire envelope (docs/17-jcode-device-relay §6.2).
 *
 * A payload is either plaintext JSON (M3 gray rollout) or an envelope:
 *
 *   { "enc": "aes-256-gcm", "key_gen": N, "nonce": "<base64 12B>", "ct": "<base64>" }
 *
 * Detection rule (shared with the device/orchestrator): an OBJECT with a
 * string `enc` field is an envelope; anything else is plaintext and must be
 * passed through untouched. The server never parses payloads — all
 * encrypt/decrypt happens here, in the client.
 */

/** The AES-256-GCM envelope shape on the wire. */
export interface DeviceEnvelope {
  enc: string;
  key_gen: number;
  /** base64, 12 bytes. */
  nonce: string;
  /** base64 ciphertext (+ GCM tag). */
  ct: string;
}

/** isEnvelope implements the shared detection rule: object + string `enc`. */
export function isEnvelope(v: unknown): v is DeviceEnvelope {
  return (
    typeof v === 'object' &&
    v !== null &&
    typeof (v as { enc?: unknown }).enc === 'string'
  );
}

export function b64encode(bytes: Uint8Array): string {
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

export function b64decode(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** importCek loads a raw 32-byte CEK for AES-256-GCM use. */
export function importCek(raw: Uint8Array): Promise<CryptoKey> {
  return crypto.subtle.importKey('raw', raw as BufferSource, { name: 'AES-GCM' }, false, [
    'encrypt',
    'decrypt',
  ]);
}

/** encryptJson seals a JSON value under the CEK as a fresh envelope. */
export async function encryptJson(
  cek: CryptoKey,
  keyGen: number,
  value: unknown,
): Promise<DeviceEnvelope> {
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const ct = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv: nonce },
    cek,
    new TextEncoder().encode(JSON.stringify(value)),
  );
  return {
    enc: 'aes-256-gcm',
    key_gen: keyGen,
    nonce: b64encode(nonce),
    ct: b64encode(new Uint8Array(ct)),
  };
}

/** decryptJson opens an envelope and parses its JSON plaintext. */
export async function decryptJson(cek: CryptoKey, env: DeviceEnvelope): Promise<unknown> {
  const pt = await decryptText(cek, env);
  return JSON.parse(pt);
}

/** decryptText opens an envelope and returns the raw UTF-8 plaintext. */
export async function decryptText(cek: CryptoKey, env: DeviceEnvelope): Promise<string> {
  const pt = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: b64decode(env.nonce) as BufferSource },
    cek,
    b64decode(env.ct) as BufferSource,
  );
  return new TextDecoder().decode(pt);
}
