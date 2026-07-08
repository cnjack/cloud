/*
 * Collapsible — a hairline-bordered mono block that's collapsed by default and
 * expands on click. Used for the ToolCard's args section and for the
 * degraded/orphan tool_call, tool_result and unknown-event rows.
 */
import { useState } from 'react';
import type { ReactNode } from 'react';
import styles from './Collapsible.module.css';

export function Collapsible({
  title,
  body,
  error,
  defaultOpen = false,
}: {
  title: ReactNode;
  body: string;
  error?: boolean;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const empty = !body.trim();
  return (
    <div className={styles.collapsible} data-error={error || undefined}>
      <button
        type="button"
        className={styles.head}
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        disabled={empty}
      >
        <span className={styles.chevron} data-open={open || undefined} aria-hidden>
          ▸
        </span>
        {title}
      </button>
      {open && !empty && (
        <pre className={styles.code}>
          <code>{body}</code>
        </pre>
      )}
    </div>
  );
}
