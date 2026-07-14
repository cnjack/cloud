import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ArrowLeft, DotsThree, GitPullRequest, Plus, Trash, Warning } from '@phosphor-icons/react';
import {
  useCreateServiceAutomation,
  useDeleteAutomation,
  useServiceAutomations,
  useUpdateAutomation,
} from '../api/queries';
import { ApiError } from '../api/client';
import type {
  Automation,
  AutomationEvent,
  AuthProviderInfo,
  Me,
  ProjectModel,
  Service,
  WebhookBinding,
  CreateAutomationInput,
} from '../api/types';
import { Button } from '../components/Button';
import { SchedulesPanel } from '../pages/SchedulesPanel';
import styles from './AutomationsPanel.module.css';

type Filter = 'all' | 'schedule' | 'review';

export function AutomationsPanel({
  service,
  canManage,
  models,
  scheduleCreateOpen,
  onScheduleCreateOpenChange,
  me,
  providers,
  oauthReturnTo,
  initialEditorOpen = false,
}: {
  service: Service;
  canManage: boolean;
  models: ProjectModel[];
  scheduleCreateOpen: boolean;
  onScheduleCreateOpenChange: (open: boolean) => void;
  me: Me | null;
  providers: readonly AuthProviderInfo[];
  oauthReturnTo: string;
  initialEditorOpen?: boolean;
}) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState<Filter>('all');
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<Automation | null>(null);
  const query = useServiceAutomations(service.id);
  const create = useCreateServiceAutomation(service.id);
  const update = useUpdateAutomation(service.id);
  const remove = useDeleteAutomation(service.id);
  const [mutationError, setMutationError] = useState('');

  const binding = query.data?.webhook_binding ?? null;
  const automations = query.data?.automations ?? [];
  const supportsReview = service.repo_kind === 'provider' && service.provider === 'gitea';

  useEffect(() => {
    if (initialEditorOpen) setEditorOpen(true);
  }, [initialEditorOpen]);

  const toggle = (automation: Automation) => {
    setMutationError('');
    update.mutate(
      { automationId: automation.id, input: { enabled: !automation.enabled } },
      { onError: (error) => setMutationError(errorMessage(error, t('automations.errUpdate'))) },
    );
  };

  const deleteAutomation = (automation: Automation) => {
    setMutationError('');
    remove.mutate(automation.id, {
      onError: (error) => setMutationError(errorMessage(error, t('automations.errDelete'))),
    });
  };

  if (editorOpen) {
    return (
      <AutomationEditor
        key={editing?.id ?? 'new'}
        service={service}
        automation={editing}
        models={models}
        supportsReview={supportsReview}
        me={me}
        providers={providers}
        oauthReturnTo={oauthReturnTo}
        busy={create.isPending || update.isPending}
        error={editing
          ? update.isError ? errorMessage(update.error, t('automations.errUpdate')) : ''
          : create.isError ? errorMessage(create.error, t('automations.errCreate')) : ''}
        onCancel={() => { setEditorOpen(false); setEditing(null); }}
        onSubmit={(input) => {
          if (editing) {
            update.mutate({
              automationId: editing.id,
              input: {
                name: input.name,
                instructions: input.instructions,
                model_id: input.model_id,
                events: input.events,
                base_branch: input.base_branch,
                include_drafts: input.include_drafts,
                enabled: input.enabled,
              },
            }, { onSuccess: () => { setEditorOpen(false); setEditing(null); } });
            return;
          }
          create.mutate(input, { onSuccess: () => setEditorOpen(false) });
        }}
      />
    );
  }

  return (
    <section className={styles.workspace} data-testid="automations-panel">
      <header className={styles.head}>
        <div>
          <span className={styles.eyebrow}>{t('automations.eyebrow')}</span>
          <h2>{t('automations.title')}</h2>
          <p>{t('automations.subtitle')}</p>
        </div>
        {canManage && (
          <Button
            type="button"
            variant="primary"
            size="sm"
            onClick={() => onScheduleCreateOpenChange(true)}
            data-testid="automation-new-schedule"
          >
            <Plus size={16} aria-hidden="true" />
            <span>{t('automations.newAutomation')}</span>
          </Button>
        )}
      </header>

      {!supportsReview && (
        <div className={styles.warning} data-testid="automation-provider-unavailable" role="status">
          <Warning size={18} aria-hidden="true" />
          <span><strong>{t('automations.giteaFirstStrong')}</strong> {t('automations.giteaFirstBody', { provider: service.provider ?? t('automations.thisSource') })}</span>
        </div>
      )}

      {mutationError && <div className={styles.error} role="alert">{mutationError}</div>}

      <div className={styles.toolbar}>
        <div className={styles.filters} aria-label={t('automations.filtersAria')}>
          {(['all', 'schedule', 'review'] as const).map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={filter === value}
              onClick={() => setFilter(value)}
            >
              {value === 'all' ? t('automations.filterAll') : value === 'schedule' ? t('automations.filterScheduled') : t('automations.filterReviews')}
            </button>
          ))}
        </div>
        {canManage && (
          <button
            type="button"
            className={styles.reviewAction}
            onClick={() => setEditorOpen(true)}
            disabled={!supportsReview}
            data-testid="automation-new-review"
          >
            <GitPullRequest size={17} aria-hidden="true" />
            {t('automations.newReview')}
          </button>
        )}
      </div>

      {filter !== 'schedule' && (
        <div className={styles.reviewList} aria-label={t('automations.reviewListAria')}>
          {query.isLoading && <p className={styles.empty}>{t('automations.loadingReviews')}</p>}
          {query.isError && (
            <div className={styles.error} role="alert">
              <span>{errorMessage(query.error, t('automations.errLoad'))}</span>
              <button type="button" onClick={() => void query.refetch()}>{t('common.retry')}</button>
            </div>
          )}
          {!query.isLoading && !query.isError && automations.length === 0 && (
            <p className={styles.empty}>{t('automations.emptyReviews')}</p>
          )}
          {automations.map((automation) => (
            <AutomationRow
              key={automation.id}
              automation={automation}
              binding={binding}
              canManage={canManage}
              busy={update.isPending || remove.isPending}
              onToggle={() => toggle(automation)}
              onEdit={() => {
                update.reset();
                setEditing(automation);
                setEditorOpen(true);
              }}
              onDelete={() => deleteAutomation(automation)}
            />
          ))}
        </div>
      )}

      {filter !== 'review' && (
        <div className={styles.scheduleSection}>
          <SchedulesPanel
            service={service}
            canManage={canManage}
            createOpen={scheduleCreateOpen}
            onCreateOpenChange={onScheduleCreateOpenChange}
          />
        </div>
      )}
    </section>
  );
}

