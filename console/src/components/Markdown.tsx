import { renderMarkdown } from 'jcode-ui';
import styles from './Markdown.module.css';

/**
 * Review-output adapter for jcode-ui's canonical Markdown pipeline.
 *
 * renderMarkdown uses GFM + syntax highlighting and sanitizes browser output
 * with DOMPurify. Keeping this wrapper lets review panels retain their local
 * spacing while messages and reviews share the exact same parser.
 */
export function Markdown({ source }: { source: string }) {
  return (
    <div
      className={`${styles.md} jcode-prose`}
      data-jcode-ui=""
      dangerouslySetInnerHTML={{ __html: renderMarkdown(source ?? '') }}
    />
  );
}
