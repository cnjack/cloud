/*
 * ProjectSettingsModal — owner/cluster-admin project settings (blueprint §2/§5).
 * Tabs:
 *   - General: project rename, the guardrails editor (owner only — concurrency
 *     cap, run timeout, injected env), and a Delete-project action behind a
 *     confirm step. Repo config (branch / git mode) lives on each repository on
 *     the project page — a project is a pure container.
 *   - Members: roster with role management + add-by-search (MembersPanel).
 *   - Bot integrations (D19 / F5): git host bindings for unattended service
 *     execution. They are intentionally distinct from a member's OAuth-based
 *     provider webhook setup in the Service Automation area.
 *   - Kanban: jtype board→service bindings (owner).
 *   - API keys (F12 / D24): project-scoped, revocable automation credentials
 *     (owner) — replaces borrowing CONSOLE_TOKEN for external/CI use.
 */
import { useEffect, useState } from 'react';
import { Modal } from '../components/Modal';
import { Button } from '../components/Button';
import { SelectField, TextField } from '../components/Field';
import { MembersPanel } from './MembersPanel';
import { IntegrationsPanel } from './IntegrationsPanel';
import {
  useUpdateProject,
  useDeleteProject,
  useSystem,
  useProjectKanbanLinks,
  useCreateProjectKanbanLink,
  useUpdateProjectKanbanLinkToken,
  useDeleteProjectKanbanLink,
  useJtypeWorkspaces,
  useJtypeBoards,
  useStartLinkConnect,
  useLinkConnectStatus,
  useApiKeys,
  useCreateApiKey,
  useRevokeApiKey,
} from '../api/queries';
import { useToast } from '../components/Toast';
import { KanbanConnectFlow, expiryLabel } from '../components/KanbanConnect';
import { ApiError } from '../api/client';
import { isReservedEnvKey, isValidEnvKey } from '../lib/env';
import { timeAgo } from '../lib/format';
import type {
  ApiKey,
  CreateApiKeyResponse,
  KanbanLink,
  Project,
  UpdateProjectInput,
} from '../api/types';
import styles from './ProjectSettingsModal.module.css';

type Tab = 'general' | 'members' | 'integrations' | 'kanban' | 'apikeys';

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
    setEnvRows(envToRows(project.injected_env));
    setConfirmDelete(false);
    setTab('general');
  };

  const close = () => {
    if (busy) return;
    reset();
    onClose();
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
      <Button variant="secondary" onClick={close} type="button" data-testid="settings-done">
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
        {canManage && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'integrations'}
            className={styles.tab}
            data-active={tab === 'integrations' || undefined}
            onClick={() => setTab('integrations')}
            data-testid="tab-integrations"
          >
            Bot integrations
          </button>
        )}
        {canManage && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'kanban'}
            className={styles.tab}
            data-active={tab === 'kanban' || undefined}
            onClick={() => setTab('kanban')}
            data-testid="tab-kanban"
          >
            Kanban
          </button>
        )}
        {canManage && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'apikeys'}
            className={styles.tab}
            data-active={tab === 'apikeys' || undefined}
            onClick={() => setTab('apikeys')}
            data-testid="tab-apikeys"
          >
            API keys
          </button>
        )}
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
      ) : tab === 'members' ? (
        <MembersPanel projectId={project.id} canManage={canManage} />
      ) : tab === 'integrations' ? (
        <IntegrationsPanel project={project} />
      ) : tab === 'kanban' ? (
        <KanbanPanel project={project} />
      ) : (
        <ApiKeysPanel project={project} />
      )}
    </Modal>
  );
}

/**
 * KanbanPanel — the project owner's jtype kanban links (F6 / D25). Lists the
 * project's board→service bindings with a token badge (own vs cluster fallback),
 * and an add form. The per-link jtype token is WRITE-ONLY: it is sent on create
 * and never returned (the badge is the only echo). Service is chosen from the
 * project's own services; workspace/board/columns live in jtype and are typed.
 *
 * D27: links can't function until the cluster jtype config is set. When the
 * cluster integration is OFF (system.kanban.enabled === false) the add form is
 * disabled and a fail-visible notice points the owner at a cluster admin — rather
 * than letting them create a link that silently never fires.
 */