function AutomationRow({
  automation,
  binding,
  canManage,
  busy,
  onToggle,
  onEdit,
  onDelete,
}: {
  automation: Automation;
  binding: WebhookBinding | null;
  canManage: boolean;
  busy: boolean;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();
  const [menuOpen, setMenuOpen] = useState(false);
  const delivery = bindingStatus(binding, automation, t);
  return (
    <article className={styles.row}>
      <div className={styles.main}>
        <div className={styles.titleRow}>
          <span className={styles.icon}><GitPullRequest size={17} aria-hidden="true" /></span>
          <strong>{automation.name}</strong>
        </div>
        <p>{automation.instructions}</p>
      </div>
      <div className={styles.cell}>
        <span>{automation.events.map((event) => eventLabel(event, t)).join(' · ')}</span>
        <small>{t('automations.branchMeta', { branch: automation.base_branch, draft: automation.include_drafts ? t('automations.includeDraft') : t('automations.ignoreDraft') })}</small>
      </div>
      <div className={styles.cell}>
        <span className={styles.status} data-status={delivery.tone}>
          <i aria-hidden="true" />{delivery.label}
        </span>
        <small>{delivery.detail}</small>
      </div>
      <div className={styles.actions}>
        <button
          type="button"
          role="switch"
          aria-checked={automation.enabled}
          aria-label={t('automations.toggleAria', { action: automation.enabled ? t('common.disable') : t('common.enable'), name: automation.name })}
          className={styles.switch}
          disabled={!canManage || busy}
          onClick={onToggle}
        ><span /></button>
        {canManage && (
          <div className={styles.actionMenu}>
            <button
              type="button"
              className={styles.iconButton}
              aria-label={t('automations.rowActionsAria', { name: automation.name })}
              aria-expanded={menuOpen}
              disabled={busy}
              onClick={() => setMenuOpen((open) => !open)}
            ><DotsThree size={20} weight="bold" aria-hidden="true" /></button>
            {menuOpen && (
              <div className={styles.actionMenuPopup} role="menu">
                <button type="button" role="menuitem" onClick={() => { setMenuOpen(false); onEdit(); }}>{t('automations.editAction')}</button>
                <button type="button" role="menuitem" className={styles.dangerAction} onClick={() => { setMenuOpen(false); onDelete(); }}><Trash size={15} aria-hidden="true" /> {t('automations.deleteAction')}</button>
              </div>
            )}
          </div>
        )}
      </div>
    </article>
  );
}

function AutomationEditor({
  service,
  automation,
  models,
  supportsReview,
  me,
  providers,
  oauthReturnTo,
  busy,
  error,
  onCancel,
  onSubmit,
}: {
  service: Service;
  automation: Automation | null;
  models: ProjectModel[];
  supportsReview: boolean;
  me: Me | null;
  providers: readonly AuthProviderInfo[];
  oauthReturnTo: string;
  busy: boolean;
  error: string;
  onCancel: () => void;
  onSubmit: (input: CreateAutomationInput) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(automation?.name ?? t('automations.defaultName'));
  const [instructions, setInstructions] = useState(automation?.instructions ?? t('automations.defaultInstructions'));
  const [modelID, setModelID] = useState(automation?.model_id ?? service.default_model_id ?? '');
  const [baseBranch, setBaseBranch] = useState(automation?.base_branch ?? service.default_branch);
  const [includeDrafts, setIncludeDrafts] = useState(automation?.include_drafts ?? false);
  const [events, setEvents] = useState<AutomationEvent[]>(automation?.events ?? ['opened', 'ready', 'synchronize']);
  const [localError, setLocalError] = useState('');
  const providerConfigured = providers.some((provider) => provider.id === 'gitea');
  const oauthStateKnown = providers.length > 0 || me !== null;
  const hasGiteaIdentity = me?.identities.some((identity) => identity.provider === 'gitea') === true;
  const oauthBlocked = oauthStateKnown && (!providerConfigured || !!me?.is_service || !hasGiteaIdentity);
  const connectURL = `/auth/link/gitea?${new URLSearchParams({ return_to: oauthReturnTo }).toString()}`;

  const toggleEvent = (event: AutomationEvent) => {
    setEvents((current) => current.includes(event) ? current.filter((item) => item !== event) : [...current, event]);
  };

  return (
    <section className={styles.editor} data-testid="automation-editor">
      <button type="button" className={styles.back} onClick={onCancel}>
        <ArrowLeft size={16} aria-hidden="true" /> {t('automations.backToAutomations')}
      </button>
      <header className={styles.editorHead}>
        <h2>{automation ? t('automations.editHeading') : t('automations.newHeading')}</h2>
        <p>{t('automations.editorSubtitle')}</p>
      </header>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!name.trim() || !instructions.trim() || !modelID || !baseBranch.trim() || events.length === 0) {
            setLocalError(t('automations.errIncomplete'));
            return;
          }
          setLocalError('');
          onSubmit({
            name: name.trim(), instructions: instructions.trim(), trigger_type: 'pr_review', model_id: modelID,
            events, base_branch: baseBranch.trim(), include_drafts: includeDrafts, enabled: true,
          });
        }}
      >
        <section className={styles.formSection}>
          <div><h3>{t('automations.basicInfoTitle')}</h3><p>{t('automations.basicInfoSubtitle')}</p></div>
          <div className={styles.fields}>
            <label>{t('automations.nameLabel')}<input data-testid="automation-name" value={name} onChange={(event) => setName(event.target.value)} /></label>
            <label>{t('automations.instructionsLabel')}<textarea data-testid="automation-instructions" rows={5} value={instructions} onChange={(event) => setInstructions(event.target.value)} /></label>
          </div>
        </section>
        <section className={styles.formSection}>
          <div><h3>{t('automations.triggerTitle')}</h3><p>{t('automations.triggerSubtitle')}</p></div>
          <div className={styles.fields}>
            {!supportsReview && <div className={styles.warning}><Warning size={18} /><span>{t('automations.cannotReceive')}</span></div>}
            {supportsReview && oauthStateKnown && !providerConfigured && <div className={styles.warning}><Warning size={18} /><span><strong>{t('automations.oauthNotConfiguredStrong')}</strong> {t('automations.oauthNotConfiguredBody')}</span></div>}
            {supportsReview && providerConfigured && me?.is_service && <div className={styles.warning}><Warning size={18} /><span>{t('automations.mustAuthorize')}</span></div>}
            {supportsReview && providerConfigured && !me?.is_service && !hasGiteaIdentity && (
              <div className={styles.oauthGate}>
                <div><strong>{t('automations.connectGiteaTitle')}</strong><span>{t('automations.connectGiteaBody')}</span></div>
                <a href={connectURL}>{t('automations.connectGitea')}</a>
              </div>
            )}
            <fieldset>
              <legend>{t('automations.eventsLegend')}</legend>
              {(['opened', 'ready', 'synchronize', 'reopened'] as AutomationEvent[]).map((event) => (
                <label className={styles.check} key={event}>
                  <input type="checkbox" checked={events.includes(event)} onChange={() => toggleEvent(event)} />
                  {eventLabel(event, t)}
                </label>
              ))}
            </fieldset>
            <div className={styles.fieldRow}>
              <label>{t('automations.baseBranchLabel')}<input value={baseBranch} onChange={(event) => setBaseBranch(event.target.value)} /></label>
              <label>{t('automations.draftPrLabel')}<select value={includeDrafts ? 'include' : 'skip'} onChange={(event) => setIncludeDrafts(event.target.value === 'include')}><option value="skip">{t('automations.ignoreDraftOption')}</option><option value="include">{t('automations.reviewDraftOption')}</option></select></label>
            </div>
          </div>
        </section>
        <section className={styles.formSection}>
          <div><h3>{t('automations.runtimeTitle')}</h3><p>{t('automations.runtimeSubtitle')}</p></div>
          <div className={styles.fields}>
            <label>{t('automations.serviceLabel')}<input value={`${service.name} · Gitea`} disabled /></label>
            <label>{t('automations.modelLabel')}<select data-testid="automation-model" value={modelID} onChange={(event) => setModelID(event.target.value)}><option value="">{t('automations.selectModel')}</option>{models.map((model) => <option key={model.id} value={model.id}>{model.name}</option>)}</select></label>
          </div>
        </section>
        {(localError || error) && <div className={styles.error} role="alert">{localError || error}</div>}
        <footer className={styles.footer}>
          <span>{t('automations.footerNote')}</span>
          <Button type="button" variant="secondary" size="sm" onClick={onCancel}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" size="sm" disabled={busy || !supportsReview || oauthBlocked} data-testid="automation-submit">{busy ? t('automations.verifying') : automation ? t('automations.saveChanges') : t('automations.createAutomation')}</Button>
        </footer>
      </form>
    </section>
  );
}

