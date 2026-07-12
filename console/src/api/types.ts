/*
 * types.ts — the API contract, typed verbatim from the orchestrator domain
 * (cloud/orchestrator/internal/domain/domain.go) and the route spec in the
 * console brief. When cloud/docs/11-api.md lands, reconcile drift HERE first —
 * every other module depends on these types, so this is the one place to fix.
 */

export type RunStatus =
  | 'queued'
  | 'scheduling'
  | 'running'
  // `awaiting_input` (D22): a multi-turn session run finished a turn and is
  // waiting for the user's next message. Non-terminal — the SSE stream stays
  // open and the run accepts POST /runs/{id}/messages.
  | 'awaiting_input'
  | 'succeeded'
  | 'failed'
  | 'canceled'
  // `blocked` is modelled + rendered in the badge system but never produced
  // this period (agent runs full_access). Kept so the badge set is complete.
  | 'blocked';

export const TERMINAL_STATUSES: readonly RunStatus[] = [
  'succeeded',
  'failed',
  'canceled',
];

export function isTerminal(status: RunStatus): boolean {
  return TERMINAL_STATUSES.includes(status);
}

export type FailureReason =
  | 'clone_failed'
  | 'setup_failed'
  | 'agent_error'
  | 'timeout'
  // push_failed (ST-1): draft_pr mode produced a diff but could not push the
  // agent/run-<id> branch to the provider. See 11-api.md §1.4.
  | 'push_failed';

/**
 * git_mode (ST-1; 11-api.md §1.1):
 *  - `readonly` (default) — a successful run ends in a diff artifact only;
 *    nothing is pushed, no PR is opened.
 *  - `draft_pr` — after a successful run with a non-empty diff the runner pushes
 *    an `agent/run-<id>` branch and the orchestrator opens a draft PR on the
 *    provider. Never auto-merges, never triggers CI.
 */
export type GitMode = 'readonly' | 'draft_pr';

/**
 * Git providers the orchestrator understands (multitenant blueprint §1). Gitea is
 * the only one wired end-to-end for push/PR; github/gitlab classify repos and
 * carry OAuth identities.
 */
export type GitProvider = 'gitea' | 'github' | 'gitlab';

/** A project member's role on a project (blueprint §2 RBAC). */
export type MemberRole = 'owner' | 'member' | 'viewer';

/** How a service addresses its repository (blueprint §1). */
export type RepoKind = 'provider' | 'raw';

/**
 * A run's kind (blueprint §1/§5): an ordinary `agent` invocation that produces a
 * diff / draft PR, or a `review` run that reviews a PR and produces review_output.
 * Absent is treated as `agent` by the UI.
 */
export type RunKind = 'agent' | 'review';

/**
 * A service is one repository configuration inside a project. The simple "one
 * repo = one project" UX is a project with a single service named `default`;
 * the console only surfaces the service dimension once a project has more than
 * one (multitenant blueprint §0/§4).
 */
export interface Service {
  id: string;
  project_id: string;
  name: string;
  repo_kind: RepoKind;
  provider?: GitProvider | string;
  repo_owner_name?: string;
  raw_repo_url?: string;
  default_branch: string;
  git_mode: GitMode;
  /**
   * The catalog model (D21) this service's runs use by default when the composer
   * doesn't pick one. Absent/null = no default (the project's sole granted model
   * is used, or the composer must choose when several are granted).
   */
  default_model_id?: string | null;
  /**
   * The project integration (D19 / F5) this service's runs act as. When set, all
   * git operations use the integration's bot credential (not the triggering
   * user's OAuth); absent/null = the legacy per-user path.
   */
  integration_id?: string | null;
  created_at: string;
}

/** Result of an explicit OAuth-backed provider webhook synchronization. The
 * endpoint is returned for operator visibility; no credential or secret is ever
 * exposed to the browser. */
export interface ServiceWebhookSetup {
  provider: string;
  endpoint: string;
  status: 'synced';
}

export interface Project {
  id: string;
  name: string;
  created_at: string;
  /**
   * The requesting principal's role on this project (blueprint §2). A
   * cluster-admin / service principal reports "owner". Absent for demo/legacy
   * shapes — the UI treats absent as owner (full affordances).
   */
  role?: MemberRole;
  /** The project's owner user id (empty for a service-principal-created project). */
  owner_user_id?: string;
  /**
   * Guardrails (blueprint §1). Absent/empty means "inherit the cluster default":
   *  - max_concurrent_runs — cap on this project's simultaneously-active runs.
   *  - run_timeout_secs — per-run wall-clock budget (Job deadline + runner).
   *  - injected_env — extra environment variables merged into every runner Job
   *    (system/reserved keys are refused server-side).
   */
  max_concurrent_runs?: number | null;
  run_timeout_secs?: number | null;
  /**
   * @deprecated D20 / F5 — the per-project provider allowlist is retired: the
   * server still serializes the column for legacy projects (read-only historic
   * data) but no console surface reads or edits it, and a PATCH carrying it is a
   * 400 deprecated_key. Git-host policy is now the cluster ALLOWED_GIT_HOSTS
   * allowlist enforced at integration create (see Integration).
   */
  provider_allowlist?: string[];
  injected_env?: Record<string, string>;
  /**
   * Session guardrails (D22). Absent = inherit the cluster default:
   *  - max_live_sessions — cap on simultaneously live (running/awaiting_input)
   *    session runs in this project.
   *  - session_idle_timeout_secs — idle time in awaiting_input before the
   *    session is auto-finished.
   *  - session_ttl_secs — whole-session wall-clock budget.
   */
  max_live_sessions?: number | null;
  session_idle_timeout_secs?: number | null;
  session_ttl_secs?: number | null;
  /**
   * All repositories of the project. A project is a pure container — repo config
   * lives ONLY here (the old flattened repo_url/git_mode fields are gone with the
   * simple-mode shim). The UI shows the service dimension only when length > 1.
   */
  services?: Service[];
}

