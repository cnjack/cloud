/*
 * States — the standard loading / error blocks used by every data fetch so the
 * quality bar (loading + error + empty for every fetch) is met consistently.
 */
import type { ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { Spinner } from './Spinner';
import { Button } from './Button';
import { ApiError } from '../api/client';
import styles from './States.module.css';

export function LoadingBlock({ label }: { label?: string }) {
  const { t } = useTranslation();
  return (
    <div className={styles.center}>
      <Spinner label={label ?? t('common.loading')} />
    </div>
  );
}

function errorMessage(err: unknown, t: TFunction): string {
  if (err instanceof ApiError) {
    if (err.status === 0) return t('components.states.cannotReach');
    return `${err.message}`;
  }
  if (err instanceof Error) return err.message;
  return t('components.states.somethingWrong');
}

export function ErrorBlock({
  error,
  onRetry,
  title,
}: {
  error: unknown;
  onRetry?: () => void;
  title?: string;
}) {
  const { t } = useTranslation();
  return (
    <div className={[styles.center, styles.error].join(' ')} role="alert">
      <div className={styles.errTitle}>{title ?? t('components.states.failedToLoad')}</div>
      <div className={styles.errMsg}>{errorMessage(error, t)}</div>
      {onRetry && (
        <Button variant="secondary" size="sm" onClick={onRetry}>
          {t('common.retry')}
        </Button>
      )}
    </div>
  );
}

export function InlineHint({ children }: { children: ReactNode }) {
  return <p className={styles.hint}>{children}</p>;
}
