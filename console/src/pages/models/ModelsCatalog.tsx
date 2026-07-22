/*
 * ModelsCatalog — the shared, scope-parameterized model-provider catalog UI used
 * by both the cluster page (ClusterModelsPage) and the project-scoped manager
 * (ProjectModelsPanel). ONE implementation of provider cards, catalog discovery,
 * custom models, the provider form (with an Advanced custom-headers disclosure)
 * and per-model controls. The right-side control on each model row branches on
 * scope: cluster → grant management; project → an enable/disable Switch + edit +
 * delete (jcode parity).
 *
 * The provider add/edit dialog state is CONTROLLED by the parent (`editor` /
 * `onEditorChange`) so the cluster page keeps its page-header "Add provider"
 * button while the project panel owns its own — one dialog, two placements.
 */
import {
  Check,
  Lightning,
  Lock,
  MagnifyingGlass,
  PencilSimple,
  Plus,
  SlidersHorizontal,
  Terminal,
  Trash,
} from '@phosphor-icons/react';
import { useEffect, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { ApiError, apiErrorCode } from '../../api/client';
import { useProjects, useSetModelGrant } from '../../api/queries';
import type {
  CatalogModel,
  CreateModelProviderInput,
  ModelCapabilities,
  ModelProvider,
  ModelProviderAuthType,
  ModelProviderCatalogMode,
  ProviderModel,
  UpdateModelProviderInput,
} from '../../api/types';
import { Button } from '../../components/Button';
import { SelectField, TextField } from '../../components/Field';
import { Modal } from '../../components/Modal';
import { ProviderIcon } from '../../components/ProviderIcon';
import { StatusLabel } from '../../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../../components/States';
import { useToast } from '../../components/Toast';
import { DESKTOP_PROVIDERS, desktopProvider } from '../../lib/desktopProviders';
import type { ModelsAdminApi, ModelsScope } from './scope';
import { useModelsAdminApi } from './scope';
import styles from './ModelsCatalog.module.css';

const EMPTY_CAPABILITIES: ModelCapabilities = { reasoning: false, tools: false, image: false };

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.message : fallback;
}

function contextLabel(contextWindow: number, t: TFunction): string {
  if (contextWindow <= 0) return t('cluster.models.contextUnknown');
  if (contextWindow >= 1000 && contextWindow % 1000 === 0) return t('cluster.models.contextK', { value: contextWindow / 1000 });
  return t('cluster.models.context', { value: contextWindow.toLocaleString() });
}

/** An accessible on/off switch (role="switch"). */
export function Switch({ checked, onChange, disabled, label }: {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      className={styles.switch}
      data-on={checked || undefined}
      disabled={disabled}
      onClick={() => onChange(!checked)}
    >
      <span className={styles.switchKnob} aria-hidden="true" />
    </button>
  );
}

export interface ModelsCatalogProps {
  scope: ModelsScope;
  /** Controlled provider add/edit dialog state, owned by the parent. */
  editor: ModelProvider | 'new' | null;
  onEditorChange: (next: ModelProvider | 'new' | null) => void;
  /** Cluster shows a search box over many providers; project scope hides it. */
  searchable?: boolean;
}

/**
 * The catalog surface: security note, (optional) search toolbar, the configured-
 * providers heading, the provider cards, and the provider add/edit dialog.
 */
