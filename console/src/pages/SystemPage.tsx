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
import {
  useSystem,
  useModels,
  useCreateModel,
  useUpdateModel,
  useDeleteModel,
  useSetModelGrant,
  useProjects,
  useKanbanLinks,
  useKanbanConfig,
  useUpdateKanbanConfig,
  useDeleteKanbanConfig,
  useStartKanbanConnect,
  useKanbanConnectStatus,
} from '../api/queries';
import { useRole } from '../api/ApiProvider';
import { ApiError } from '../api/client';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { KanbanConnectFlow, expiryLabel } from '../components/KanbanConnect';
import { TextField } from '../components/Field';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { EmptyState } from '../components/EmptyState';
import { useToast } from '../components/Toast';
import type {
  KanbanClusterConfig,
  Model,
  Project,
  SystemInfo,
  UpdateKanbanConfigInput,
} from '../api/types';
import styles from './SystemPage.module.css';

export function SystemPage() {
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
          title="Cluster view is for cluster admins"
          description="Your role manages projects only. Ask a cluster administrator for capacity, guardrail, and provider details."
        />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Cluster</h1>
          <p className={styles.subtitle}>
            Snapshot of this orchestrator: capacity, guardrails, and wiring. The
            model is configured here; other changes are made out-of-band (env /
            kubectl).
          </p>
        </div>
      </header>

      {system.isLoading ? (
        <LoadingBlock label="Loading cluster snapshot…" />
      ) : system.isError ? (
        <ErrorBlock
          error={system.error}
          onRetry={() => system.refetch()}
          title="Couldn't load the cluster snapshot"
        />
      ) : !system.data ? (
        <EmptyState
          title="No snapshot"
          description="The orchestrator returned no system information."
        />
      ) : (
        <SystemCards data={system.data} />
      )}
    </div>
  );
}