export interface Run {
  id: string;
  project_id: string;
  /** The service this run was created against (blueprint §1). */
  service_id?: string;
  /** agent (default) or review (blueprint §5). Absent is treated as agent. */
  kind?: RunKind;
  prompt: string;
  status: RunStatus;
  /**
   * D18/D26: whether a succeeded run actually produced a diff. `no_changes`
   * means the agent ran to completion but made no code changes (still a
   * success, just nothing to show in the Diff tab). Absent/null otherwise.
   * Optional so the UI tolerates the backend landing this field later
   * (fail-visible, not silently mocked — see CLAUDE.md).
   */
  result?: 'no_changes' | null;
  /** Fine-grained run-phase detail (e.g. PreparingWorkspace). Optional. */
  phase?: string;
  /** Low-level error string; failure_message is the human-readable one. */
  error?: string;
  k8s_job_name?: string;
  /** Set on retries: the run id this was cloned from (PRD J2-S4 / AC-10). */
  retried_from?: string | null;
  /**
   * F9b (D23 ①②): set on a session-resume run — the finished session run this
   * one continues from (a twin of retried_from). Absent/null for ordinary runs.
   */
  resumed_from?: string | null;
  /**
   * F9b: the ACP session id this run drives. Recorded from the run.session event
   * (or pre-filled on a resume run). Non-empty on a terminal session run gates
   * the "Continue session" affordance. Absent for non-session runs.
   */
  acp_session_id?: string;
  failure_reason?: FailureReason;
  failure_message?: string;
  attempt?: number;
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
  /**
   * Stretch (ST-1): the draft PR the orchestrator opened on Gitea when the
   * project is git_mode=draft_pr. Both empty/absent for readonly (diff-only)
   * runs. pr_number is the PR index for the "#N" chip.
   */
  pr_url?: string | null;
  pr_number?: number | null;
  /**
   * The markdown a review run (kind=review) produced (blueprint §5). Empty/absent
   * for agent runs; populated once the runner posts its review output.
   */
  review_output?: string;
  /** When the review comment was posted to the PR (idempotency marker). */
  review_posted_at?: string | null;
  /**
   * How the run was triggered (M7 / blueprint §8): the API/console (default,
   * absent) or a Gitea PR comment `@jcode …` webhook. A webhook run carries the
   * triggering comment's url, surfaced as the "from PR comment ↗" header chip.
   */
  origin?: RunOrigin;
  origin_comment_id?: string | null;
  origin_comment_url?: string | null;
  /**
   * The catalog model (D21) this run was dispatched with, chosen by the
   * resolution chain at create time. Absent/null when the run resolved to the
   * MODEL_* env fallback (empty catalog) or predates the catalog.
   */
  model_id?: string | null;
  /** Immutable dispatch-time model identifier shown in Run audit details. */
  model_name?: string;
  /** Branch recorded by run.git once the agent has published its changes. */
  git_branch?: string;
  /**
   * D22 multi-turn session: true when this run keeps one agent session alive
   * across turns — it parks in `awaiting_input` between turns and accepts
   * follow-up messages (POST /runs/{id}/messages) + an explicit finish
   * (POST /runs/{id}/finish). Absent/false = ordinary single-shot run.
   */
  session?: boolean;
  /**
   * F8b permission approval: "approval" = the runner forwards each agent
   * permission request as an agent.permission_request event and waits for the
   * user's answer (POST /runs/{id}/permission-response). Absent/"" =
   * full_access (auto-approve, the default). Only ever set on session runs.
   */
  permission_mode?: 'approval' | '';
  /** When the run entered awaiting_input (idle-timeout epoch). */
  awaiting_since?: string | null;
}

/* ---- session permission approval (F8b / D22) ------------------------------ */

/** One option a permission request offered (echoed verbatim from jcode). */
export interface PermissionOption {
  option_id: string;
  name: string;
  /** jcode's option classification, e.g. "allow_once" / "reject_once". */
  kind: string;
}

/**
 * POST /api/v1/runs/{id}/permission-response response — the committed ledger
 * row. The decided_* half is the user's answer; the resolved_* half arrives
 * later via the agent.permission_resolved event (they can differ when a
 * decision lost the race with the runner's client-side timeout).
 */