export function ModelsCatalog({ scope, editor, onEditorChange, searchable = true }: ModelsCatalogProps) {
  const { t } = useTranslation();
  const api = useModelsAdminApi(scope);
  const providers = api.providersQuery;
  const [search, setSearch] = useState('');

  const query = searchable ? search.trim().toLowerCase() : '';
  const list = providers.data ?? [];
  const filtered = list.map((provider) => {
    if (!query) return provider;
    const providerMatches = `${provider.name} ${provider.kind} ${provider.base_url}`.toLowerCase().includes(query);
    const models = provider.models.filter((model) => `${model.name} ${model.model_id} ${model.source} ${Object.entries(model.capabilities).filter(([, enabled]) => enabled).map(([name]) => name).join(' ')}`.toLowerCase().includes(query));
    return providerMatches ? provider : { ...provider, models };
  }).filter((provider) => !query || provider.models.length > 0 || `${provider.name} ${provider.kind} ${provider.base_url}`.toLowerCase().includes(query));
  const totalModels = list.reduce((sum, provider) => sum + provider.models.length, 0);
  const visibleModels = filtered.reduce((sum, provider) => sum + provider.models.length, 0);
  const totalGrants = list.reduce((sum, provider) => sum + (provider.project_grants ?? 0), 0);
  const summary = scope.kind === 'cluster'
    ? t('cluster.models.providersSummary', { providers: list.length, grants: totalGrants })
    : t('projectSettings.models.providersSummary', { providers: list.length, models: totalModels });

  return (
    <section className={styles.catalog} aria-labelledby="provider-catalog-title">
      <div className={styles.security}><Lock size={14} aria-hidden="true" /><span>{t('cluster.models.securityNote')}</span></div>
      {searchable && (
        <div className={styles.toolbar}>
          <label className={styles.search}>
            <MagnifyingGlass size={14} aria-hidden="true" />
            <span className={styles.srOnly}>{t('cluster.models.filterLabel')}</span>
            <input type="search" aria-label={t('cluster.models.filterLabel')} placeholder={t('cluster.models.filterPlaceholder')} value={search} onChange={(event) => setSearch(event.target.value)} />
          </label>
          <span className={styles.result}><b>{visibleModels}</b> {t('cluster.models.resultCount', { total: totalModels })}</span>
        </div>
      )}
      <div className={styles.heading}><div><h2 id="provider-catalog-title">{t('cluster.models.configuredProviders')}</h2><p>{summary}</p></div></div>

      {providers.isLoading ? <LoadingBlock label={t('cluster.models.loadingProviders')} /> : providers.isError ? (
        <ErrorBlock error={providers.error} onRetry={() => providers.refetch()} title={t('cluster.models.loadProvidersError')} />
      ) : filtered.length === 0 ? (
        <div className={styles.searchEmpty} role="status"><MagnifyingGlass size={24} aria-hidden="true" /><strong>{list.length ? t('cluster.models.noMatches') : (scope.kind === 'cluster' ? t('cluster.models.noProviders') : t('projectSettings.models.noProviders'))}</strong><span>{list.length ? t('cluster.models.noMatchesHint') : (scope.kind === 'cluster' ? t('cluster.models.noProvidersHint') : t('projectSettings.models.noProvidersHint'))}</span></div>
      ) : (
        <div className={styles.stack}>{filtered.map((provider) => <ProviderCard key={provider.id} api={api} provider={provider} onEdit={() => onEditorChange(provider)} />)}</div>
      )}

      <ProviderDialog
        api={api}
        provider={editor === 'new' ? null : editor}
        configuredKinds={list.map((configured) => configured.kind)}
        open={editor !== null}
        onClose={() => onEditorChange(null)}
      />
    </section>
  );
}