function KanbanPanel({ project }: { project: Project }) {
  const toast = useToast();
  const system = useSystem();
  const links = useProjectKanbanLinks(project.id);
  const create = useCreateProjectKanbanLink(project.id);
  const del = useDeleteProjectKanbanLink(project.id);
  const services = project.services ?? [];
  // Strictly false (an absent kanban block ⇒ don't block, we can't tell).
  const kanbanOff = system.data?.kanban?.enabled === false;

  const [serviceId, setServiceId] = useState('');
  const [workspaceId, setWorkspaceId] = useState('');
  const [boardRef, setBoardRef] = useState('');
  const [triggerCol, setTriggerCol] = useState('');
  const [doneCol, setDoneCol] = useState('');
  const [token, setToken] = useState('');
  // D29: default to the cascading discovery pickers; "Enter manually" (or an
  // auto-fallback when discovery errors) swaps them for free-text fields. Manual
  // entry is NOT a second create path — the server resolves + canonicalizes a
  // typed board ref exactly like a picked one.
  const [manual, setManual] = useState(false);
  const [discoveryError, setDiscoveryError] = useState('');

  // Discovery queries fire only in picker mode with the integration on. retry is
  // off (in the hooks), so a typed 409/503/400 surfaces at once → auto-fallback.
  const pickerActive = !kanbanOff && !manual;
  const workspaces = useJtypeWorkspaces(project.id, pickerActive);
  const boards = useJtypeBoards(project.id, workspaceId, pickerActive && !!workspaceId);
  const boardList = boards.data ?? [];
  const selectedBoard = boardList.find((b) => b.ref === boardRef);
  const columnOptions = (selectedBoard?.columns ?? []).map((c) => ({ value: c.key, label: c.name }));

  // Fail-visible auto-fallback: if EITHER discovery call errors (integration
  // off/unreachable, bad token, or a workspace whose boards won't list), drop to
  // manual entry and show the server's typed message — never a blank, spinning, or
  // silently-empty picker. The `!isFetching` guard means a refetch in flight (e.g.
  // right after the user switches back to the pickers) doesn't bounce to manual.
  useEffect(() => {
    if (manual) return;
    const wsErr = workspaces.isError && !workspaces.isFetching;
    const boardErr = boards.isError && !boards.isFetching;
    if (!wsErr && !boardErr) return;
    const err = wsErr ? workspaces.error : boards.error;
    setManual(true);
    setDiscoveryError(
      err instanceof ApiError
        ? err.message
        : 'Could not reach jtype to list workspaces/boards — enter the details manually.',
    );
  }, [
    manual,
    workspaces.isError,
    workspaces.isFetching,
    workspaces.error,
    boards.isError,
    boards.isFetching,
    boards.error,
  ]);

  const pickWorkspace = (id: string) => {
    setWorkspaceId(id);
    // A new workspace invalidates the board + its columns.
    setBoardRef('');
    setTriggerCol('');
    setDoneCol('');
  };
  const pickBoard = (ref: string) => {
    setBoardRef(ref);
    // A new board invalidates the column picks.
    setTriggerCol('');
    setDoneCol('');
  };

  // Required-field gate (a link that can't function shouldn't be creatable). The
  // values are the same in either mode, so this covers pickers and manual entry.
  const incomplete =
    !serviceId || !workspaceId.trim() || !boardRef.trim() || !triggerCol.trim();

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      {
        workspace_id: workspaceId.trim(),
        board_ref: boardRef.trim(),
        service_id: serviceId,
        trigger_column: triggerCol.trim(),
        done_column: doneCol.trim() || undefined,
        token: token.trim() || undefined,
      },
      {
        onSuccess: () => {
          setWorkspaceId('');
          setBoardRef('');
          setTriggerCol('');
          setDoneCol('');
          setToken('');
          toast.push({ kind: 'success', message: 'Kanban link added.' });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Could not add the link.',
          }),
      },
    );
  };

  const remove = (id: string) => {
    del.mutate(id, {
      onSuccess: () => toast.push({ kind: 'success', message: 'Kanban link removed.' }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Could not remove the link.',
        }),
    });
  };

  return (
    <div className={styles.body} data-testid="kanban-panel">
      <p className={styles.guardrailHint}>
        Drag a card into a link&apos;s trigger column to dispatch an agent run; the result is
        written back as a card comment (and the card moved to the done column when set). Each link
        can carry its own jtype token; leave it blank to use the cluster fallback.
      </p>

      {kanbanOff && (
        <p className={styles.kanbanError} data-testid="kanban-disabled">
          jtype kanban isn’t enabled for this cluster yet — ask a cluster admin to configure it on
          the Cluster page.
        </p>
      )}

      {links.data && links.data.length > 0 ? (
        <div className={styles.kanbanList} data-testid="kanban-links">
          {links.data.map((l) => (
            <KanbanLinkRow
              key={l.id}
              projectId={project.id}
              link={l}
              serviceName={services.find((s) => s.id === l.service_id)?.name ?? l.service_id}
              deleting={del.isPending}
              kanbanOff={kanbanOff}
              onRemove={() => remove(l.id)}
            />
          ))}
        </div>
      ) : (
        <p className={styles.guardrailHint} data-testid="kanban-empty">
          No kanban links yet — add one below.
        </p>
      )}

      <form className={styles.kanbanForm} onSubmit={submit} noValidate data-testid="kanban-link-form">
        <SelectField
          label="Service"
          required
          value={serviceId}
          onChange={setServiceId}
          disabled={kanbanOff}
          data-testid="kanban-link-service"
          placeholder="Select service…"
          options={services.map((s) => ({ value: s.id, label: s.name }))}
        />

        {/* Manual-entry fallback (an un-enumerable board, or jtype unreachable):
            the server resolves a typed ref the same way it resolves a picked one. */}
        {!kanbanOff && (
          <div className={styles.kanbanModeRow}>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                setManual((m) => {
                  const next = !m;
                  if (!next) {
                    // Returning to the pickers: refetch so a stale discovery error
                    // clears (the auto-fallback's !isFetching guard prevents a
                    // bounce), letting the owner retry once jtype recovers.
                    void workspaces.refetch();
                    if (workspaceId) void boards.refetch();
                  }
                  return next;
                });
                setDiscoveryError('');
              }}
              data-testid="kanban-link-manual-toggle"
            >
              {manual ? 'Use pickers' : 'Enter manually'}
            </Button>
          </div>
        )}
        {discoveryError && (
          <p className={styles.kanbanError} data-testid="kanban-link-discovery-error">
            {discoveryError}
          </p>
        )}

        {manual ? (
          <>
            <TextField
              label="jtype workspace id"
              placeholder="f006b727-…"
              value={workspaceId}
              onChange={(e) => setWorkspaceId(e.target.value)}
              required
              disabled={kanbanOff}
              data-testid="kanban-link-workspace"
              autoComplete="off"
            />
            <TextField
              label="Board ref"
              placeholder="jtype.board"
              value={boardRef}
              onChange={(e) => setBoardRef(e.target.value)}
              required
              disabled={kanbanOff}
              data-testid="kanban-link-board"
              autoComplete="off"
              hint="A board name or path (e.g. jtype.board). The server resolves it to the board’s id."
            />
            <TextField
              label="Trigger column"
              placeholder="ai"
              value={triggerCol}
              onChange={(e) => setTriggerCol(e.target.value)}
              required
              disabled={kanbanOff}
              data-testid="kanban-link-trigger"
              autoComplete="off"
            />
            <TextField
              label="Done column (optional)"
              placeholder="done"
              value={doneCol}
              onChange={(e) => setDoneCol(e.target.value)}
              disabled={kanbanOff}
              data-testid="kanban-link-done"
              autoComplete="off"
            />
          </>
        ) : (
          <>
            <SelectField
              label="jtype workspace"
              required
              value={workspaceId}
              onChange={pickWorkspace}
              disabled={kanbanOff || workspaces.isLoading}
              data-testid="kanban-link-workspace-select"
              placeholder={workspaces.isLoading ? 'Loading workspaces…' : 'Select workspace…'}
              options={(workspaces.data ?? []).map((w) => ({ value: w.id, label: w.name }))}
            />
            <SelectField
              label="Board"
              required
              value={boardRef}
              onChange={pickBoard}
              disabled={kanbanOff || !workspaceId || boards.isLoading}
              data-testid="kanban-link-board-select"
              placeholder={
                !workspaceId
                  ? 'Pick a workspace first'
                  : boards.isLoading
                    ? 'Loading boards…'
                    : 'Select board…'
              }
              options={boardList.map((b) => ({ value: b.ref, label: b.title }))}
            />
            <SelectField
              label="Trigger column"
              required
              value={triggerCol}
              onChange={setTriggerCol}
              disabled={kanbanOff || !boardRef}
              data-testid="kanban-link-trigger-select"
              placeholder={boardRef ? 'Select column…' : 'Pick a board first'}
              options={columnOptions}
            />
            <SelectField
              label="Done column (optional)"
              value={doneCol}
              onChange={setDoneCol}
              disabled={kanbanOff || !boardRef}
              data-testid="kanban-link-done-select"
              placeholder={boardRef ? '— none —' : 'Pick a board first'}
              options={[{ value: '', label: '— none —' }, ...columnOptions]}
            />
          </>
        )}

        <TextField
          label="jtype token (optional)"
          type="password"
          placeholder="blank = use cluster fallback token"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          disabled={kanbanOff}
          data-testid="kanban-link-token"
          autoComplete="off"
          hint="Stored encrypted. Never displayed after saving."
        />
        <div className={styles.kanbanFormActions}>
          <Button
            type="submit"
            variant="primary"
            loading={create.isPending}
            disabled={kanbanOff || incomplete}
            data-testid="kanban-link-add"
          >
            Add link
          </Button>
        </div>
      </form>
    </div>
  );
}

