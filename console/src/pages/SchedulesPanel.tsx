/*
 * SchedulesPanel — a service's cron triggers (F11 / D24). Lists the schedules that
 * dispatch a run against this repository on a cron, using its default model. A
 * member sees the list read-only; an owner can add, edit (cron / prompt), toggle
 * enabled, and delete.
 *
 * Fail-visible (CLAUDE.md red line #1): a schedule's `last_error` — why the most
 * recent due window was ABANDONED without dispatching (no/ambiguous model, or a
 * git host no longer allowed) — is surfaced as a loud red badge, not hidden. It
 * clears on the next successful dispatch. Cron validation errors from the server
 * (invalid_cron / cron_too_frequent) surface as a toast.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '../components/Button';
import { TextField, TextAreaField } from '../components/Field';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import {
  useServiceSchedules,
  useCreateServiceSchedule,
  useUpdateSchedule,
  useDeleteSchedule,
} from '../api/queries';
import { summarize, timeAgo } from '../lib/format';
import type { Schedule, Service } from '../api/types';
import styles from './SchedulesPanel.module.css';

export function SchedulesPanel({
  service,
  canManage,
  createOpen,
  onCreateOpenChange,
}: {
  service: Service;
  canManage: boolean;
  /** Lets the Automation workspace own the primary creation affordance. */
  createOpen?: boolean;
  onCreateOpenChange?: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const schedulesQ = useServiceSchedules(service.id);
  const create = useCreateServiceSchedule(service.id);
  const update = useUpdateSchedule(service.id);
  const del = useDeleteSchedule(service.id);

  const [localShowForm, setLocalShowForm] = useState(false);
  const [cron, setCron] = useState('');
  const [prompt, setPrompt] = useState('');
  const showForm = createOpen ?? localShowForm;
  const setShowForm = (open: boolean) => {
    onCreateOpenChange?.(open);
    if (createOpen === undefined) setLocalShowForm(open);
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { cron_expr: cron.trim(), prompt: prompt.trim() },
      {
        onSuccess: () => {
          setCron('');
          setPrompt('');
          setShowForm(false);
          toast.push({ kind: 'success', message: t('schedules.added') });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : t('schedules.addError'),
          }),
      },
    );
  };

  const schedules = schedulesQ.data ?? [];

  return (
    <section className={styles.panel} data-testid="schedules-panel">
      <div className={styles.head}>
          <div>
            <span className={styles.eyebrow}>{t('schedules.eyebrow')}</span>
            <h2 className={styles.title}>{t('schedules.title')}</h2>
          </div>
        {canManage && !showForm && (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => setShowForm(true)}
            data-testid="schedule-add-open"
          >
            {t('schedules.addSchedule')}
          </Button>
        )}
      </div>
      <p className={styles.hint}>
        {t('schedules.hint', { name: service.name })}
      </p>

      {schedulesQ.isLoading ? (
        <p className={styles.hint}>{t('common.loading')}</p>
      ) : schedulesQ.isError ? (
        <div className={styles.loadError} role="alert" data-testid="schedules-load-error">
          <p>
            {schedulesQ.error instanceof ApiError
              ? schedulesQ.error.message
              : t('schedules.loadError')}
          </p>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => void schedulesQ.refetch()}
            disabled={schedulesQ.isFetching}
            data-testid="schedules-retry"
          >
            {schedulesQ.isFetching ? t('schedules.retrying') : t('common.retry')}
          </Button>
        </div>
      ) : schedules.length === 0 ? (
        <p className={styles.empty} data-testid="schedules-empty">
          {canManage ? t('schedules.emptyManage') : t('schedules.empty')}
        </p>
      ) : (
        <ul className={styles.list} data-testid="schedules-list">
          {schedules.map((sc) => (
            <ScheduleRow
              key={sc.id}
              schedule={sc}
              canManage={canManage}
              toggling={update.isPending}
              deleting={del.isPending}
              onToggle={() =>
                update.mutate(
                  { scheduleId: sc.id, input: { enabled: !sc.enabled } },
                  {
                    onError: (err) =>
                      toast.push({
                        kind: 'error',
                        message:
                          err instanceof ApiError ? err.message : t('schedules.updateError'),
                      }),
                  },
                )
              }
              onSaveEdit={(input, done) =>
                update.mutate(
                  { scheduleId: sc.id, input },
                  {
                    onSuccess: () => {
                      done();
                      toast.push({ kind: 'success', message: t('schedules.updated') });
                    },
                    onError: (err) =>
                      toast.push({
                        kind: 'error',
                        message:
                          err instanceof ApiError ? err.message : t('schedules.updateError'),
                      }),
                  },
                )
              }
              onRemove={() =>
                del.mutate(sc.id, {
                  onSuccess: () => toast.push({ kind: 'success', message: t('schedules.removed') }),
                  onError: (err) =>
                    toast.push({
                      kind: 'error',
                      message:
                        err instanceof ApiError ? err.message : t('schedules.removeError'),
                    }),
                })
              }
            />
          ))}
        </ul>
      )}

      {canManage && showForm && (
        <form className={styles.form} onSubmit={submit} noValidate data-testid="schedule-form">
          <TextField
            label={t('schedules.cronLabel')}
            placeholder="0 9 * * 1-5"
            value={cron}
            onChange={(e) => setCron(e.target.value)}
            required
            data-testid="schedule-cron"
            autoComplete="off"
            hint={t('schedules.cronHint')}
          />
          <TextAreaField
            label={t('schedules.promptLabel')}
            placeholder={t('schedules.promptPlaceholder')}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            required
            rows={2}
            data-testid="schedule-prompt"
          />
          <div className={styles.formActions}>
            <Button
              type="submit"
              variant="primary"
              size="sm"
              loading={create.isPending}
              data-testid="schedule-create"
            >
              {t('common.add')}
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => setShowForm(false)}>
              {t('common.cancel')}
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}