function ProviderCard({ api, provider, onEdit }: { api: ModelsAdminApi; provider: ModelProvider; onEdit: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const verify = api.verifyProvider;
  const remove = api.deleteProvider;
  const [customOpen, setCustomOpen] = useState(false);
  const [catalogOpen, setCatalogOpen] = useState(false);
  const authLabel = provider.auth_type === 'service_identity' ? t('cluster.models.authServiceIdentity') : provider.api_key_set ? t('cluster.models.authKeySet') : provider.auth_type === 'none' ? t('cluster.models.authKeyless') : t('cluster.models.authKeyMissing');
  const authTone: 'success' | 'warning' = provider.auth_type !== 'api_key' || provider.api_key_set ? 'success' : 'warning';
  const verified = provider.last_verified_at && !provider.last_verification_error;

  const testProvider = () => verify.mutate(provider.id, {
    onSuccess: (result) => toast.push({ kind: 'success', message: result.catalog_available ? t('cluster.models.verifiedIn', { ms: result.latency_ms }) : t('cluster.models.reachableIn', { ms: result.latency_ms }) }),
    onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, t('cluster.models.verifyError')) }),
  });

  const deleteProvider = () => {
    if (!window.confirm(t('cluster.models.removeConfirm', { name: provider.name }))) return;
    remove.mutate(provider.id, {
      onSuccess: () => toast.push({ kind: 'success', message: t('cluster.models.providerRemoved') }),
      onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, t('cluster.models.removeError')) }),
    });
  };

  return (
    <article className={styles.providerCard} data-testid={`provider-card-${provider.id}`}>
      <header className={styles.providerHead}>
        <ProviderIcon kind={provider.kind} name={provider.name} />
        <div className={styles.providerCopy}>
          <div className={styles.providerTitle}><h2>{provider.name}</h2>{provider.kind === 'custom' && <StatusLabel>{t('cluster.models.customLabel')}</StatusLabel>}<StatusLabel tone={authTone}>{authLabel}</StatusLabel></div>
          <p><span className={styles.mono}>{provider.base_url}</span><span aria-hidden="true"> · </span>{provider.auth_type === 'service_identity' ? t('cluster.models.credKeylessIdentity') : provider.api_key_set ? t('cluster.models.credConfigured') : provider.auth_type === 'none' ? t('cluster.models.credNoneRequired') : t('cluster.models.credRequired')}</p>
          {provider.last_verification_error && <p className={styles.providerError}>{t('cluster.models.lastTest', { error: provider.last_verification_error })}</p>}
        </div>
        <div className={styles.providerActions}>
          <Button size="sm" variant="ghost" onClick={testProvider} loading={verify.isPending}>{verified && <Check size={13} aria-hidden="true" />}{verified ? t('cluster.models.verified') : t('cluster.models.test')}</Button>
          <button className={styles.iconButton} type="button" aria-label={t('cluster.models.editProviderAria', { name: provider.name })} onClick={onEdit}><SlidersHorizontal size={16} aria-hidden="true" /></button>
          <button className={styles.iconButton} type="button" aria-label={t('cluster.models.removeProviderAria', { name: provider.name })} onClick={deleteProvider} disabled={remove.isPending}><Trash size={16} aria-hidden="true" /></button>
        </div>
      </header>
      <div className={styles.providerModels}>
        <div className={styles.modelBar}>
          <span><b>{provider.models.length}</b> {provider.models.length === 1 ? (provider.models[0]?.source === 'custom' ? t('cluster.models.customModel') : t('cluster.models.configuredModel')) : t('cluster.models.configuredModels')}</span>
          <div className={styles.modelActions}>
            {provider.catalog_mode === 'disabled' ? (
              <button className={styles.textButton} type="button" disabled><MagnifyingGlass size={13} aria-hidden="true" />{t('cluster.models.catalogUnavailable')}</button>
            ) : (
              <button className={styles.textButton} type="button" onClick={() => setCatalogOpen(true)}><MagnifyingGlass size={13} aria-hidden="true" />{t('cluster.models.browseCatalog')}</button>
            )}
            <Button size="sm" onClick={() => setCustomOpen(true)}><Plus size={13} aria-hidden="true" />{t('cluster.models.customModelAction')}</Button>
          </div>
        </div>
        {provider.models.length > 0 && <div className={styles.modelList}>{provider.models.map((model) => <ModelRow key={model.id} api={api} provider={provider} model={model} />)}</div>}
        {provider.catalog_mode === 'disabled' && <p className={styles.limitNote}>{t('cluster.models.limitNote')}</p>}
      </div>
      <CustomModelDialog api={api} provider={provider} open={customOpen} onClose={() => setCustomOpen(false)} />
      <CatalogDialog api={api} provider={provider} open={catalogOpen} onClose={() => setCatalogOpen(false)} />
    </article>
  );
}

function CapabilityChips({ model }: { model: ProviderModel }) {
  const { t } = useTranslation();
  return (
    <span className={styles.capabilities} aria-label={t('cluster.models.capabilitiesAria')}>
      {model.capabilities.reasoning && <span><Lightning size={11} aria-hidden="true" />{t('cluster.models.capReasoning')}</span>}
      {model.capabilities.tools && <span><Terminal size={11} aria-hidden="true" />{t('cluster.models.capTools')}</span>}
      {model.capabilities.image && <span>{t('cluster.models.capImage')}</span>}
    </span>
  );
}