/**
 * KanbanLinkRow — one project kanban link: the board binding, a three-state
 * credential badge (P1 — "missing" is a loud error: the poller skips the link
 * until a token is set), a write-only "Update token" editor (P2 — rotate with a
 * value, clear with an empty submit; the token is never displayed), and Remove.
 */
function KanbanLinkRow({
  projectId,
  link,
  serviceName,
  deleting,
  kanbanOff,
  onRemove,
}: {
  projectId: string;
  link: KanbanLink;
  serviceName: string;
  deleting: boolean;
  kanbanOff: boolean;
  onRemove: () => void;
}) {
  const toast = useToast();
  const updateToken = useUpdateProjectKanbanLinkToken(projectId);
  const [editing, setEditing] = useState(false);
  const [token, setToken] = useState('');

  // D28: per-link "Connect with jtype" device flow. The link already exists
  // (create-then-connect), so we start a flow against it and poll to completion,
  // which seals a per-link token server-side (credential_status → per_link).
  const startConnect = useStartLinkConnect(projectId);
  const [connectId, setConnectId] = useState<string | undefined>();
  const connectStatus = useLinkConnectStatus(projectId, link.id, connectId, !!connectId);
  const launchConnect = () =>
    startConnect.mutate(link.id, { onSuccess: (s) => setConnectId(s.connect_id) });
  const resetConnect = () => {
    setConnectId(undefined);
    startConnect.reset();
  };

  const badge = {
    per_link: 'own token',
    cluster_fallback: 'cluster token',
    missing: 'no credential — set a token',
  }[link.credential_status];

  // D29: an absent board_status is a pre-D29 row backfilled to "ok" (validated).
  const boardStatus = link.board_status ?? 'ok';
  // The stored board_ref becomes the opaque b_… id after canonicalization, so
  // prefer the captured title; keep the raw workspace/ref pair as a tooltip.
  const boardLabel = link.board_title || `${link.workspace_id} / ${link.board_ref}`;

  // Expiry badge for a device-flow token (unknown for manual/fallback ⇒ no badge).
  const linkExpiry = expiryLabel(link.token_expires_at, 'expired — reconnect');

  const saveToken = (e: React.FormEvent) => {
    e.preventDefault();
    updateToken.mutate(
      { linkId: link.id, token: token.trim() },
      {
        onSuccess: (updated) => {
          setToken('');
          setEditing(false);
          toast.push({
            kind: 'success',
            message: updated.token_set
              ? 'Token updated.'
              : 'Token cleared — the link now uses the cluster fallback (if configured).',
          });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Could not update the token.',
          }),
      },
    );
  };

  return (
    <div className={styles.kanbanRow} data-testid={`kanban-link-${link.id}`}>
      <div className={styles.kanbanMeta}>
        <div className={styles.kanbanTitle}>
          <span title={`${link.workspace_id} / ${link.board_ref}`}>{boardLabel}</span>
          <span
            className={styles.badge}
            data-state={link.credential_status}
            data-testid={`kanban-cred-${link.id}`}
          >
            {badge}
          </span>
          {boardStatus !== 'ok' && (
            <span
              className={styles.badge}
              data-state={boardStatus === 'invalid' ? 'invalid' : 'unvalidated'}
              data-testid={`kanban-board-status-${link.id}`}
            >
              {boardStatus === 'invalid' ? 'board/columns invalid' : 'columns not validated'}
            </span>
          )}
          {linkExpiry && (
            <span
              className={styles.badge}
              data-state={linkExpiry.startsWith('expired') ? 'missing' : 'per_link'}
              data-testid={`kanban-link-expiry-${link.id}`}
            >
              {linkExpiry}
            </span>
          )}
        </div>
        <div className={styles.kanbanSub}>
          {serviceName} · {link.trigger_column}
          {link.done_column ? ` → ${link.done_column}` : ''}
        </div>
        {boardStatus === 'unvalidated' && (
          <p className={styles.kanbanBoardNotice} data-testid={`kanban-board-notice-${link.id}`}>
            This link was created without a token — its board and columns haven’t been checked yet.
            Connect a jtype token below (or check the board ref).
          </p>
        )}
        {boardStatus === 'invalid' && (
          <p
            className={styles.kanbanError}
            role="alert"
            data-testid={`kanban-board-notice-${link.id}`}
          >
            Board not found or its columns changed — the poller is skipping this link. Fix the board
            (delete and re-add via the pickers) or reconnect a token.
          </p>
        )}
        {/* D28: one-click connect for this link's own token. Disabled while the
            cluster integration is off (same gate as the add form). */}
        <div className={styles.kanbanConnect}>
          <KanbanConnectFlow
            idPrefix={`kanban-link-connect-${link.id}`}
            disabled={kanbanOff}
            disabledHint="Enable jtype on the Cluster page first"
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
        {editing && (
          <form className={styles.tokenEditor} onSubmit={saveToken} noValidate>
            <TextField
              label="New jtype token"
              type="password"
              placeholder="blank = clear (use cluster fallback)"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              data-testid={`kanban-token-input-${link.id}`}
              autoComplete="off"
            />
            <Button
              type="submit"
              variant="primary"
              size="sm"
              loading={updateToken.isPending}
              data-testid={`kanban-token-save-${link.id}`}
            >
              Save
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </form>
        )}
      </div>
      <div style={{ display: 'flex', gap: 8 }}>
        {!editing && (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => setEditing(true)}
            data-testid={`kanban-token-edit-${link.id}`}
          >
            Update token
          </Button>
        )}
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabled={deleting}
          onClick={onRemove}
          data-testid={`kanban-link-delete-${link.id}`}
        >
          Remove
        </Button>
      </div>
    </div>
  );
}

