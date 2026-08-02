export const ARTIFACT_SHARE_PROTOCOL = 'jcode-artifact-share-v1' as const;

export type ArtifactSharePart = 'metadata' | 'content';

export type EncryptedArtifactMetadata = {
  nonce: string;
  ciphertext: string;
  plaintext_length: number;
};

export type SharedArtifactEnvelope = {
  share_id: string;
  protocol: typeof ARTIFACT_SHARE_PROTOCOL;
  artifact_id: string;
  revision: number;
  encrypted_metadata: EncryptedArtifactMetadata;
  ciphertext_size: number;
  ciphertext_sha256: string;
  expires_at: string;
};

export type SharedArtifactMetadata = {
  title: string;
  relative_path: string;
  media_type: string;
  kind: string;
  size: number;
};

const encoder = new TextEncoder();

function exactArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  return Uint8Array.from(bytes).buffer;
}

export function artifactShareAAD(
  shareId: string,
  part: ArtifactSharePart,
  artifactId: string,
  revision: number,
  plaintextLength: number,
): Uint8Array {
  if (!Number.isSafeInteger(revision) || revision <= 0 || !Number.isSafeInteger(plaintextLength) || plaintextLength < 0) {
    throw new Error('artifact_decrypt_failed');
  }
  return encoder.encode(`${ARTIFACT_SHARE_PROTOCOL}\n${shareId}\n${part}\n${artifactId}\n${revision}\n${plaintextLength}`);
}

function fromBase64URL(value: string): Uint8Array {
  if (!/^[A-Za-z0-9_-]+$/.test(value)) throw new Error('artifact_decrypt_failed');
  const padded = value.replaceAll('-', '+').replaceAll('_', '/') + '='.repeat((4 - (value.length % 4)) % 4);
  const binary = atob(padded);
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

export function parseArtifactShareKey(hash: string): Uint8Array {
  const match = /^#k=v1\.([A-Za-z0-9_-]{43})$/.exec(hash);
  if (!match) throw new Error('artifact_decrypt_failed');
  const key = fromBase64URL(match[1]!);
  if (key.length !== 32) throw new Error('artifact_decrypt_failed');
  return key;
}

async function importShareKey(key: Uint8Array): Promise<CryptoKey> {
  return crypto.subtle.importKey('raw', exactArrayBuffer(key), 'AES-GCM', false, ['decrypt']);
}

async function decrypt(
  key: CryptoKey,
  nonce: Uint8Array,
  ciphertext: Uint8Array,
  aad: Uint8Array,
): Promise<Uint8Array> {
  if (nonce.length !== 12 || ciphertext.length < 16) throw new Error('artifact_decrypt_failed');
  try {
    return new Uint8Array(await crypto.subtle.decrypt({
      name: 'AES-GCM',
      iv: exactArrayBuffer(nonce),
      additionalData: exactArrayBuffer(aad),
    }, key, exactArrayBuffer(ciphertext)));
  } catch {
    throw new Error('artifact_decrypt_failed');
  }
}

export async function decryptArtifactShare(
  envelope: SharedArtifactEnvelope,
  contentWire: Uint8Array,
  keyBytes: Uint8Array,
): Promise<{ metadata: SharedArtifactMetadata; content: Uint8Array }> {
  if (envelope.protocol !== ARTIFACT_SHARE_PROTOCOL || contentWire.length !== envelope.ciphertext_size || contentWire.length < 28) {
    throw new Error('artifact_decrypt_failed');
  }
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', exactArrayBuffer(contentWire)));
  const digestHex = Array.from(digest, (byte) => byte.toString(16).padStart(2, '0')).join('');
	if (!/^[a-fA-F0-9]{64}$/.test(envelope.ciphertext_sha256) || digestHex !== envelope.ciphertext_sha256.toLowerCase()) {
    throw new Error('artifact_decrypt_failed');
  }
  const key = await importShareKey(keyBytes);
  const metadataEnvelope = envelope.encrypted_metadata;
  const metadataPlaintext = await decrypt(
    key,
    fromBase64URL(metadataEnvelope.nonce),
    fromBase64URL(metadataEnvelope.ciphertext),
    artifactShareAAD(envelope.share_id, 'metadata', envelope.artifact_id, envelope.revision, metadataEnvelope.plaintext_length),
  );
  if (metadataPlaintext.length !== metadataEnvelope.plaintext_length) throw new Error('artifact_decrypt_failed');
  const contentLength = envelope.ciphertext_size - 28;
  const content = await decrypt(
    key,
    contentWire.slice(0, 12),
    contentWire.slice(12),
    artifactShareAAD(envelope.share_id, 'content', envelope.artifact_id, envelope.revision, contentLength),
  );
  if (content.length !== contentLength) throw new Error('artifact_decrypt_failed');
  try {
    const metadata = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(metadataPlaintext)) as SharedArtifactMetadata;
    if (!metadata.title || !metadata.media_type || !metadata.kind || metadata.size !== content.length) {
      throw new Error('artifact_decrypt_failed');
    }
    return { metadata, content };
  } catch {
    throw new Error('artifact_decrypt_failed');
  }
}