function ModelRow({ api, provider, model }: { api: ModelsAdminApi; provider: ModelProvider; model: ProviderModel }) {
  const { t } = useTranslation();
  const toast = useToast();
  const [grantsOpen, setGrantsOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);

  if (api.scope.kind === 'cluster') {
    const grantCount = (model.granted_project_ids ?? []).length;
    return (
      <div className={styles.modelRow}>
        <span className={styles.modelCopy}><strong>{model.name}</strong><small>{model.model_id} · {contextLabel(model.context_window, t)}</small></span>
        <CapabilityChips model={model} />
        <span className={styles.grantCount} data-testid={`grant-count-${model.id}`}><b>{grantCount}</b><small>{t('cluster.models.projectGrants', { count: grantCount })}</small></span>
        <Button size="sm" variant="ghost" onClick={() => setGrantsOpen(true)}>{t('cluster.models.manageGrants')}</Button>
        <GrantDialog model={model} open={grantsOpen} onClose={() => setGrantsOpen(false)} />
      </div>
    );
  }

  // Project scope: enable Switch + edit + delete.
  const enabled = model.enabled !== false;
  const toggleEnabled = () => api.updateModel?.mutate(
    { providerId: provider.id, modelId: model.id, input: { enabled: !enabled } },
    { onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, t('projectSettings.models.updateModelError')) }) },
  );
  const deleteModel = () => {
    if (!window.confirm(t('projectSettings.models.removeModelConfirm', { name: model.name }))) return;
    api.deleteModel?.mutate(
      { providerId: provider.id, modelId: model.id },
      {
        onSuccess: () => toast.push({ kind: 'success', message: t('projectSettings.models.modelRemoved') }),
        onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, t('projectSettings.models.removeModelError')) }),
      },
    );
  };

  return (
    <div className={[styles.modelRow, styles.modelRowProject].join(' ')} data-testid={`model-row-${model.id}`}>
      <span className={styles.modelCopy}><strong>{model.name}</strong><small>{model.model_id} · {contextLabel(model.context_window, t)}</small></span>
      <CapabilityChips model={model} />
      <div className={styles.modelControls}>
        <Switch
          checked={enabled}
          onChange={toggleEnabled}
          disabled={api.updateModel?.isPending && api.updateModel?.variables?.modelId === model.id}
          label={enabled ? t('projectSettings.models.disableAria', { name: model.name }) : t('projectSettings.models.enableAria', { name: model.name })}
        />
        <button className={styles.iconButton} type="button" aria-label={t('projectSettings.models.editModelAria', { name: model.name })} onClick={() => setEditOpen(true)}><PencilSimple size={16} aria-hidden="true" /></button>
        <button className={styles.iconButton} type="button" aria-label={t('projectSettings.models.deleteModelAria', { name: model.name })} onClick={deleteModel} disabled={api.deleteModel?.isPending && api.deleteModel?.variables?.modelId === model.id}><Trash size={16} aria-hidden="true" /></button>
      </div>
      <CustomModelDialog api={api} provider={provider} model={model} open={editOpen} onClose={() => setEditOpen(false)} />
    </div>
  );
}

function CapabilityFields({ value, onChange }: { value: ModelCapabilities; onChange: (next: ModelCapabilities) => void }) {
  const { t } = useTranslation();
  return (
    <fieldset className={styles.capabilityFields}>
      <legend>{t('cluster.models.capabilitiesLegend')}</legend>
      <label><input type="checkbox" checked={value.reasoning} onChange={(event) => onChange({ ...value, reasoning: event.target.checked })} />{t('cluster.models.capReasoning')}</label>
      <label><input type="checkbox" checked={value.tools} onChange={(event) => onChange({ ...value, tools: event.target.checked })} />{t('cluster.models.capToolUse')}</label>
      <label><input type="checkbox" checked={value.image} onChange={(event) => onChange({ ...value, image: event.target.checked })} />{t('cluster.models.capImageInput')}</label>
    </fieldset>
  );
}

/**
 * Add a custom model (both scopes) or, in project scope, edit an existing one.
 * Editing PATCHes name / context_window / capabilities (the model_id is fixed).
 */
