import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAccountUsage } from '../api/queries';
import type { UsageGroup } from '../api/types';
import { UsageSummary } from '../components/UsageSummary';
import { ErrorBlock, LoadingBlock } from '../components/States';
import styles from './AccountUsagePanel.module.css';

type RangeDays = 7 | 30 | 90;
type GroupBy = 'device' | 'model' | 'grant';

export function AccountUsagePanel({ enabled = true }: { enabled?: boolean }) {
  const { t } = useTranslation();
  const [days, setDays] = useState<RangeDays>(30);
  const [groupBy, setGroupBy] = useState<GroupBy>('device');
  const range = useMemo(() => {
    const to = new Date();
    const from = new Date(to.getTime() - days * 24 * 60 * 60 * 1000);
    return { from: from.toISOString(), to: to.toISOString() };
  }, [days]);
  const query = useAccountUsage(groupBy, range.from, range.to, enabled);

  if (!enabled) return null;

  return (
    <section className={styles.panel} aria-labelledby="account-usage-title" data-testid="account-usage">
      <header className={styles.header}>
        <div>
          <h2 id="account-usage-title">{t('usage.accountTitle')}</h2>
          <p>{t('usage.accountDescription')}</p>
          <small>{t('usage.proxyOnly')}</small>
        </div>
        <div className={styles.controls}>
          <UsageSelector
            label={t('usage.range')}
            values={([7, 30, 90] as RangeDays[])}
            active={days}
            labelFor={(value) => t(`usage.range${value}`)}
            onChange={setDays}
          />
          <UsageSelector
            label={t('usage.groupBy')}
            values={(['device', 'model', 'grant'] as GroupBy[])}
            active={groupBy}
            labelFor={(value) => t(`usage.group.${value}`)}
            onChange={setGroupBy}
          />
        </div>
      </header>
      {query.isLoading ? (
        <LoadingBlock label={t('usage.loading')} />
      ) : query.isError || !query.data ? (
        <ErrorBlock error={query.error} onRetry={() => void query.refetch()} title={t('usage.loadError')} />
      ) : (
        <div className={styles.content}>
          <div className={styles.total}><UsageSummary value={query.data.summary} compact /></div>
          {query.data.groups.length === 0 ? (
            <p className={styles.empty}>{t('usage.noGroups')}</p>
          ) : (
            <div className={styles.groups}>
              {query.data.groups.map((group) => (
                <article key={group.id}>
                  <div className={styles.identity}>
                    <AccountUsageGroupLink group={group} />
                    <small>{t(`usage.group.${group.kind}`)}</small>
                  </div>
                  <UsageSummary value={group.summary} compact />
                </article>
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function UsageSelector<T extends string | number>({
  label,
  values,
  active,
  labelFor,
  onChange,
}: {
  label: string;
  values: T[];
  active: T;
  labelFor: (value: T) => string;
  onChange: (value: T) => void;
}) {
  return (
    <div>
      <span>{label}</span>
      <div className={styles.segmented}>
        {values.map((value) => (
          <button key={value} type="button" aria-pressed={active === value} onClick={() => onChange(value)}>
            {labelFor(value)}
          </button>
        ))}
      </div>
    </div>
  );
}

function AccountUsageGroupLink({ group }: { group: UsageGroup }) {
  const label = group.name || group.id;
  if (group.kind === 'device') {
    return <Link to={`/devices/${encodeURIComponent(group.id)}`}>{label}</Link>;
  }
  return <strong>{label}</strong>;
}