export interface RunPermission {
  request_id: string;
  run_id: string;
  tool_call_id?: string;
  title: string;
  options: PermissionOption[];
  created_at: string;
  decided_option_id?: string | null;
  decided_by?: string | null;
  decided_at?: string | null;
  resolved_option_id?: string | null;
  resolution?: 'user' | 'timeout' | string | null;
  resolved_at?: string | null;
}

/**
 * POST /api/v1/runs/{id}/messages response — one queued follow-up prompt on a
 * session run's delivery queue (D22). The timeline shows the message via its
 * user.message event; this is just the create acknowledgement.
 */
export interface RunMessage {
  id: string;
  run_id: string;
  seq: number;
  prompt: string;
  created_by?: string;
  created_at: string;
  delivered_at?: string | null;
}

/**
 * How a run was triggered (blueprint §8). Absent is treated as `api`.
 *   'webhook'  — a Gitea PR comment `@jcode …` (carries origin_comment_url).
 *   'kanban'   — a jtype card dragged into a link's trigger column (Feature E).
 *   'schedule' — a service-level cron trigger came due (F11 / D24).
 */
export type RunOrigin = 'api' | 'webhook' | 'kanban' | 'schedule';

/* ---- PR view (GET /runs/{id}/pr; blueprint §4/§5) ------------------------ */

/** Live PR state from the provider. `unknown` when it can't be determined. */
export type PrState = 'open' | 'merged' | 'closed' | 'unknown';

/** One review run summarised for the PR tab. */
export interface ReviewRunSummary {
  id: string;
  status: RunStatus;
  review_output: string;
  review_posted_at?: string | null;
  created_at: string;
  /** Display name of the user who requested the review (empty for a service run). */
  triggered_by_display_name?: string;
}

/**
 * GET /api/v1/runs/{id}/pr — the run's pull request, its live state, and the
 * review runs targeting it (newest first). state is queried live from the
 * provider and degrades to "unknown" rather than failing.
 */
export interface PrInfo {
  url: string;
  state: PrState | string;
  head_branch: string;
  review_runs: ReviewRunSummary[];
}

/** Event types emitted on the run stream. Contract with runner + orchestrator. */
export type RunEventType =
  | 'run.status'
  | 'agent.text'
  | 'agent.tool_call'
  | 'agent.tool_result'
  | 'run.artifact'
  | 'run.failure'
  // run.git (ST-1): runner reports the pushed branch/commit in draft_pr mode.
  | 'run.git'
  // run.result (D18/D26): { outcome: "no_changes" } — a successful run that
  // produced no diff. Rendered as a one-line informational row.
  | 'run.result'
  // run.session (F9b / D23 ①②): the runner established its ACP session
  // ({ acp_session_id, resumed }). Rendered as a low-key system row
  // ("Session established" / "Session resumed").
  | 'run.session'
  // user.message (D22): a follow-up prompt posted to a session run
  // ({ prompt, by }). Rendered as a user chat bubble in the timeline.
  | 'user.message'
  // session.finish (D22): the session was wound down ({ reason: "user" |
  // "idle_timeout", by? }). Rendered as a compact system row.
  | 'session.finish'
  // agent.permission_request (F8b): a permission_mode=approval session hit an
  // agent permission request ({ request_id, tool_call_id, title, options }).
  // Rendered as a PermissionCard with option buttons. May arrive BEFORE the
  // tool_call it references — pair by request_id, never by adjacency.
  | 'agent.permission_request'
  // agent.permission_resolved (F8b): the request's final outcome
  // ({ request_id, option_id, resolution: "user" | "timeout" }).
  | 'agent.permission_resolved';

export interface RunEvent {
  seq: number;
  ts: string;
  type: RunEventType | string;
  payload: RunEventPayload;
}

/**
 * Loose payload union. The orchestrator types payload as map[string]any, so we
 * keep it open and narrow at render time via the helpers in eventModel.ts.
 */
export interface RunEventPayload {
  // run.status
  status?: RunStatus;
  // agent.text
  text?: string;
  // agent.tool_call / agent.tool_result
  tool?: string;
  tool_name?: string;
  call_id?: string;
  args?: unknown;
  input?: unknown;
  result?: unknown;
  output?: string;
  // 11-api.md §4: tool_result carries `ok` (boolean) + `exit_code`; a legacy
  // `is_error` is also tolerated.
  ok?: boolean;
  exit_code?: number;
  is_error?: boolean;
  // run.failure
  // run.failure carries a FailureReason; session.finish (D22) reuses the key
  // with "user" | "idle_timeout" — kept open so both type-check.
  reason?: FailureReason | string;
  message?: string;
  // run.artifact
  kind?: string;
  // run.result (D18/D26): "no_changes" is the only outcome produced today; kept
  // as `string` too so an unrecognized future outcome degrades gracefully
  // instead of narrowing the type and failing to compile against new payloads.
  outcome?: 'no_changes' | string;
  // run.status (ST-1): the draft PR link rides on the status frame so the live
  // header updates without a refetch. run.git carries branch/commit_sha.
  pr_url?: string | null;
  pr_number?: number | null;
  branch?: string;
  commit_sha?: string;
  // user.message / session.finish (D22): the follow-up prompt and its author /
  // the wind-down reason ("user" | "idle_timeout").
  prompt?: string;
  by?: string;
  // run.session (F9b): the established ACP session id and whether it was resumed
  // (session/load) vs freshly created (session/new).
  acp_session_id?: string;
  resumed?: boolean;
  // agent.permission_request / agent.permission_resolved (F8b). `resolution`
  // is "user" | "timeout"; `options` is the offered PermissionOption array.
  request_id?: string;
  tool_call_id?: string;
  title?: string;
  options?: PermissionOption[];
  option_id?: string;
  resolution?: 'user' | 'timeout' | string;
  [key: string]: unknown;
}