function CustomModelDialog({ api, provider, open, onClose, model }: { api: ModelsAdminApi; provider: ModelProvider; open: boolean; onClose: () => void; model?: ProviderModel }) {
  const { t } = useTranslation();
  const editing = !!model;
  const toast = useToast();
  const [name, setName] = useState('');
  const [modelId, setModelId] = useState('');
  const [contextWindow, setContextWindow] = useState('');
  const [capabilities, setCapabilities] = useState(EMPTY_CAPABILITIES);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!open) return;
    setName(model?.name ?? '');
    setModelId(model?.model_id ?? '');
    setContextWindow(model && model.context_window > 0 ? String(model.context_window) : '');
    setCapabilities(model ? { ...model.capabilities } : EMPTY_CAPABILITIES);
    setError(undefined);
  }, [open, model]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const parsedContext = contextWindow === '' ? 0 : Number(contextWindow);
    if (!name.trim() || (!editing && !modelId.trim())) { setError(t('cluster.models.customValidationRequired')); return; }
    if (!Number.isFinite(parsedContext) || parsedContext < 0) { setError(t('cluster.models.customValidationContext')); return; }
    if (editing && model) {
      api.updateModel?.mutate({ providerId: provider.id, modelId: model.id, input: { name: name.trim(), context_window: parsedContext, capabilities } }, {
        onSuccess: () => { toast.push({ kind: 'success', message: t('projectSettings.models.modelSaved') }); onClose(); },
        onError: (cause) => setError(errorMessage(cause, t('projectSettings.models.saveModelError'))),
      });
      return;
    }
    api.createModel.mutate({ providerId: provider.id, input: { name: name.trim(), model_id: modelId.trim(), context_window: parsedContext, capabilities, source: 'custom' } }, {
      onSuccess: () => { toast.push({ kind: 'success', message: t('cluster.models.customAdded') }); setName(''); setModelId(''); setContextWindow(''); setCapabilities(EMPTY_CAPABILITIES); setError(undefined); onClose(); },
      onError: (cause) => setError(errorMessage(cause, t('cluster.models.addModelError'))),
    });
  };

  const formId = editing && model ? `custom-model-edit-${model.id}` : `custom-model-${provider.id}`;
  const pending = editing ? api.updateModel?.isPending : api.createModel.isPending;
  return (
    <Modal open={open} onClose={onClose} title={editing && model ? t('projectSettings.models.editDialogTitle', { name: model.name }) : t('cluster.models.customDialogTitle', { name: provider.name })} footer={<><Button variant="ghost" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => document.getElementById(formId)?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))} loading={pending}>{editing ? t('projectSettings.models.saveModel') : t('cluster.models.addCustomModel')}</Button></>}>
      <form id={formId} className={styles.dialogForm} onSubmit={submit}>
        <TextField label={t('cluster.models.displayNameLabel')} required value={name} onChange={(event) => setName(event.target.value)} placeholder={t('cluster.models.displayNamePlaceholder')} />
        {!editing && <TextField label={t('cluster.models.modelIdLabel')} required value={modelId} onChange={(event) => setModelId(event.target.value)} placeholder="coding-plan" hint={t('cluster.models.modelIdHint')} />}
        <TextField label={t('cluster.models.contextWindowLabel')} type="number" min={0} value={contextWindow} onChange={(event) => setContextWindow(event.target.value)} placeholder="32000" />
        <CapabilityFields value={capabilities} onChange={setCapabilities} />
        {error && <p className={styles.formError} role="alert">{error}</p>}
      </form>
    </Modal>
  );
}

function CatalogDialog({ api, provider, open, onClose }: { api: ModelsAdminApi; provider: ModelProvider; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const catalog = api.useCatalog(provider.id, open);
  const create = api.createModel;
  const toast = useToast();
  const add = (model: CatalogModel) => create.mutate({ providerId: provider.id, input: { name: model.name || model.id, model_id: model.id, context_window: model.context_window, capabilities: model.capabilities, source: 'catalog' } }, {
    onSuccess: () => toast.push({ kind: 'success', message: t('cluster.models.modelAdded', { name: model.name || model.id }) }),
    onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, t('cluster.models.addCatalogError')) }),
  });
  return (
    <Modal open={open} onClose={onClose} title={t('cluster.models.catalogDialogTitle', { name: provider.name })}>
      {catalog.isLoading ? <LoadingBlock label={t('cluster.models.loadingCatalog')} /> : catalog.isError ? (
        <div className={styles.catalogError}><strong>{apiErrorCode(catalog.error) === 'catalog_unavailable' ? t('cluster.models.catalogUnavailable') : t('cluster.models.catalogLoadError')}</strong><p>{errorMessage(catalog.error, t('cluster.models.catalogLoadFallback'))}</p><Button onClick={() => catalog.refetch()}>{t('common.retry')}</Button></div>
      ) : (
        <ul className={styles.catalogList}>{(catalog.data ?? []).map((model) => {
          const configured = provider.models.some((item) => item.model_id === model.id);
          return <li key={model.id}><span><strong>{model.name || model.id}</strong><small>{model.id} · {contextLabel(model.context_window, t)}</small></span><Button size="sm" onClick={() => add(model)} disabled={configured || create.isPending}>{configured ? t('common.configured') : t('common.add')}</Button></li>;
        })}</ul>
      )}
    </Modal>
  );
}

