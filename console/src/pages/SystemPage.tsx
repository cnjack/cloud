/*
 * SystemPage — the cluster-admin's home ("Cluster" view). Renders the read-only
 * GET /api/v1/system snapshot as clean info cards: Capacity (with a simple bar),
 * Guardrails, Provider, Runner, Version — plus the ONE admin mutation this
 * console has: the Model card (Feature A), where a cluster admin sets the LLM
 * the agent uses. Everything else stays read-only (kubectl remains that path).
 *
 * Role gating: the route itself is presentation-gated to cluster-admin (the nav
 * link is hidden for project-admin, and this page shows a plain notice if a
 * project-admin lands on /system directly). This is NOT authorization — the
 * orchestrator has one console token; real RBAC is on the roadmap (see 11-api.md
 * § "System / admin").
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useSystem,
  useModels,
  useCreateModel,
  useUpdateModel,
  useDeleteModel,
  useSetModelGrant,
  useProjects,
} from '../api/queries';
import { useRole } from '../api/ApiProvider';
import { ApiError } from '../api/client';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { EmptyState } from '../components/EmptyState';
import { useToast } from '../components/Toast';
import type { Model, Project, SystemInfo } from '../api/types';
import styles from './SystemPage.module.css';

export function SystemPage() {
  const { t } = useTranslation();
  const role = useRole();
  const isClusterAdmin = role === 'cluster-admin';
  // Gate the fetch to cluster-admins so a project-admin never issues the request.
  const system = useSystem(isClusterAdmin);

  // Presentation-only gate: project-admins don't get the Cluster view. This is a
  // UI affordance, not authz — documented as such.
  if (!isClusterAdmin) {
    return (
      <div className={styles.page}>
        <EmptyState
          data-testid="system-forbidden"
          title={t('cluster.system.forbiddenTitle')}
          description={t('cluster.system.forbiddenDescription')}
        />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>{t('cluster.system.title')}</h1>
          <p className={styles.subtitle}>
            {t('cluster.system.subtitle')}
          </p>
        </div>
      </header>

      {system.isLoading ? (
        <LoadingBlock label={t('cluster.system.loadingSnapshot')} />
      ) : system.isError ? (
        <ErrorBlock
          error={system.error}
          onRetry={() => system.refetch()}
          title={t('cluster.system.snapshotError')}
        />
      ) : !system.data ? (
        <EmptyState
          title={t('cluster.system.noSnapshotTitle')}
          description={t('cluster.system.noSnapshotDescription')}
        />
      ) : (
        <SystemCards data={system.data} />
      )}
    </div>
  );
}

function SystemCards({ data }: { data: SystemInfo }) {
  const { t } = useTranslation();
  const { capacity, guardrails, provider, runner, version, namespace, launcher } =
    data;
  const unlimited = capacity.max_concurrent_runs <= 0;
  const active = capacity.running + capacity.scheduling;
  const pct = unlimited
    ? 0
    : Math.min(100, Math.round((active / capacity.max_concurrent_runs) * 100));

  return (
    <div className={styles.grid} data-testid="system-cards">
      {/* Model (Feature A) — configured status + admin form. */}
      <ModelCard />

      {/* Capacity */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>{t('cluster.system.capacityTitle')}</h2>
          <span className={styles.cardHint}>
            {unlimited
              ? t('cluster.system.unlimitedConcurrency')
              : t('cluster.system.activeMax', { active, max: capacity.max_concurrent_runs })}
          </span>
        </div>
        {!unlimited && (
          <div
            className={styles.bar}
            role="progressbar"
            aria-valuenow={active}
            aria-valuemin={0}
            aria-valuemax={capacity.max_concurrent_runs}
            aria-label={t('cluster.system.capacityBarAria')}
          >
            <span className={styles.barFill} style={{ width: `${pct}%` }} />
          </div>
        )}
        <dl className={styles.stats}>
          <Stat label={t('cluster.system.statRunning')} value={capacity.running} />
          <Stat label={t('cluster.system.statScheduling')} value={capacity.scheduling} />
          <Stat label={t('cluster.system.statQueued')} value={capacity.queued} />
          <Stat
            label={t('cluster.system.statMaxConcurrent')}
            value={unlimited ? '∞' : capacity.max_concurrent_runs}
          />
        </dl>
      </Card>

      {/* Guardrails */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>{t('cluster.system.guardrailsTitle')}</h2>
        </div>
        <dl className={styles.rows}>
          <Row label={t('cluster.system.runTimeoutLabel')} value={formatSeconds(guardrails.run_timeout_seconds)} />
          <Row label={t('cluster.system.jobTtlLabel')} value={formatSeconds(guardrails.job_ttl_seconds)} />
        </dl>
      </Card>

      {/* Provider */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>{t('cluster.system.providerTitle')}</h2>
          <span
            className={styles.pill}
            data-on={provider.gitea_enabled || undefined}
            data-testid="provider-status"
          >
            {provider.gitea_enabled ? t('cluster.system.giteaEnabled') : t('cluster.system.giteaDisabled')}
          </span>
        </div>
        <dl className={styles.rows}>
          <Row label={t('cluster.system.draftPrsLabel')} value={provider.gitea_enabled ? t('cluster.system.on') : t('cluster.system.offDiffOnly')} />
          <Row
            label={t('cluster.system.giteaUrlLabel')}
            value={provider.gitea_url || '—'}
            mono
          />
          <Row
            label={t('cluster.system.allowedHostsLabel')}
            value={
              provider.allowed_git_hosts && provider.allowed_git_hosts.length > 0
                ? provider.allowed_git_hosts.join(', ')
                : t('cluster.system.unrestrictedAnyHost')
            }
            mono
          />
        </dl>
        <p className={styles.cardHint} data-testid="allowed-git-hosts-hint">
          {t('cluster.system.allowedHostsHint')}
        </p>
      </Card>

      {/* Runner */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>{t('cluster.system.runnerTitle')}</h2>
        </div>
        <dl className={styles.rows}>
          <Row label={t('cluster.system.imageLabel')} value={runner.image || '—'} mono />
          <Row label={t('cluster.system.namespaceLabel')} value={namespace || '—'} mono />
          <Row label={t('cluster.system.launcherLabel')} value={launcher || '—'} mono />
          <Row
            label={t('cluster.system.persistentWorkspaceLabel')}
            value={runner.persistent_workspace ? t('cluster.system.on') : t('cluster.system.off')}
          />
        </dl>
      </Card>

      {/* Auth (M2/M4) */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>{t('cluster.system.authTitle')}</h2>
          <span
            className={styles.pill}
            data-on={(data.auth?.providers.length ?? 0) > 0 || undefined}
            data-testid="auth-status"
          >
            {(data.auth?.providers.length ?? 0) > 0
              ? t('cluster.system.providerCount', { count: data.auth!.providers.length })
              : t('cluster.system.tokenOnly')}
          </span>
        </div>
        <dl className={styles.rows}>
          <Row
            label={t('cluster.system.oauthProvidersLabel')}
            value={
              data.auth && data.auth.providers.length > 0
                ? data.auth.providers.join(', ')
                : t('cluster.system.noneConsoleToken')
            }
            mono
          />
          <Row label={t('cluster.system.usersLabel')} value={String(data.auth?.users_count ?? 0)} mono />
        </dl>
      </Card>

      {/* Version */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>{t('cluster.system.versionTitle')}</h2>
        </div>
        <dl className={styles.rows}>
          <Row label={t('cluster.system.orchestratorLabel')} value={version.version || '—'} mono />
          <Row label={t('cluster.system.commitLabel')} value={version.commit || '—'} mono />
        </dl>
      </Card>
    </div>
  );
}

/**
 * ModelCard — the cluster model catalog (D21). Lists the registered models and,
 * since the Cluster page is cluster-admin only, exposes add / edit / delete and
 * per-model project authorization (grants). The plaintext API key is never
 * displayed — only whether one is set. Feedback goes through the app-wide toast.
 */
function ModelCard() {
  const { t } = useTranslation();
  const models = useModels(true);
  const projects = useProjects();

  return (
    <Card className={[styles.card, styles.modelCard].join(' ')} data-testid="model-card">
      <div className={styles.cardHead}>
        <h2 className={styles.cardTitle}>{t('cluster.system.modelCatalogTitle')}</h2>
        {models.data && (
          <span className={styles.pill} data-on={models.data.length > 0 || undefined} data-testid="model-status">
            {models.data.length > 0
              ? t('cluster.system.modelCount', { count: models.data.length })
              : t('cluster.system.noModels')}
          </span>
        )}
      </div>

      <p className={styles.modelHint} data-testid="model-hint">
        {t('cluster.system.modelCatalogHint')}
      </p>

      {models.isLoading ? (
        <LoadingBlock label={t('cluster.system.loadingModelCatalog')} />
      ) : models.isError ? (
        <ErrorBlock error={models.error} onRetry={() => models.refetch()} title={t('cluster.system.modelCatalogError')} />
      ) : (
        <div data-testid="model-list">
          {(models.data ?? []).map((m) => (
            <ModelRow key={m.id} model={m} projects={projects.data ?? []} />
          ))}
          {(models.data ?? []).length === 0 && (
            <p className={styles.modelHint}>{t('cluster.system.noModelsYet')}</p>
          )}
          <ModelAddForm />
        </div>
      )}
    </Card>
  );
}

/**
 * ModelRow — one catalog model: its name/model id, key badge, an inline editor
 * (base URL, model, rotate key), the delete action, and the per-project grants
 * checklist. The API key input is always blank (never returned by the server).
 * The three key states are reachable explicitly (D21 api_key semantics): leaving
 * it blank OMITS the key (unchanged); typing a value ROTATES it; ticking "Clear
 * key" sends api_key:"" to make the endpoint keyless.
 */
function ModelRow({ model, projects }: { model: Model; projects: Project[] }) {
  const { t } = useTranslation();
  const toast = useToast();
  const update = useUpdateModel();
  const del = useDeleteModel();
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(model.name);
  const [baseUrl, setBaseUrl] = useState(model.base_url);
  const [modelName, setModelName] = useState(model.model_name);
  const [apiKey, setApiKey] = useState('');
  const [clearKey, setClearKey] = useState(false);

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    const input: { name: string; base_url: string; model_name: string; api_key?: string } = {
      name: name.trim(),
      base_url: baseUrl.trim(),
      model_name: modelName.trim(),
    };
    // Key: explicit clear (api_key:"") wins; otherwise rotate on a typed value;
    // otherwise omit (leave unchanged).
    if (clearKey) input.api_key = '';
    else if (apiKey !== '') input.api_key = apiKey;
    update.mutate(
      { id: model.id, input },
      {
        onSuccess: () => {
          setApiKey('');
          setClearKey(false);
          setEditing(false);
          toast.push({ kind: 'success', message: t('cluster.system.modelSaved') });
        },
        onError: (err) =>
          toast.push({ kind: 'error', message: err instanceof ApiError ? err.message : t('cluster.system.modelSaveError') }),
      },
    );
  };

  const remove = () => {
    del.mutate(model.id, {
      onSuccess: () => toast.push({ kind: 'success', message: t('cluster.system.modelRemoved') }),
      onError: (err) =>
        toast.push({ kind: 'error', message: err instanceof ApiError ? err.message : t('cluster.system.modelRemoveError') }),
    });
  };

  return (
    <div className={styles.kanbanLinkRow} data-testid={`model-row-${model.id}`}>
      <div className={styles.kanbanLinkMeta} style={{ width: '100%' }}>
        <div className={styles.kanbanLinkTitle}>
          {model.name}
          <span className={styles.pill} data-on={model.api_key_set || undefined} style={{ marginLeft: 8 }}>
            {model.api_key_set ? t('cluster.system.keySet') : t('cluster.system.keyless')}
          </span>
        </div>
        <div className={styles.kanbanLinkSub}>{model.model_name}</div>

        {editing ? (
          <form className={styles.modelForm} onSubmit={save} noValidate>
            <TextField label={t('cluster.system.nameLabel')} value={name} onChange={(e) => setName(e.target.value)} data-testid={`model-edit-name-${model.id}`} autoComplete="off" required />
            <TextField label={t('cluster.system.baseUrlLabel')} value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} data-testid={`model-edit-base-${model.id}`} autoComplete="off" required />
            <TextField label={t('cluster.system.modelProviderLabel')} value={modelName} onChange={(e) => setModelName(e.target.value)} data-testid={`model-edit-model-${model.id}`} autoComplete="off" required />
            <TextField
              label={t('cluster.system.apiKeyLabel')}
              type="password"
              placeholder={
                clearKey
                  ? t('cluster.system.apiKeyPlaceholderClear')
                  : model.api_key_set
                    ? t('cluster.system.apiKeyPlaceholderRotate')
                    : t('cluster.system.apiKeyPlaceholderNew')
              }
              value={clearKey ? '' : apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              disabled={clearKey}
              data-testid={`model-edit-key-${model.id}`}
              autoComplete="off"
              hint={t('cluster.system.apiKeyHint')}
            />
            {model.api_key_set && (
              <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <input
                  type="checkbox"
                  checked={clearKey}
                  onChange={(e) => setClearKey(e.target.checked)}
                  data-testid={`model-edit-clear-key-${model.id}`}
                />
                {t('cluster.system.clearKeyLabel')}
              </label>
            )}
            <div className={styles.modelActions}>
              <Button type="submit" variant="primary" loading={update.isPending} data-testid={`model-save-${model.id}`}>
                {t('common.save')}
              </Button>
              <Button type="button" variant="secondary" onClick={() => setEditing(false)}>
                {t('common.cancel')}
              </Button>
            </div>
          </form>
        ) : (
          <GrantsEditor model={model} projects={projects} />
        )}
      </div>
      {!editing && (
        <div style={{ display: 'flex', gap: 8 }}>
          <Button type="button" variant="secondary" onClick={() => setEditing(true)} data-testid={`model-edit-${model.id}`}>
            {t('common.edit')}
          </Button>
          <Button type="button" variant="secondary" onClick={remove} disabled={del.isPending} data-testid={`model-delete-${model.id}`}>
            {t('common.remove')}
          </Button>
        </div>
      )}
    </div>
  );
}

/**
 * GrantsEditor — per-model project authorization: a checkbox per project toggles
 * the grant (PUT/DELETE). The granted set drives which projects can run on this
 * model.
 */
function GrantsEditor({ model, projects }: { model: Model; projects: Project[] }) {
  const { t } = useTranslation();
  const toast = useToast();
  const setGrant = useSetModelGrant();
  const granted = new Set(model.granted_project_ids);

  if (projects.length === 0) {
    return <p className={styles.modelHint}>{t('cluster.system.noProjectsToAuthorize')}</p>;
  }
  return (
    <div data-testid={`model-grants-${model.id}`} style={{ marginTop: 8 }}>
      <div className={styles.fieldLabel}>{t('cluster.system.authorizedProjects')}</div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12, marginTop: 4 }}>
        {projects.map((p) => (
          <label key={p.id} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input
              type="checkbox"
              checked={granted.has(p.id)}
              disabled={setGrant.isPending}
              data-testid={`model-grant-${model.id}-${p.id}`}
              onChange={(e) =>
                setGrant.mutate(
                  { modelId: model.id, projectId: p.id, granted: e.target.checked },
                  {
                    onError: (err) =>
                      toast.push({
                        kind: 'error',
                        message: err instanceof ApiError ? err.message : t('cluster.system.grantUpdateError'),
                      }),
                  },
                )
              }
            />
            {p.name}
          </label>
        ))}
      </div>
    </div>
  );
}

/** ModelAddForm — the inline "register a model" form (name, base URL, model, key). */
function ModelAddForm() {
  const { t } = useTranslation();
  const toast = useToast();
  const create = useCreateModel();
  const [name, setName] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [modelName, setModelName] = useState('');
  const [apiKey, setApiKey] = useState('');

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name: name.trim(), base_url: baseUrl.trim(), model_name: modelName.trim(), api_key: apiKey },
      {
        onSuccess: () => {
          setName('');
          setBaseUrl('');
          setModelName('');
          setApiKey('');
          toast.push({ kind: 'success', message: t('cluster.system.modelAdded') });
        },
        onError: (err) =>
          toast.push({ kind: 'error', message: err instanceof ApiError ? err.message : t('cluster.system.modelAddError') }),
      },
    );
  };

  return (
    <form className={styles.modelForm} onSubmit={submit} noValidate data-testid="model-add-form">
      <TextField label={t('cluster.system.nameLabel')} placeholder="GPT-4o" value={name} onChange={(e) => setName(e.target.value)} data-testid="model-add-name" autoComplete="off" required />
      <TextField label={t('cluster.system.baseUrlLabel')} placeholder="https://api.openai.com/v1" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} data-testid="model-add-base" autoComplete="off" required />
      <TextField label={t('cluster.system.modelProviderLabel')} placeholder="openai/gpt-4o" value={modelName} onChange={(e) => setModelName(e.target.value)} data-testid="model-add-model" autoComplete="off" required />
      <TextField
        label={t('cluster.system.apiKeyLabel')}
        type="password"
        placeholder={t('cluster.system.apiKeyPlaceholderAdd')}
        value={apiKey}
        onChange={(e) => setApiKey(e.target.value)}
        data-testid="model-add-key"
        autoComplete="off"
        hint={t('cluster.system.apiKeyHint')}
      />
      <div className={styles.modelActions}>
        <Button type="submit" variant="primary" loading={create.isPending} data-testid="model-add-submit">
          {t('cluster.system.addModel')}
        </Button>
      </div>
    </form>
  );
}

function Stat({ label, value }: { label: string; value: number | string }) {
  return (
    <div className={styles.stat}>
      <dt className={styles.statLabel}>{label}</dt>
      <dd className={styles.statValue}>{value}</dd>
    </div>
  );
}

function Row({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className={styles.row}>
      <dt className={styles.rowLabel}>{label}</dt>
      <dd className={[styles.rowValue, mono && styles.mono].filter(Boolean).join(' ')}>
        {value}
      </dd>
    </div>
  );
}

/** Human-friendly seconds → "30m" / "1h" / "1h 30m" / "45s". */
function formatSeconds(total: number): string {
  if (!Number.isFinite(total) || total <= 0) return '—';
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const parts: string[] = [];
  if (h) parts.push(`${h}h`);
  if (m) parts.push(`${m}m`);
  if (s && !h) parts.push(`${s}s`);
  return `${parts.join(' ')} (${total.toLocaleString()}s)`;
}
