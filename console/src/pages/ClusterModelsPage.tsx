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

function contextLabel(contextWindow: number): string {
  if (contextWindow <= 0) return 'context unknown';
  if (contextWindow >= 1000 && contextWindow % 1000 === 0) return `${contextWindow / 1000}K context`;
  return `${contextWindow.toLocaleString()} context`;
}

export function ClusterModelsPage() {
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
          eyebrow="Cluster catalog"
          title="Model providers"
          description="Connect providers once, keep credentials sealed, then grant Projects only the models they are allowed to run."
          actions={<Button variant="primary" onClick={() => setProviderEditor('new')}><Plus size={14} aria-hidden="true" />Add provider</Button>}
        />

        <section className={styles.catalog} aria-labelledby="provider-catalog-title">
          <div className={styles.security}><Lock size={14} aria-hidden="true" /><span>Provider keys are write-only. The catalog reports only whether a credential is configured, never any part of the stored value.</span></div>
          <div className={styles.toolbar}>
            <label className={styles.search}>
              <MagnifyingGlass size={14} aria-hidden="true" />
              <span className={styles.srOnly}>Filter models or providers</span>
              <input type="search" aria-label="Filter models or providers" placeholder="Filter models or providers…" value={search} onChange={(event) => setSearch(event.target.value)} />
            </label>
            <span className={styles.result}><b>{visibleModels}</b> / {totalModels} models</span>
          </div>
          <div className={styles.heading}><div><h2 id="provider-catalog-title">Configured providers</h2><p>{providers.data?.length ?? 0} providers · {totalGrants} Project grants</p></div></div>

          {providers.isLoading ? <LoadingBlock label="Loading model providers…" /> : providers.isError ? (
            <ErrorBlock error={providers.error} onRetry={() => providers.refetch()} title="Couldn't load model providers" />
          ) : filtered.length === 0 ? (
            <div className={styles.searchEmpty} role="status"><MagnifyingGlass size={24} aria-hidden="true" /><strong>{providers.data?.length ? 'No matching models or providers.' : 'No model providers configured.'}</strong><span>{providers.data?.length ? 'Try a provider name, model id, or capability.' : 'Add a provider to make models available to Projects.'}</span></div>
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
  const toast = useToast();
  const verify = useVerifyModelProvider();
  const remove = useDeleteModelProvider();
  const [customOpen, setCustomOpen] = useState(false);
  const [catalogOpen, setCatalogOpen] = useState(false);
  const authLabel = provider.auth_type === 'service_identity' ? 'service identity' : provider.api_key_set ? 'key set' : provider.auth_type === 'none' ? 'keyless' : 'key missing';
  const authTone: 'success' | 'warning' = provider.auth_type !== 'api_key' || provider.api_key_set ? 'success' : 'warning';
  const verified = provider.last_verified_at && !provider.last_verification_error;

  const testProvider = () => verify.mutate(provider.id, {
    onSuccess: (result) => toast.push({ kind: 'success', message: result.catalog_available ? `Provider verified in ${result.latency_ms} ms.` : `Provider reachable in ${result.latency_ms} ms; catalog unavailable.` }),
    onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, 'Could not verify the provider.') }),
  });

  const deleteProvider = () => {
    if (!window.confirm(`Remove ${provider.name} and all of its configured models?`)) return;
    remove.mutate(provider.id, {
      onSuccess: () => toast.push({ kind: 'success', message: 'Provider removed.' }),
      onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, 'Could not remove the provider.') }),
    });
  };

  return (
    <article className={styles.providerCard} data-testid={`provider-card-${provider.id}`}>
      <header className={styles.providerHead}>
        <ProviderIcon kind={provider.kind} name={provider.name} />
        <div className={styles.providerCopy}>
          <div className={styles.providerTitle}><h2>{provider.name}</h2>{provider.kind === 'custom' && <StatusLabel>custom</StatusLabel>}<StatusLabel tone={authTone}>{authLabel}</StatusLabel></div>
          <p><span className={styles.mono}>{provider.base_url}</span><span aria-hidden="true"> · </span>{provider.auth_type === 'service_identity' ? 'keyless workload identity' : provider.api_key_set ? 'credential configured' : provider.auth_type === 'none' ? 'no credential required' : 'credential required'}</p>
          {provider.last_verification_error && <p className={styles.providerError}>Last test: {provider.last_verification_error}</p>}
        </div>
        <div className={styles.providerActions}>
          <Button size="sm" variant="ghost" onClick={testProvider} loading={verify.isPending}>{verified && <Check size={13} aria-hidden="true" />}{verified ? 'Verified' : 'Test'}</Button>
          <button className={styles.iconButton} type="button" aria-label={`Edit ${provider.name} provider`} onClick={onEdit}><SlidersHorizontal size={16} aria-hidden="true" /></button>
          <button className={styles.iconButton} type="button" aria-label={`Remove ${provider.name} provider`} onClick={deleteProvider} disabled={remove.isPending}><Trash size={16} aria-hidden="true" /></button>
        </div>
      </header>
      <div className={styles.providerModels}>
        <div className={styles.modelBar}>
          <span><b>{provider.models.length}</b> {provider.models.length === 1 ? (provider.models[0]?.source === 'custom' ? 'custom model' : 'configured model') : 'configured models'}</span>
          <div className={styles.modelActions}>
            {provider.catalog_mode === 'disabled' ? (
              <button className={styles.textButton} type="button" disabled><MagnifyingGlass size={13} aria-hidden="true" />Catalog unavailable</button>
            ) : (
              <button className={styles.textButton} type="button" onClick={() => setCatalogOpen(true)}><MagnifyingGlass size={13} aria-hidden="true" />Browse catalog</button>
            )}
            <Button size="sm" onClick={() => setCustomOpen(true)}><Plus size={13} aria-hidden="true" />Custom model</Button>
          </div>
        </div>
        {provider.models.length > 0 && <div className={styles.modelList}>{provider.models.map((model) => <ModelRow key={model.id} model={model} />)}</div>}
        {provider.catalog_mode === 'disabled' && <p className={styles.limitNote}>This endpoint does not expose a model catalog. Models must be added explicitly; no fallback catalog is substituted.</p>}
      </div>
      <CustomModelDialog provider={provider} open={customOpen} onClose={() => setCustomOpen(false)} />
      <CatalogDialog provider={provider} open={catalogOpen} onClose={() => setCatalogOpen(false)} />
    </article>
  );
}

