#!/usr/bin/env node
/*
 * j9-client.mjs — E2EE client-side helper for j9-device-e2ee.sh (docs/17 §6).
 * Plays the role of a paired console/mobile client using node:crypto's
 * WebCrypto — the same algorithm stack as console/src/devicecrypto (verified
 * against jcode-cloud-relay/shared/test-vectors.json):
 *
 *   envelope: {"enc":"aes-256-gcm","key_gen":N,"nonce":b64(12B),"ct":b64}
 *             AES-256-GCM, no AAD.
 *   wrap:     ECDH(P-256, client priv × ephemeral pub)
 *             → HKDF-SHA256(salt="", info="jcode-device-cek")
 *             → AES-256-GCM over {"cek":b64,"key_gen":N}
 *
 * Subcommands:
 *   keygen <privkey-file>                      prints the P-256 pubkey (b64 SPKI)
 *   unwrap <wrap.json> <privkey-file> <cek-file>  writes {cek,key_gen} JSON
 *   seal <cek-file> <plaintext-file>           prints the envelope JSON
 *   open <cek-file> <envelope.json>            prints the decrypted plaintext
 */
import { webcrypto } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';

const subtle = webcrypto.subtle;
const HKDF_INFO = 'jcode-device-cek';
const te = new TextEncoder();
const td = new TextDecoder();

const b64e = (buf) => Buffer.from(buf).toString('base64');
const b64d = (s) => new Uint8Array(Buffer.from(s, 'base64'));

const die = (msg) => {
  console.error(`j9-client: ${msg}`);
  process.exit(2);
};

async function importCek(cekFile) {
  const { cek } = JSON.parse(readFileSync(cekFile, 'utf8'));
  return subtle.importKey('raw', b64d(cek), { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);
}

async function main() {
  const [cmd, ...args] = process.argv.slice(2);
  switch (cmd) {
    case 'keygen': {
      const [privFile] = args;
      if (!privFile) die('keygen <privkey-file>');
      const pair = await subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, true, [
        'deriveBits',
      ]);
      const spki = await subtle.exportKey('spki', pair.publicKey);
      const pkcs8 = await subtle.exportKey('pkcs8', pair.privateKey);
      writeFileSync(privFile, b64e(pkcs8));
      process.stdout.write(b64e(spki));
      return;
    }
    case 'unwrap': {
      const [wrapFile, privFile, cekFile] = args;
      if (!wrapFile || !privFile || !cekFile) die('unwrap <wrap.json> <privkey-file> <cek-file>');
      const wrap = JSON.parse(readFileSync(wrapFile, 'utf8'));
      const priv = await subtle.importKey(
        'pkcs8',
        b64d(readFileSync(privFile, 'utf8').trim()),
        { name: 'ECDH', namedCurve: 'P-256' },
        false,
        ['deriveBits'],
      );
      const ephPub = await subtle.importKey(
        'spki',
        b64d(wrap.ephemeral_pubkey),
        { name: 'ECDH', namedCurve: 'P-256' },
        false,
        [],
      );
      const shared = await subtle.deriveBits({ name: 'ECDH', public: ephPub }, priv, 256);
      const hkdfKey = await subtle.importKey('raw', shared, 'HKDF', false, ['deriveKey']);
      const wrapKey = await subtle.deriveKey(
        {
          name: 'HKDF',
          hash: 'SHA-256',
          salt: new Uint8Array(0),
          info: te.encode(HKDF_INFO),
        },
        hkdfKey,
        { name: 'AES-GCM', length: 256 },
        false,
        ['decrypt'],
      );
      const plain = await subtle.decrypt(
        { name: 'AES-GCM', iv: b64d(wrap.nonce) },
        wrapKey,
        b64d(wrap.ct),
      );
      const payload = JSON.parse(td.decode(plain));
      if (typeof payload.cek !== 'string' || typeof payload.key_gen !== 'number') {
        die('unwrap payload missing cek/key_gen');
      }
      writeFileSync(cekFile, JSON.stringify(payload));
      process.stdout.write(String(payload.key_gen));
      return;
    }
    case 'seal': {
      const [cekFile, plainFile] = args;
      if (!cekFile || !plainFile) die('seal <cek-file> <plaintext-file>');
      const { key_gen: keyGen } = JSON.parse(readFileSync(cekFile, 'utf8'));
      const key = await importCek(cekFile);
      const nonce = webcrypto.getRandomValues(new Uint8Array(12));
      const ct = await subtle.encrypt(
        { name: 'AES-GCM', iv: nonce },
        key,
        new Uint8Array(readFileSync(plainFile)),
      );
      process.stdout.write(
        JSON.stringify({ enc: 'aes-256-gcm', key_gen: keyGen, nonce: b64e(nonce), ct: b64e(ct) }),
      );
      return;
    }
    case 'open': {
      const [cekFile, envFile] = args;
      if (!cekFile || !envFile) die('open <cek-file> <envelope.json>');
      const env = JSON.parse(readFileSync(envFile, 'utf8'));
      if (env.enc !== 'aes-256-gcm') die(`unsupported enc ${JSON.stringify(env.enc)}`);
      const key = await importCek(cekFile);
      const plain = await subtle.decrypt({ name: 'AES-GCM', iv: b64d(env.nonce) }, key, b64d(env.ct));
      process.stdout.write(td.decode(plain));
      return;
    }
    default:
      die('usage: keygen|unwrap|seal|open (see header)');
  }
}

main().catch((err) => die(err instanceof Error ? err.message : String(err)));
