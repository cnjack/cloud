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
  useModelConfig,
  useSetModelConfig,
  useClearModelConfig,
} from '../api/queries';
import { useRole } from '../api/ApiProvider';
import { ApiError } from '../api/client';
import { Card } from '../components/Card';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { EmptyState } from '../components/EmptyState';
import { useToast } from '../components/Toast';
import type { ModelConfigInfo, SystemInfo } from '../api/types';
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
        </dl>
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
 * ModelCard — the cluster LLM configuration (Feature A). Shows the effective
 * configured/source status and, since the Cluster page is cluster-admin only,
 * an inline form to set (Base URL, Model, API key) or clear it. Save/clear
 * feedback goes through the app-wide toast (ToastProvider wraps the whole app
 * in main.tsx), matching PrPanel and the settings modals. The plaintext API key
 * is never displayed — only whether one is set.
 */
function ModelCard() {
  const cfg = useModelConfig(true);
  const info = cfg.data;
  const configured = info?.configured ?? false;

  return (
    <Card className={[styles.card, styles.modelCard].join(' ')}>
      <div className={styles.cardHead}>
        <h2 className={styles.cardTitle}>Model</h2>
        {info && (
          <span
            className={styles.pill}
            data-on={configured || undefined}
            data-testid="model-status"
          >
            {configured ? `Configured · ${info.source ?? ''}` : 'Not configured'}
          </span>
        )}
      </div>

      {cfg.isLoading ? (
        <LoadingBlock label="Loading model configuration…" />
      ) : cfg.isError ? (
        <ErrorBlock
          error={cfg.error}
          onRetry={() => cfg.refetch()}
          title="Couldn't load the model configuration"
        />
      ) : info ? (
        <ModelForm info={info} />
      ) : null}
    </Card>
  );
}

/**
 * ModelForm — the admin edit form. Mounted only once the CURRENT config has
 * loaded, so the plain lazy useState initializers ARE the prefill: no effect,
 * no touched-tracking, and a background refetch can never clobber in-progress
 * edits (initializers run once, on mount). The API key is never returned by the
 * server, so it always starts empty — an empty save stores a keyless config.
 */
function ModelForm({ info }: { info: ModelConfigInfo }) {
  const toast = useToast();
  const setCfg = useSetModelConfig();
  const clearCfg = useClearModelConfig();

  const [baseUrl, setBaseUrl] = useState(info.base_url ?? '');
  const [modelName, setModelName] = useState(info.model_name ?? '');
  const [apiKey, setApiKey] = useState('');

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    setCfg.mutate(
      { base_url: baseUrl.trim(), model_name: modelName.trim(), api_key: apiKey },
      {
        onSuccess: () => {
          setApiKey('');
          toast.push({ kind: 'success', message: 'Model configuration saved.' });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message:
              err instanceof ApiError ? err.message : 'Could not save the model configuration.',
          }),
      },
    );
  };

  const clear = () => {
    clearCfg.mutate(undefined, {
      onSuccess: (next) => {
        setApiKey('');
        setBaseUrl(next.base_url ?? '');
        setModelName(next.model_name ?? '');
        toast.push({ kind: 'success', message: 'Model configuration cleared.' });
      },
      onError: (err) =>
        toast.push({
          kind: 'error',
          message:
            err instanceof ApiError ? err.message : 'Could not clear the model configuration.',
        }),
    });
  };

  return (
    <>
      <p className={styles.modelHint} data-testid="model-hint">
        {info.configured
          ? 'The agent uses this OpenAI-compatible endpoint. Runs are blocked when no model is configured.'
          : 'No LLM is configured — runs are blocked until you set one below. This is required before the agent can run.'}
      </p>

      <form className={styles.modelForm} onSubmit={save} noValidate>
        <TextField
          label="Base URL"
          placeholder="https://api.openai.com/v1"
          value={baseUrl}
          onChange={(e) => setBaseUrl(e.target.value)}
          data-testid="model-base-url"
          autoComplete="off"
          required
        />
        <TextField
          label="Model (provider/model)"
          placeholder="openai/gpt-4o"
          value={modelName}
          onChange={(e) => setModelName(e.target.value)}
          data-testid="model-name"
          autoComplete="off"
          required
        />
        <TextField
          label="API key"
          type="password"
          placeholder={info.api_key_set ? '•••••••• (set — retype to change)' : 'sk-…  (blank for keyless endpoints)'}
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          data-testid="model-api-key"
          autoComplete="off"
          hint="Stored encrypted. Never displayed after saving."
        />
        <div className={styles.modelActions}>
          <Button
            type="submit"
            variant="primary"
            loading={setCfg.isPending}
            data-testid="model-save"
          >
            Save
          </Button>
          {info.source === 'db' && (
            <Button
              type="button"
              variant="secondary"
              onClick={clear}
              loading={clearCfg.isPending}
              data-testid="model-clear"
            >
              Clear
            </Button>
          )}
        </div>
      </form>
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