function ModelRow({ model }: { model: ProviderModel }) {
  const [grantsOpen, setGrantsOpen] = useState(false);
  const grantCount = model.granted_project_ids.length;
  return (
    <div className={styles.modelRow}>
      <span className={styles.modelCopy}><strong>{model.name}</strong><small>{model.model_id} · {contextLabel(model.context_window)}</small></span>
      <span className={styles.capabilities} aria-label="Model capabilities">
        {model.capabilities.reasoning && <span><Lightning size={11} aria-hidden="true" />Reasoning</span>}
        {model.capabilities.tools && <span><Terminal size={11} aria-hidden="true" />Tools</span>}
        {model.capabilities.image && <span>Image</span>}
      </span>
      <span className={styles.grantCount} data-testid={`grant-count-${model.id}`}><b>{grantCount}</b><small>Project {grantCount === 1 ? 'grant' : 'grants'}</small></span>
      <Button size="sm" variant="ghost" onClick={() => setGrantsOpen(true)}>Manage grants</Button>
      <GrantDialog model={model} open={grantsOpen} onClose={() => setGrantsOpen(false)} />
    </div>
  );
}

function CapabilityFields({ value, onChange }: { value: ModelCapabilities; onChange: (next: ModelCapabilities) => void }) {
  return (
    <fieldset className={styles.capabilityFields}>
      <legend>Capabilities</legend>
      <label><input type="checkbox" checked={value.reasoning} onChange={(event) => onChange({ ...value, reasoning: event.target.checked })} />Reasoning</label>
      <label><input type="checkbox" checked={value.tools} onChange={(event) => onChange({ ...value, tools: event.target.checked })} />Tool use</label>
      <label><input type="checkbox" checked={value.image} onChange={(event) => onChange({ ...value, image: event.target.checked })} />Image input</label>
    </fieldset>
  );
}

