/*
 * ProjectSettingsModal — owner/cluster-admin project settings (blueprint §2/§5).
 * Two tabs:
 *   - General: project rename, the guardrails editor (owner only — concurrency
 *     cap, run timeout, provider allowlist, injected env), and a Delete-project
 *     action behind a confirm step. Repo config (branch / git mode) lives on each
 *     repository on the project page — a project is a pure container.
 *   - Members: roster with role management + add-by-search (MembersPanel).
 */
import { useState } from 'react';
import { Modal } from '../components/Modal';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { MembersPanel } from './MembersPanel';
import { useUpdateProject, useDeleteProject } from '../api/queries';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import { isReservedEnvKey, isValidEnvKey } from '../lib/env';
import { ALLOWLIST_PROVIDERS } from '../lib/providers';
import type { Project, UpdateProjectInput } from '../api/types';
import styles from './ProjectSettingsModal.module.css';

type Tab = 'general' | 'members';

interface EnvRow {
  key: string;
  value: string;
}

/** Parse a guardrail number field: empty/≤0/NaN => null ("inherit"). */
function parseGuardrail(s: string): number | null {
  const t = s.trim();
  if (t === '') return null;
  const n = Number(t);
  if (!Number.isFinite(n) || !Number.isInteger(n) || n <= 0) return null;
  return n;
}

function rowsToEnv(rows: EnvRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (k) out[k] = r.value;
  }
  return out;
}

function envToRows(env: Record<string, string> | undefined): EnvRow[] {
  return Object.entries(env ?? {}).map(([key, value]) => ({ key, value }));
}

function sameStringSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const s = new Set(a);
  return b.every((x) => s.has(x));
}

function sameEnv(a: Record<string, string>, b: Record<string, string>): boolean {
  const ak = Object.keys(a);
  const bk = Object.keys(b);
  if (ak.length !== bk.length) return false;
  return ak.every((k) => b[k] === a[k]);
}

