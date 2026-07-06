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
  | 'timeout';

export interface Project {
  id: string;
  name: string;
  repo_url: string;
  default_branch: string;
  created_at: string;
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
  /** Stretch (ST-1): draft MR link when Gitea export is enabled. */
  mr_url?: string | null;
}

/** Event types emitted on the run stream. Contract with runner + orchestrator. */
export type RunEventType =
  | 'run.status'
  | 'agent.text'
  | 'agent.tool_call'
  | 'agent.tool_result'
  | 'run.artifact'
  | 'run.failure';

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
  [key: string]: unknown;
}

export type ArtifactKind = 'diff';

export interface RunArtifact {
  run_id: string;
  kind: ArtifactKind;
  content: string;
  created_at: string;
}

/* ---- request bodies ------------------------------------------------------ */

export interface CreateProjectInput {
  name: string;
  repo_url: string;
  default_branch: string;
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
