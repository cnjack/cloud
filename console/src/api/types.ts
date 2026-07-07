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
  created_at: string;
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
}

/** How a run was triggered (blueprint §8). Absent is treated as `api`. */
export type RunOrigin = 'api' | 'webhook';

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
  /**
   * Auth snapshot (M2/M4): the configured OAuth provider ids and the user count.
   * Never a secret. Optional so older snapshots (and lean test fixtures) still
   * type-check; the Cluster view renders empty state when absent.
   */
  auth?: {
    providers: string[];
    users_count: number;
  };
}

/* ---- cluster model config (Feature A) ------------------------------------ */

/**
 * GET /api/v1/system/model — the effective LLM configuration.
 *
 * Any logged-in principal sees `configured`; only a cluster-admin additionally
 * sees source/base_url/model_name/api_key_set. The plaintext API key is NEVER
 * returned — only whether one is set (api_key_set). source is where the
 * effective config came from: an admin-set DB row, the MODEL_* env fallback, or
 * "none" when nothing is configured (the fail-visible state).
 */
export interface ModelConfigInfo {
  configured: boolean;
  source?: 'db' | 'env' | 'none' | string;
  base_url?: string;
  model_name?: string;
  api_key_set?: boolean;
}

/**
 * PUT /api/v1/system/model body (cluster-admin only). api_key may be empty for
 * keyless OpenAI-compatible endpoints. base_url must be http(s); model_name must
 * be "provider/model".
 */
export interface PutModelConfigInput {
  base_url: string;
  model_name: string;
  api_key: string;
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
 * PATCH /projects/{id} body. A project rename is the only project-level edit;
 * repo config changes go through the service endpoints instead.
 */
export interface UpdateProjectInput {
  name?: string;
}

export interface CreateRunInput {
  prompt: string;
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