function ScheduleRow({
  schedule,
  canManage,
  toggling,
  deleting,
  onToggle,
  onSaveEdit,
  onRemove,
}: {
  schedule: Schedule;
  canManage: boolean;
  toggling: boolean;
  deleting: boolean;
  onToggle: () => void;
  onSaveEdit: (input: { cron_expr: string; prompt: string }, done: () => void) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [cron, setCron] = useState(schedule.cron_expr);
  const [prompt, setPrompt] = useState(schedule.prompt);

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    onSaveEdit({ cron_expr: cron.trim(), prompt: prompt.trim() }, () => setEditing(false));
  };

  return (
    <li className={styles.row} data-testid={`schedule-row-${schedule.id}`} data-enabled={schedule.enabled}>
      <div className={styles.meta}>
        <div className={styles.titleRow}>
          <code className={styles.cron} data-testid={`schedule-cron-${schedule.id}`}>
            {schedule.cron_expr}
          </code>
          <span
            className={styles.badge}
            data-state={schedule.enabled ? 'on' : 'off'}
            data-testid={`schedule-state-${schedule.id}`}
          >
            {schedule.enabled ? t('schedules.stateEnabled') : t('schedules.stateDisabled')}
          </span>
          {schedule.last_error ? (
            <span
              className={styles.errorBadge}
              title={schedule.last_error}
              data-testid={`schedule-error-${schedule.id}`}
            >
              {t('schedules.didNotDispatch')}
            </span>
          ) : null}
        </div>
        <div className={styles.sub}>{summarize(schedule.prompt, 100)}</div>
        <div className={styles.stamp}>
          {schedule.last_fired_at
            ? t('schedules.lastFired', { time: timeAgo(schedule.last_fired_at) })
            : t('schedules.notYetFired')}
          {schedule.last_error ? ` · ${schedule.last_error}` : ''}
        </div>
        {editing && (
          <form className={styles.editForm} onSubmit={save} noValidate>
            <TextField
              label={t('schedules.cronLabel')}
              value={cron}
              onChange={(e) => setCron(e.target.value)}
              required
              data-testid={`schedule-edit-cron-${schedule.id}`}
              autoComplete="off"
            />
            <TextAreaField
              label={t('schedules.promptLabel')}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              required
              rows={2}
              data-testid={`schedule-edit-prompt-${schedule.id}`}
            />
            <div className={styles.formActions}>
              <Button
                type="submit"
                variant="primary"
                size="sm"
                data-testid={`schedule-edit-save-${schedule.id}`}
              >
                {t('common.save')}
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
                {t('common.cancel')}
              </Button>
            </div>
          </form>
        )}
      </div>
      {canManage && !editing && (
        <div className={styles.actions}>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={toggling}
            onClick={onToggle}
            data-testid={`schedule-toggle-${schedule.id}`}
          >
            {schedule.enabled ? t('common.disable') : t('common.enable')}
          </Button>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => {
              setCron(schedule.cron_expr);
              setPrompt(schedule.prompt);
              setEditing(true);
            }}
            data-testid={`schedule-edit-${schedule.id}`}
          >
            {t('common.edit')}
          </Button>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={deleting}
            onClick={onRemove}
            data-testid={`schedule-delete-${schedule.id}`}
          >
            {t('common.remove')}
          </Button>
        </div>
      )}
    </li>
  );
}
