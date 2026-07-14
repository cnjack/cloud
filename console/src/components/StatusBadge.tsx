import { useTranslation } from 'react-i18next';
import type { RunStatus } from '../api/types';
import styles from './StatusBadge.module.css';

/*
 * StatusBadge — the single source of truth for run-status presentation (PRD §6
 * badge semantics). Covers all six lifecycle states plus `blocked` (modelled +
 * rendered but never produced this period). Colors come entirely from the
 * --status-*-{bg,fg,dot} tokens, so a re-skin never touches this file.
 */

const LABEL_KEYS: Record<RunStatus, string> = {
  queued: 'components.statusBadge.queued',
  scheduling: 'components.statusBadge.scheduling',
  running: 'components.statusBadge.running',
  // D22 session: the run finished a turn and waits for the user's next message.
  awaiting_input: 'components.statusBadge.awaitingInput',
  succeeded: 'components.statusBadge.succeeded',
  failed: 'components.statusBadge.failed',
  canceled: 'components.statusBadge.canceled',
  blocked: 'components.statusBadge.blocked',
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
  const { t } = useTranslation();
  const label = t(LABEL_KEYS[status]);
  return (
    <span
      className={[styles.badge, styles[size]].join(' ')}
      data-status={status}
      role="status"
      aria-label={t('components.statusBadge.aria', { status: label })}
    >
      <span
        className={styles.dot}
        data-animated={ANIMATED.has(status) || undefined}
        aria-hidden
      />
      {label}
    </span>
  );
}
