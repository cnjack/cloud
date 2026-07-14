import {
  Check,
  Lightning,
  Lock,
  MagnifyingGlass,
  Plus,
  SlidersHorizontal,
  Terminal,
  Trash,
} from '@phosphor-icons/react';
import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { useRole } from '../api/ApiProvider';
import { ApiError, apiErrorCode } from '../api/client';
import {
  useCreateModelProvider,
  useCreateProviderModel,
  useDeleteModelProvider,
  useModelProviderCatalog,
  useModelProviders,
  useProjects,
  useSetModelGrant,
  useUpdateModelProvider,
  useVerifyModelProvider,
} from '../api/queries';
import type {
  CatalogModel,
  CreateModelProviderInput,
  ModelCapabilities,
  ModelProvider,
  ModelProviderAuthType,
  ModelProviderCatalogMode,
  ProviderModel,
  UpdateModelProviderInput,
} from '../api/types';
import { Button } from '../components/Button';
import { SelectField, TextField } from '../components/Field';
import { Modal } from '../components/Modal';
import { ClusterSubnav, PageHeader, StatusLabel, SurfaceInner } from '../components/PageLayout';
import { ProviderIcon } from '../components/ProviderIcon';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { useToast } from '../components/Toast';
import { ClusterAccessDenied } from './ClusterAccessDenied';
import styles from './ClusterModelsPage.module.css';

const EMPTY_CAPABILITIES: ModelCapabilities = { reasoning: false, tools: false, image: false };

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.message : fallback;
}

function contextLabel(contextWindow: number, t: TFunction): string {
  if (contextWindow <= 0) return t('cluster.models.contextUnknown');
  if (contextWindow >= 1000 && contextWindow % 1000 === 0) return t('cluster.models.contextK', { value: contextWindow / 1000 });
  return t('cluster.models.context', { value: contextWindow.toLocaleString() });
}

export function ClusterModelsPage() {
  const { t } = useTranslation();
  const isAdmin = useRole() === 'cluster-admin';
  const providers = useModelProviders(isAdmin);
  const [search, setSearch] = useState('');
  const [providerEditor, setProviderEditor] = useState<ModelProvider | 'new' | null>(null);
  if (!isAdmin) return <ClusterAccessDenied />;

  const query = search.trim().toLowerCase();
  const filtered = (providers.data ?? []).map((provider) => {
    if (!query) return provider;
    const providerMatches = `${provider.name} ${provider.kind} ${provider.base_url}`.toLowerCase().includes(query);
    const models = provider.models.filter((model) => `${model.name} ${model.model_id} ${model.source} ${Object.entries(model.capabilities).filter(([, enabled]) => enabled).map(([name]) => name).join(' ')}`.toLowerCase().includes(query));
    return providerMatches ? provider : { ...provider, models };
  }).filter((provider) => !query || provider.models.length > 0 || `${provider.name} ${provider.kind} ${provider.base_url}`.toLowerCase().includes(query));
  const totalModels = (providers.data ?? []).reduce((sum, provider) => sum + provider.models.length, 0);
  const visibleModels = filtered.reduce((sum, provider) => sum + provider.models.length, 0);
  const totalGrants = (providers.data ?? []).reduce((sum, provider) => sum + provider.project_grants, 0);

  return (
    <>
      <ClusterSubnav />
      <SurfaceInner className={styles.page}>
        <PageHeader
          eyebrow={t('cluster.models.eyebrow')}
          title={t('cluster.models.title')}
          description={t('cluster.models.description')}
          actions={<Button variant="primary" onClick={() => setProviderEditor('new')}><Plus size={14} aria-hidden="true" />{t('cluster.models.addProvider')}</Button>}
        />

        <section className={styles.catalog} aria-labelledby="provider-catalog-title">
          <div className={styles.security}><Lock size={14} aria-hidden="true" /><span>{t('cluster.models.securityNote')}</span></div>
          <div className={styles.toolbar}>
            <label className={styles.search}>
              <MagnifyingGlass size={14} aria-hidden="true" />
              <span className={styles.srOnly}>{t('cluster.models.filterLabel')}</span>
              <input type="search" aria-label={t('cluster.models.filterLabel')} placeholder={t('cluster.models.filterPlaceholder')} value={search} onChange={(event) => setSearch(event.target.value)} />
            </label>
            <span className={styles.result}><b>{visibleModels}</b> {t('cluster.models.resultCount', { total: totalModels })}</span>
          </div>
          <div className={styles.heading}><div><h2 id="provider-catalog-title">{t('cluster.models.configuredProviders')}</h2><p>{t('cluster.models.providersSummary', { providers: providers.data?.length ?? 0, grants: totalGrants })}</p></div></div>

          {providers.isLoading ? <LoadingBlock label={t('cluster.models.loadingProviders')} /> : providers.isError ? (
            <ErrorBlock error={providers.error} onRetry={() => providers.refetch()} title={t('cluster.models.loadProvidersError')} />
          ) : filtered.length === 0 ? (
            <div className={styles.searchEmpty} role="status"><MagnifyingGlass size={24} aria-hidden="true" /><strong>{providers.data?.length ? t('cluster.models.noMatches') : t('cluster.models.noProviders')}</strong><span>{providers.data?.length ? t('cluster.models.noMatchesHint') : t('cluster.models.noProvidersHint')}</span></div>
          ) : (
            <div className={styles.stack}>{filtered.map((provider) => <ProviderCard key={provider.id} provider={provider} onEdit={() => setProviderEditor(provider)} />)}</div>
          )}
        </section>
      </SurfaceInner>

      <ProviderDialog
        provider={providerEditor === 'new' ? null : providerEditor}
        open={providerEditor !== null}
        onClose={() => setProviderEditor(null)}
      />
    </>
  );
}