export type ArtifactKind = 'diff';

export interface RunArtifact {
  run_id: string;
  kind: ArtifactKind;
  content: string;
  created_at: string;
}

/* ---- system / admin snapshot (11-api.md § "System / admin") -------------- */

/**
 * The read-only cluster-admin snapshot from GET /api/v1/system. It NEVER carries
 * a secret: `provider.gitea_enabled` is a derived boolean (the PAT is set), the
 * token itself is never on the wire. Mirrors the orchestrator systemResponse.
 */
export interface SystemInfo {
  version: { version: string; commit: string };
  capacity: {
    /** MAX_CONCURRENT_RUNS; 0 means unlimited. */
    max_concurrent_runs: number;
    running: number;
    queued: number;
    scheduling: number;
  };
  guardrails: {
    run_timeout_seconds: number;
    job_ttl_seconds: number;
  };
  provider: {
    /** True iff GITEA_TOKEN is set on the orchestrator; the token is never returned. */
    gitea_enabled: boolean;
    gitea_url: string;
    /**
     * D20 / F5: the cluster git-host allowlist an integration may target. Empty
     * => unrestricted. Read-only (the Cluster page shows it); never a secret.
     * Optional so lean fixtures still type-check.
     */
    allowed_git_hosts?: string[];
  };
  runner: {
    image: string;
    /**
     * Feature C (D05): whether the cluster PERSISTENT_WORKSPACE switch is on —
     * services keep a persistent workspace PVC (reused checkout + jcode memory)
     * and runs serialize per service. Optional so lean fixtures still type-check.
     */
    persistent_workspace?: boolean;
  };
  namespace: string;
  /** kubernetes | process | disabled */
  launcher: string;
  /**
   * Auth snapshot (M2/M4): the configured OAuth provider ids and the user count.
   * Never a secret. Optional so older snapshots (and lean test fixtures) still
   * type-check; the Cluster view renders empty state when absent.
   */
  auth?: {
    providers: string[];
    users_count: number;
  };
  /**
   * Feature E — jtype kanban integration snapshot. `enabled` reflects the
   * EFFECTIVE config (D27 resolver: DB override › JTYPE_BASE_URL env › off);
   * base_url is the effective base URL (never the token). Optional so lean
   * fixtures still type-check; the Cluster view renders an "off" state when absent.
   */
  kanban?: {
    enabled: boolean;
    base_url?: string;
    poll_interval?: string;
    // F6 / D25: whether the cluster JTYPE_TOKEN fallback is set. Per-link tokens
    // authorise each link; this is only the fallback (never the token itself).
    cluster_token_set?: boolean;
    /**
     * D27: which layer supplies the effective config — the DB override set from
     * the console, the JTYPE_BASE_URL env fallback, or none (off). Optional so
     * lean fixtures still type-check.
     */
    source?: 'db' | 'env' | 'none';
    /**
     * D27: set (with source=none / enabled=false) when the resolver could not
     * produce an effective config — e.g. a DB token is present but AUTH_TOKEN_KEY
     * isn't. Fail-visible (mirrors systemArchive's reason).
     */
    reason?: string;
  };
}

/* ---- kanban links (Feature E) --------------------------------------------- */

/**
 * One binding of a jtype board column to a project/service. A card dragged into
 * `trigger_column` dispatches an agent run; a finished run is written back as a
 * card comment and (when `done_column` is set) the card is moved there. Mirrors
 * the orchestrator kanbanLinkView.
 *
 * `token_set` (F6 / D25) reports whether the link carries its own encrypted jtype
 * PAT; false means it falls back to the cluster JTYPE_TOKEN. The token itself is
 * never returned. `credential_status` is the server-derived runtime credential
 * state (P1): "missing" means the poller/writeback skip this link fail-visibly
 * until a token is set — the UI must surface it as an error.
 */
export type KanbanCredentialStatus = 'per_link' | 'cluster_fallback' | 'missing';

/**
 * D29 board-validation state (independent of credential_status): whether the
 * link's board_ref/columns have been checked against a live jtype board.
 *  - `ok`          — resolved + columns validated (at create, or by the poller's
 *                    first runtime check). No board is dead-linked.
 *  - `unvalidated` — created without a credential (the bootstrap "soft create"),
 *                    so it has never been checked. The console shows a loud
 *                    "columns not validated" state prompting a token connect.
 *  - `invalid`     — a runtime check RAN and FAILED (board gone/renamed, columns
 *                    changed). The poller is skipping the link — surfaced loudly.
 * Optional so pre-D29 fixtures/rows (which backfill to `ok`) still type-check;
 * the UI treats an absent value as `ok`.
 */
