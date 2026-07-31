import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useProjectUsage } from '../api/queries';
import type { UsageGroup, UsageMoneyTotal, UsageSummary as UsageSummaryValue } from '../api/types';
import { formatUsageMoney, UsageSummary } from '../components/UsageSummary';
import { ErrorBlock, LoadingBlock } from '../components/States';
import styles from './ProjectUsagePanel.module.css';

type RangePreset = '24h' | '7d' | '30d';
type GroupBy = 'service' | 'automation' | 'model';

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

export function ProjectUsagePanel({ projectId }: { projectId: string }) {
  const { t } = useTranslation();
  const [rangePreset, setRangePreset] = useState<RangePreset>('7d');
  const [groupBy, setGroupBy] = useState<GroupBy>('service');
  const [rangeAnchor] = useState(() => Date.now());
  const range = useMemo(() => {
    const to = new Date(rangeAnchor);
    const from = new Date(rangeAnchor - rangeMilliseconds[rangePreset]);
    return { from: from.toISOString(), to: to.toISOString() };
  }, [rangeAnchor, rangePreset]);
  const query = useProjectUsage(projectId, groupBy, range.from, range.to);

  if (query.isLoading) return <LoadingBlock label={t('usage.loading')} />;
  if (query.isError || !query.data) {
    return <ErrorBlock error={query.error} onRetry={() => void query.refetch()} title={t('usage.loadError')} />;
  }

  return (
    <section className={styles.page} aria-label={t('usage.title')} data-testid="project-usage">
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
        <div>
          <span>{t('usage.groupBy')}</span>
          <div className={styles.segmented}>
            {(['service', 'automation', 'model'] as GroupBy[]).map((value) => (
              <button key={value} type="button" aria-pressed={groupBy === value} onClick={() => setGroupBy(value)}>
                {t(`usage.group.${value}`)}
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
      <div className={styles.overview}>
        <div className={styles.total}>
          <UsageSummary value={query.data.summary} compact hideCosts />
        </div>
        <ProjectCaptureHealth value={query.data.summary} />
      </div>
      <ProjectCostSources value={query.data.summary} />
      <div className={styles.groups}>
        <h2>{t('usage.groupedUsage', { group: t(`usage.group.${groupBy}`) })}</h2>
        {query.data.groups.length === 0 ? (
          <p className={styles.empty}>{t('usage.noGroups')}</p>
        ) : (
          <div className={styles.table} role="table">
            {query.data.groups.map((group) => (
              <article key={group.id} role="row">
                <div className={styles.identity} role="cell">
                  <UsageGroupLink projectId={projectId} group={group} />
                  <small>{group.kind}</small>
                </div>
                <div role="cell"><UsageSummary value={group.summary} compact /></div>
              </article>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}

function ProjectCaptureHealth({ value }: { value: UsageSummaryValue }) {
  const { t } = useTranslation();
  return (
    <section className={styles.captureHealth} data-testid="project-capture-health">
      <h2>{t('usage.captureHealth')}</h2>
      <dl>
        <div><dt>{t('usage.reportedCapture')}</dt><dd>{value.capture.reported}</dd></div>
        <div><dt>{t('usage.partialCapture')}</dt><dd>{value.capture.partial}</dd></div>
        <div><dt>{t('usage.unavailableCapture')}</dt><dd>{value.capture.unavailable}</dd></div>
        <div><dt>{t('usage.parseErrors')}</dt><dd>{value.capture.parse_error}</dd></div>
      </dl>
    </section>
  );
}

function ProjectCostSources({ value }: { value: UsageSummaryValue }) {
  const { t } = useTranslation();
  const money = (items: UsageMoneyTotal[]) => items.length > 0
    ? items.map(formatUsageMoney).join(' · ')
    : t('usage.none');
  const uncosted = value.costs.uncosted.length > 0
    ? value.costs.uncosted.map((item) => `${item.tokens.toLocaleString()} ${t(`usage.category.${item.category}`)}`).join(' · ')
    : t('usage.none');
  return (
    <section className={styles.costSources} data-testid="project-cost-sources" aria-label={t('usage.costSources')}>
      <article><span>{t('usage.reported')}</span><strong>{money(value.costs.reported)}</strong><small>{t('usage.reportedHelp')}</small></article>
      <article><span>{t('usage.estimated')}</span><strong>{money(value.costs.estimated)}</strong><small>{t('usage.estimatedHelp')}</small></article>
      <article><span>{t('usage.uncosted')}</span><strong>{uncosted}</strong><small>{t('usage.uncostedHelp')}</small></article>
    </section>
  );
}

function UsageGroupLink({ projectId, group }: { projectId: string; group: UsageGroup }) {
  const label = group.name || group.id;
  if (group.kind === 'service') {
    return <Link to={`/projects/${encodeURIComponent(projectId)}?service=${encodeURIComponent(group.id)}&tab=tasks`}>{label}</Link>;
  }
  if (group.kind === 'automation') {
    return <Link to={`/projects/${encodeURIComponent(projectId)}/automations/${encodeURIComponent(group.id)}`}>{label}</Link>;
  }
  return <strong>{label}</strong>;
}