/**
 * ApiKeysPanel — the project owner's API keys (F12 / D24). A key is a
 * revocable, project-scoped automation credential (`Authorization: Bearer
 * <key>`, capped at the Member role on THIS project only) meant to replace
 * borrowing the cluster-wide console token for external/CI use. The plaintext
 * is shown ONCE, right after creation — there is no read-back endpoint, so the
 * reveal card below is the only chance to copy it.
 */
function ApiKeysPanel({ project }: { project: Project }) {
  const toast = useToast();
  const keys = useApiKeys(project.id);
  const create = useCreateApiKey(project.id);
  const revoke = useRevokeApiKey(project.id);

  const [name, setName] = useState('');
  const [revealed, setRevealed] = useState<CreateApiKeyResponse | null>(null);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name: name.trim() },
      {
        onSuccess: (created) => {
          setName('');
          setRevealed(created);
          toast.push({ kind: 'success', message: `API key “${created.name}” created.` });
        },
        onError: (err) =>
          toast.push({
            kind: 'error',
            message: err instanceof ApiError ? err.message : 'Could not create the API key.',
          }),
      },
    );
  };

  const doRevoke = (id: string) => {
    revoke.mutate(id, {
      onSuccess: () => toast.push({ kind: 'success', message: 'API key revoked.' }),
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Could not revoke the API key.',
        }),
    });
  };

  return (
    <div className={styles.body} data-testid="apikeys-panel">
      <p className={styles.guardrailHint}>
        A project-scoped key authenticates as <code>Authorization: Bearer &lt;key&gt;</code> and can
        trigger runs and read this project only — never another project, never this project&apos;s
        settings or members, and never the cluster-admin surface. Use it for external/CI automation
        instead of the cluster-wide console token.
      </p>

      {revealed && (
        <ApiKeyReveal created={revealed} onDismiss={() => setRevealed(null)} />
      )}

      {keys.data && keys.data.length > 0 ? (
        <div className={styles.kanbanList} data-testid="apikeys-list">
          {keys.data.map((k) => (
            <ApiKeyRow
              key={k.id}
              apiKey={k}
              revoking={revoke.isPending}
              onRevoke={() => doRevoke(k.id)}
            />
          ))}
        </div>
      ) : (
        <p className={styles.guardrailHint} data-testid="apikeys-empty">
          No API keys yet — create one below.
        </p>
      )}

      <form className={styles.kanbanForm} onSubmit={submit} noValidate data-testid="apikey-form">
        <TextField
          label="Name"
          placeholder="ci-bot"
          value={name}
          onChange={(e) => setName(e.target.value)}
          required
          data-testid="apikey-name"
          autoComplete="off"
          hint="Helps you tell keys apart later — pick something identifying, like the CI job that will use it."
        />
        <div className={styles.kanbanFormActions}>
          <Button type="submit" variant="primary" loading={create.isPending} data-testid="apikey-create">
            Create key
          </Button>
        </div>
      </form>
    </div>
  );
}

