import { DownloadSimple, LockKeyOpen } from '@phosphor-icons/react';
import { useEffect, useMemo, useState } from 'react';
import { useLocation, useParams } from 'react-router-dom';
import { Markdown } from '../components/Markdown';
import {
  decryptArtifactShare,
  parseArtifactShareKey,
  type SharedArtifactEnvelope,
  type SharedArtifactMetadata,
} from '../lib/artifactShareCrypto';
import styles from './SharedArtifactPage.module.css';

type ViewState =
  | { phase: 'loading' }
  | { phase: 'error'; message: string }
  | { phase: 'ready'; metadata: SharedArtifactMetadata; content: Uint8Array };

const htmlCSP = "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'";

function escapedMarkdown(source: string): string {
  return source.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
}

function ArtifactContent({ metadata, content }: { metadata: SharedArtifactMetadata; content: Uint8Array }) {
  const text = useMemo(() => {
    if (!['markdown', 'text', 'code', 'csv'].includes(metadata.kind)) return '';
    try { return new TextDecoder('utf-8', { fatal: true }).decode(content); } catch { return ''; }
  }, [content, metadata.kind]);
  const blobURL = useMemo(() => {
    if (!['image', 'pdf', 'binary'].includes(metadata.kind) || typeof URL.createObjectURL !== 'function') return '';
    return URL.createObjectURL(new Blob([Uint8Array.from(content).buffer], { type: metadata.media_type || 'application/octet-stream' }));
  }, [content, metadata.kind, metadata.media_type]);
  useEffect(() => () => { if (blobURL) URL.revokeObjectURL(blobURL); }, [blobURL]);

  if (metadata.kind === 'markdown') return <Markdown source={escapedMarkdown(text)} />;
  if (['text', 'code', 'csv'].includes(metadata.kind)) return <pre className={styles.text}>{text}</pre>;
  if (metadata.kind === 'html') {
    const source = new TextDecoder().decode(content);
    return <iframe className={styles.frame} title={metadata.title} sandbox="allow-scripts" srcDoc={`<meta http-equiv="Content-Security-Policy" content="${htmlCSP}">${source}`} />;
  }
  if (metadata.kind === 'image' && blobURL) return <img className={styles.image} src={blobURL} alt={metadata.title} />;
  if (metadata.kind === 'pdf' && blobURL) return <iframe className={styles.frame} title={metadata.title} src={blobURL} />;
  return blobURL ? <a className={styles.download} href={blobURL} download={metadata.relative_path.split('/').at(-1)}><DownloadSimple size={18} />Download decrypted file</a> : <p>Preview is unavailable.</p>;
}

export function SharedArtifactPage() {
  const { shareID = '' } = useParams();
  const location = useLocation();
  const [state, setState] = useState<ViewState>({ phase: 'loading' });
  const key = useMemo(() => {
    try { return parseArtifactShareKey(location.hash); } catch { return null; }
  }, [location.hash]);

  useEffect(() => {
    if (location.hash) window.history.replaceState(null, '', location.pathname + location.search);
  }, [location.hash, location.pathname, location.search]);

  useEffect(() => {
    if (!shareID || !key) {
      setState({ phase: 'error', message: 'This share link is incomplete or invalid.' });
      return;
    }
    const controller = new AbortController();
    const options: RequestInit = { signal: controller.signal, credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer' };
    void (async () => {
      try {
        const metadataResponse = await fetch(`/api/v1/shared-artifacts/${encodeURIComponent(shareID)}`, options);
        if (!metadataResponse.ok) throw new Error('not_found');
        const envelope = await metadataResponse.json() as SharedArtifactEnvelope;
        const contentResponse = await fetch(`/api/v1/shared-artifacts/${encodeURIComponent(shareID)}/content`, options);
        if (!contentResponse.ok) throw new Error('not_found');
        const contentWire = new Uint8Array(await contentResponse.arrayBuffer());
        const decrypted = await decryptArtifactShare(envelope, contentWire, key);
        setState({ phase: 'ready', ...decrypted });
      } catch (error) {
        if (!controller.signal.aborted) {
          setState({ phase: 'error', message: error instanceof Error && error.message === 'not_found' ? 'This share is unavailable or has expired.' : 'This share could not be decrypted.' });
        }
      }
    })();
    return () => controller.abort();
  }, [key, shareID]);

  return (
    <main className={styles.page}>
      <header className={styles.header}>
        <div className={styles.brand}><LockKeyOpen size={19} weight="duotone" /><span>jcode Artifact</span></div>
        <span className={styles.security}>End-to-end encrypted</span>
      </header>
      {state.phase === 'loading' && <section className={styles.status} role="status"><span className={styles.spinner} />Decrypting artifact…</section>}
      {state.phase === 'error' && <section className={styles.status} role="alert"><h1>Artifact unavailable</h1><p>{state.message}</p></section>}
      {state.phase === 'ready' && (
        <article className={styles.artifact}>
          <header className={styles.meta}>
            <div><p>{state.metadata.kind} · {state.metadata.media_type}</p><h1>{state.metadata.title}</h1></div>
            <span>{state.metadata.relative_path}</span>
          </header>
          <section className={styles.viewer}><ArtifactContent metadata={state.metadata} content={state.content} /></section>
        </article>
      )}
    </main>
  );
}
