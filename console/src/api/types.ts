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

/** The only provider in the MVP (11-api.md §1.1, decision D09). */
export type GitProvider = 'gitea';

export interface Project {
  id: string;
  name: string;
  repo_url: string;
  default_branch: string;
  created_at: string;
  /** ST-1 git integration. Absent is treated as `readonly` by the UI. */
  git_mode?: GitMode;
  /** Provider for draft_pr; `gitea` only in the MVP. Empty for readonly. */
  provider?: GitProvider | '' | string;
  /** Gitea base URL for draft_pr (optional; orchestrator falls back to GITEA_URL). */
  provider_url?: string;
  /** `owner/name` on the provider; required when git_mode=draft_pr. */
  provider_repo?: string;
}

export interface Run {
  id: string;
  project_id: string;
  prompt: string;
  status: RunStatus;
  /** Fine-grained run-phase detail (e.g. PreparingWorkspace). Optional. */
  phase?: string;
  /** Low-level error string; failure_message is the human-readable one. */
  error?: string;
  k8s_job_name?: string;
  /** Set on retries: the run id this was cloned from (PRD J2-S4 / AC-10). */
  retried_from?: string | null;
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
  | 'run.git';

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
  reason?: FailureReason;
  message?: string;
  // run.artifact
  kind?: string;
  // run.status (ST-1): the draft PR link rides on the status frame so the live
  // header updates without a refetch. run.git carries branch/commit_sha.
  pr_url?: string | null;
  pr_number?: number | null;
  branch?: string;
  commit_sha?: string;
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
  };
  runner: { image: string };
  namespace: string;
  /** kubernetes | process | disabled */
  launcher: string;
}

/* ---- request bodies ------------------------------------------------------ */

export interface CreateProjectInput {
  name: string;
  repo_url: string;
  default_branch: string;
  /**
   * ST-1 git integration (11-api.md §2.1). Omit for readonly (diff-only). When
   * git_mode=draft_pr the orchestrator requires provider_repo (owner/name) and
   * defaults provider to `gitea`. provider_url is optional.
   */
  git_mode?: GitMode;
  provider?: GitProvider;
  provider_url?: string;
  provider_repo?: string;
}

/**
 * PATCH /projects/{id} body (11-api.md §2.1). All fields optional — only the
 * ones provided are updated. The orchestrator ignores empty strings, so the UI
 * sends only the fields the operator actually changed.
 */
export interface UpdateProjectInput {
  name?: string;
  repo_url?: string;
  default_branch?: string;
  git_mode?: GitMode;
  provider?: GitProvider;
  provider_url?: string;
  provider_repo?: string;
}

export interface CreateRunInput {
  prompt: string;
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
