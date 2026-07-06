/*
 * States — the standard loading / error blocks used by every data fetch so the
 * quality bar (loading + error + empty for every fetch) is met consistently.
 */
import type { ReactNode } from 'react';
import { Spinner } from './Spinner';
import { Button } from './Button';
import { ApiError } from '../api/client';
import styles from './States.module.css';

export function LoadingBlock({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className={styles.center}>
      <Spinner label={label} />
    </div>
  );
}

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 0) return 'Cannot reach the orchestrator. Is it running?';
    return `${err.message}`;
  }
  if (err instanceof Error) return err.message;
  return 'Something went wrong.';
}

export function ErrorBlock({
  error,
  onRetry,
  title = 'Failed to load',
}: {
  error: unknown;
  onRetry?: () => void;
  title?: string;
}) {
  return (
    <div className={[styles.center, styles.error].join(' ')} role="alert">
      <div className={styles.errTitle}>{title}</div>
      <div className={styles.errMsg}>{errorMessage(error)}</div>
      {onRetry && (
        <Button variant="secondary" size="sm" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  );
}

export function InlineHint({ children }: { children: ReactNode }) {
  return <p className={styles.hint}>{children}</p>;
}
