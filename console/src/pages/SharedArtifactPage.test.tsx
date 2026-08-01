import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { App } from '../App';
import { artifactShareAAD } from '../lib/artifactShareCrypto';

const raw = (bytes: Uint8Array) =>
  btoa(String.fromCharCode(...bytes)).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');

async function encrypt(
  key: CryptoKey,
  nonce: Uint8Array,
  plaintext: Uint8Array,
  aad: Uint8Array,
): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.encrypt({
    name: 'AES-GCM',
    iv: Uint8Array.from(nonce).buffer,
    additionalData: Uint8Array.from(aad).buffer,
  }, key, Uint8Array.from(plaintext).buffer));
}

async function mockEncryptedShare(options: {
  shareId: string;
  kind: string;
  mediaType: string;
  content: string;
  title?: string;
}) {
  const artifactId = 'artifact-public';
  const revision = 2;
  const title = options.title ?? 'Artifact report';
  const keyBytes = Uint8Array.from({ length: 32 }, (_, i) => i + 1);
  const key = await crypto.subtle.importKey('raw', keyBytes, 'AES-GCM', false, ['encrypt']);
  const contentPlaintext = new TextEncoder().encode(options.content);
  const metadataPlaintext = new TextEncoder().encode(JSON.stringify({
    title,
    relative_path: `reports/result.${options.kind}`,
    media_type: options.mediaType,
    kind: options.kind,
    size: contentPlaintext.length,
  }));
  const metadataNonce = Uint8Array.from({ length: 12 }, (_, i) => i + 11);
  const contentNonce = Uint8Array.from({ length: 12 }, (_, i) => i + 31);
  const metadataCiphertext = await encrypt(key, metadataNonce, metadataPlaintext, artifactShareAAD(options.shareId, 'metadata', artifactId, revision, metadataPlaintext.length));
  const contentCiphertext = await encrypt(key, contentNonce, contentPlaintext, artifactShareAAD(options.shareId, 'content', artifactId, revision, contentPlaintext.length));
  const contentWire = new Uint8Array(contentNonce.length + contentCiphertext.length);
  contentWire.set(contentNonce);
  contentWire.set(contentCiphertext, contentNonce.length);
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', Uint8Array.from(contentWire).buffer));
  const digestHex = Array.from(digest, (byte) => byte.toString(16).padStart(2, '0')).join('');

  vi.spyOn(globalThis, 'fetch')
    .mockResolvedValueOnce(new Response(JSON.stringify({
      share_id: options.shareId,
      protocol: 'jcode-artifact-share-v1',
      artifact_id: artifactId,
      revision,
      encrypted_metadata: {
        nonce: raw(metadataNonce),
        ciphertext: raw(metadataCiphertext),
        plaintext_length: metadataPlaintext.length,
      },
      ciphertext_size: contentWire.length,
      ciphertext_sha256: digestHex,
      expires_at: '2026-08-02T12:00:00Z',
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    .mockResolvedValueOnce(new Response(contentWire, { status: 200, headers: { 'Content-Type': 'application/octet-stream' } }));

  return keyBytes;
}

function renderShare(shareId: string, keyBytes: Uint8Array) {
  render(<MemoryRouter initialEntries={[`/s/${shareId}#k=v1.${raw(keyBytes)}`]}><App /></MemoryRouter>);
}

describe('SharedArtifactPage', () => {
  afterEach(() => vi.restoreAllMocks());

  it('bypasses authenticated console boot, decrypts from the fragment, and renders safe text', async () => {
    const shareId = 'share-public';
    const keyBytes = await mockEncryptedShare({
      shareId,
      kind: 'markdown',
      mediaType: 'text/markdown',
      content: '# Result\n\nCiphertext only.',
    });
    renderShare(shareId, keyBytes);

    expect(await screen.findByRole('heading', { name: 'Artifact report' })).toBeTruthy();
    expect(screen.getByText(/Ciphertext only/)).toBeTruthy();
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    for (const call of vi.mocked(fetch).mock.calls) {
      expect(String(call[0])).not.toContain('#k=');
      expect(call[1]).toMatchObject({ credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer' });
    }
    expect(vi.mocked(fetch).mock.calls.some(([request]) => String(request).includes('/api/v1/me'))).toBe(false);
  });

  it('renders HTML artifacts in a script-capable sandbox without same-origin access', async () => {
    const shareId = 'share-html';
    const keyBytes = await mockEncryptedShare({
      shareId,
      kind: 'html',
      mediaType: 'text/html',
      content: '<script>document.body.textContent = "isolated"</script>',
      title: 'HTML demo',
    });
    renderShare(shareId, keyBytes);

    const frame = await screen.findByTitle('HTML demo');
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts');
    expect(frame.getAttribute('sandbox')).not.toContain('allow-same-origin');
    expect(frame.getAttribute('srcdoc')).toContain("default-src 'none'");
    expect(frame.getAttribute('srcdoc')).toContain('<script>document.body.textContent = "isolated"</script>');
  });
});