/**
 * ApiKeyReveal — the one-time plaintext display right after creation. There is
 * no read-back endpoint, so this card (plus its copy button) is the only
 * chance the owner gets to grab the key; dismissing it is a UI-only action
 * (the key keeps working — dismissing does NOT revoke it).
 */
function ApiKeyReveal({
  created,
  onDismiss,
}: {
  created: CreateApiKeyResponse;
  onDismiss: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(created.key);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable — the text is still selectable */
    }
  };
  return (
    <section className={styles.apiKeyReveal} data-testid="apikey-reveal">
      <div className={styles.guardrailHead}>
        <span className={styles.guardrailTitle}>“{created.name}” created</span>
        <span className={styles.guardrailHint}>
          Shown once — copy it now. It cannot be displayed again; if you lose it, revoke this key and
          create a new one.
        </span>
      </div>
      <div className={styles.apiKeyRevealRow}>
        <code className={styles.apiKeyRevealCode} data-testid="apikey-reveal-value">
          {created.key}
        </code>
        <Button type="button" variant="secondary" size="sm" onClick={copy} data-testid="apikey-reveal-copy">
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>
      <div className={styles.kanbanFormActions}>
        <Button type="button" variant="ghost" size="sm" onClick={onDismiss} data-testid="apikey-reveal-dismiss">
          Done
        </Button>
      </div>
    </section>
  );
}