function eventLabel(event: AutomationEvent, t: TFunction): string {
  if (event === 'opened') return t('automations.eventOpened');
  if (event === 'ready') return t('automations.eventReady');
  if (event === 'synchronize') return t('automations.eventSynchronize');
  return t('automations.eventReopened');
}

function bindingStatus(binding: WebhookBinding | null, automation: Automation, t: TFunction) {
  if (!automation.enabled) return { label: t('automations.statusPaused'), detail: t('automations.statusPausedDetail'), tone: 'muted' };
  if (automation.last_error) return { label: t('automations.statusNeedsAttention'), detail: automation.last_error, tone: 'error' };
  if (!binding) return { label: t('automations.statusNotSynced'), detail: t('automations.statusNotSyncedDetail'), tone: 'error' };
  if (binding.status === 'error') return { label: t('automations.statusNeedsAttention'), detail: binding.last_error || t('automations.statusWebhookFailed'), tone: 'error' };
  if (!binding.last_delivery_at) return { label: t('automations.statusWaiting'), detail: t('automations.statusWaitingDetail'), tone: 'ready' };
  const label = binding.last_delivery_status === 'error' ? t('automations.deliveryFailed') : binding.last_delivery_status === 'duplicate' ? t('automations.deliveryDuplicate') : binding.last_delivery_status === 'ignored' ? t('automations.deliveryIgnored') : t('automations.delivered');
  return { label, detail: t('automations.lastDelivery', { time: formatTimestamp(binding.last_delivery_at) }), tone: binding.last_delivery_status === 'error' ? 'error' : 'ready' };
}

function formatTimestamp(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.message : fallback;
}