export function ProjectSettingsModal({
  open,
  project,
  onClose,
  onDeleted,
}: {
  open: boolean;
  project: Project;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const update = useUpdateProject();
  const del = useDeleteProject();
  const toast = useToast();

  // Absent role (demo / legacy) is treated as owner (full affordances).
  const canManage = (project.role ?? 'owner') === 'owner';

  const [tab, setTab] = useState<Tab>('general');
  const [name, setName] = useState(project.name);
  const [confirmDelete, setConfirmDelete] = useState(false);

  // Guardrail form state (owner only). Numbers are kept as strings so empty means
  // "inherit the cluster default".
  const [maxConcurrent, setMaxConcurrent] = useState(
    project.max_concurrent_runs != null ? String(project.max_concurrent_runs) : '',
  );
  const [runTimeout, setRunTimeout] = useState(
    project.run_timeout_secs != null ? String(project.run_timeout_secs) : '',
  );
  const [allowlist, setAllowlist] = useState<string[]>(project.provider_allowlist ?? []);
  const [envRows, setEnvRows] = useState<EnvRow[]>(envToRows(project.injected_env));

  const busy = update.isPending || del.isPending;

  // Front-end injected_env validation (mirrors the server's typed 400). The first
  // offending non-empty key wins; a truthy value blocks Save.
  const envError = (() => {
    for (const r of envRows) {
      const k = r.key.trim();
      if (!k) continue;
      if (!isValidEnvKey(k)) return `“${k}” is not a valid environment variable name.`;
      if (isReservedEnvKey(k)) return `“${k}” is reserved by the orchestrator and can’t be set.`;
    }
    return '';
  })();

  const reset = () => {
    setName(project.name);
    setMaxConcurrent(project.max_concurrent_runs != null ? String(project.max_concurrent_runs) : '');
    setRunTimeout(project.run_timeout_secs != null ? String(project.run_timeout_secs) : '');
    setAllowlist(project.provider_allowlist ?? []);
    setEnvRows(envToRows(project.injected_env));
    setConfirmDelete(false);
    setTab('general');
  };

  const close = () => {
    if (busy) return;
    reset();
    onClose();
  };

  const toggleProvider = (p: string) => {
    setAllowlist((cur) => (cur.includes(p) ? cur.filter((x) => x !== p) : [...cur, p]));
  };

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    if (envError) return; // blocked — the inline error explains why

    // Build a minimal PATCH: only include a field that actually changed, so a
    // rename-only save sends { name } and never disturbs the guardrails.
    const input: UpdateProjectInput = {};

    const nextName = name.trim();
    if (nextName && nextName !== project.name) input.name = nextName;

    if (canManage) {
      const nextMax = parseGuardrail(maxConcurrent);
      if (nextMax !== (project.max_concurrent_runs ?? null)) input.max_concurrent_runs = nextMax;

      const nextTimeout = parseGuardrail(runTimeout);
      if (nextTimeout !== (project.run_timeout_secs ?? null)) input.run_timeout_secs = nextTimeout;

      const nextAllow = allowlist.map((p) => p.toLowerCase());
      if (!sameStringSet(nextAllow, project.provider_allowlist ?? [])) {
        input.provider_allowlist = nextAllow;
      }

      const nextEnv = rowsToEnv(envRows);
      if (!sameEnv(nextEnv, project.injected_env ?? {})) input.injected_env = nextEnv;
    }

    if (Object.keys(input).length === 0) {
      onClose();
      return;
    }

    update.mutate(
      { id: project.id, input },
      {
        onSuccess: (updated) => {
          toast.push({ kind: 'success', message: `Project “${updated.name}” updated.` });
          onClose();
        },
        onError: (err) => {
          // The server's typed 400 (e.g. reserved_env_key) message is shown verbatim.
          const msg = err instanceof ApiError ? err.message : 'Failed to update project.';
          toast.push({ kind: 'error', message: msg });
        },
      },
    );
  };

  const remove = () => {
    del.mutate(project.id, {
      onSuccess: () => {
        toast.push({ kind: 'success', message: `Project “${project.name}” deleted.` });
        onDeleted();
      },
      onError: (err) => {
        const msg = err instanceof ApiError ? err.message : 'Failed to delete project.';
        toast.push({ kind: 'error', message: msg });
      },
    });
  };

  const footer =
    tab === 'general' ? (
      <>
        <Button variant="ghost" onClick={close} type="button">
          Cancel
        </Button>
        <Button
          variant="primary"
          type="submit"
          form="project-settings-form"
          loading={update.isPending}
          disabled={!!envError}
          data-testid="project-settings-save"
        >
          Save changes
        </Button>
      </>
    ) : (
      <Button variant="secondary" onClick={close} type="button" data-testid="members-done">
        Done
      </Button>
    );

  return (
    <Modal
      open={open}
      onClose={close}
      title="Project settings"
      data-testid="project-settings-modal"
      footer={footer}
    >
      <div className={styles.tabs} role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'general'}
          className={styles.tab}
          data-active={tab === 'general' || undefined}
          onClick={() => setTab('general')}
          data-testid="tab-general"
        >
          General
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'members'}
          className={styles.tab}
          data-active={tab === 'members' || undefined}
          onClick={() => setTab('members')}
          data-testid="tab-members"
        >
          Members
        </button>
      </div>

      {tab === 'general' ? (
        <form id="project-settings-form" onSubmit={save} noValidate>
          <div className={styles.body}>
            <TextField
              label="Name"
              placeholder="demo"
              value={name}
              onChange={(e) => setName(e.target.value)}
              hint="Repository settings (branch, git mode) live on each repository on the project page."
              data-testid="settings-name-input"
              autoComplete="off"
            />

            {canManage && (
              <section className={styles.guardrails} data-testid="guardrails">
                <div className={styles.guardrailHead}>
                  <span className={styles.guardrailTitle}>Guardrails</span>
                  <span className={styles.guardrailHint}>
                    Leave a limit blank to inherit the cluster default.
                  </span>
                </div>

                <div className={styles.guardrailGrid}>
                  <TextField
                    label="Max concurrent runs"
                    type="number"
                    min={1}
                    inputMode="numeric"
                    placeholder="cluster default"
                    value={maxConcurrent}
                    onChange={(e) => setMaxConcurrent(e.target.value)}
                    data-testid="settings-max-concurrent"
                    autoComplete="off"
                  />
                  <TextField
                    label="Run timeout (seconds)"
                    type="number"
                    min={1}
                    inputMode="numeric"
                    placeholder="cluster default"
                    value={runTimeout}
                    onChange={(e) => setRunTimeout(e.target.value)}
                    data-testid="settings-run-timeout"
                    autoComplete="off"
                  />
                </div>

                <fieldset className={styles.allowlist} data-testid="settings-allowlist">
                  <legend className={styles.allowlistLegend}>Provider allowlist</legend>
                  <span className={styles.guardrailHint}>
                    Restrict which git hosts a repository may target. Select none to allow all.
                  </span>
                  <div className={styles.allowlistRow}>
                    {ALLOWLIST_PROVIDERS.map((p) => (
                      <label key={p} className={styles.checkLabel}>
                        <input
                          type="checkbox"
                          checked={allowlist.includes(p)}
                          onChange={() => toggleProvider(p)}
                          data-testid={`allowlist-${p}`}
                        />
                        {p}
                      </label>
                    ))}
                  </div>
                </fieldset>

                <div className={styles.envBlock} data-testid="settings-injected-env">
                  <div className={styles.guardrailHead}>
                    <span className={styles.guardrailTitle}>Injected environment</span>
                    <span className={styles.guardrailHint}>
                      Extra variables merged into every run. System variables (RUN_*, MODEL_*, …)
                      are reserved.
                    </span>
                  </div>
                  {envRows.length > 0 && (
                    <div className={styles.envRows}>
                      {envRows.map((row, i) => {
                        const k = row.key.trim();
                        const rowError =
                          k !== '' && (!isValidEnvKey(k) || isReservedEnvKey(k));
                        return (
                          <div key={i} className={styles.envRow} data-testid="env-row">
                            <input
                              className={[styles.envInput, rowError && styles.envInvalid]
                                .filter(Boolean)
                                .join(' ')}
                              placeholder="KEY"
                              value={row.key}
                              aria-invalid={rowError || undefined}
                              onChange={(e) =>
                                setEnvRows((rows) =>
                                  rows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)),
                                )
                              }
                              data-testid={`env-key-${i}`}
                              autoComplete="off"
                            />
                            <span className={styles.envEq}>=</span>
                            <input
                              className={styles.envInput}
                              placeholder="value"
                              value={row.value}
                              onChange={(e) =>
                                setEnvRows((rows) =>
                                  rows.map((r, j) =>
                                    j === i ? { ...r, value: e.target.value } : r,
                                  ),
                                )
                              }
                              data-testid={`env-value-${i}`}
                              autoComplete="off"
                            />
                            <button
                              type="button"
                              className={styles.envRemove}
                              onClick={() => setEnvRows((rows) => rows.filter((_, j) => j !== i))}
                              data-testid={`env-remove-${i}`}
                              aria-label="Remove variable"
                            >
                              ×
                            </button>
                          </div>
                        );
                      })}
                    </div>
                  )}
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => setEnvRows((rows) => [...rows, { key: '', value: '' }])}
                    data-testid="env-add"
                  >
                    + Add variable
                  </Button>
                  {envError && (
                    <span className={styles.envError} data-testid="env-error">
                      {envError}
                    </span>
                  )}
                </div>
              </section>
            )}

            <section className={styles.danger} data-testid="danger-zone">
              <div className={styles.dangerText}>
                <span className={styles.dangerTitle}>Delete project</span>
                <span className={styles.dangerHint}>
                  Permanently removes this project and all of its runs, events and
                  artifacts. This cannot be undone.
                </span>
              </div>
              {confirmDelete ? (
                <div className={styles.confirmRow} data-testid="delete-confirm">
                  <span className={styles.confirmLabel}>Delete for good?</span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => setConfirmDelete(false)}
                    disabled={del.isPending}
                  >
                    Keep
                  </Button>
                  <Button
                    type="button"
                    variant="danger"
                    size="sm"
                    loading={del.isPending}
                    onClick={remove}
                    data-testid="project-delete-confirm"
                  >
                    Delete project
                  </Button>
                </div>
              ) : (
                <Button
                  type="button"
                  variant="danger"
                  size="sm"
                  onClick={() => setConfirmDelete(true)}
                  disabled={busy}
                  data-testid="project-delete"
                >
                  Delete project
                </Button>
              )}
            </section>
          </div>
        </form>
      ) : (
        <MembersPanel projectId={project.id} canManage={canManage} />
      )}
    </Modal>
  );
}