/** Cluster scope only: manage which projects may select a model. */
function GrantDialog({ model, open, onClose }: { model: ProviderModel; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const projects = useProjects();
  const setGrant = useSetModelGrant();
  const toast = useToast();
  const granted = model.granted_project_ids ?? [];
  const toggle = (projectId: string, next: boolean) => setGrant.mutate({ modelId: model.id, projectId, granted: next }, {
    onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, t('cluster.models.grantUpdateError')) }),
  });
  return (
    <Modal open={open} onClose={onClose} title={t('cluster.models.grantsDialogTitle', { name: model.name })} footer={<Button onClick={onClose}>{t('common.done')}</Button>}>
      <p className={styles.dialogIntro}>{t('cluster.models.grantsIntro')}</p>
      {projects.isLoading ? <LoadingBlock label={t('cluster.models.loadingProjects')} /> : (projects.data ?? []).length === 0 ? <p className={styles.dialogIntro}>{t('cluster.models.noProjects')}</p> : (
        <div className={styles.grantList}>{(projects.data ?? []).map((project) => {
          const isGranted = granted.includes(project.id);
          return <label key={project.id}><input type="checkbox" aria-label={project.name} checked={isGranted} disabled={setGrant.isPending} onChange={(event) => toggle(project.id, event.target.checked)} /><span><strong>{project.name}</strong><small>{t('cluster.models.servicesCount', { count: project.services?.length ?? 0 })}</small></span></label>;
        })}</div>
      )}
    </Modal>
  );
}

interface HeaderRow { key: string; value: string }