export type KanbanBoardStatus = 'ok' | 'unvalidated' | 'invalid';

export interface KanbanLink {
  id: string;
  workspace_id: string;
  board_ref: string;
  project_id: string;
  service_id: string;
  trigger_column: string;
  done_column?: string;
  enabled: boolean;
  token_set: boolean;
  credential_status: KanbanCredentialStatus;
  /**
   * D29: the board-validation state (see KanbanBoardStatus). Absent = `ok` (a
   * pre-D29 row backfilled by the 0024 migration). Independent of
   * credential_status: a link can be `per_link` yet `invalid` (token fine, board
   * renamed), or `unvalidated` with no credential at all.
   */
  board_status?: KanbanBoardStatus;
  /**
   * D29: the board's human title, captured server-side at validation (the stored
   * board_ref is the opaque `b_…` config id after canonicalization, so the row
   * shows this instead). Absent for a soft-created/unvalidated link whose board
   * hasn't been resolved yet.
   */
  board_title?: string;
  /**
   * D28: when this link's token was minted by the "Connect with jtype" device
   * flow, its 90-day expiry (device-flow tokens carry no refresh). NULL/omitted
   * for a manual PAT / cluster-fallback / no credential (unknown expiry) — the
   * UI shows an expiry badge only when this is set.
   */
  token_expires_at?: string;
  created_at: string;
}

/* ---- kanban discovery pickers (D29) --------------------------------------- */

/**
 * GET /api/v1/projects/{id}/kanban/jtype/workspaces — one of the caller's jtype
 * workspaces, for the create-link workspace picker. The effective token is used
 * server-side and NEVER serialized here.
 */
export interface JtypeWorkspace {
  id: string;
  name: string;
}

/** One column of a jtype board (its stable key + human name). */
export interface JtypeBoardColumn {
  key: string;
  name: string;
}

/**
 * GET /api/v1/projects/{id}/kanban/jtype/boards?workspace=<id> — one `.board`
 * document in a workspace, for the board picker.
 *  - `id`   — the board's `config.id` (`b_…`), the value the server persists as
 *             board_ref after canonicalization (the console never submits it).
 *  - `ref`  — the board document's relativePath (e.g. `jtype.board`) — what the
 *             create request submits; the server resolves + canonicalizes it.
 *  - `title`— the friendly board name shown in the picker.
 *  - `columns` — the board's columns, powering the trigger/done column selects.
 */
export interface JtypeBoard {
  id: string;
  ref: string;
  title: string;
  columns: JtypeBoardColumn[];
}

/* ---- kanban board embed (D31) --------------------------------------------- */

/**
 * GET /api/v1/projects/{id}/kanban/board/links — the reduced, **member+** view
 * of a project's kanban links that gates the "Kanban" header button and feeds
 * the board-embed modal's link selector.
 *
 * Deliberately NOT the owner-only `KanbanLink` (which serializes credential
 * posture — `token_set`, `credential_status`, `token_expires_at`): a
 * member-visible button must not 403 for members nor leak credential state to
 * non-owners. This view therefore carries NO token/credential fields — only the
 * metadata the embed needs to open a board.
 */
export interface BoardEmbedLink {
  id: string;
  workspace_id: string;
  /**
   * The board's `config.id` (`b_…`), as stored on the link after
   * canonicalization — NOT a relativePath. The modal resolves this to the
   * board's `.board` relativePath (via the member+ document proxy) before
   * handing it to `<JTypeBoard boardRef>` (which resolves by name/path).
   */
  board_ref: string;
  board_title?: string;
  board_status?: KanbanBoardStatus;
  service_id: string;
  trigger_column: string;
  done_column?: string;
  enabled: boolean;
}

/* ---- cluster kanban config (D27) ------------------------------------------ */

/**
 * GET /api/v1/system/kanban — the cluster-admin view of the cluster jtype config
 * (base_url + optional cluster fallback token), resolved DB-override › env › off.
 *
 * `base_url` / `token_set` describe the DB OVERRIDE row (base_url is "" and
 * token_set false when there is no DB row); `source` names which layer is
 * effective; the `effective_*` / `cluster_token_set` fields describe the RESOLVED
 * config the poller/writeback actually use (source-coupled — a DB config never
 * borrows the env token, and vice versa). The token itself is NEVER returned.
 * Mirrors the orchestrator kanbanConfigView.
 */
export interface KanbanClusterConfig {
  /** The DB override's base URL ("" when there is no DB row). */
  base_url: string;
  /** Whether the DB override carries an encrypted cluster fallback token. */
  token_set: boolean;
  source: 'db' | 'env' | 'none';
  effective_enabled: boolean;
  effective_base_url: string;
  /** Whether the EFFECTIVE source (per `source`) carries a cluster fallback token. */
  cluster_token_set: boolean;
  poll_interval: string;
  /**
   * D27, fail-visible: set (omitempty) only when the config is BROKEN — e.g. the
   * DB row carries a token but AUTH_TOKEN_KEY is unset, so the resolver refuses
   * to produce an effective config (source=none). The UI must surface it loudly,
   * never render a bare "off" for a misconfiguration.
   */
  reason?: string;
  /**
   * D28: when the cluster fallback token was minted by the "Connect with jtype"
   * device flow, its 90-day expiry. NULL/omitted for a manual PAT / env token /
   * no token (unknown expiry). The token itself is NEVER returned — only whether
   * it is set (token_set) and, if known, when it expires.
   */
  token_expires_at?: string;
}

