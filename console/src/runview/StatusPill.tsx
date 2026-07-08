/*
 * StatusPill — a small inline status label for timeline rows (the run.status
 * row, and a ToolCard's running/succeeded/failed state).
 *
 * Deliberately NOT the console's `components/StatusBadge` — runview must not
 * import application code (see README.md). It reads the same `--status-*`
 * design tokens (declared in the host's global stylesheet) for visual
 * consistency, which is a CSS-only coupling, not a code dependency: a future
 * host only needs to provide those custom properties, not this component.
 */
import styles from './StatusPill.module.css';

const LABELS: Record<string, string> = {
  queued: 'Queued',
  scheduling: 'Scheduling',
  running: 'Running',
  // D22 session: turn done, waiting for the user's next message.
  awaiting_input: 'Awaiting input',
  succeeded: 'Succeeded',
  failed: 'Failed',
  canceled: 'Canceled',
  blocked: 'Blocked',
};

export function StatusPill({ status }: { status: string }) {
  const label = LABELS[status] ?? status;
  return (
    <span className={styles.pill} data-status={status}>
      <span className={styles.dot} aria-hidden />
      {label}
    </span>
  );
}
