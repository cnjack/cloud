import type { RunStatus } from '../api/types';
import styles from './StatusBadge.module.css';

/*
 * StatusBadge — the single source of truth for run-status presentation (PRD §6
 * badge semantics). Covers all six lifecycle states plus `blocked` (modelled +
 * rendered but never produced this period). Colors come entirely from the
 * --status-*-{bg,fg,dot} tokens, so a re-skin never touches this file.
 */

const LABELS: Record<RunStatus, string> = {
  queued: 'Queued',
  scheduling: 'Scheduling',
  running: 'Running',
  // D22 session: the run finished a turn and waits for the user's next message.
  awaiting_input: 'Awaiting input',
  succeeded: 'Succeeded',
  failed: 'Failed',
  canceled: 'Canceled',
  blocked: 'Blocked',
};

// Statuses that get a pulsing dot to signal "live / in motion". awaiting_input
// pulses too — the session is live and waiting on YOU.
const ANIMATED: ReadonlySet<RunStatus> = new Set([
  'scheduling',
  'running',
  'awaiting_input',
]);

export function StatusBadge({
  status,
  size = 'md',
}: {
  status: RunStatus;
  size?: 'sm' | 'md';
}) {
  return (
    <span
      className={[styles.badge, styles[size]].join(' ')}
      data-status={status}
      role="status"
      aria-label={`Status: ${LABELS[status]}`}
    >
      <span
        className={styles.dot}
        data-animated={ANIMATED.has(status) || undefined}
        aria-hidden
      />
      {LABELS[status]}
    </span>
  );
}