/**
 * PUT /api/v1/system/kanban body (cluster-admin). `base_url` is required
 * (validated http(s); 400 otherwise). `token` is write-only with three-state
 * presence semantics: OMITTED = leave the stored token unchanged; "" = clear it
 * (links rely on their own tokens); a value = set/rotate it. A token write with
 * no cipher configured is a 409 cipher_not_configured.
 */
export interface UpdateKanbanConfigInput {
  base_url: string;
  token?: string;
}

/**
 * POST /api/v1/projects/{id}/kanban/links body (owner). project_id comes from the
 * path. `token` is the optional per-link jtype PAT — write-only (never echoed);
 * omit it to fall back to the cluster JTYPE_TOKEN.
 */
export interface CreateKanbanLinkInput {
  workspace_id: string;
  board_ref: string;
  service_id: string;
  trigger_column: string;
  done_column?: string;
  token?: string;
}

/* ---- kanban "Connect with jtype" OAuth device flow (D28) ------------------ */

/**
 * The start of a "Connect with jtype" device flow (RFC 8628). Returned by
 * POST …/kanban/connect (cluster) or …/kanban/links/{id}/connect (per-link).
 *
 * `user_code` is the short 6-digit code the user confirms in jtype's browser
 * page; `verification_uri_complete` deep-links there with the code prefilled.
 * The `device_code` (the SECRET that can mint the token) is DELIBERATELY WITHHELD
 * — the orchestrator holds it in-memory keyed by the opaque `connect_id`, and the
 * console only ever polls with `connect_id`. `expires_in` / `interval` are the
 * jtype-advertised flow lifetime and minimum poll cadence (seconds).
 */
export interface KanbanConnectStart {
  connect_id: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  expires_in: number;
  interval: number;
}

/**
 * The state of an in-flight (or just-finished) device flow, returned by
 * GET …/kanban/connect/{connectID}. The console polls this while `pending` and
 * stops on any terminal state:
 *   - `complete`   — the token was minted, sealed server-side into the target
 *                    row, and `token_set` is now true (`token_expires_at` gives
 *                    its 90-day expiry). The plaintext token is NEVER returned.
 *   - `expired`    — the flow lapsed (also how a user *denial* eventually reads,
 *                    since jtype's device page has no explicit Deny) — reconnect.
 *   - `denied`     — defensive: jtype never emits access_denied today, but the
 *                    contract carries it so the console can surface it if it does.
 *   - `unsupported`— this jtype deployment lacks the OAuth device routes; the
 *                    user must paste a token instead (fail-visible fallback).
 * A 404 `connect_expired` on poll (e.g. after an orchestrator restart dropped the
 * in-memory flow) is treated by the UI exactly like `expired`.
 */
export interface KanbanConnectStatus {
  status: 'pending' | 'complete' | 'expired' | 'denied' | 'unsupported';
  token_set: boolean;
  token_expires_at?: string;
}

/* ---- schedules (F11 / D24) ------------------------------------------------ */

/**
 * A service-level cron trigger. On each matching tick the schedule poller
 * dispatches a headless agent run (origin=schedule) against the service, using
 * the service's default model. Mirrors the orchestrator domain.Schedule.
 *
 * `cron_expr` is a standard 5-field expression (minute hour dom month dow).
 * `last_fired_at` is when the poller last claimed a window (null = never).
 * `last_error` (fail-visible, P1) is why the most recent due window was ABANDONED
 * without dispatching — no/ambiguous model, or a git host no longer allowed. The
 * UI surfaces it as a loud badge; it clears on the next successful dispatch.
 */
export interface Schedule {
  id: string;
  service_id: string;
  cron_expr: string;
  prompt: string;
  enabled: boolean;
  last_fired_at?: string | null;
  last_error?: string;
  created_by?: string | null;
  created_at: string;
  updated_at: string;
}

/** POST /api/v1/services/{id}/schedules body (owner). enabled defaults to true. */
export interface CreateScheduleInput {
  cron_expr: string;
  prompt: string;
  enabled?: boolean;
}

/**
 * PATCH /api/v1/schedules/{id} body (owner). Every field optional — omitted =
 * unchanged; a supplied cron_expr is re-validated server-side.
 */
export interface UpdateScheduleInput {
  cron_expr?: string;
  prompt?: string;
  enabled?: boolean;
}

/* ---- integrations (D19 / F5) ---------------------------------------------- */

/**
 * A project-level git host binding with a BOT service credential. A service bound
 * to it performs every git operation as this bot identity (the PR body annotates
 * the real trigger). `token_set` reports whether a sealed token is stored — the
 * token itself is NEVER returned. `bot_username` is the token's account, discovered
 * from the provider at create/rotate time. Mirrors the orchestrator integrationView.
 */
