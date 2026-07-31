import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft, ArrowSquareOut, Play, Wrench } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { ErrorBlock, LoadingBlock } from '../components/States';
import {
  useAutomationExecutions,
  useProject,
  useProjectAutomation,
  useRunAutomationNow,
} from '../api/queries';
import type { AutomationExecution, AutomationExecutionState } from '../api/types';
import styles from './AutomationDetailPage.module.css';

type Filter = '' | 'blocked' | 'running' | 'terminal';

export function AutomationDetailPage() {
  const { t } = useTranslation();
  const { projectId = '', automationId = '' } = useParams();
  const project = useProject(projectId);
  const automation = useProjectAutomation(projectId, automationId);
  const [filter, setFilter] = useState<Filter>('');
  const executions = useAutomationExecutions(automationId, filter);
  const runNow = useRunAutomationNow(automationId);
  const retryKey = useRef('');
  const [selectedId, setSelectedId] = useState('');
  const items = useMemo(
    () => executions.data?.pages.flatMap((page) => page.items) ?? [],
    [executions.data],
  );
  const selected = items.find((item) => item.id === selectedId) ?? items[0];
  const canRun = project.data?.role !== 'viewer';

  useEffect(() => {
    if (!selectedId && items[0]) setSelectedId(items[0].id);
  }, [items, selectedId]);

  if (project.isLoading || automation.isLoading) {
    return <LoadingBlock label={t('automationExecutions.loading')} />;
  }
  if (project.isError || automation.isError) {
    return <ErrorBlock error={project.error ?? automation.error} title={t('automationExecutions.loadError')} />;
  }
  const spec = automation.data;
  if (!spec) return null;
  const service = project.data?.services?.find((item) => item.id === spec.automation.service_id);

  const triggerSummary = spec.scm
    ? (spec.actions ?? []).map((action) => `${action.event_family}.${action.action}`).join(', ')
    : `${spec.cron?.cron_expr ?? ''} · ${spec.cron?.output_mode === 'create_card'
      ? t('automationExecutions.cardOutput')
      : t('automationExecutions.runOutput')}`;

  const triggerNow = () => {
    if (!retryKey.current) retryKey.current = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
    runNow.mutate(retryKey.current, {
      onSuccess: (value) => {
        retryKey.current = '';
        setSelectedId(value.id);
      },
    });
  };

  return (
    <main className={styles.page} data-testid="automation-detail-page">
      <Link className={styles.back} to={`/projects/${encodeURIComponent(projectId)}?tab=automations&service=${encodeURIComponent(spec.automation.service_id)}`}>
        <ArrowLeft size={16} aria-hidden />{t('automationExecutions.back')}
      </Link>
      <header className={styles.head}>
        <div>
          <span>{spec.automation.trigger_kind} · {spec.automation.enabled
            ? t('projectAutomations.enabled') : t('projectAutomations.disabled')}</span>
          <h1>{spec.automation.name}</h1>
          <p>{service?.name ?? spec.automation.service_id} · {triggerSummary}</p>
        </div>
        <div className={styles.headActions}>
          <Link to={`/projects/${encodeURIComponent(projectId)}/automations/${encodeURIComponent(automationId)}/edit?service=${encodeURIComponent(spec.automation.service_id)}`}>
            {t('projectAutomations.edit')}
          </Link>
          <Button onClick={triggerNow} disabled={!canRun || runNow.isPending}>
            <Play size={15} weight="fill" aria-hidden />
            {t('automationExecutions.runNow')}
          </Button>
        </div>
      </header>
      {runNow.error && <p className={styles.actionError} role="alert">{t('automationExecutions.runNowError')}</p>}

      <div className={styles.layout}>
        <section className={styles.ledger} aria-label={t('automationExecutions.history')}>
          <div className={styles.toolbar}>
            <strong>{t('automationExecutions.history')}</strong>
            <div className={styles.filters}>
              {(['', 'blocked', 'running', 'terminal'] as Filter[]).map((value) => (
                <button
                  key={value || 'all'}
                  type="button"
                  aria-pressed={filter === value}
                  onClick={() => {
                    setFilter(value);
                    setSelectedId('');
                  }}
                >
                  {t(`automationExecutions.filter.${value || 'all'}`)}
                </button>
              ))}
            </div>
          </div>
          {executions.isLoading ? (
            <LoadingBlock label={t('automationExecutions.loadingHistory')} />
          ) : executions.isError ? (
            <ErrorBlock error={executions.error} onRetry={() => void executions.refetch()} title={t('automationExecutions.historyError')} />
          ) : !items.length ? (
            <p className={styles.empty}>{t('automationExecutions.empty')}</p>
          ) : (
            <>
              <ol className={styles.list}>
                {items.map((item) => (
                  <li key={item.id}>
                    <button
                      type="button"
                      className={selected?.id === item.id ? styles.selected : ''}
                      onClick={() => setSelectedId(item.id)}
                    >
                      <StateBadge state={item.state} outcome={item.outcome} />
                      <span>
                        <strong>{executionTitle(item, t)}</strong>
                        <small>{item.reason || item.output.label}</small>
                      </span>
                      <time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString()}</time>
                    </button>
                  </li>
                ))}
              </ol>
              {executions.hasNextPage && (
                <button className={styles.more} type="button" onClick={() => void executions.fetchNextPage()}>
                  {t('automationExecutions.loadMore')}
                </button>
              )}
            </>
          )}
        </section>
        <ExecutionInspector execution={selected} />
      </div>
    </main>
  );
}

