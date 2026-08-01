import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { decryptArtifactShare, parseArtifactShareKey, type SharedArtifactEnvelope } from './artifactShareCrypto';

type Vector = {
  protocol: 'jcode-artifact-share-v1';
  share_id: string;
  artifact_id: string;
  revision: number;
  key_b64url: string;
  metadata_plaintext: string;
  metadata_nonce_b64url: string;
  metadata_ciphertext_b64url: string;
  content_plaintext_b64url: string;
  content_wire_b64url: string;
  content_wire_sha256: string;
};

const decode = (value: string) => {
  const padded = value.replaceAll('-', '+').replaceAll('_', '/') + '='.repeat((4 - value.length % 4) % 4);
  return Uint8Array.from(atob(padded), (char) => char.charCodeAt(0));
};

describe('artifact share cross-runtime vector', () => {
  it('decrypts the canonical JCode AES-GCM wire format in WebCrypto', async () => {
    const vector = JSON.parse(readFileSync(resolve(process.cwd(), '../shared/artifact-share-v1.json'), 'utf8')) as Vector;
    const metadataLength = new TextEncoder().encode(vector.metadata_plaintext).length;
    const wire = decode(vector.content_wire_b64url);
    const envelope: SharedArtifactEnvelope = {
      share_id: vector.share_id,
      protocol: vector.protocol,
      artifact_id: vector.artifact_id,
      revision: vector.revision,
      encrypted_metadata: {
        nonce: vector.metadata_nonce_b64url,
        ciphertext: vector.metadata_ciphertext_b64url,
        plaintext_length: metadataLength,
      },
      ciphertext_size: wire.length,
      ciphertext_sha256: vector.content_wire_sha256,
      expires_at: '2030-01-01T00:00:00Z',
    };

    const result = await decryptArtifactShare(envelope, wire, parseArtifactShareKey(`#k=v1.${vector.key_b64url}`));
    expect(result.metadata).toEqual(JSON.parse(vector.metadata_plaintext));
    expect(result.content).toEqual(decode(vector.content_plaintext_b64url));
  });

  it('fails closed when the ciphertext digest is absent', async () => {
    const vector = JSON.parse(readFileSync(resolve(process.cwd(), '../shared/artifact-share-v1.json'), 'utf8')) as Vector;
    const metadataLength = new TextEncoder().encode(vector.metadata_plaintext).length;
    const wire = decode(vector.content_wire_b64url);
    const envelope: SharedArtifactEnvelope = {
      share_id: vector.share_id,
      protocol: vector.protocol,
      artifact_id: vector.artifact_id,
      revision: vector.revision,
      encrypted_metadata: {
        nonce: vector.metadata_nonce_b64url,
        ciphertext: vector.metadata_ciphertext_b64url,
        plaintext_length: metadataLength,
      },
      ciphertext_size: wire.length,
      ciphertext_sha256: '',
      expires_at: '2030-01-01T00:00:00Z',
    };

    await expect(decryptArtifactShare(envelope, wire, parseArtifactShareKey(`#k=v1.${vector.key_b64url}`)))
      .rejects.toThrow('artifact_decrypt_failed');
  });
});