export interface Integration {
  id: string;
  project_id: string;
  name: string;
  provider: GitProvider;
  host: string;
  cred_type: string;
  bot_username: string;
  token_set: boolean;
  created_at: string;
  updated_at: string;
}

/**
 * POST /api/v1/projects/{id}/integrations body (owner). `token` is the write-only
 * bot credential (never echoed). The server verifies it against the provider
 * (discovering bot_username) and validates `host` against the cluster allowlist
 * (400 host_not_allowed). cred_type defaults to "pat".
 */
export interface CreateIntegrationInput {
  name?: string;
  provider: GitProvider;
  host: string;
  cred_type?: string;
  token: string;
}

/**
 * PATCH /api/v1/integrations/{id} body (owner). `name` renames; `token` rotates
 * the credential (re-verified; refreshes bot_username). host/provider are
 * immutable. token is write-only and cannot be cleared (delete to remove).
 */
export interface UpdateIntegrationInput {
  name?: string;
  token?: string;
}

export interface IntegrationsEnvelope {
  integrations: Integration[];
}

export interface ProviderReposEnvelope {
  repos: ProviderRepo[];
}

/* ---- project-scoped API keys (F12 / D24) ---------------------------------- */

/**
 * A project-scoped, revocable automation credential — replaces borrowing the
 * cluster-wide CONSOLE_TOKEN for external/CI use. Authenticates as
 * `Authorization: Bearer <key>` and resolves to a principal capped at the
 * Member role on THIS project only (never another project, never this
 * project's owner-level actions, never the cluster-admin surface, never these
 * apikeys endpoints themselves). `prefix` (e.g. "jck_a1b2") is shown in the
 * list for identification only; the full key is NEVER returned again after
 * creation. Mirrors the orchestrator domain.APIKey / apiKeyView.
 */
export interface ApiKey {
  id: string;
  project_id: string;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at?: string | null;
  revoked_at?: string | null;
}

/**
 * POST /api/v1/projects/{id}/apikeys response — an ApiKey plus the ONE-TIME
 * plaintext `key`. This is the only response that ever carries it; show it
 * once with a copy affordance and never fetch it again (there is no read-back
 * endpoint).
 */
export interface CreateApiKeyResponse extends ApiKey {
  key: string;
}

/** POST /api/v1/projects/{id}/apikeys body (owner). name is required. */
export interface CreateApiKeyInput {
  name: string;
}

export interface ApiKeysEnvelope {
  api_keys: ApiKey[];
}

/* ---- model catalog + project grants (D21) -------------------------------- */

/**
 * GET /api/v1/system/models — one catalog model, the cluster-admin view.
 *
 * The plaintext API key is NEVER returned — only api_key_set. granted_project_ids
 * lists the projects authorized to use this model (managed inline on the Cluster
 * page).
 */
export interface Model {
  id: string;
  name: string;
  base_url: string;
  model_name: string;
  api_key_set: boolean;
  created_at: string;
  updated_at: string;
  updated_by: string;
  granted_project_ids: string[];
}

/** POST /api/v1/system/models body. api_key may be empty (keyless endpoints). */
export interface CreateModelInput {
  name: string;
  base_url: string;
  model_name: string;
  api_key: string;
}

/**
 * PATCH /api/v1/system/models/{id} body. Every field optional (omitted =
 * unchanged). api_key: omitted = unchanged; '' = clear (keyless); a value re-
 * encrypts.
 */
export interface UpdateModelInput {
  name?: string;
  base_url?: string;
  model_name?: string;
  api_key?: string;
}

/**
 * GET /api/v1/projects/{id}/models — a member's view of the models granted to a
 * project. Carries ONLY id/name/model_name (never the base_url or key).
 */
export interface ProjectModel {
  id: string;
  name: string;
  model_name: string;
}

/**
 * The project models response: the granted models plus whether the MODEL_* env
 * fallback is active (empty catalog). configured = models non-empty OR
 * env_fallback — the ModelGate keys off that.
 */
export interface ProjectModels {
  models: ProjectModel[];
  env_fallback: boolean;
}

/* ---- auth / identity (multitenant blueprint §2) -------------------------- */

export interface MeUser {
  id?: string;
  display_name: string;
  avatar_url?: string;
  is_cluster_admin: boolean;
}

export interface MeIdentity {
  provider: string;
  username: string;
}

/**
 * GET /api/v1/me — the current principal. All three principal kinds (user,
 * console-token service, cluster-admin) return 200; only an unauthenticated
 * request 401s. is_service marks the CONSOLE_TOKEN principal.
 */
export interface Me {
  user: MeUser;
  is_service?: boolean;
  identities: MeIdentity[];
}

/** One entry of GET /auth/providers (unauthenticated). */
export interface AuthProviderInfo {
  id: string;
  name: string;
  /** Server route to start the OAuth flow, e.g. /auth/login/gitea. */
  login_url: string;
}

/** One row of GET /api/v1/projects/{id}/members. */
export interface Member {
  user_id: string;
  role: MemberRole;
  display_name: string;
  avatar_url?: string;
  username?: string;
  is_cluster_admin: boolean;
}