function StateBadge({ state, outcome }: { state: AutomationExecutionState; outcome?: string }) {
  const { t } = useTranslation();
  const label = state === 'terminal' && outcome
    ? t(`automationExecutions.outcome.${outcome}`)
    : t(`automationExecutions.state.${state}`);
  return <span className={`${styles.state} ${styles[state] ?? ''}`}>{label}</span>;
}

function ExecutionInspector({ execution }: { execution?: AutomationExecution }) {
  const { t } = useTranslation();
  if (!execution) {
    return <aside className={styles.inspector}><p className={styles.empty}>{t('automationExecutions.select')}</p></aside>;
  }
  return (
    <aside className={styles.inspector} aria-label={t('automationExecutions.selected')}>
      <header>
        <span>{t('automationExecutions.execution')}</span>
        <h2>{executionTitle(execution, t)}</h2>
      </header>
      <section>
        <h3>{t('automationExecutions.trigger')}</h3>
        <dl>
          <dt>{t('automationExecutions.kind')}</dt><dd>{execution.trigger_kind}</dd>
          <dt>{t('automationExecutions.requested')}</dt><dd>{execution.requested_actor?.label ?? t('automationExecutions.notApplicable')}</dd>
          <dt>{t('automationExecutions.accountable')}</dt><dd>{execution.accountable_actor?.label ?? t('automationExecutions.unattributed')}</dd>
        </dl>
      </section>
      <section>
        <h3>{t('automationExecutions.output')}</h3>
        <dl>
          <dt>{t('automationExecutions.expected')}</dt><dd>{execution.output_mode === 'create_card'
            ? t('automationExecutions.cardThenRun') : t('automationExecutions.directRun')}</dd>
          <dt>{t('automationExecutions.actual')}</dt>
          <dd>{execution.output.href && execution.output.available
            ? <Link to={execution.output.href}>{execution.output.label}<ArrowSquareOut size={13} aria-hidden /></Link>
            : execution.output.label}</dd>
          <dt>{t('automationExecutions.writeback')}</dt><dd>{execution.writeback_state || t('automationExecutions.notApplicable')}</dd>
          <dt>{t('automationExecutions.usage')}</dt><dd>{t('automationExecutions.usageUnavailable')}</dd>
        </dl>
        {execution.reason && (
          <div className={styles.repair}>
            <Wrench size={15} aria-hidden />
            <p><strong>{execution.reason_code}</strong>{execution.reason}
              {execution.repair_role && <small>{t(`automationExecutions.repair.${execution.repair_role}`)}</small>}
            </p>
          </div>
        )}
      </section>
    </aside>
  );
}

function executionTitle(item: AutomationExecution, t: (key: string) => string): string {
  if (item.state === 'blocked') return t('automationExecutions.title.blocked');
  if (item.state === 'ignored') return t('automationExecutions.title.ignored');
  if (item.output.kind === 'card') return t('automationExecutions.title.card');
  if (item.output.kind === 'run') return t('automationExecutions.title.run');
  return t('automationExecutions.execution');
}
