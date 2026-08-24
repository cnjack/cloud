import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useServiceUsage } from '../api/queries';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { UsageSummary } from '../components/UsageSummary';
import styles from './ProjectUsagePanel.module.css';

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

export function RepositoryUsagePanel({ repositoryId }: { repositoryId: string }) {
  const { t } = useTranslation();
  const [rangePreset, setRangePreset] = useState<RangePreset>('7d');
  const [rangeAnchor] = useState(() => Date.now());
  const range = useMemo(() => ({
    from: new Date(rangeAnchor - rangeMilliseconds[rangePreset]).toISOString(),
    to: new Date(rangeAnchor).toISOString(),
  }), [rangeAnchor, rangePreset]);
  const query = useServiceUsage(repositoryId, range.from, range.to);

  if (query.isLoading) return <LoadingBlock label={t('usage.loading')} />;
  if (query.isError || !query.data) {
    return <ErrorBlock error={query.error} onRetry={() => void query.refetch()} title={t('usage.loadError')} />;
  }

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
      <UsageSummary value={query.data} />
    </section>
  );
}