/** One result of GET /api/v1/users?q= (add-member picker). */
export interface UserSearchResult {
  id: string;
  display_name: string;
  avatar_url?: string;
  is_cluster_admin: boolean;
}

/* ---- request bodies ------------------------------------------------------ */

/**
 * POST /projects — a project is created empty (name only); repositories are
 * attached afterwards via createService. The orchestrator rejects unknown
 * fields loudly (DisallowUnknownFields), so no legacy repo fields here.
 */
export interface CreateProjectInput {
  name: string;
}

/**
 * PATCH /projects/{id} body. Carries a rename and/or the project guardrails.
 * Presence semantics mirror the server: an OMITTED field is left unchanged; a
 * numeric guardrail sent as `null` clears it back to the cluster default. Reserved
 * system keys in injected_env are rejected server-side (400 reserved_env_key).
 * NOTE: `provider_allowlist` is deliberately NOT typed here (D20 / F5) — the
 * server rejects a PATCH carrying it with 400 deprecated_key.
 */
export interface UpdateProjectInput {
  name?: string;
  max_concurrent_runs?: number | null;
  run_timeout_secs?: number | null;
  injected_env?: Record<string, string>;
  /** Session guardrails (D22) — null clears back to the cluster default. */
  max_live_sessions?: number | null;
  session_idle_timeout_secs?: number | null;
  session_ttl_secs?: number | null;
}

export interface CreateRunInput {
  prompt: string;
  /**
   * The composer's optional model pick (D21). Omitted => the server resolves via
   * the service default / the project's sole granted model. Must be in the
   * project's grant set (else 403 model_not_granted).
   */
  model_id?: string;
  /**
   * D22: start this run as a multi-turn SESSION — it parks in awaiting_input
   * after each turn and accepts follow-up messages. Default false (single-shot).
   */
  session?: boolean;
  /**
   * F8b: "approval" = ask the user before agent actions (the runner forwards
   * every permission request for interactive approval). Only valid together
   * with session: true (the server 400s otherwise). Omitted = full_access.
   */
  permission_mode?: 'approval';
}

/**
 * POST /api/v1/runs/{id}/resume body (F9b / D23 ①②): continue a FINISHED session
 * run in a new run that reloads the same ACP session. Just the first prompt of
 * the resumed conversation — model/session/permission_mode are inherited from
 * the original run server-side.
 */
export interface ResumeRunInput {
  prompt: string;
}

/**
 * PATCH /api/v1/services/{id} body (owner). Currently the console only edits the
 * service's default model. default_model_id presence semantics: omitted =
 * unchanged; '' = clear the default; an id = set (server-validated to be granted).
 */
export interface UpdateServiceInput {
  default_model_id?: string;
}

/**
 * POST /projects/{id}/services (blueprint §4). repo_url is smart-parsed by the
 * server; git_mode defaults readonly. name defaults `default`.
 */
export interface CreateServiceInput {
  name?: string;
  repo_url?: string;
  provider?: GitProvider;
  owner_name?: string;
  git_mode?: GitMode;
  default_branch?: string;
  /** The provider's numeric repo id (from the repo picker) — rename-proof identity. */
  provider_repo_id?: number;
  /**
   * Bind the new service to a project integration (D19 / F5). When set, a MEMBER
   * (not just owner) may create the service, the repo must be reachable by the
   * integration's bot token, and the service's provider comes from the integration.
   */
  integration_id?: string;
}

/** One entry from GET /providers/{id}/repos — the onboarding repo picker. */
export interface ProviderRepo {
  id: number;
  full_name: string;
  description?: string;
  default_branch: string;
  private: boolean;
  html_url?: string;
}

/**
 * POST /projects/{id}/members. Identify the target by user_id OR by
 * {provider, username} (blueprint §2).
 */
export interface AddMemberInput {
  user_id?: string;
  provider?: string;
  username?: string;
  role: MemberRole;
}

/* ---- list envelopes (11-api.md §2: lists are wrapped, not bare arrays) --- */

export interface ProjectsEnvelope {
  projects: Project[];
}
export interface RunsEnvelope {
  runs: Run[];
}
export interface EventsEnvelope {
  events: RunEvent[];
}
export interface ServicesEnvelope {
  services: Service[];
}
export interface MembersEnvelope {
  members: Member[];
}
export interface UsersEnvelope {
  users: UserSearchResult[];
}
export interface AuthProvidersEnvelope {
  providers: AuthProviderInfo[];
}

/** Nested error envelope: { error: { code, message } } (11-api.md §0). */
export type ApiErrorCode =
  | 'bad_request'
  | 'unauthorized'
  | 'not_found'
  | 'conflict'
  | 'internal';

export interface ErrorEnvelope {
  error: { code: ApiErrorCode | string; message: string };
}

/* ---- SSE stream envelope ------------------------------------------------- */

/**
 * A parsed SSE frame from GET /runs/{id}/stream. The server replays events with
 * seq > after_seq, then streams live. `run.status` frames also carry the full
 * run so the client can update the header without an extra GET.
 */
export interface StreamFrame {
  event: RunEventType | string;
  data: RunEvent & { run?: Run };
}