function ProviderCard({ provider, onEdit }: { provider: ModelProvider; onEdit: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const verify = useVerifyModelProvider();
  const remove = useDeleteModelProvider();
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
        {provider.models.length > 0 && <div className={styles.modelList}>{provider.models.map((model) => <ModelRow key={model.id} model={model} />)}</div>}
        {provider.catalog_mode === 'disabled' && <p className={styles.limitNote}>{t('cluster.models.limitNote')}</p>}
      </div>
      <CustomModelDialog provider={provider} open={customOpen} onClose={() => setCustomOpen(false)} />
      <CatalogDialog provider={provider} open={catalogOpen} onClose={() => setCatalogOpen(false)} />
    </article>
  );
}

function ModelRow({ model }: { model: ProviderModel }) {
  const { t } = useTranslation();
  const [grantsOpen, setGrantsOpen] = useState(false);
  const grantCount = model.granted_project_ids.length;
  return (
    <div className={styles.modelRow}>
      <span className={styles.modelCopy}><strong>{model.name}</strong><small>{model.model_id} · {contextLabel(model.context_window, t)}</small></span>
      <span className={styles.capabilities} aria-label={t('cluster.models.capabilitiesAria')}>
        {model.capabilities.reasoning && <span><Lightning size={11} aria-hidden="true" />{t('cluster.models.capReasoning')}</span>}
        {model.capabilities.tools && <span><Terminal size={11} aria-hidden="true" />{t('cluster.models.capTools')}</span>}
        {model.capabilities.image && <span>{t('cluster.models.capImage')}</span>}
      </span>
      <span className={styles.grantCount} data-testid={`grant-count-${model.id}`}><b>{grantCount}</b><small>{t('cluster.models.projectGrants', { count: grantCount })}</small></span>
      <Button size="sm" variant="ghost" onClick={() => setGrantsOpen(true)}>{t('cluster.models.manageGrants')}</Button>
      <GrantDialog model={model} open={grantsOpen} onClose={() => setGrantsOpen(false)} />
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

function CustomModelDialog({ provider, open, onClose }: { provider: ModelProvider; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateProviderModel();
  const toast = useToast();
  const [name, setName] = useState('');
  const [modelId, setModelId] = useState('');
  const [contextWindow, setContextWindow] = useState('');
  const [capabilities, setCapabilities] = useState(EMPTY_CAPABILITIES);
  const [error, setError] = useState<string>();
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const parsedContext = contextWindow === '' ? 0 : Number(contextWindow);
    if (!name.trim() || !modelId.trim()) { setError(t('cluster.models.customValidationRequired')); return; }
    if (!Number.isFinite(parsedContext) || parsedContext < 0) { setError(t('cluster.models.customValidationContext')); return; }
    create.mutate({ providerId: provider.id, input: { name: name.trim(), model_id: modelId.trim(), context_window: parsedContext, capabilities, source: 'custom' } }, {
      onSuccess: () => { toast.push({ kind: 'success', message: t('cluster.models.customAdded') }); setName(''); setModelId(''); setContextWindow(''); setCapabilities(EMPTY_CAPABILITIES); setError(undefined); onClose(); },
      onError: (cause) => setError(errorMessage(cause, t('cluster.models.addModelError'))),
    });
  };
  return (
    <Modal open={open} onClose={onClose} title={t('cluster.models.customDialogTitle', { name: provider.name })} footer={<><Button variant="ghost" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => document.getElementById(`custom-model-${provider.id}`)?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))} loading={create.isPending}>{t('cluster.models.addCustomModel')}</Button></>}>
      <form id={`custom-model-${provider.id}`} className={styles.dialogForm} onSubmit={submit}>
        <TextField label={t('cluster.models.displayNameLabel')} required value={name} onChange={(event) => setName(event.target.value)} placeholder={t('cluster.models.displayNamePlaceholder')} />
        <TextField label={t('cluster.models.modelIdLabel')} required value={modelId} onChange={(event) => setModelId(event.target.value)} placeholder="coding-plan" hint={t('cluster.models.modelIdHint')} />
        <TextField label={t('cluster.models.contextWindowLabel')} type="number" min={0} value={contextWindow} onChange={(event) => setContextWindow(event.target.value)} placeholder="32000" />
        <CapabilityFields value={capabilities} onChange={setCapabilities} />
        {error && <p className={styles.formError} role="alert">{error}</p>}
      </form>
    </Modal>
  );
}