function CustomModelDialog({ provider, open, onClose }: { provider: ModelProvider; open: boolean; onClose: () => void }) {
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
    if (!name.trim() || !modelId.trim()) { setError('Display name and Model ID are required.'); return; }
    if (!Number.isFinite(parsedContext) || parsedContext < 0) { setError('Context window must be zero or a positive number.'); return; }
    create.mutate({ providerId: provider.id, input: { name: name.trim(), model_id: modelId.trim(), context_window: parsedContext, capabilities, source: 'custom' } }, {
      onSuccess: () => { toast.push({ kind: 'success', message: 'Custom model added.' }); setName(''); setModelId(''); setContextWindow(''); setCapabilities(EMPTY_CAPABILITIES); setError(undefined); onClose(); },
      onError: (cause) => setError(errorMessage(cause, 'Could not add the model.')),
    });
  };
  return (
    <Modal open={open} onClose={onClose} title={`Custom model · ${provider.name}`} footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" onClick={() => document.getElementById(`custom-model-${provider.id}`)?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))} loading={create.isPending}>Add custom model</Button></>}>
      <form id={`custom-model-${provider.id}`} className={styles.dialogForm} onSubmit={submit}>
        <TextField label="Display name" required value={name} onChange={(event) => setName(event.target.value)} placeholder="e.g. Coding Plan" />
        <TextField label="Model ID" required value={modelId} onChange={(event) => setModelId(event.target.value)} placeholder="coding-plan" hint="The bare upstream model id; the runtime provider prefix is added server-side." />
        <TextField label="Context window" type="number" min={0} value={contextWindow} onChange={(event) => setContextWindow(event.target.value)} placeholder="32000" />
        <CapabilityFields value={capabilities} onChange={setCapabilities} />
        {error && <p className={styles.formError} role="alert">{error}</p>}
      </form>
    </Modal>
  );
}

function CatalogDialog({ provider, open, onClose }: { provider: ModelProvider; open: boolean; onClose: () => void }) {
  const catalog = useModelProviderCatalog(provider.id, open);
  const create = useCreateProviderModel();
  const toast = useToast();
  const add = (model: CatalogModel) => create.mutate({ providerId: provider.id, input: { name: model.name || model.id, model_id: model.id, context_window: model.context_window, capabilities: model.capabilities, source: 'catalog' } }, {
    onSuccess: () => toast.push({ kind: 'success', message: `${model.name || model.id} added.` }),
    onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, 'Could not add the catalog model.') }),
  });
  return (
    <Modal open={open} onClose={onClose} title={`Catalog · ${provider.name}`}>
      {catalog.isLoading ? <LoadingBlock label="Loading provider catalog…" /> : catalog.isError ? (
        <div className={styles.catalogError}><strong>{apiErrorCode(catalog.error) === 'catalog_unavailable' ? 'Catalog unavailable' : 'Could not load catalog'}</strong><p>{errorMessage(catalog.error, 'The provider catalog could not be loaded.')}</p><Button onClick={() => catalog.refetch()}>Retry</Button></div>
      ) : (
        <ul className={styles.catalogList}>{(catalog.data ?? []).map((model) => {
          const configured = provider.models.some((item) => item.model_id === model.id);
          return <li key={model.id}><span><strong>{model.name || model.id}</strong><small>{model.id} · {contextLabel(model.context_window)}</small></span><Button size="sm" onClick={() => add(model)} disabled={configured || create.isPending}>{configured ? 'Configured' : 'Add'}</Button></li>;
        })}</ul>
      )}
    </Modal>
  );
}

