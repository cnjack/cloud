import { useTranslation } from 'react-i18next';
import type { UsageMoneyTotal, UsageSummary as UsageSummaryValue } from '../api/types';
import styles from './UsageSummary.module.css';

function integer(value: number): string {
  return new Intl.NumberFormat().format(value);
}

export function formatUsageMoney(value: UsageMoneyTotal): string {
  const sign = value.micros < 0 ? '-' : '';
  const absolute = Math.abs(value.micros);
  const whole = Math.trunc(absolute / 1_000_000);
  const fractional = Math.trunc(absolute % 1_000_000).toString().padStart(6, '0');
  return `${value.currency} ${sign}${whole}.${fractional}`;
}

export function UsageSummary({
  value,
  compact = false,
  hideCosts = false,
}: {
  value?: UsageSummaryValue;
  compact?: boolean;
  hideCosts?: boolean;
}) {
  const { t } = useTranslation();
  if (!value || value.availability === 'unavailable') {
    const reason = (value?.capture.parse_error ?? 0) > 0
      ? t('usage.reasonParseError')
      : value?.reason === 'usage_not_reported'
      ? t('usage.reasonNotReported')
      : t('usage.reasonNoRequests');
    return (
      <section className={styles.root} data-compact={compact || undefined} data-state="unavailable" aria-label={t('usage.title')}>
        <div className={styles.heading}>
          <strong>{t('usage.title')}</strong>
          <span className={styles.unavailable}>{t('usage.unavailable')}</span>
        </div>
        <p className={styles.reason}>{reason}</p>
        {value && value.requests > 0 && (
          <dl className={styles.unavailableDetails}>
            <div><dt>{t('usage.requests')}</dt><dd>{integer(value.requests)}</dd></div>
            <div><dt>{t('usage.unavailableCapture')}</dt><dd>{integer(value.capture.unavailable)}</dd></div>
            <div><dt>{t('usage.parseErrors')}</dt><dd>{integer(value.capture.parse_error)}</dd></div>
          </dl>
        )}
        <p className={styles.disclaimer}>{t('usage.disclaimer')}</p>
      </section>
    );
  }

  const tokens = [
    ['input', t('usage.input'), value.tokens.input],
    ['output', t('usage.output'), value.tokens.output],
    ['cache-read', t('usage.cacheRead'), value.tokens.cache_read],
    ['cache-write', t('usage.cacheWrite'), value.tokens.cache_write],
  ] as const;
  const revisions = [...new Set(value.costs.estimated.flatMap((item) => item.pricing_revision_ids ?? []))];
  const captured = value.capture.reported + value.capture.partial;
  const captureLabel = value.capture.partial > 0 || value.capture.unavailable > 0 || value.capture.parse_error > 0
    ? t('usage.captureIncomplete')
    : t('usage.captureReported');

  return (
    <section className={styles.root} data-compact={compact || undefined} data-state="available" aria-label={t('usage.title')}>
      <div className={styles.heading}>
        <strong>{t('usage.title')}</strong>
        <span className={styles.capture}>
          <strong>{captureLabel}</strong>
          <span>{t('usage.captured', { captured, requests: value.requests })}</span>
        </span>
      </div>
      <div className={styles.tokens}>
        {tokens.map(([key, label, amount]) => amount == null ? null : (
          <div key={key}>
            <span>{label}</span>
            <strong>{integer(amount)}</strong>
          </div>
        ))}
      </div>
      {!hideCosts && (
        <div className={styles.costs}>
          <UsageCostRow label={t('usage.reported')} values={value.costs.reported.map(formatUsageMoney)} />
          <UsageCostRow label={t('usage.estimated')} values={value.costs.estimated.map(formatUsageMoney)} />
          <UsageCostRow
            label={t('usage.uncosted')}
            values={value.costs.uncosted.map((item) => `${integer(item.tokens)} ${t(`usage.category.${item.category}`)}`)}
          />
        </div>
      )}
      <details className={styles.details}>
        <summary>{t('usage.details')}</summary>
        <dl>
          <div><dt>{t('usage.requests')}</dt><dd>{integer(value.requests)}</dd></div>
          <div><dt>{t('usage.reportedCapture')}</dt><dd>{integer(value.capture.reported)}</dd></div>
          <div><dt>{t('usage.partialCapture')}</dt><dd>{integer(value.capture.partial)}</dd></div>
          <div><dt>{t('usage.unavailableCapture')}</dt><dd>{integer(value.capture.unavailable)}</dd></div>
          <div><dt>{t('usage.parseErrors')}</dt><dd>{integer(value.capture.parse_error)}</dd></div>
          {revisions.length > 0 && (
            <div><dt>{t('usage.pricingRevisions')}</dt><dd>{revisions.join(', ')}</dd></div>
          )}
        </dl>
      </details>
      <p className={styles.disclaimer}>{t('usage.disclaimer')}</p>
    </section>
  );
}

function UsageCostRow({ label, values }: { label: string; values: string[] }) {
  const { t } = useTranslation();
  return (
    <div>
      <span>{label}</span>
      <strong>{values.length > 0 ? values.join(' · ') : t('usage.none')}</strong>
    </div>
  );
}
