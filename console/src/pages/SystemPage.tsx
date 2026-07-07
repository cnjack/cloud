/*
 * SystemPage — the cluster-admin's home ("Cluster" view). Renders the read-only
 * GET /api/v1/system snapshot as clean info cards: Capacity (with a simple bar),
 * Guardrails, Provider, Runner, Version. All read-only — this console has no
 * admin *mutations*; kubectl remains the write path (honest about the MVP).
 *
 * Role gating: the route itself is presentation-gated to cluster-admin (the nav
 * link is hidden for project-admin, and this page shows a plain notice if a
 * project-admin lands on /system directly). This is NOT authorization — the
 * orchestrator has one console token; real RBAC is on the roadmap (see 11-api.md
 * § "System / admin").
 */
import { useSystem } from '../api/queries';
import { useRole } from '../api/ApiProvider';
import { Card } from '../components/Card';
import { LoadingBlock, ErrorBlock } from '../components/States';
import { EmptyState } from '../components/EmptyState';
import type { SystemInfo } from '../api/types';
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
            Read-only snapshot of this orchestrator: capacity, guardrails, and
            wiring. Changes are made out-of-band (env / kubectl).
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
