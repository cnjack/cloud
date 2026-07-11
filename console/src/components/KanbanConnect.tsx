/*
 * KanbanConnect — the presentational "Connect with jtype" device-flow panel
 * (D28), shared by the cluster fallback-token editor (SystemPage) and each
 * per-link row (ProjectSettingsModal). It is deliberately DUMB: the two call
 * sites own the flow state (connect_id + the surface-specific start/poll hooks)
 * and feed the results in; this file only renders the state machine.
 *
 * States:
 *   idle        — a "Connect with jtype" button (disabled + a visible reason
 *                 when the prerequisite isn't met: base URL unsaved / cluster off).
 *   pending     — the prominent 6-digit user_code, an "Open jtype to authorize"
 *                 deep link (verification_uri_complete, new tab), and a live
 *                 "waiting…" status while the poll runs.
 *   complete    — a success line + the minted token's expiry (from the poll's
 *                 token_expires_at). The plaintext token is NEVER surfaced.
 *   expired/    — a fail-visible notice + a "Connect again" button. A 404
 *   denied        connect_expired on poll (e.g. after an orchestrator restart)
 *                 lands here too; a user denial eventually reads as expired.
 *   unsupported — an old jtype without the OAuth routes: a notice that steers the
 *                 user to paste a token instead (the paste field is always kept
 *                 by the parent), never a silent failure.
 */
import { ApiError, apiErrorCode } from '../api/client';
import { daysUntil } from '../lib/format';
import type { KanbanConnectStart, KanbanConnectStatus } from '../api/types';
import { Button } from './Button';
import styles from './KanbanConnect.module.css';

/**
 * "expires in N days" / an expired-copy for a KNOWN token expiry; null when the
 * expiry is unknown (manual PAT / env / none) so the caller renders no badge.
 */
export function expiryLabel(iso: string | undefined, expiredCopy = 'expired'): string | null {
  const d = daysUntil(iso);
  if (d === null) return null;
  if (d <= 0) return expiredCopy;
  return `expires in ${d} day${d === 1 ? '' : 's'}`;
}

export interface KanbanConnectFlowProps {
  /** testid namespace, e.g. "kanban-connect" or `kanban-link-connect-${linkId}`. */
  idPrefix: string;
  /** The prerequisite isn't met (base URL unsaved / cluster integration off). */
  disabled: boolean;
  /** The visible reason shown next to a disabled Connect button. */
  disabledHint: string;
  /** True once a flow has been launched (a connect_id exists). */
  active: boolean;
  /** Start mutation state. */
  starting: boolean;
  startError: unknown;
  connectStart: KanbanConnectStart | undefined;
  /** Poll state. */
  status: KanbanConnectStatus | undefined;
  statusError: unknown;
  onStart: () => void;
  onReset: () => void;
}

type Phase = 'idle' | 'pending' | 'complete' | 'expired' | 'denied' | 'failed' | 'unsupported';

