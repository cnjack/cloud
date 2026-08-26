import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useServiceUsage } from '../api/queries';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { UsageSummary } from '../components/UsageSummary';
import type { UsageSummary as UsageSummaryValue } from '../api/types';
import styles from './RepositoryUsagePanel.module.css';

type RangePreset = '24h' | '7d' | '30d';

const rangeMilliseconds: Record<RangePreset, number> = {
  '24h': 24 * 60 * 60 * 1000,
  '7d': 7 * 24 * 60 * 60 * 1000,
  '30d': 30 * 24 * 60 * 60 * 1000,
};
const rangeLabelKey: Record<RangePreset, 'usage.range24h' | 'usage.range7' | 'usage.range30'> = {
  '24h': 'usage.range24h',
  '7d': 'usage.range7',
  '30d': 'usage.range30',
};

const emptyRepositoryUsage: UsageSummaryValue = {
  availability: 'available',
  reason: 'no_requests',
  requests: 0,
  capture: { reported: 0, partial: 0, unavailable: 0, parse_error: 0 },
  tokens: { input: 0, output: 0, cache_read: 0, cache_write: 0 },
  costs: { reported: [], estimated: [], uncosted: [] },
};

export function RepositoryUsagePanel({ repositoryId }: { repositoryId?: string }) {
  const { t } = useTranslation();
  const [rangePreset, setRangePreset] = useState<RangePreset>('7d');
  const [rangeAnchor] = useState(() => Date.now());
  const range = useMemo(() => ({
    from: new Date(rangeAnchor - rangeMilliseconds[rangePreset]).toISOString(),
    to: new Date(rangeAnchor).toISOString(),
  }), [rangeAnchor, rangePreset]);
  const query = useServiceUsage(repositoryId ?? '', range.from, range.to, !!repositoryId);

  if (repositoryId && query.isLoading) return <LoadingBlock label={t('usage.loading')} />;
  if (repositoryId && (query.isError || !query.data)) {
    return <ErrorBlock error={query.error} onRetry={() => void query.refetch()} title={t('usage.loadError')} />;
  }

  const value = repositoryId ? query.data : emptyRepositoryUsage;

  return (
    <section className={styles.page} aria-label={t('usage.title')} data-testid="repository-usage">
      <div className={styles.toolbar}>
        <div>
          <span>{t('usage.range')}</span>
          <div className={styles.segmented}>
            {(['24h', '7d', '30d'] as RangePreset[]).map((value) => (
              <button key={value} type="button" aria-pressed={rangePreset === value} onClick={() => setRangePreset(value)}>
                {t(rangeLabelKey[value])}
              </button>
            ))}
          </div>
        </div>
      </div>
      <p className={styles.windowNote}>
        {t('usage.windowNote', {
          range: t(rangeLabelKey[rangePreset]),
          timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
        })}
      </p>
      {!repositoryId && <p className={styles.defaultNote} data-testid="repository-usage-default">No Repository runs yet. Usage starts at 0 and updates after the first request.</p>}
      <UsageSummary value={value} />
    </section>
  );
}
