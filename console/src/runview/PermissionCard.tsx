/*
 * PermissionCard — one agent.permission_request paired (by request_id, see
 * grouping.ts) with its agent.permission_resolved (F8b approval-mode
 * sessions). Three states:
 *
 *   1. pending          — the title + one button per offered option. Buttons
 *      call the HOST-INJECTED onDecide (runview never talks to an API itself;
 *      the console's RunDetailPage POSTs permission-response) and are disabled
 *      for read-only viewers.
 *   2. pending, decided — the user picked an option (controls.decided keyed by
 *      request_id, optimistic): every button greys out, the chosen one is
 *      marked, and a "waiting for the agent" note shows until the resolved
 *      event lands.
 *   3. resolved         — the chosen option's name plus a resolution badge:
 *      "user" (a person answered) or "timeout" (nobody did — the runner
 *      deny-safed the request and moved on).
 */
import type { PermissionCardItem, PermissionControls } from './types';
import styles from './PermissionCard.module.css';

export function PermissionCard({
  item,
  controls,
}: {
  item: PermissionCardItem;
  controls?: PermissionControls;
}) {
  const decidedOptionId = controls?.decided?.[item.requestId];

  if (item.status === 'resolved') {
    const chosen = item.options.find((o) => o.optionId === item.resolvedOptionId);
    const chosenLabel =
      chosen?.name ?? (item.resolvedOptionId ? item.resolvedOptionId : 'No action (cancelled)');
    return (
      <div className={styles.card} data-status="resolved" data-testid="permission-card">
        <div className={styles.head}>
          <span className={styles.label}>permission</span>
          <span className={styles.title}>{item.title}</span>
        </div>
        <div className={styles.outcome} data-testid="permission-outcome">
          <span className={styles.chosenName}>{chosenLabel}</span>
          <span
            className={styles.resolutionBadge}
            data-resolution={item.resolution || undefined}
            data-testid="permission-resolution"
          >
            {item.resolution === 'timeout' ? 'timed out' : item.resolution || 'resolved'}
          </span>
        </div>
      </div>
    );
  }

  // Pending. Once an option was submitted (optimistic), or for a read-only
  // viewer, the buttons are inert — the card still shows what CAN be chosen.
  const inert = controls?.disabled === true || !!decidedOptionId;
  return (
    <div className={styles.card} data-status="pending" data-testid="permission-card">
      <div className={styles.head}>
        <span className={styles.label}>permission required</span>
        <span className={styles.title}>{item.title}</span>
      </div>
      <div className={styles.options}>
        {item.options.map((o) => (
          <button
            key={o.optionId}
            type="button"
            className={styles.optionBtn}
            data-chosen={decidedOptionId === o.optionId || undefined}
            data-kind={o.kind || undefined}
            disabled={inert}
            onClick={() => controls?.onDecide?.(item.requestId, o.optionId)}
            data-testid={`permission-option-${o.optionId}`}
          >
            {o.name}
          </button>
        ))}
        {decidedOptionId && (
          <span className={styles.waiting} data-testid="permission-waiting">
            Answered — waiting for the agent…
          </span>
        )}
        {!decidedOptionId && controls?.disabled && (
          <span className={styles.waiting} data-testid="permission-readonly">
            Read-only — a project member can answer.
          </span>
        )}
      </div>
    </div>
  );
}