function ProviderDialog({ api, provider, configuredKinds, open, onClose }: { api: ModelsAdminApi; provider: ModelProvider | null; configuredKinds: string[]; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const create = api.createProvider;
  const update = api.updateProvider;
  const toast = useToast();
  const [mode, setMode] = useState<'registry' | 'custom'>('registry');
  const [selectedProvider, setSelectedProvider] = useState('');
  const [name, setName] = useState('');
  const [kind, setKind] = useState('openai');
  const [baseUrl, setBaseUrl] = useState('');
  const [authType, setAuthType] = useState<ModelProviderAuthType>('api_key');
  const [apiKey, setApiKey] = useState('');
  const [catalogMode, setCatalogMode] = useState<ModelProviderCatalogMode>('auto');
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [headerRows, setHeaderRows] = useState<HeaderRow[]>([]);
  const [clearHeaders, setClearHeaders] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!open) return;
    setMode('registry');
    setSelectedProvider('');
    setName(provider?.name ?? '');
    setKind(provider?.kind ?? 'openai');
    setBaseUrl(provider?.base_url ?? '');
    setAuthType(provider?.auth_type ?? 'api_key');
    setApiKey('');
    setCatalogMode(provider?.catalog_mode ?? 'auto');
    setAdvancedOpen(!!provider?.headers_set);
    setHeaderRows([]);
    setClearHeaders(false);
    setError(undefined);
  }, [open, provider]);

  const chooseMode = (next: 'registry' | 'custom') => {
    setMode(next);
    setSelectedProvider('');
    setName('');
    setKind(next === 'custom' ? 'openai' : '');
    setBaseUrl('');
  };

  const chooseProvider = (id: string) => {
    setSelectedProvider(id);
    const preset = desktopProvider(id);
    setName(preset?.name ?? '');
    setKind(preset?.id ?? '');
    setBaseUrl(preset?.baseUrl ?? '');
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim() || !kind.trim() || !baseUrl.trim()) { setError(t('cluster.models.providerValidationRequired')); return; }
    if (!provider && authType === 'api_key' && !apiKey) { setError(t('cluster.models.providerValidationKey')); return; }
    // Collect complete header rows; a half-filled row mirrors the server's 400.
    const headers: Record<string, string> = {};
    let touchedHeaders = false;
    for (const row of headerRows) {
      const key = row.key.trim();
      if (!key && row.value.trim() === '') continue;
      touchedHeaders = true;
      if (!key || row.value.trim() === '') { setError(t('cluster.models.headerValidation')); return; }
      headers[key] = row.value;
    }
    const headerPatch = clearHeaders ? {} : touchedHeaders ? headers : undefined;
    if (provider) {
      const input: UpdateModelProviderInput = { name: name.trim(), kind: kind.trim(), base_url: baseUrl.trim(), auth_type: authType, catalog_mode: catalogMode };
      if (apiKey !== '') input.api_key = apiKey;
      if (headerPatch) input.headers = headerPatch;
      update.mutate({ id: provider.id, input }, {
        onSuccess: () => { toast.push({ kind: 'success', message: t('cluster.models.providerSaved') }); onClose(); },
        onError: (cause) => setError(errorMessage(cause, t('cluster.models.saveProviderError'))),
      });
    } else {
      const input: CreateModelProviderInput = { name: name.trim(), kind: kind.trim(), base_url: baseUrl.trim(), auth_type: authType, api_key: authType === 'api_key' ? apiKey : '', catalog_mode: catalogMode };
      if (headerPatch) input.headers = headerPatch;
      create.mutate(input, {
        onSuccess: () => { toast.push({ kind: 'success', message: t('cluster.models.providerAdded') }); onClose(); },
        onError: (cause) => setError(errorMessage(cause, t('cluster.models.addProviderError'))),
      });
    }
  };
  const setHeaderRow = (index: number, patch: Partial<HeaderRow>) =>
    setHeaderRows((rows) => rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  const formId = provider ? `provider-edit-${provider.id}` : 'provider-add';
  return (
    <Modal open={open} onClose={onClose} title={provider ? t('cluster.models.editProviderTitle', { name: provider.name }) : t('cluster.models.addProviderTitle')} footer={<><Button variant="ghost" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" loading={create.isPending || update.isPending} onClick={() => document.getElementById(formId)?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))}>{provider ? t('cluster.models.saveProvider') : t('cluster.models.addProvider')}</Button></>}>
      <form id={formId} className={styles.dialogForm} onSubmit={submit}>
        {!provider && (
          <div className={styles.providerPicker}>
            <div className={styles.providerMode} role="group" aria-label={t('cluster.models.providerSourceLabel')}>
              <button type="button" data-active={mode === 'registry' || undefined} data-testid="provider-mode-registry" onClick={() => chooseMode('registry')}>{t('cluster.models.providerRegistryMode')}</button>
              <button type="button" data-active={mode === 'custom' || undefined} data-testid="provider-mode-custom" onClick={() => chooseMode('custom')}>{t('cluster.models.providerCustomMode')}</button>
            </div>
            {mode === 'registry' && (
              <label className={styles.nativeSelectField}>
                <span>{t('cluster.models.providerSelectLabel')} <b aria-hidden="true">*</b></span>
                <select aria-label={t('cluster.models.providerSelectLabel')} value={selectedProvider} onChange={(event) => chooseProvider(event.target.value)}>
                  <option value="">{t('cluster.models.providerSelectPlaceholder')}</option>
                  {DESKTOP_PROVIDERS.filter((preset) => !configuredKinds.includes(preset.id)).map((preset) => <option key={preset.id} value={preset.id}>{preset.name}</option>)}
                </select>
              </label>
            )}
            {mode === 'registry' && selectedProvider && (
              <div className={styles.providerPreset} data-testid="selected-provider-preset">
                <ProviderIcon kind={kind} name={name} />
                <span><strong>{name}</strong><code>{kind}</code></span>
              </div>
            )}
          </div>
        )}
        {(provider || mode === 'custom') && <TextField label={t('cluster.models.providerNameLabel')} required value={name} onChange={(event) => setName(event.target.value)} placeholder={t('cluster.models.providerNamePlaceholder')} />}
        {!provider && mode === 'custom' && <TextField label={t('cluster.models.providerTypeLabel')} required value={kind} onChange={(event) => setKind(event.target.value.toLowerCase())} placeholder="openai" hint={t('cluster.models.providerTypeHint')} />}
        <SelectField label={t('cluster.models.authLabel')} value={authType} onChange={(value) => setAuthType(value as ModelProviderAuthType)} options={[{ value: 'api_key', label: t('cluster.models.authApiKey') }, { value: 'service_identity', label: t('cluster.models.authServiceIdentityOption') }, { value: 'none', label: t('cluster.models.authNone') }]} />
        {authType === 'api_key' && <TextField label={provider ? t('cluster.models.rotateApiKeyLabel') : t('cluster.models.authApiKey')} type="password" autoComplete="off" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={provider?.api_key_set ? t('cluster.models.apiKeyPlaceholderKeep') : t('cluster.models.apiKeyPlaceholderEnter')} hint={t('cluster.models.apiKeyHint')} />}
        <div className={styles.advanced}>
          <button type="button" className={styles.advancedToggle} aria-expanded={advancedOpen} data-testid="provider-advanced-toggle" onClick={() => setAdvancedOpen((o) => !o)}>
            <SlidersHorizontal size={13} aria-hidden="true" />{t('cluster.models.advancedLabel')}
          </button>
          {advancedOpen && (
            <div className={styles.headerFields}>
              <TextField label={t('cluster.models.baseUrlLabel')} required value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://api.example.com/v1" />
              <SelectField label={t('cluster.models.catalogLabel')} value={catalogMode} onChange={(value) => setCatalogMode(value as ModelProviderCatalogMode)} options={[{ value: 'auto', label: t('cluster.models.catalogAuto') }, { value: 'disabled', label: t('cluster.models.catalogDisabled') }]} />
              <span className={styles.headerHint}>{provider?.headers_set ? t('cluster.models.headersConfiguredHint') : t('cluster.models.headersHint')}</span>
              {headerRows.map((row, index) => (
                <div className={styles.headerRow} key={index}>
                  <input className={styles.headerInput} placeholder={t('cluster.models.headerKeyPlaceholder')} value={row.key} autoComplete="off" data-testid={`header-key-${index}`} onChange={(event) => setHeaderRow(index, { key: event.target.value })} />
                  <input className={styles.headerInput} placeholder={t('cluster.models.headerValuePlaceholder')} value={row.value} autoComplete="off" data-testid={`header-value-${index}`} onChange={(event) => setHeaderRow(index, { value: event.target.value })} />
                  <button className={styles.iconButton} type="button" aria-label={t('cluster.models.removeHeader')} onClick={() => setHeaderRows((rows) => rows.filter((_, i) => i !== index))}><Trash size={15} aria-hidden="true" /></button>
                </div>
              ))}
              <Button type="button" variant="ghost" size="sm" data-testid="add-header" onClick={() => { setClearHeaders(false); setHeaderRows((rows) => [...rows, { key: '', value: '' }]); }}><Plus size={13} aria-hidden="true" />{t('cluster.models.addHeader')}</Button>
              {provider?.headers_set && headerRows.length === 0 && (
                <Button type="button" variant={clearHeaders ? 'danger' : 'ghost'} size="sm" data-testid="clear-headers" onClick={() => setClearHeaders((c) => !c)}>{clearHeaders ? t('projectSettings.models.clearHeadersPending') : t('projectSettings.models.clearHeadersAction')}</Button>
              )}
            </div>
          )}
        </div>
        {error && <p className={styles.formError} role="alert">{error}</p>}
      </form>
    </Modal>
  );
}

/** A read-only rendering of the models available to a project (the union list). */
export function GrantedModelsList({ models, envFallback, note }: { models: ReadonlyArray<{ id: string; name: string; model_name: string }>; envFallback: boolean; note?: ReactNode }) {
  const { t } = useTranslation();
  if (models.length === 0 && !envFallback) {
    return <p className={styles.panelState}>{t('projectSettings.noModelConfigured')}</p>;
  }
  return (
    <div className={styles.grantedList}>
      {models.map((model) => (
        <div key={model.id} className={styles.grantedRow}>
          <span className={styles.grantedMark} aria-hidden>AI</span>
          <span><strong>{model.name}</strong><code>{model.model_name}</code></span>
          <small>{note ?? t('projectSettings.granted')}</small>
        </div>
      ))}
      {envFallback && <p className={styles.panelState}>{t('projectSettings.envFallbackActive')}</p>}
    </div>
  );
}
