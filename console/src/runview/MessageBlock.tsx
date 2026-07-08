/*
 * MessageBlock — renders a merged run of agent.text chunks (see grouping.ts)
 * as one prose block, markdown-formatted.
 *
 * Uses react-markdown + remark-gfm (tables/strikethrough/task lists) rather
 * than the console's hand-rolled `components/Markdown` — that renderer covers
 * only the small subset a review comment needs, whereas agent chat text
 * routinely includes tables, links and nested lists. SECURITY: react-markdown
 * builds React elements only (no dangerouslySetInnerHTML) and, with no
 * rehype-raw plugin wired in, raw HTML in the source renders as literal text —
 * the same "never let model output become live markup" guarantee the existing
 * Markdown component documents.
 */
import ReactMarkdown, { type Components } from 'react-markdown';
import remarkGfm from 'remark-gfm';
import styles from './MessageBlock.module.css';

const components: Components = {
  a: ({ href, children, ...rest }) => (
    <a href={href} target="_blank" rel="noreferrer" {...rest}>
      {children}
    </a>
  ),
  pre: ({ children }) => <pre className={styles.pre}>{children}</pre>,
  code: ({ className, children, ...rest }) => {
    const isBlock = /language-/.test(className ?? '');
    if (isBlock) {
      return (
        <code className={[styles.codeBlock, className].filter(Boolean).join(' ')} {...rest}>
          {children}
        </code>
      );
    }
    return (
      <code className={styles.inlineCode} {...rest}>
        {children}
      </code>
    );
  },
};

export function MessageBlock({ text }: { text: string }) {
  return (
    <div className={styles.prose}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
}