function GrantDialog({ model, open, onClose }: { model: ProviderModel; open: boolean; onClose: () => void }) {
  const projects = useProjects();
  const setGrant = useSetModelGrant();
  const toast = useToast();
  const toggle = (projectId: string, granted: boolean) => setGrant.mutate({ modelId: model.id, projectId, granted }, {
    onError: (error) => toast.push({ kind: 'error', message: errorMessage(error, 'Could not update the Project grant.') }),
  });
  return (
    <Modal open={open} onClose={onClose} title={`Project grants · ${model.name}`} footer={<Button onClick={onClose}>Done</Button>}>
      <p className={styles.dialogIntro}>Projects receive permission to select this model. Provider credentials remain sealed at the Cluster boundary.</p>
      {projects.isLoading ? <LoadingBlock label="Loading Projects…" /> : (projects.data ?? []).length === 0 ? <p className={styles.dialogIntro}>No Projects exist yet.</p> : (
        <div className={styles.grantList}>{(projects.data ?? []).map((project) => {
          const granted = model.granted_project_ids.includes(project.id);
          return <label key={project.id}><input type="checkbox" aria-label={project.name} checked={granted} disabled={setGrant.isPending} onChange={(event) => toggle(project.id, event.target.checked)} /><span><strong>{project.name}</strong><small>{project.services?.length ?? 0} Services</small></span></label>;
        })}</div>
      )}
    </Modal>
  );
}

function ProviderDialog({ provider, open, onClose }: { provider: ModelProvider | null; open: boolean; onClose: () => void }) {
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
    if (!name.trim() || !kind.trim() || !baseUrl.trim()) { setError('Name, provider type, and Base URL are required.'); return; }
    if (!provider && authType === 'api_key' && !apiKey) { setError('API key is required for a new API-key provider.'); return; }
    if (provider) {
      const input: UpdateModelProviderInput = { name: name.trim(), kind: kind.trim(), base_url: baseUrl.trim(), auth_type: authType, catalog_mode: catalogMode };
      if (apiKey !== '') input.api_key = apiKey;
      update.mutate({ id: provider.id, input }, {
        onSuccess: () => { toast.push({ kind: 'success', message: 'Provider saved.' }); onClose(); },
        onError: (cause) => setError(errorMessage(cause, 'Could not save the provider.')),
      });
    } else {
      const input: CreateModelProviderInput = { name: name.trim(), kind: kind.trim(), base_url: baseUrl.trim(), auth_type: authType, api_key: authType === 'api_key' ? apiKey : '', catalog_mode: catalogMode };
      create.mutate(input, {
        onSuccess: () => { toast.push({ kind: 'success', message: 'Provider added.' }); onClose(); },
        onError: (cause) => setError(errorMessage(cause, 'Could not add the provider.')),
      });
    }
  };
  const formId = provider ? `provider-edit-${provider.id}` : 'provider-add';
  return (
    <Modal open={open} onClose={onClose} title={provider ? `Edit provider · ${provider.name}` : 'Add model provider'} footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" loading={create.isPending || update.isPending} onClick={() => document.getElementById(formId)?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))}>{provider ? 'Save provider' : 'Add provider'}</Button></>}>
      <form id={formId} className={styles.dialogForm} onSubmit={submit}>
        <TextField label="Provider name" required value={name} onChange={(event) => setName(event.target.value)} placeholder="e.g. Zhipu AI" />
        <TextField label="Provider type" required value={kind} onChange={(event) => setKind(event.target.value.toLowerCase())} placeholder="openai" hint="A lowercase runtime prefix such as openai, qwen, or zhipu." />
        <TextField label="Base URL" required value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://api.example.com/v1" />
        <SelectField label="Authentication" value={authType} onChange={(value) => setAuthType(value as ModelProviderAuthType)} options={[{ value: 'api_key', label: 'API key' }, { value: 'service_identity', label: 'Service identity' }, { value: 'none', label: 'No credential' }]} />
        {authType === 'api_key' && <TextField label={provider ? 'Rotate API key' : 'API key'} type="password" autoComplete="off" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={provider?.api_key_set ? 'Leave blank to keep current key' : 'Enter provider key'} hint="Write-only. The saved value can never be read back." />}
        <SelectField label="Model catalog" value={catalogMode} onChange={(value) => setCatalogMode(value as ModelProviderCatalogMode)} options={[{ value: 'auto', label: 'Discover from /models' }, { value: 'disabled', label: 'Unavailable — custom models only' }]} />
        {error && <p className={styles.formError} role="alert">{error}</p>}
      </form>
    </Modal>
  );
}