function CatalogDialog({ provider, open, onClose }: { provider: ModelProvider; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const catalog = useModelProviderCatalog(provider.id, open);
  const create = useCreateProviderModel();
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

function GrantDialog({ model, open, onClose }: { model: ProviderModel; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const projects = useProjects();
  const setGrant = useSetModelGrant();
  const toast = useToast();
  const toggle = (projectId: string, granted: boolean) => setGrant.mutate({ modelId: model.id, projectId, granted }, {
    onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, t('cluster.models.grantUpdateError')) }),
  });
  return (
    <Modal open={open} onClose={onClose} title={t('cluster.models.grantsDialogTitle', { name: model.name })} footer={<Button onClick={onClose}>{t('common.done')}</Button>}>
      <p className={styles.dialogIntro}>{t('cluster.models.grantsIntro')}</p>
      {projects.isLoading ? <LoadingBlock label={t('cluster.models.loadingProjects')} /> : (projects.data ?? []).length === 0 ? <p className={styles.dialogIntro}>{t('cluster.models.noProjects')}</p> : (
        <div className={styles.grantList}>{(projects.data ?? []).map((project) => {
          const granted = model.granted_project_ids.includes(project.id);
          return <label key={project.id}><input type="checkbox" aria-label={project.name} checked={granted} disabled={setGrant.isPending} onChange={(event) => toggle(project.id, event.target.checked)} /><span><strong>{project.name}</strong><small>{t('cluster.models.servicesCount', { count: project.services?.length ?? 0 })}</small></span></label>;
        })}</div>
      )}
    </Modal>
  );
}

