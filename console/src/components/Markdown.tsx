/*
 * Markdown — a deliberately tiny, dependency-free markdown renderer for AI review
 * output (blueprint §5). It supports exactly the subset the runner emits:
 * headings (#..######), unordered lists (- / *), **bold**, `inline code`, fenced
 * ```code blocks``` and paragraphs.
 *
 * SECURITY: it parses line-by-line and builds React elements only — it NEVER uses
 * dangerouslySetInnerHTML, so review text (which originates from a model /
 * arbitrary PR content) can carry no markup, scripts or links that execute. Any
 * raw HTML in the source is rendered as literal text.
 */
import { createElement, type ReactNode } from 'react';
import styles from './Markdown.module.css';

/** Inline parse: split out `code` spans first (so their contents are literal),
 *  then **bold** within the remaining text. Returns an array of strings/elements. */
function renderInline(text: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  const codeRe = /`([^`]+)`/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = codeRe.exec(text)) !== null) {
    if (m.index > last) out.push(...renderBold(text.slice(last, m.index), `${key}-a${i}`));
    out.push(
      <code key={`${key}-c${i}`} className={styles.code}>
        {m[1]}
      </code>,
    );
    last = m.index + m[0].length;
    i++;
  }
  if (last < text.length) out.push(...renderBold(text.slice(last), `${key}-a${i}`));
  return out;
}

function renderBold(text: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  const boldRe = /\*\*([^*]+)\*\*/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = boldRe.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index));
    out.push(<strong key={`${key}-b${i}`}>{m[1]}</strong>);
    last = m.index + m[0].length;
    i++;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

const HEADING = /^(#{1,6})\s+(.*)$/;
const LIST_ITEM = /^\s*[-*]\s+(.*)$/;
const FENCE = /^```/;

function parseBlocks(source: string): ReactNode[] {
  const lines = source.replace(/\r\n/g, '\n').split('\n');
  const blocks: ReactNode[] = [];
  let i = 0;
  let key = 0;

  while (i < lines.length) {
    const line = lines[i]!;

    // Fenced code block — capture verbatim until the closing fence (or EOF).
    if (FENCE.test(line)) {
      const buf: string[] = [];
      i++;
      while (i < lines.length && !FENCE.test(lines[i]!)) {
        buf.push(lines[i]!);
        i++;
      }
      if (i < lines.length) i++; // consume closing fence
      blocks.push(
        <pre key={key++} className={styles.pre}>
          <code>{buf.join('\n')}</code>
        </pre>,
      );
      continue;
    }

    // Heading.
    const h = HEADING.exec(line);
    if (h) {
      const level = Math.min(h[1]!.length + 2, 6); // # -> h3 … ###### -> h6
      blocks.push(
        createElement(
          `h${level}`,
          { key: key++, className: styles.heading },
          renderInline(h[2]!, `h${key}`),
        ),
      );
      i++;
      continue;
    }

    // Unordered list — gather consecutive item lines.
    if (LIST_ITEM.test(line)) {
      const items: ReactNode[] = [];
      let li = 0;
      while (i < lines.length) {
        const mi = LIST_ITEM.exec(lines[i]!);
        if (!mi) break;
        items.push(
          <li key={li} className={styles.li}>
            {renderInline(mi[1]!, `l${key}-${li}`)}
          </li>,
        );
        li++;
        i++;
      }
      blocks.push(
        <ul key={key++} className={styles.ul}>
          {items}
        </ul>,
      );
      continue;
    }

    // Blank line — block separator.
    if (line.trim() === '') {
      i++;
      continue;
    }

    // Paragraph — gather consecutive plain lines.
    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i]!.trim() !== '' &&
      !FENCE.test(lines[i]!) &&
      !HEADING.test(lines[i]!) &&
      !LIST_ITEM.test(lines[i]!)
    ) {
      para.push(lines[i]!);
      i++;
    }
    blocks.push(
      <p key={key++} className={styles.p}>
        {renderInline(para.join(' '), `p${key}`)}
      </p>,
    );
  }

  return blocks;
}

export function Markdown({ source }: { source: string }) {
  return <div className={styles.md}>{parseBlocks(source ?? '')}</div>;
}