/**
 * ApiKeyRow — one API key: name, status badge (active/revoked), prefix,
 * created/last-used, and Revoke (hidden once already revoked).
 */
function ApiKeyRow({
  apiKey,
  revoking,
  onRevoke,
}: {
  apiKey: ApiKey;
  revoking: boolean;
  onRevoke: () => void;
}) {
  const revoked = !!apiKey.revoked_at;
  return (
    <div className={styles.kanbanRow} data-testid={`apikey-${apiKey.id}`}>
      <div className={styles.kanbanMeta}>
        <div className={styles.kanbanTitle}>
          {apiKey.name}
          <span
            className={styles.badge}
            data-state={revoked ? 'missing' : 'per_link'}
            data-testid={`apikey-status-${apiKey.id}`}
          >
            {revoked ? 'revoked' : 'active'}
          </span>
          <code className={styles.repoField}>{apiKey.prefix}…</code>
        </div>
        <div className={styles.kanbanSub}>
          Created {timeAgo(apiKey.created_at)}
          {apiKey.last_used_at ? ` · last used ${timeAgo(apiKey.last_used_at)}` : ' · never used'}
        </div>
      </div>
      {!revoked && (
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabled={revoking}
          onClick={onRevoke}
          data-testid={`apikey-revoke-${apiKey.id}`}
        >
          Revoke
        </Button>
      )}
    </div>
  );
}