function ProviderDialog({ provider, open, onClose }: { provider: ModelProvider | null; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateModelProvider();
  const update = useUpdateModelProvider();
  const toast = useToast();
  const [name, setName] = useState('');
  const [kind, setKind] = useState('openai');
  const [baseUrl, setBaseUrl] = useState('');
  const [authType, setAuthType] = useState<ModelProviderAuthType>('api_key');
  const [apiKey, setApiKey] = useState('');
  const [catalogMode, setCatalogMode] = useState<ModelProviderCatalogMode>('auto');
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!open) return;
    setName(provider?.name ?? '');
    setKind(provider?.kind ?? 'openai');
    setBaseUrl(provider?.base_url ?? '');
    setAuthType(provider?.auth_type ?? 'api_key');
    setApiKey('');
    setCatalogMode(provider?.catalog_mode ?? 'auto');
    setError(undefined);
  }, [open, provider]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim() || !kind.trim() || !baseUrl.trim()) { setError(t('cluster.models.providerValidationRequired')); return; }
    if (!provider && authType === 'api_key' && !apiKey) { setError(t('cluster.models.providerValidationKey')); return; }
    if (provider) {
      const input: UpdateModelProviderInput = { name: name.trim(), kind: kind.trim(), base_url: baseUrl.trim(), auth_type: authType, catalog_mode: catalogMode };
      if (apiKey !== '') input.api_key = apiKey;
      update.mutate({ id: provider.id, input }, {
        onSuccess: () => { toast.push({ kind: 'success', message: t('cluster.models.providerSaved') }); onClose(); },
        onError: (cause) => setError(errorMessage(cause, t('cluster.models.saveProviderError'))),
      });
    } else {
      const input: CreateModelProviderInput = { name: name.trim(), kind: kind.trim(), base_url: baseUrl.trim(), auth_type: authType, api_key: authType === 'api_key' ? apiKey : '', catalog_mode: catalogMode };
      create.mutate(input, {
        onSuccess: () => { toast.push({ kind: 'success', message: t('cluster.models.providerAdded') }); onClose(); },
        onError: (cause) => setError(errorMessage(cause, t('cluster.models.addProviderError'))),
      });
    }
  };
  const formId = provider ? `provider-edit-${provider.id}` : 'provider-add';
  return (
    <Modal open={open} onClose={onClose} title={provider ? t('cluster.models.editProviderTitle', { name: provider.name }) : t('cluster.models.addProviderTitle')} footer={<><Button variant="ghost" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" loading={create.isPending || update.isPending} onClick={() => document.getElementById(formId)?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))}>{provider ? t('cluster.models.saveProvider') : t('cluster.models.addProvider')}</Button></>}>
      <form id={formId} className={styles.dialogForm} onSubmit={submit}>
        <TextField label={t('cluster.models.providerNameLabel')} required value={name} onChange={(event) => setName(event.target.value)} placeholder={t('cluster.models.providerNamePlaceholder')} />
        <TextField label={t('cluster.models.providerTypeLabel')} required value={kind} onChange={(event) => setKind(event.target.value.toLowerCase())} placeholder="openai" hint={t('cluster.models.providerTypeHint')} />
        <TextField label={t('cluster.models.baseUrlLabel')} required value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://api.example.com/v1" />
        <SelectField label={t('cluster.models.authLabel')} value={authType} onChange={(value) => setAuthType(value as ModelProviderAuthType)} options={[{ value: 'api_key', label: t('cluster.models.authApiKey') }, { value: 'service_identity', label: t('cluster.models.authServiceIdentityOption') }, { value: 'none', label: t('cluster.models.authNone') }]} />
        {authType === 'api_key' && <TextField label={provider ? t('cluster.models.rotateApiKeyLabel') : t('cluster.models.authApiKey')} type="password" autoComplete="off" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={provider?.api_key_set ? t('cluster.models.apiKeyPlaceholderKeep') : t('cluster.models.apiKeyPlaceholderEnter')} hint={t('cluster.models.apiKeyHint')} />}
        <SelectField label={t('cluster.models.catalogLabel')} value={catalogMode} onChange={(value) => setCatalogMode(value as ModelProviderCatalogMode)} options={[{ value: 'auto', label: t('cluster.models.catalogAuto') }, { value: 'disabled', label: t('cluster.models.catalogDisabled') }]} />
        {error && <p className={styles.formError} role="alert">{error}</p>}
      </form>
    </Modal>
  );
}
