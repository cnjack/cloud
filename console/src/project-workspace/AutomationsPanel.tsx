import { useEffect, useState } from 'react';
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
      { onError: (error) => setMutationError(errorMessage(error, 'Could not update the Automation.')) },
    );
  };

  const deleteAutomation = (automation: Automation) => {
    setMutationError('');
    remove.mutate(automation.id, {
      onError: (error) => setMutationError(errorMessage(error, 'Could not delete the Automation.')),
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
          ? update.isError ? errorMessage(update.error, 'Could not update the Automation.') : ''
          : create.isError ? errorMessage(create.error, 'Could not create the Automation.') : ''}
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
          <span className={styles.eyebrow}>Project automation</span>
          <h2>Automations</h2>
          <p>Bind instructions to this Service and trigger them on a schedule or a provider PR event.</p>
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
            <span>New automation</span>
          </Button>
        )}
      </header>

      {!supportsReview && (
        <div className={styles.warning} data-testid="automation-provider-unavailable" role="status">
          <Warning size={18} aria-hidden="true" />
          <span><strong>Automatic PR review is Gitea-first.</strong> {service.provider ?? 'This source'} can still use its existing comment command, but PR lifecycle events are not presented as available.</span>
        </div>
      )}

      {mutationError && <div className={styles.error} role="alert">{mutationError}</div>}

      <div className={styles.toolbar}>
        <div className={styles.filters} aria-label="Automation filters">
          {(['all', 'schedule', 'review'] as const).map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={filter === value}
              onClick={() => setFilter(value)}
            >
              {value === 'all' ? 'All' : value === 'schedule' ? 'Scheduled' : 'PR reviews'}
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
            New PR review
          </button>
        )}
      </div>

      {filter !== 'schedule' && (
        <div className={styles.reviewList} aria-label="PR review Automations">
          {query.isLoading && <p className={styles.empty}>Loading PR review Automations…</p>}
          {query.isError && (
            <div className={styles.error} role="alert">
              <span>{errorMessage(query.error, 'Could not load PR review Automations.')}</span>
              <button type="button" onClick={() => void query.refetch()}>Retry</button>
            </div>
          )}
          {!query.isLoading && !query.isError && automations.length === 0 && (
            <p className={styles.empty}>No PR review Automations for this Service yet.</p>
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
  const [menuOpen, setMenuOpen] = useState(false);
  const delivery = bindingStatus(binding, automation);
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
        <span>{automation.events.map(eventLabel).join(' · ')}</span>
        <small>Base {automation.base_branch} · {automation.include_drafts ? 'include draft' : 'ignore draft'}</small>
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
          aria-label={`${automation.enabled ? 'Disable' : 'Enable'} ${automation.name}`}
          className={styles.switch}
          disabled={!canManage || busy}
          onClick={onToggle}
        ><span /></button>
        {canManage && (
          <div className={styles.actionMenu}>
            <button
              type="button"
              className={styles.iconButton}
              aria-label={`Automation actions for ${automation.name}`}
              aria-expanded={menuOpen}
              disabled={busy}
              onClick={() => setMenuOpen((open) => !open)}
            ><DotsThree size={20} weight="bold" aria-hidden="true" /></button>
            {menuOpen && (
              <div className={styles.actionMenuPopup} role="menu">
                <button type="button" role="menuitem" onClick={() => { setMenuOpen(false); onEdit(); }}>Edit Automation</button>
                <button type="button" role="menuitem" className={styles.dangerAction} onClick={() => { setMenuOpen(false); onDelete(); }}><Trash size={15} aria-hidden="true" /> Delete Automation</button>
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
  const [name, setName] = useState(automation?.name ?? 'Gitea PR automatic review');
  const [instructions, setInstructions] = useState(automation?.instructions ?? 'Review security, regressions, and fail-visible behavior.');
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
        <ArrowLeft size={16} aria-hidden="true" /> Back to Automations
      </button>
      <header className={styles.editorHead}>
        <h2>{automation ? 'Edit PR review Automation' : 'New PR review Automation'}</h2>
        <p>Instructions, Service, trigger policy, and an explicit model run headlessly on each matching event.</p>
      </header>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!name.trim() || !instructions.trim() || !modelID || !baseBranch.trim() || events.length === 0) {
            setLocalError('Complete the name, instructions, model, base branch, and at least one event.');
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
          <div><h3>Basic information</h3><p>Name the policy and describe exactly what every review should inspect.</p></div>
          <div className={styles.fields}>
            <label>Name<input data-testid="automation-name" value={name} onChange={(event) => setName(event.target.value)} /></label>
            <label>Instructions<textarea data-testid="automation-instructions" rows={5} value={instructions} onChange={(event) => setInstructions(event.target.value)} /></label>
          </div>
        </section>
        <section className={styles.formSection}>
          <div><h3>Trigger</h3><p>Gitea sends lifecycle events to the shared, OAuth-managed Service webhook.</p></div>
          <div className={styles.fields}>
            {!supportsReview && <div className={styles.warning}><Warning size={18} /><span>This Service cannot receive Gitea PR review events.</span></div>}
            {supportsReview && oauthStateKnown && !providerConfigured && <div className={styles.warning}><Warning size={18} /><span><strong>Gitea OAuth is not configured.</strong> Ask a cluster administrator to configure the provider and webhook receiver.</span></div>}
            {supportsReview && providerConfigured && me?.is_service && <div className={styles.warning}><Warning size={18} /><span>A signed-in project owner must authorize Gitea; a console-token session has no personal OAuth grant.</span></div>}
            {supportsReview && providerConfigured && !me?.is_service && !hasGiteaIdentity && (
              <div className={styles.oauthGate}>
                <div><strong>Connect Gitea with OAuth</strong><span>Authorize repository-hook access without pasting a personal access token into jcode Cloud.</span></div>
                <a href={connectURL}>Connect Gitea</a>
              </div>
            )}
            <fieldset>
              <legend>Events</legend>
              {(['opened', 'ready', 'synchronize', 'reopened'] as AutomationEvent[]).map((event) => (
                <label className={styles.check} key={event}>
                  <input type="checkbox" checked={events.includes(event)} onChange={() => toggleEvent(event)} />
                  {eventLabel(event)}
                </label>
              ))}
            </fieldset>
            <div className={styles.fieldRow}>
              <label>Base branch<input value={baseBranch} onChange={(event) => setBaseBranch(event.target.value)} /></label>
              <label>Draft PR<select value={includeDrafts ? 'include' : 'skip'} onChange={(event) => setIncludeDrafts(event.target.value === 'include')}><option value="skip">Ignore draft</option><option value="include">Review draft</option></select></label>
            </div>
          </div>
        </section>
        <section className={styles.formSection}>
          <div><h3>Runtime</h3><p>Headless Automations use full access and must select a granted model.</p></div>
          <div className={styles.fields}>
            <label>Service<input value={`${service.name} · Gitea`} disabled /></label>
            <label>Model<select data-testid="automation-model" value={modelID} onChange={(event) => setModelID(event.target.value)}><option value="">Select a model</option>{models.map((model) => <option key={model.id} value={model.id}>{model.name}</option>)}</select></label>
          </div>
        </section>
        {(localError || error) && <div className={styles.error} role="alert">{localError || error}</div>}
        <footer className={styles.footer}>
          <span>Creation verifies OAuth and reconciles the Gitea webhook before the policy becomes active.</span>
          <Button type="button" variant="secondary" size="sm" onClick={onCancel}>Cancel</Button>
          <Button type="submit" variant="primary" size="sm" disabled={busy || !supportsReview || oauthBlocked} data-testid="automation-submit">{busy ? 'Verifying…' : automation ? 'Save changes' : 'Create Automation'}</Button>
        </footer>
      </form>
    </section>
  );
}

function eventLabel(event: AutomationEvent): string {
  if (event === 'opened') return 'PR opened';
  if (event === 'ready') return 'Ready for review';
  if (event === 'synchronize') return 'New commits';
  return 'Reopened';
}

function bindingStatus(binding: WebhookBinding | null, automation: Automation) {
  if (!automation.enabled) return { label: 'Paused', detail: 'This policy will not dispatch Runs.', tone: 'muted' };
  if (automation.last_error) return { label: 'Needs attention', detail: automation.last_error, tone: 'error' };
  if (!binding) return { label: 'Not synchronized', detail: 'Enable or re-save to verify the webhook.', tone: 'error' };
  if (binding.status === 'error') return { label: 'Needs attention', detail: binding.last_error || 'Webhook synchronization failed.', tone: 'error' };
  if (!binding.last_delivery_at) return { label: 'Waiting for event', detail: 'Webhook synchronized; no PR event received yet.', tone: 'ready' };
  const label = binding.last_delivery_status === 'error' ? 'Delivery failed' : binding.last_delivery_status === 'duplicate' ? 'Duplicate ignored' : binding.last_delivery_status === 'ignored' ? 'Event ignored' : 'Delivered';
  return { label, detail: `Last delivery ${formatTimestamp(binding.last_delivery_at)}`, tone: binding.last_delivery_status === 'error' ? 'error' : 'ready' };
}

function formatTimestamp(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.message : fallback;
}