export function KanbanConnectFlow({
  idPrefix,
  disabled,
  disabledHint,
  active,
  starting,
  startError,
  connectStart,
  status,
  statusError,
  onStart,
  onReset,
}: KanbanConnectFlowProps) {
  // An old jtype (no OAuth routes) fails at START with a typed 409.
  const unsupportedStart = apiErrorCode(startError) === 'jtype_oauth_unsupported';
  // Any OTHER start failure (jtype unreachable, cipher_not_configured, a raced
  // base_url_not_configured, a per-link 404…) must be fail-visible too: surface
  // the server's message verbatim next to the (still idle) Connect button.
  const startErrorMessage =
    startError && !unsupportedStart
      ? startError instanceof ApiError
        ? startError.message
        : 'Could not start the connect flow.'
      : null;
  // A dropped in-memory flow (e.g. orchestrator restart) 404s on poll — treated
  // exactly like an expired flow so the user just reconnects.
  const expiredPoll =
    statusError instanceof ApiError &&
    (statusError.status === 404 || apiErrorCode(statusError) === 'connect_expired');

  let phase: Phase = 'idle';
  if (unsupportedStart) phase = 'unsupported';
  else if (active) {
    if (expiredPoll) phase = 'expired';
    // Any other poll failure (401/403 mid-flow, 5xx…) is terminal for THIS flow
    // (refetchInterval stops on error): a visible "failed — reconnect", never a
    // pending panel stuck on "Waiting…" forever.
    else if (statusError) phase = 'failed';
    else if (status?.status === 'complete') phase = 'complete';
    else if (status?.status === 'expired') phase = 'expired';
    else if (status?.status === 'denied') phase = 'denied';
    else if (status?.status === 'unsupported') phase = 'unsupported';
    else phase = 'pending';
  }

  const connectButton = (testid: string) => (
    <Button
      type="button"
      variant="secondary"
      onClick={onStart}
      loading={starting}
      disabled={disabled}
      data-testid={testid}
    >
      Connect with jtype
    </Button>
  );

  return (
    <div className={styles.wrap} data-testid={idPrefix}>
      {phase === 'idle' && (
        <>
          <div className={styles.row}>
            {connectButton(`${idPrefix}-start`)}
            {disabled && (
              <span className={styles.hint} data-testid={`${idPrefix}-hint`}>
                {disabledHint}
              </span>
            )}
          </div>
          {startErrorMessage && (
            <p className={styles.notice} role="alert" data-testid={`${idPrefix}-start-error`}>
              {startErrorMessage}
            </p>
          )}
        </>
      )}

      {phase === 'pending' && connectStart && (
        <div className={styles.panel} data-testid={`${idPrefix}-panel`}>
          <p className={styles.instructions}>Enter this code in jtype to authorize:</p>
          <div className={styles.code} data-testid={`${idPrefix}-code`}>
            {connectStart.user_code}
          </div>
          <a
            className={styles.link}
            href={connectStart.verification_uri_complete}
            target="_blank"
            rel="noopener noreferrer"
            data-testid={`${idPrefix}-link`}
          >
            Open jtype to authorize ↗
          </a>
          <p className={styles.status} data-testid={`${idPrefix}-status`}>
            Waiting for you to authorize in jtype…
          </p>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={onReset}
            data-testid={`${idPrefix}-cancel`}
          >
            Cancel
          </Button>
        </div>
      )}

      {phase === 'complete' && (
        <div className={styles.panel} data-testid={`${idPrefix}-panel`}>
          <p className={styles.success} data-testid={`${idPrefix}-complete`}>
            <span className={styles.badge}>Connected — token set</span>
            {expiryLabel(status?.token_expires_at)
              ? ` ${expiryLabel(status?.token_expires_at)}`
              : ''}
          </p>
          <Button type="button" variant="ghost" size="sm" onClick={onReset}>
            Done
          </Button>
        </div>
      )}

      {(phase === 'expired' || phase === 'denied' || phase === 'failed') && (
        <div className={styles.panel} data-testid={`${idPrefix}-panel`}>
          <p
            className={styles.notice}
            role="alert"
            data-testid={phase === 'failed' ? `${idPrefix}-failed` : `${idPrefix}-expired`}
          >
            {phase === 'failed'
              ? 'Connection failed — click Connect again.'
              : 'Connection expired — click Connect again.'}
          </p>
          {/* Reset first so onStart begins a clean flow. */}
          <Button
            type="button"
            variant="secondary"
            onClick={() => {
              onReset();
              onStart();
            }}
            loading={starting}
            disabled={disabled}
            data-testid={`${idPrefix}-start`}
          >
            Connect with jtype
          </Button>
        </div>
      )}

      {phase === 'unsupported' && (
        <div className={styles.panel} data-testid={`${idPrefix}-panel`}>
          <p className={styles.notice} role="alert" data-testid={`${idPrefix}-unsupported`}>
            This jtype deployment doesn’t support one-click Connect — paste a token instead.
          </p>
          <Button type="button" variant="ghost" size="sm" onClick={onReset}>
            Dismiss
          </Button>
        </div>
      )}
    </div>
  );
}