function SystemCards({ data }: { data: SystemInfo }) {
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

      {/* Kanban (Feature E / D27) — editable cluster jtype config + link wiring. */}
      <KanbanCard systemReason={data.kanban?.reason} />

      {/* Capacity */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>Capacity</h2>
          <span className={styles.cardHint}>
            {unlimited
              ? 'unlimited concurrency'
              : `${active} active / ${capacity.max_concurrent_runs} max`}
          </span>
        </div>
        {!unlimited && (
          <div
            className={styles.bar}
            role="progressbar"
            aria-valuenow={active}
            aria-valuemin={0}
            aria-valuemax={capacity.max_concurrent_runs}
            aria-label="Active runs against max concurrency"
          >
            <span className={styles.barFill} style={{ width: `${pct}%` }} />
          </div>
        )}
        <dl className={styles.stats}>
          <Stat label="Running" value={capacity.running} />
          <Stat label="Scheduling" value={capacity.scheduling} />
          <Stat label="Queued" value={capacity.queued} />
          <Stat
            label="Max concurrent"
            value={unlimited ? '∞' : capacity.max_concurrent_runs}
          />
        </dl>
      </Card>

      {/* Guardrails */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>Guardrails</h2>
        </div>
        <dl className={styles.rows}>
          <Row label="Run timeout" value={formatSeconds(guardrails.run_timeout_seconds)} />
          <Row label="Job TTL" value={formatSeconds(guardrails.job_ttl_seconds)} />
        </dl>
      </Card>

      {/* Provider */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>Provider</h2>
          <span
            className={styles.pill}
            data-on={provider.gitea_enabled || undefined}
            data-testid="provider-status"
          >
            {provider.gitea_enabled ? 'Gitea enabled' : 'Gitea disabled'}
          </span>
        </div>
        <dl className={styles.rows}>
          <Row label="Draft PRs" value={provider.gitea_enabled ? 'On' : 'Off (diff-only)'} />
          <Row
            label="Gitea URL"
            value={provider.gitea_url || '—'}
            mono
          />
          <Row
            label="Allowed git hosts"
            value={
              provider.allowed_git_hosts && provider.allowed_git_hosts.length > 0
                ? provider.allowed_git_hosts.join(', ')
                : 'unrestricted (any host)'
            }
            mono
          />
        </dl>
        <p className={styles.cardHint} data-testid="allowed-git-hosts-hint">
          Project integrations may only target these hosts (ALLOWED_GIT_HOSTS). Empty = any host.
        </p>
      </Card>

      {/* Runner */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>Runner</h2>
        </div>
        <dl className={styles.rows}>
          <Row label="Image" value={runner.image || '—'} mono />
          <Row label="Namespace" value={namespace || '—'} mono />
          <Row label="Launcher" value={launcher || '—'} mono />
          <Row
            label="Persistent workspace"
            value={runner.persistent_workspace ? 'On' : 'Off'}
          />
        </dl>
      </Card>

      {/* Auth (M2/M4) */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>Auth</h2>
          <span
            className={styles.pill}
            data-on={(data.auth?.providers.length ?? 0) > 0 || undefined}
            data-testid="auth-status"
          >
            {(data.auth?.providers.length ?? 0) > 0
              ? `${data.auth!.providers.length} provider${data.auth!.providers.length === 1 ? '' : 's'}`
              : 'token only'}
          </span>
        </div>
        <dl className={styles.rows}>
          <Row
            label="OAuth providers"
            value={
              data.auth && data.auth.providers.length > 0
                ? data.auth.providers.join(', ')
                : 'none (console token only)'
            }
            mono
          />
          <Row label="Users" value={String(data.auth?.users_count ?? 0)} mono />
        </dl>
      </Card>

      {/* Version */}
      <Card className={styles.card}>
        <div className={styles.cardHead}>
          <h2 className={styles.cardTitle}>Version</h2>
        </div>
        <dl className={styles.rows}>
          <Row label="Orchestrator" value={version.version || '—'} mono />
          <Row label="Commit" value={version.commit || '—'} mono />
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
  const models = useModels(true);
  const projects = useProjects();

  return (
    <Card className={[styles.card, styles.modelCard].join(' ')} data-testid="model-card">
      <div className={styles.cardHead}>
        <h2 className={styles.cardTitle}>Model catalog</h2>
        {models.data && (
          <span className={styles.pill} data-on={models.data.length > 0 || undefined} data-testid="model-status">
            {models.data.length > 0
              ? `${models.data.length} model${models.data.length === 1 ? '' : 's'}`
              : 'No models'}
          </span>
        )}
      </div>

      <p className={styles.modelHint} data-testid="model-hint">
        Register OpenAI-compatible endpoints, then authorize them per project.
        Runs are blocked for a project until it has at least one granted model.
      </p>

      {models.isLoading ? (
        <LoadingBlock label="Loading model catalog…" />
      ) : models.isError ? (
        <ErrorBlock error={models.error} onRetry={() => models.refetch()} title="Couldn't load the model catalog" />
      ) : (
        <div data-testid="model-list">
          {(models.data ?? []).map((m) => (
            <ModelRow key={m.id} model={m} projects={projects.data ?? []} />
          ))}
          {(models.data ?? []).length === 0 && (
            <p className={styles.modelHint}>No models yet — add one below.</p>
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
          toast.push({ kind: 'success', message: 'Model saved.' });
        },
        onError: (err) =>
          toast.push({ kind: 'error', message: err instanceof ApiError ? err.message : 'Could not save the model.' }),
      },
    );
  };

  const remove = () => {
    del.mutate(model.id, {
      onSuccess: () => toast.push({ kind: 'success', message: 'Model removed.' }),
      onError: (err) =>
        toast.push({ kind: 'error', message: err instanceof ApiError ? err.message : 'Could not remove the model.' }),
    });
  };

  return (
    <div className={styles.kanbanLinkRow} data-testid={`model-row-${model.id}`}>
      <div className={styles.kanbanLinkMeta} style={{ width: '100%' }}>
        <div className={styles.kanbanLinkTitle}>
          {model.name}
          <span className={styles.pill} data-on={model.api_key_set || undefined} style={{ marginLeft: 8 }}>
            {model.api_key_set ? 'key set' : 'keyless'}
          </span>
        </div>
        <div className={styles.kanbanLinkSub}>{model.model_name}</div>

        {editing ? (
          <form className={styles.modelForm} onSubmit={save} noValidate>
            <TextField label="Name" value={name} onChange={(e) => setName(e.target.value)} data-testid={`model-edit-name-${model.id}`} autoComplete="off" required />
            <TextField label="Base URL" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} data-testid={`model-edit-base-${model.id}`} autoComplete="off" required />
            <TextField label="Model (provider/model)" value={modelName} onChange={(e) => setModelName(e.target.value)} data-testid={`model-edit-model-${model.id}`} autoComplete="off" required />
            <TextField
              label="API key"
              type="password"
              placeholder={
                clearKey
                  ? 'will be cleared (keyless)'
                  : model.api_key_set
                    ? '•••••••• (blank = unchanged; type to rotate)'
                    : 'sk-…  (blank for keyless)'
              }
              value={clearKey ? '' : apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              disabled={clearKey}
              data-testid={`model-edit-key-${model.id}`}
              autoComplete="off"
              hint="Stored encrypted. Never displayed after saving."
            />
            {model.api_key_set && (
              <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <input
                  type="checkbox"
                  checked={clearKey}
                  onChange={(e) => setClearKey(e.target.checked)}
                  data-testid={`model-edit-clear-key-${model.id}`}
                />
                Clear key (make this endpoint keyless)
              </label>
            )}
            <div className={styles.modelActions}>
              <Button type="submit" variant="primary" loading={update.isPending} data-testid={`model-save-${model.id}`}>
                Save
              </Button>
              <Button type="button" variant="secondary" onClick={() => setEditing(false)}>
                Cancel
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
            Edit
          </Button>
          <Button type="button" variant="secondary" onClick={remove} disabled={del.isPending} data-testid={`model-delete-${model.id}`}>
            Remove
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
  const toast = useToast();
  const setGrant = useSetModelGrant();
  const granted = new Set(model.granted_project_ids);

  if (projects.length === 0) {
    return <p className={styles.modelHint}>No projects to authorize yet.</p>;
  }
  return (
    <div data-testid={`model-grants-${model.id}`} style={{ marginTop: 8 }}>
      <div className={styles.fieldLabel}>Authorized projects</div>
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
                        message: err instanceof ApiError ? err.message : 'Could not update the grant.',
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
          toast.push({ kind: 'success', message: 'Model added.' });
        },
        onError: (err) =>
          toast.push({ kind: 'error', message: err instanceof ApiError ? err.message : 'Could not add the model.' }),
      },
    );
  };

  return (
    <form className={styles.modelForm} onSubmit={submit} noValidate data-testid="model-add-form">
      <TextField label="Name" placeholder="GPT-4o" value={name} onChange={(e) => setName(e.target.value)} data-testid="model-add-name" autoComplete="off" required />
      <TextField label="Base URL" placeholder="https://api.openai.com/v1" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} data-testid="model-add-base" autoComplete="off" required />
      <TextField label="Model (provider/model)" placeholder="openai/gpt-4o" value={modelName} onChange={(e) => setModelName(e.target.value)} data-testid="model-add-model" autoComplete="off" required />
      <TextField
        label="API key"
        type="password"
        placeholder="sk-…  (blank for keyless endpoints)"
        value={apiKey}
        onChange={(e) => setApiKey(e.target.value)}
        data-testid="model-add-key"
        autoComplete="off"
        hint="Stored encrypted. Never displayed after saving."
      />
      <div className={styles.modelActions}>
        <Button type="submit" variant="primary" loading={create.isPending} data-testid="model-add-submit">
          Add model
        </Button>
      </div>
    </form>
  );
}

/**
 * KanbanCard — the jtype kanban integration (Feature E / D27). The cluster jtype
 * config (base_url + optional cluster fallback token) is now SETTABLE here by a
 * cluster admin, not only via orchestrator env: the card shows the effective
 * config + its SOURCE (DB override / JTYPE_BASE_URL env / off) as a badge, an
 * editable form (mirroring the Model card's write-only-secret UX), and a
 * "Clear cluster config" action that drops the DB override back to env/off. The
 * cross-project link overview below stays READ-ONLY — link management is the
 * project owner's (D25, Project settings → Kanban). The token is write-only
 * (never returned) and an unconfigured integration renders a fail-visible "off"
 * state, never a silent mock.
 *
 * `systemReason` is the /system snapshot's kanban.reason (same resolver, same
 * failure); the config view's own `reason` wins when both are present. Either
 * way a BROKEN config (e.g. DB token without AUTH_TOKEN_KEY) renders a loud
 * error notice next to the badge — never a bare, unexplained "off".
 */
function KanbanCard({ systemReason }: { systemReason?: string }) {
  const config = useKanbanConfig();
  // Fail-visible (D27): why the resolver refused to produce an effective config.
  const reason = config.data?.reason || systemReason;

  return (
    <Card className={[styles.card, styles.modelCard].join(' ')} data-testid="kanban-card">
      <div className={styles.cardHead}>
        <h2 className={styles.cardTitle}>Kanban</h2>
        {config.data && (
          <span
            className={styles.pill}
            data-on={config.data.effective_enabled || undefined}
            data-err={(!config.data.effective_enabled && !!reason) || undefined}
            data-testid="kanban-status"
          >
            {config.data.source === 'db'
              ? 'DB (console)'
              : config.data.source === 'env'
                ? 'env (JTYPE_BASE_URL)'
                : 'off'}
          </span>
        )}
      </div>

      {reason && (
        <p className={styles.errorNotice} role="alert" data-testid="kanban-config-reason">
          {reason}
        </p>
      )}

      {config.isLoading ? (
        <LoadingBlock label="Loading kanban config…" />
      ) : config.isError ? (
        <ErrorBlock
          error={config.error}
          onRetry={() => config.refetch()}
          title="Couldn't load the kanban config"
        />
      ) : config.data ? (
        // Re-key on the resolved config's identity so a Save (env→db) or a Clear
        // (db→env/off) re-seeds the form fields from the fresh server state.
        // token_set is deliberately NOT part of the key (D28): a completed
        // device flow flips it false→true via invalidation, and a remount here
        // would tear down the in-flight connect panel one refetch tick after it
        // shows "Connected". token_set-driven UI (the token field's placeholder,
        // the clear-token checkbox) reads it reactively as a prop, and the save
        // handler already clears the typed token itself on success.
        <KanbanConfigEditor
          key={`${config.data.source}:${config.data.base_url}`}
          config={config.data}
        />
      ) : null}
    </Card>
  );
}

/**
 * KanbanConfigEditor — the editable cluster jtype config + the read-only link
 * overview. The token field is write-only with three explicit states (D27 /
 * mirrors ModelRow): blank OMITS it (unchanged), a value ROTATES it, ticking
 * "Clear cluster token" sends token:"" (links then rely on their own tokens).
 * "Clear cluster config" DELETEs the whole DB override behind a confirm step
 * (only offered when a DB override exists — there is nothing to clear otherwise).
 */
function KanbanConfigEditor({ config }: { config: KanbanClusterConfig }) {
  const toast = useToast();
  const update = useUpdateKanbanConfig();
  const del = useDeleteKanbanConfig();
  const links = useKanbanLinks(config.effective_enabled);
  const projects = useProjects();
  const projectName = (id: string) => projects.data?.find((p) => p.id === id)?.name ?? id;

  const [baseUrl, setBaseUrl] = useState(config.base_url);
  const [token, setToken] = useState('');
  const [clearToken, setClearToken] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);

  // D28: "Connect with jtype" device flow for the cluster fallback token. The
  // flow requires a SAVED DB base_url (config.base_url, not the unsaved input —
  // the orchestrator binds to the persisted row); until then the button is
  // disabled with a visible reason. connectId drives the poll once launched.
  const startConnect = useStartKanbanConnect();
  const [connectId, setConnectId] = useState<string | undefined>();
  const connectStatus = useKanbanConnectStatus(connectId, !!connectId);
  const launchConnect = () =>
    startConnect.mutate(undefined, { onSuccess: (s) => setConnectId(s.connect_id) });
  const resetConnect = () => {
    setConnectId(undefined);
    startConnect.reset();
  };
  // The current fallback token's expiry, when known (device-flow tokens only).
  const clusterExpiry = config.token_set ? expiryLabel(config.token_expires_at) : null;

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    const input: UpdateKanbanConfigInput = { base_url: baseUrl.trim() };
    // Token: explicit clear (token:"") wins; otherwise rotate on a typed value;
    // otherwise omit (leave the stored token unchanged). Trimmed BEFORE the
    // decision — a whitespace-only entry is a keep, never an accidental clear.
    const typedToken = token.trim();
    if (clearToken) input.token = '';
    else if (typedToken !== '') input.token = typedToken;
    update.mutate(input, {
      onSuccess: () => {
        setToken('');
        setClearToken(false);
        toast.push({ kind: 'success', message: 'Kanban config saved.' });
      },
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Could not save the kanban config.',
        }),
    });
  };

  const clearConfig = () => {
    del.mutate(undefined, {
      onSuccess: () => {
        setConfirmClear(false);
        toast.push({ kind: 'success', message: 'Cluster kanban config cleared.' });
      },
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Could not clear the kanban config.',
        }),
    });
  };

  return (
    <>
      <p className={styles.modelHint} data-testid="kanban-hint">
        {config.effective_enabled
          ? `Cards dragged into a link's trigger column dispatch an agent run; finished runs write back as a card comment (effective jtype: ${
              config.effective_base_url || '—'
            }, source: ${config.source}). Links are managed by each project owner in Project settings → Kanban.`
          : 'Set the cluster jtype base URL below to enable card-triggered runs. Each link then authorises with its own jtype token (or the cluster fallback token set here).'}
      </p>

      <form className={styles.modelForm} onSubmit={save} noValidate data-testid="kanban-config-form">
        <TextField
          label="jtype base URL"
          placeholder="http://jtype.jcloud.svc.cluster.local:13345"
          value={baseUrl}
          onChange={(e) => setBaseUrl(e.target.value)}
          data-testid="kanban-config-base"
          autoComplete="off"
          required
        />
        <TextField
          label="Cluster jtype token (fallback)"
          type="password"
          placeholder={
            clearToken
              ? 'will be cleared (links rely on their own tokens)'
              : config.token_set
                ? '•••••••• (blank = unchanged; type to rotate)'
                : 'blank = rely on each link’s own token'
          }
          value={clearToken ? '' : token}
          onChange={(e) => setToken(e.target.value)}
          disabled={clearToken}
          data-testid="kanban-config-token"
          autoComplete="off"
          hint="Stored encrypted. Never displayed after saving. A per-link token always takes precedence; this is only the fallback."
        />
        {config.token_set && (
          <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input
              type="checkbox"
              checked={clearToken}
              onChange={(e) => setClearToken(e.target.checked)}
              data-testid="kanban-config-clear-token"
            />
            Clear cluster token (links rely on their own tokens)
          </label>
        )}
        <div className={styles.modelActions}>
          <Button type="submit" variant="primary" loading={update.isPending} data-testid="kanban-config-save">
            Save
          </Button>
          {/* Offered whenever a DB ROW exists — not gated on source: a broken row
              (e.g. token without AUTH_TOKEN_KEY) resolves to source "none", and
              deleting it is exactly the way out. */}
          {(config.base_url !== '' || config.token_set) &&
            (confirmClear ? (
              <>
                <span className={styles.cardHint}>Clear the DB config?</span>
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => setConfirmClear(false)}
                  disabled={del.isPending}
                >
                  Keep
                </Button>
                <Button
                  type="button"
                  variant="danger"
                  onClick={clearConfig}
                  loading={del.isPending}
                  data-testid="kanban-config-clear-confirm"
                >
                  Clear cluster config
                </Button>
              </>
            ) : (
              <Button
                type="button"
                variant="secondary"
                onClick={() => setConfirmClear(true)}
                data-testid="kanban-config-clear"
              >
                Clear cluster config
              </Button>
            ))}
        </div>
      </form>

      {/* D28: one-click device flow for the cluster fallback token. Sits next to
          the config form; the paste field above stays as the manual fallback. */}
      <div className={styles.connectSection}>
        <div className={styles.connectHead}>
          <span className={styles.connectLabel}>Cluster fallback token</span>
          {clusterExpiry && (
            <span
              className={styles.pill}
              data-on={!clusterExpiry.startsWith('expired') || undefined}
              data-err={clusterExpiry.startsWith('expired') || undefined}
              data-testid="kanban-connect-expiry"
            >
              {clusterExpiry}
            </span>
          )}
        </div>
        <KanbanConnectFlow
          idPrefix="kanban-connect"
          disabled={config.base_url === ''}
          disabledHint="Save the jtype base URL first"
          active={!!connectId}
          starting={startConnect.isPending}
          startError={startConnect.error}
          connectStart={startConnect.data}
          status={connectStatus.data}
          statusError={connectStatus.error}
          onStart={launchConnect}
          onReset={resetConnect}
        />
      </div>

      {/* Read-only cross-project link overview (management stays with owners). */}
      {links.data && links.data.length > 0 ? (
        <div data-testid="kanban-links">
          {links.data.map((l) => (
            <div className={styles.kanbanLinkRow} key={l.id}>
              <div className={styles.kanbanLinkMeta}>
                <div className={styles.kanbanLinkTitle}>
                  <span title={`${l.workspace_id} / ${l.board_ref}`}>
                    {l.board_title || `${l.workspace_id} / ${l.board_ref}`}
                  </span>
                  <span
                    className={styles.pill}
                    data-on={l.credential_status === 'per_link' || undefined}
                    data-err={l.credential_status === 'missing' || undefined}
                    style={{ marginLeft: 8 }}
                    data-testid={`kanban-cred-${l.id}`}
                  >
                    {{
                      per_link: 'own token',
                      cluster_fallback: 'cluster token',
                      missing: 'no credential',
                    }[l.credential_status]}
                  </span>
                  {/* D29: a fail-visible board-validation pill (absent board_status
                      is a pre-D29 row, treated as validated). */}
                  {(l.board_status ?? 'ok') !== 'ok' && (
                    <span
                      className={styles.pill}
                      data-err={l.board_status === 'invalid' || undefined}
                      style={{ marginLeft: 8 }}
                      data-testid={`kanban-board-status-${l.id}`}
                    >
                      {l.board_status === 'invalid' ? 'board/columns invalid' : 'not validated'}
                    </span>
                  )}
                </div>
                <div className={styles.kanbanLinkSub}>
                  {projectName(l.project_id)} · {l.trigger_column}
                  {l.done_column ? ` → ${l.done_column}` : ''}
                </div>
              </div>
            </div>
          ))}
        </div>
      ) : (
        <p className={styles.modelHint}>No kanban links yet — project owners add them in Project settings.</p>
      )}
    </>
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
