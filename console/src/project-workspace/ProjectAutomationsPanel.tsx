import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Plus, Warning } from '@phosphor-icons/react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { ErrorBlock, LoadingBlock } from '../components/States';
import {
  useDeleteProjectAutomation,
  useProjectAutomations,
  useUpdateProjectAutomation,
} from '../api/queries';
import type { AutomationTriggerKind, ProjectAutomationSpec, Service } from '../api/types';
import styles from './ProjectAutomationsPanel.module.css';

const triggerLabel = (kind: AutomationTriggerKind, t: TFunction) =>
  t(`projectAutomations.trigger.${kind}`);

const automationErrorLabel = (message: string, t: TFunction) => {
  if (message === 'Automation model is unavailable.') return t('projectAutomations.error.modelUnavailable');
  if (message.includes('several models') || message.includes('Service has no default model')) {
    return t('projectAutomations.error.modelNotSelected');
  }
  if (message.includes('No model is authorized') || message.includes('no LLM is configured')) {
    return t('projectAutomations.error.modelNotConfigured');
  }
  if (message.includes('temporary internal')) return t('projectAutomations.error.modelTemporary');
  if (message.startsWith('invalid cron expression:')) {
    return t('projectAutomations.error.invalidCron', { expression: message.slice('invalid cron expression:'.length).trim() });
  }
  if (message === 'dispatch failed') return t('projectAutomations.error.dispatchFailed');
  if (message.startsWith('SCM webhook could not be reconciled')) return t('projectAutomations.error.webhookReconcileFailed');
  if (message === 'Automation trigger configuration is unavailable.') return t('projectAutomations.error.triggerUnavailable');
  if (message === 'Automation Service is unavailable.') return t('projectAutomations.error.serviceUnavailable');
  if (message === 'Automation Service repository binding is unavailable.') return t('projectAutomations.error.repositoryUnavailable');
  if (message === 'Automation Run could not be created.') return t('projectAutomations.error.runCreateFailed');
  return message;
};

export function ProjectAutomationsPanel({
  projectId,
  services,
  canManage,
  initialServiceId,
}: {
  projectId: string;
  services: Service[];
  canManage: boolean;
  initialServiceId?: string;
}) {
  const { t } = useTranslation();
  const query = useProjectAutomations(projectId);
  const update = useUpdateProjectAutomation(projectId);
  const remove = useDeleteProjectAutomation(projectId);
  const [filter, setFilter] = useState<'all' | AutomationTriggerKind>('all');
  const visible = useMemo(
    () => (query.data ?? []).filter((item) =>
      filter === 'all' || item.automation.trigger_kind === filter),
    [filter, query.data],
  );
  const createHref = `/projects/${encodeURIComponent(projectId)}/automations/new${
    initialServiceId ? `?service=${encodeURIComponent(initialServiceId)}` : ''
  }`;

  if (query.isLoading) return <LoadingBlock label={t('projectAutomations.loading')} />;
  if (query.isError) {
    return <ErrorBlock error={query.error} onRetry={() => void query.refetch()} title={t('projectAutomations.loadError')} />;
  }

  return (
    <section className={styles.workspace} data-testid="project-automations-panel">
      <header className={styles.head}>
        <div>
          <span>{t('projectAutomations.eyebrow')}</span>
          <h2>{t('projectAutomations.title')}</h2>
          <p>{t('projectAutomations.subtitle')}</p>
        </div>
        {canManage && (
          services.length ? (
            <Link className={styles.primaryLink} to={createHref}>
              <Plus size={16} aria-hidden />
              {t('projectAutomations.new')}
            </Link>
          ) : (
            <span className={styles.primaryLink} aria-disabled="true">
              <Plus size={16} aria-hidden />
              {t('projectAutomations.new')}
            </span>
          )
        )}
      </header>
      {!services.length && (
        <div className={styles.warning}>
          <Warning size={17} aria-hidden />
          {t('projectAutomations.noServices')}
        </div>
      )}
      <div className={styles.filters} aria-label={t('projectAutomations.filters')}>
        {(['all', 'scm', 'cron'] as const).map((kind) => (
          <button key={kind} type="button" aria-pressed={filter === kind} onClick={() => setFilter(kind)}>
            {kind === 'all' ? t('projectAutomations.all') : triggerLabel(kind, t)}
          </button>
        ))}
      </div>
      {!visible.length ? (
        <p className={styles.empty}>{t('projectAutomations.empty')}</p>
      ) : (
        <ul className={styles.list}>
          {visible.map((item) => {
            const automation = item.automation;
            return (
              <li key={automation.id} className={styles.row}>
                <div>
                  <strong>{automation.name}</strong>
                  <p>{automation.prompt_template}</p>
                </div>
                <div className={styles.meta}>
                  <span>{triggerLabel(automation.trigger_kind, t)}</span>
                  <small>{triggerSummary(item)}</small>
                  {automation.last_error && <em>{automationErrorLabel(automation.last_error, t)}</em>}
                </div>
                <div className={styles.actions}>
                  <button
                    type="button"
                    role="switch"
                    aria-label={t('projectAutomations.toggleLabel', {
                      name: automation.name,
                      status: automation.enabled ? t('projectAutomations.enabled') : t('projectAutomations.disabled'),
                    })}
                    aria-checked={automation.enabled}
                    disabled={!canManage || update.isPending}
                    onClick={() => update.mutate({
                      automationId: automation.id,
                      input: { enabled: !automation.enabled },
                    })}
                  >
                    <span />
                  </button>
                  {canManage && (
                    <Link to={`/projects/${encodeURIComponent(projectId)}/automations/${encodeURIComponent(automation.id)}/edit`}>
                      {t('projectAutomations.edit')}
                    </Link>
                  )}
                  {canManage && (
                    <button
                      type="button"
                      className={styles.delete}
                      disabled={remove.isPending}
                      onClick={() => remove.mutate(automation.id)}
                    >
                      {t('projectAutomations.delete')}
                    </button>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function triggerSummary(item: ProjectAutomationSpec): string {
  if (item.scm) return (item.actions ?? []).map((action) => `${action.event_family}.${action.action}`).join(', ');
  return item.cron?.cron_expr ?? '';
}
