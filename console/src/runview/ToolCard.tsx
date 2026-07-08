/*
 * ToolCard — one agent.tool_call paired with its agent.tool_result (by
 * call_id; see grouping.ts). Shows the tool name + running/succeeded/failed
 * state, args collapsed by default (expand for pretty JSON), and output
 * truncated by default (expand for the full text) — an error output is
 * expanded and red-highlighted immediately since that's the case a human
 * actually needs to read first.
 */
import { useState } from 'react';
import type { ToolCardItem } from './types';
import { Collapsible } from './Collapsible';
import { StatusPill } from './StatusPill';
import styles from './ToolCard.module.css';

// Output preview length before a "Show full output" toggle appears. Args stay
// fully collapsed (Collapsible) since they're rarely needed at a glance; output
// is the part a human is actually scanning for, so it gets a readable preview
// instead of a fully-hidden block.
const OUTPUT_PREVIEW_CHARS = 400;

export function ToolCard({ item }: { item: ToolCardItem }) {
  return (
    <div className={styles.card} data-status={item.status} data-error={item.isError || undefined}>
      <div className={styles.head}>
        <span className={styles.toolName}>{item.tool}</span>
        <StatusPill status={item.status} />
      </div>

      <Collapsible title={<span className={styles.sectionLabel}>args</span>} body={item.args} />

      {item.status !== 'running' && (
        <OutputSection output={item.output ?? ''} isError={item.isError} />
      )}
    </div>
  );
}

function OutputSection({ output, isError }: { output: string; isError: boolean }) {
  const [expanded, setExpanded] = useState(isError);
  if (!output.trim()) return null;

  const isLong = output.length > OUTPUT_PREVIEW_CHARS;
  const shown = expanded || !isLong ? output : `${output.slice(0, OUTPUT_PREVIEW_CHARS)}…`;

  return (
    <div className={styles.output} data-error={isError || undefined}>
      <div className={styles.outputLabel}>
        <span className={styles.sectionLabel}>{isError ? 'error output' : 'output'}</span>
      </div>
      <pre className={styles.code}>
        <code>{shown}</code>
      </pre>
      {isLong && (
        <button type="button" className={styles.toggle} onClick={() => setExpanded((v) => !v)}>
          {expanded ? 'Show less' : 'Show full output'}
        </button>
      )}
    </div>
  );
}
