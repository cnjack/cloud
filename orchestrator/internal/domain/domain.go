// Package domain holds the core entities and the run state machine.
//
// The state machine is adapted from Symphony's SPEC (see
// cloud/docs/05-symphony-and-references.md). Symphony models two machines: a
// claim state machine (Unclaimed -> Claimed -> Running -> RetryQueued ->
// Released) and a run-phase machine (PreparingWorkspace ... Succeeded/Failed).
//
// In this MVP runs are triggered via REST rather than reconciled off a kanban
// board, so we collapse the *claim* machine into the Run.Status field and keep
// the run-phase detail in Run.Phase. Divergences from Symphony are documented in
// cloud/docs/11-api.md and in the reconciler package.
package domain

import (
	"time"
)

// RunStatus is the top-level lifecycle state of a run. It is the durable,
// authoritative state reconciled against the cluster.
type RunStatus string

const (
	// StatusQueued: run created, awaiting the reconciler to schedule a Job.
	// Symphony analogue: claim Unclaimed/Claimed (dispatch-eligible).
	StatusQueued RunStatus = "queued"
	// StatusScheduling: reconciler has created (or is creating) the K8s Job but
	// it has not been observed Running yet. Symphony analogue: run phase
	// PreparingWorkspace..LaunchingAgentProcess.
	StatusScheduling RunStatus = "scheduling"
	// StatusRunning: the Job's pod is active. Symphony analogue: claim Running /
	// run phase StreamingTurn.
	StatusRunning RunStatus = "running"
	// StatusAwaitingInput: a multi-turn SESSION run (D22) finished a turn and is
	// holding its pod, long-polling for the next user message. NON-terminal — the
	// reconciler leaves the still-active Job alone and the SSE stream stays open.
	// Only session runs (Run.Session) ever enter it; a single-shot headless run
	// never does. See docs/14-cloud-v2-design.md §2/§3 and docs/02-decision-log.md
	// D22.
	StatusAwaitingInput RunStatus = "awaiting_input"
	// StatusSucceeded: terminal, Job completed successfully.
	StatusSucceeded RunStatus = "succeeded"
	// StatusFailed: terminal, Job failed / disappeared / errored.
	StatusFailed RunStatus = "failed"
	// StatusCanceled: terminal, operator cancelled the run.
	StatusCanceled RunStatus = "canceled"
	// StatusBlocked: needs human input (Symphony first-class "blocked"). Modeled
	// and rendered by the console's badge system, but in this MVP the fully
	// automatic (full_access) runner never produces it. Included so the enum is
	// complete for the console. See PRD §6 badge table.
	StatusBlocked RunStatus = "blocked"
)

// Valid reports whether s is a recognised status.
func (s RunStatus) Valid() bool {
	switch s {
	case StatusQueued, StatusScheduling, StatusRunning, StatusAwaitingInput,
		StatusSucceeded, StatusFailed, StatusCanceled, StatusBlocked:
		return true
	}
	return false
}

// Terminal reports whether s is a terminal state that the reconciler will not
// move out of.
func (s RunStatus) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	}
	return false
}

// transitions is the adjacency list of allowed status transitions. It is the
// single source of truth for the state machine and is exercised directly by
// table-driven tests.
var transitions = map[RunStatus]map[RunStatus]bool{
	StatusQueued: {
		StatusScheduling: true,
		StatusCanceled:   true,
		StatusFailed:     true, // e.g. project deleted / permanent scheduling error
	},
	StatusScheduling: {
		StatusRunning:   true,
		StatusSucceeded: true, // very fast Jobs may be observed already complete
		StatusFailed:    true,
		StatusCanceled:  true,
	},
	StatusRunning: {
		StatusSucceeded: true,
		StatusFailed:    true,
		StatusCanceled:  true,
		// Session (D22): a turn finished with no pending message → the run holds
		// its pod and waits for the next user message.
		StatusAwaitingInput: true,
	},
	// Session (D22): awaiting_input is a non-terminal holding state. A delivered
	// message resumes it to running; finalize/idle-timeout lets the runner exit
	// (Job succeeds → succeeded); a dead pod or cancel finishes it directly.
	StatusAwaitingInput: {
		StatusRunning:   true,
		StatusSucceeded: true,
		StatusFailed:    true,
		StatusCanceled:  true,
	},
	// terminal states have no outgoing transitions.
	StatusSucceeded: {},
	StatusFailed:    {},
	StatusCanceled:  {},
}

// CanTransition reports whether a run may move from -> to. A no-op transition
// (from == to) is always allowed so reconciliation is idempotent.
func CanTransition(from, to RunStatus) bool {
	if from == to {
		return true
	}
	return transitions[from][to]
}

// GitMode is a project's git-integration mode (stretch goal ST-1; decision D08).
type GitMode string

const (
	// GitModeReadonly is the default: a run ends in a diff artifact only. Nothing
	// is pushed and no PR is opened — today's (J1-J3) behavior.
	GitModeReadonly GitMode = "readonly"
	// GitModeDraftPR: after a successful run with a non-empty diff, the runner
	// pushes an agent/run-<id> branch and the orchestrator opens a DRAFT PR on the
	// configured provider. Never auto-merges, never triggers CI (hard gate, D08).
	GitModeDraftPR GitMode = "draft_pr"
)

// ValidGitMode reports whether m is a recognised git mode.
func ValidGitMode(m GitMode) bool {
	switch m {
	case GitModeReadonly, GitModeDraftPR:
		return true
	}
	return false
}

// GitProvider identifies the git host a service's repo lives on / the draft-PR
// flow targets. Gitea is the only provider whose push + PR flow is wired in M1
// (decision D09); github/gitlab are accepted as classifications now (multitenant
// blueprint §1) and their token/PR flow lands with OAuth in M2.
type GitProvider string

const (
	// ProviderGitea is the self-hosted provider wired end-to-end in the MVP.
	ProviderGitea GitProvider = "gitea"
	// ProviderGitHub classifies github.com repos (push/PR flow arrives in M2).
	ProviderGitHub GitProvider = "github"
	// ProviderGitLab classifies gitlab.com repos (push/PR flow arrives in M2).
	ProviderGitLab GitProvider = "gitlab"
)

// ValidProvider reports whether p is a recognised git provider.
func ValidProvider(p GitProvider) bool {
	switch p {
	case ProviderGitea, ProviderGitHub, ProviderGitLab:
		return true
	}
	return false
}

// RepoKind classifies how a service addresses its repository.
type RepoKind string

const (
	// RepoKindProvider: the repo lives on a known git host and is addressed as
	// "owner/name" via that provider. Required for draft_pr (there must be a
	// provider to open the PR on).
	RepoKindProvider RepoKind = "provider"
	// RepoKindRaw: an opaque clone URL (git://, file://, ssh, or an http(s) URL
	// with no owner/name shape, e.g. the in-cluster git-seed). Read-only; never
	// eligible for draft_pr.
	RepoKindRaw RepoKind = "raw"
)

// ValidRepoKind reports whether k is a recognised repo kind.
func ValidRepoKind(k RepoKind) bool {
	switch k {
	case RepoKindProvider, RepoKindRaw:
		return true
	}
	return false
}

// RunKind distinguishes an ordinary agent run from a PR-review run.
type RunKind string

const (
	// RunKindAgent is the default: an agent invocation that produces a diff /
	// draft PR.
	RunKindAgent RunKind = "agent"
	// RunKindReview is a review run (M5): the agent reviews a PR and produces
	// review_output. Modeled now so the schema is complete; not triggered in M1.
	RunKindReview RunKind = "review"
)

// ValidRunKind reports whether k is a recognised run kind.
func ValidRunKind(k RunKind) bool {
	switch k {
	case RunKindAgent, RunKindReview:
		return true
	}
	return false
}

// RunOrigin records how a run was triggered: the ordinary API/console path, a
// Gitea PR comment `@jcode …` webhook (M7 / blueprint §8), or a jtype kanban
// card dragged into a link's trigger column (Feature E).
type RunOrigin string

const (
	// RunOriginAPI is the default: the run was created via the REST API/console.
	RunOriginAPI RunOrigin = "api"
	// RunOriginWebhook: the run was triggered by a Gitea PR comment webhook. Such
	// runs carry the triggering comment id/url and (for agent tasks) push back onto
	// the PR head branch instead of opening a new draft PR.
	RunOriginWebhook RunOrigin = "webhook"
	// RunOriginKanban: the run was dispatched by the kanban poller (Feature E)
	// after a jtype card entered a kanban_link's trigger column. The triggering
	// document id/path live on the kanban_claims row (not the run), and the
	// reconciler's writeback pass posts the result back as a card comment (and
	// optionally moves the card to the done column).
	RunOriginKanban RunOrigin = "kanban"
	// RunOriginSchedule: the run was dispatched by the schedule poller (F11 / D24)
	// when a service-level cron trigger came due. The triggering schedule id is
	// recorded on the run's initial run.status event (schedule_id) — the runs table
	// itself is untouched (no schedule_id column).
	RunOriginSchedule RunOrigin = "schedule"
)

// ValidRunOrigin reports whether o is a recognised run origin.
func ValidRunOrigin(o RunOrigin) bool {
	switch o {
	case RunOriginAPI, RunOriginWebhook, RunOriginKanban, RunOriginSchedule:
		return true
	}
	return false
}

// Project is a tenant-owned container of services. A project's repository
// configuration lives on its Service(s) — the simple "one repo = one project"
// UX is a project with a single service named "default" (multitenant blueprint
// §0/§1). Guardrail fields are nil/empty when the project inherits the global
// defaults.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`

	// Guardrails (blueprint §1). Nil pointers / empty collections mean "inherit
	// the orchestrator-wide default".
	MaxConcurrentRuns *int              `json:"max_concurrent_runs,omitempty"`
	RunTimeoutSecs    *int64            `json:"run_timeout_secs,omitempty"`
	ProviderAllowlist []string          `json:"provider_allowlist,omitempty"`
	InjectedEnv       map[string]string `json:"injected_env,omitempty"`
	// Session guardrails (D22). Nil = inherit the cluster default
	// (MAX_LIVE_SESSIONS / SESSION_IDLE_TIMEOUT_SECONDS / SESSION_TTL_SECONDS).
	//   MaxLiveSessions       — cap on this project's simultaneously live
	//     (running+awaiting_input) SESSION runs; a new session over the cap stays
	//     queued (fail-visible, mirrors max_concurrent_runs).
	//   SessionIdleTimeoutSecs — how long an awaiting_input run may sit with no new
	//     message before the reconciler finalizes it (idle reclaim).
	//   SessionTTLSecs        — the whole session's wall-clock budget: drives the
	//     runner's RUN_TIMEOUT and the Job's activeDeadlineSeconds for a session run.
	MaxLiveSessions        *int   `json:"max_live_sessions,omitempty"`
	SessionIdleTimeoutSecs *int64 `json:"session_idle_timeout_secs,omitempty"`
	SessionTTLSecs         *int64 `json:"session_ttl_secs,omitempty"`
	// OwnerUserID is the user who created/owns the project (M2). Empty for a
	// project created by a service principal (CONSOLE_TOKEN), which has no user.
	OwnerUserID string `json:"owner_user_id,omitempty"`
}

// Service is a single repository configuration inside a project. Runs are
// created against a service; the service is the source of truth for the repo,
// its default branch and its git mode (blueprint §1).
type Service struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`

	// RepoKind selects which of the addressing fields below is authoritative.
	RepoKind RepoKind `json:"repo_kind"`
	// Provider + RepoOwnerName are set when RepoKind == provider. RepoOwnerName
	// is "owner/name".
	Provider      GitProvider `json:"provider,omitempty"`
	RepoOwnerName string      `json:"repo_owner_name,omitempty"`
	// RawRepoURL is set when RepoKind == raw (an opaque, read-only clone URL).
	RawRepoURL string `json:"raw_repo_url,omitempty"`
	// ProviderRepoID is the provider's numeric repo id, captured when the service
	// is created from the repo picker (migration 0009). It is the rename-proof
	// identity of the repo on the provider; nil for hand-entered/legacy services.
	ProviderRepoID *int64 `json:"provider_repo_id,omitempty"`

	// IntegrationID binds the service to a project-level git Integration (D19 / F5).
	// Non-nil => ALL git operations (clone/push/PR/review) for this service's runs
	// act with the integration's BOT credential, regardless of who triggers them (the
	// PR body annotates the real trigger). Nil => the legacy path: the triggering
	// user's personal OAuth, falling back to the cluster GITEA_TOKEN. The referenced
	// integration always belongs to this service's project (validated on write).
	IntegrationID *string `json:"integration_id,omitempty"`

	DefaultBranch string  `json:"default_branch"`
	GitMode       GitMode `json:"git_mode"`
	// DefaultModelID is the catalog model (D21) runs against this service use when
	// the composer does not pick one. Nil => no default: the project's sole
	// granted model is used, or the composer must choose when several are granted.
	// Always references a model the service's project is granted (validated on
	// write).
	DefaultModelID *string   `json:"default_model_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Run is a single agent invocation against a service.
type Run struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	ServiceID string    `json:"service_id"`
	Prompt    string    `json:"prompt"`
	Status    RunStatus `json:"status"`
	// Kind distinguishes an ordinary agent run from a review run (M5). Defaults
	// to agent.
	Kind       RunKind `json:"kind,omitempty"`
	Phase      string  `json:"phase,omitempty"`
	Error      string  `json:"error,omitempty"`
	K8sJobName string  `json:"k8s_job_name,omitempty"`
	// RetriedFrom links a retry run to the original run it was created from
	// (PRD J2-S4 / AC-10). Nil for first-attempt runs.
	RetriedFrom *string `json:"retried_from,omitempty"`
	// ResumedFrom links a SESSION-resume run to the original (terminal) session
	// run it continues from (F9b / D23 ①②). Semantically a twin of RetriedFrom:
	// nil for every ordinary/first-turn run; set only by POST /runs/{id}/resume,
	// which also copies the original's AcpSessionID into this run (below) so the
	// reconciler can inject RESUME_SESSION_ID at Job-launch. A non-nil ResumedFrom
	// with a non-empty AcpSessionID is what switches the runner into session/load
	// (resume) instead of session/new.
	ResumedFrom *string `json:"resumed_from,omitempty"`
	// TriggeredByUserID is the user who created the run (M2). Nil for a run
	// triggered by a service principal (CONSOLE_TOKEN) or a legacy run; M3's
	// draft-PR push falls back to the global GITEA_TOKEN when it is nil.
	TriggeredByUserID *string `json:"triggered_by_user_id,omitempty"`
	// FailureReason / FailureMessage are set together whenever Status ==
	// StatusFailed. FailureMessage is always human-readable and non-empty on
	// failure (PRD J2-S3 / AC-9).
	FailureReason  FailureReason `json:"failure_reason,omitempty"`
	FailureMessage string        `json:"failure_message,omitempty"`
	// Result is the first-class OUTCOME of a SUCCEEDED run, beyond the bare status
	// (D18). Its only value today is "no_changes": the agent ran to completion but
	// produced an EMPTY diff — a success, not a failure. Nil (serialised as JSON
	// null) for ordinary runs that produced a diff, or that have not reported a
	// result. Set by the runner via the run.result event; never changes status.
	Result     *RunResult `json:"result"`
	Attempt    int        `json:"attempt"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// JobCleanedAt is stamped when the reconciler has confirmed the run's
	// terminal Job was deleted from the cluster. K8sJobName is NEVER cleared —
	// it is part of the run's historical record (audit + e2e verification); this
	// marker is what keeps the cleanup path from re-processing the run.
	JobCleanedAt *time.Time `json:"job_cleaned_at,omitempty"`

	// Draft-PR state (ST-1). GitBranch/CommitSHA are reported by the runner via
	// the run.git event once it has pushed agent/run-<id>. PRURL/PRNumber are
	// stamped by the reconciler once it has opened (or found) the draft PR.
	// All empty for readonly-mode runs.
	GitBranch string `json:"git_branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	PRURL     string `json:"pr_url,omitempty"`
	PRNumber  int    `json:"pr_number,omitempty"`

	// ReviewOutput is the markdown a review run (Kind == review) produced (M5).
	// Empty for agent runs. Reported by the runner via POST /internal/.../review.
	ReviewOutput string `json:"review_output,omitempty"`
	// ReviewPostedAt is stamped once the orchestrator has posted a review run's
	// output as a PR review comment on the provider (idempotency marker; M3
	// reconcile review pass). Nil until posted / for agent runs.
	ReviewPostedAt *time.Time `json:"review_posted_at,omitempty"`

	// PRHeadBranch / PRBaseBranch associate a review run (Kind == review) with the
	// pull request it reviews: the runner diffs PRBaseBranch...PRHeadBranch, and
	// the reconcile review pass finds the target PR by its head branch. Empty for
	// agent runs. Populated when a review run is created (M5); the schema/env
	// plumbing lands in M3 (blueprint §3: review env PR_HEAD/PR_BASE).
	//
	// M7: a webhook @mention AGENT task also sets PRHeadBranch — the existing PR's
	// head branch it must build ON TOP OF and push BACK TO. A non-empty
	// PRHeadBranch on an agent run switches jobEnv into "baseline = PR head" mode
	// (BASE_BRANCH == BRANCH_NAME == this branch) and the reconciler into ff-only
	// update-push mode instead of opening a new draft PR (blueprint §8).
	PRHeadBranch string `json:"pr_head_branch,omitempty"`
	PRBaseBranch string `json:"pr_base_branch,omitempty"`

	// Origin records how the run was triggered (api|webhook; M7 / blueprint §8).
	// Defaults to api. OriginCommentID/URL are set only for webhook runs — the
	// triggering Gitea comment (id is the de-dup key; url backs the console chip).
	Origin           RunOrigin `json:"origin,omitempty"`
	OriginCommentID  string    `json:"origin_comment_id,omitempty"`
	OriginCommentURL string    `json:"origin_comment_url,omitempty"`

	// Session marks a multi-turn SESSION run (D22): the runner keeps one ACP
	// session alive across turns (RUN_SESSION=1), the run parks in awaiting_input
	// between turns, and the user feeds it follow-up messages via
	// POST /runs/{id}/messages. Only kind=agent runs may be sessions; false for
	// every single-shot headless run (behaviour unchanged).
	Session bool `json:"session"`
	// PermissionMode selects how the runner answers jcode's permission requests
	// (F8a/F8b, the D22 permission half). "" = full_access (auto-approve, today's
	// behaviour and the default); PermissionModeApproval = the runner forwards
	// each request to the control plane as an agent.permission_request event and
	// long-polls the user's decision. Only valid on SESSION runs — a headless
	// single-shot has nobody watching to ask — enforced at run creation.
	PermissionMode string `json:"permission_mode,omitempty"`
	// AwaitingSince is stamped when the run enters awaiting_input (a turn finished
	// with no pending message) and cleared when a message resumes it to running.
	// It is the idle-reclaim epoch: the reconciler finalizes a session whose
	// awaiting_since is older than the effective session_idle_timeout. Nil unless
	// currently awaiting_input.
	AwaitingSince *time.Time `json:"awaiting_since,omitempty"`
	// SessionFinalizing is the "wind this session down" flag: set by the finish
	// endpoint or the idle-timeout reconcile pass. Once set, next-prompt answers
	// 410 so the runner exits gracefully (Job succeeds → succeeded). Never unset.
	SessionFinalizing bool `json:"-"`
	// BundleRev / PushedRev drive the per-turn draft-PR push for a session run
	// (D22): handleIngestBundle bumps BundleRev on every bundle upload, and the
	// session-push reconcile pass advances PushedRev to BundleRev once that
	// revision is pushed (first turn opens the PR, later turns ff-update the same
	// branch). BundleRev > PushedRev means "a newer bundle awaits a push".
	BundleRev int64 `json:"-"`
	PushedRev int64 `json:"-"`

	// ModelID is the catalog model this run was dispatched with (D21), chosen by
	// the resolution chain at create time (composer pick → service default →
	// project's sole grant). Nil when the run resolved to the env MODEL_* fallback
	// (empty catalog / local rig) or predates the catalog. Recorded for audit and
	// so the reconciler + LLM reverse proxy materialise the SAME model the
	// composer picked, without re-running the chain.
	ModelID *string `json:"model_id,omitempty"`
	// ModelName is the provider/model NAME snapshotted at dispatch (D21 audit).
	// Unlike ModelID (an FK nulled when the model is deleted), this plain-text
	// snapshot survives model deletion so a run stays traceable to what it ran on.
	// Empty for legacy runs that predate the catalog.
	ModelName string `json:"model_name,omitempty"`

	// AcpSessionID is the ACP session id this run drives (F9b / D23 ①②). It is
	// reported by the runner via the run.session event (first-writer-wins in the
	// store: written only while still empty), so a plain session run picks it up
	// once the session is established. A RESUME run (ResumedFrom set) instead has
	// it PRE-FILLED at creation — copied from the original run — so the reconciler
	// can inject RESUME_SESSION_ID BEFORE this run has emitted its own run.session;
	// the runner then emits the SAME id (resumed=true) and the first-writer-wins
	// store write is a no-op. Empty for a non-session run and for a session run
	// that has not established its session yet.
	AcpSessionID string `json:"acp_session_id,omitempty"`

	// TokenHash is the SHA-256 (hex) of the per-run bearer token injected into
	// the Job. Never serialised to API clients.
	TokenHash string `json:"-"`
}

// RunPushBranch is the branch the orchestrator pushes for a draft_pr AGENT run.
// For an ordinary run it is the deterministic jcode/run-<id> branch a new draft
// PR is opened on. For a webhook @mention task (PRHeadBranch set), it is that
// existing PR's head branch — the run builds on it and the reconciler ff-only
// pushes back to it (blueprint §8, update mode). Runner (BRANCH_NAME) and
// orchestrator (bundle branch + push target) both derive it identically.
func RunPushBranch(run *Run) string {
	if run.Kind == RunKindAgent && run.PRHeadBranch != "" {
		return run.PRHeadBranch
	}
	return RunBranchName(run.ID)
}

// RunBranchName is the deterministic branch the orchestrator pushes for a
// draft_pr agent run: "jcode/run-<first 8 hex of the run id>" (blueprint §3,
// BRANCH_NAME). It is injected into the runner env (BRANCH_NAME) and recomputed
// server-side when the bundle is received and pushed, so runner and orchestrator
// always agree without the runner reporting it.
func RunBranchName(runID string) string {
	short := runID
	if len(short) > 8 {
		short = short[:8]
	}
	return "jcode/run-" + short
}

// RunEvent is one entry in a run's append-only event log. Events are ingested
// from the runner (or emitted internally, e.g. run.status) and are the source
// for the SSE stream. (run_id, seq) is unique and drives idempotent ingest.
type RunEvent struct {
	ID      int64          `json:"-"`
	RunID   string         `json:"-"`
	Seq     int64          `json:"seq"`
	TS      time.Time      `json:"ts"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// RunMessage is one queued follow-up prompt for a multi-turn session run (D22).
// It is the delivery QUEUE the runner drains via GET next-prompt — not the chat
// transcript itself (each message is ALSO appended as a user.message run_event
// for the timeline). Seq is monotonic per run.
//
// Delivery is TWO-PHASE (offer/consume) so a lost next-prompt response can
// never strand a message:
//   - OfferedAt is stamped when a next-prompt poll hands the message to the
//     runner. While offered-but-not-consumed, every re-poll IDEMPOTENTLY
//     re-delivers the SAME message (same id/prompt) — acpdrive only polls
//     between turns, so a re-poll proves the previous response never started a
//     turn (no double-prompt is possible).
//   - ConsumedAt is stamped by the NEXT turn-complete: the turn this message
//     started has finished; only then does the next queued message become
//     offerable.
type RunMessage struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	Seq        int64      `json:"seq"`
	Prompt     string     `json:"prompt"`
	CreatedBy  string     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	OfferedAt  *time.Time `json:"offered_at,omitempty"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
}

// Event type taxonomy. These strings are the contract with the console and the
// runner; see cloud/docs/11-api.md.
const (
	EventRunStatus       = "run.status"
	EventAgentText       = "agent.text"
	EventAgentToolCall   = "agent.tool_call"
	EventAgentToolResult = "agent.tool_result"
	EventRunArtifact     = "run.artifact"
	// EventRunFailure is emitted by the runner to refine the failure
	// classification (payload {reason, message}); the reconciler also emits it
	// when it fails a run from cluster state. See PRD AC-9.
	EventRunFailure = "run.failure"
	// EventRunGit is emitted by the runner after it pushes the agent/run-<id>
	// branch in draft_pr mode (payload {branch, commit_sha}). The orchestrator
	// persists it and uses branch as the idempotency key for PR creation (ST-1).
	EventRunGit = "run.git"
	// EventRunResult is emitted by the runner to record a first-class run OUTCOME
	// (payload {outcome}). The orchestrator persists the outcome on runs.result
	// (see SetRunResult) WITHOUT changing status — the Job's exit code still
	// drives the terminal status. Today the only outcome is "no_changes": an agent
	// run that finished cleanly but produced an empty diff (D18).
	EventRunResult = "run.result"
	// EventRunSession is emitted by the runner (F9a) once an ACP session is
	// established — by session/new (resumed=false) or session/load (resumed=true,
	// a RESUME run). Payload {acp_session_id, resumed}. The ingest hook records
	// acp_session_id on the run (SetRunACPSession, first-writer-wins) so a later
	// resume can reconstruct the session; the console renders a low-key system
	// row. Only session runs (and resumed single-shot runs) emit it. See F9b /
	// D23 ①②.
	EventRunSession = "run.session"
	// EventUserMessage is appended (internal seq) when a user feeds a follow-up
	// message to a session run via POST /runs/{id}/messages (D22). Payload
	// {prompt, by}; rendered as a user bubble in the timeline so the run reads as
	// one continuous conversation (agent replies interleaved).
	EventUserMessage = "user.message"
	// EventSessionFinish is appended (internal seq) when a session is wound down:
	// by the user (finish endpoint) or by the idle-timeout reconcile pass. Payload
	// {reason: "user"|"idle_timeout", by?}. Rendered as a compact system row.
	EventSessionFinish = "session.finish"
	// EventPermissionRequest is emitted by the runner (F8a, SYNCHRONOUSLY —
	// acpdrive only starts polling the decision endpoint after this event is
	// acknowledged) when a permission_mode=approval session hits a jcode
	// permission request. Payload {request_id, tool_call_id, title,
	// options: [{option_id, name, kind}]}. The ingest hook upserts a
	// run_permissions row keyed by request_id; the console renders a
	// PermissionCard. It may arrive BEFORE the tool_call event it references —
	// clients key off request_id, never event adjacency.
	EventPermissionRequest = "agent.permission_request"
	// EventPermissionResolved is emitted by the runner (async, best-effort) once
	// a forwarded permission request reached its outcome. Payload {request_id,
	// option_id, resolution: "user"|"timeout"} — option_id may be "" for a
	// timeout with no reject-kind option (ACP Cancelled). The ingest hook stamps
	// the resolved_* fields (first-writer-wins); after this the decision endpoint
	// answers 410 for the request.
	EventPermissionResolved = "agent.permission_resolved"
)

// PermissionModeApproval is Run.PermissionMode's only non-default value (F8b):
// the runner forwards jcode permission requests for interactive user approval
// instead of auto-approving (""/full_access). Session runs only.
const PermissionModeApproval = "approval"

// PermissionOption is one choice jcode offered for a permission request,
// echoed verbatim through the agent.permission_request event and stored on the
// run_permissions row (JSONB). Kind is jcode's option classification (e.g.
// allow_once / reject_once) — the runner picks a reject-kind one on timeout.
type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// RunPermission is one forwarded permission request of a permission_mode=
// approval session run (F8b) — the durable ledger row behind the approval UI
// and the runner's decision long-poll.
//
// Two independent halves record the request's outcome:
//   - Decided* — the USER's answer (POST /runs/{id}/permission-response):
//     written once, a second answer is a 409.
//   - Resolved* — the RUNNER's final word (agent.permission_resolved): which
//     option actually took effect and whether it was the user's decision
//     ("user") or a timeout-deny ("timeout"). ResolvedOptionID may be "" for a
//     timeout with no reject-kind option offered.
//
// They deliberately differ when a user decision lands after the runner's
// client-side timeout: the decision is recorded but never took effect.
type RunPermission struct {
	RequestID  string             `json:"request_id"`
	RunID      string             `json:"run_id"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	Title      string             `json:"title"`
	Options    []PermissionOption `json:"options"`
	CreatedAt  time.Time          `json:"created_at"`

	DecidedOptionID *string    `json:"decided_option_id,omitempty"`
	DecidedBy       *string    `json:"decided_by,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`

	ResolvedOptionID *string    `json:"resolved_option_id,omitempty"`
	Resolution       *string    `json:"resolution,omitempty"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
}

// Decided reports whether the user has answered this request.
func (p *RunPermission) Decided() bool { return p.DecidedOptionID != nil }

// Resolved reports whether the runner has recorded the request's final outcome
// (after which the decision endpoint answers 410).
func (p *RunPermission) Resolved() bool { return p.ResolvedAt != nil }

// OptionOffered reports whether optionID is one of the options this request
// actually offered — the validation gate for permission-response (a decision
// naming a foreign option is a 400, mirroring acpdrive's own defensive check).
func (p *RunPermission) OptionOffered(optionID string) bool {
	for i := range p.Options {
		if p.Options[i].OptionID == optionID {
			return true
		}
	}
	return false
}

// ArtifactKind enumerates the kinds of artifact a run can produce.
type ArtifactKind string

const (
	// ArtifactDiff is the unified diff the runner produced (text).
	ArtifactDiff ArtifactKind = "diff"
	// ArtifactBundle is the git bundle a draft_pr agent run produced
	// (BASE_BRANCH..BRANCH_NAME), uploaded raw and stored as bytea. The
	// orchestrator fetches it to push the branch on the user's behalf (blueprint
	// §1/§3). Binary — carried in RunArtifact.Bytes, not Content.
	ArtifactBundle ArtifactKind = "bundle"
)

// FailureReason is the machine-readable classification of why a run failed.
// Set on Run.FailureReason whenever Status == StatusFailed. See PRD J2-S3/AC-9.
type FailureReason string

const (
	// FailureCloneFailed: the runner could not clone the repository.
	FailureCloneFailed FailureReason = "clone_failed"
	// FailureSetupFailed: the project setup phase failed.
	FailureSetupFailed FailureReason = "setup_failed"
	// FailureAgentError: the agent errored, or a generic/uncategorised Job
	// failure. This is the fallback when the cluster state alone cannot
	// distinguish clone/setup failures.
	FailureAgentError FailureReason = "agent_error"
	// FailureTimeout: the Job exceeded its activeDeadlineSeconds guardrail.
	FailureTimeout FailureReason = "timeout"
	// FailurePushFailed: in draft_pr mode, the runner produced a diff but could
	// not push the agent/run-<id> branch to the provider (bad token, network,
	// protected branch). The run is failed so the failure is visible rather than
	// silently dropping the push. See ST-1 / decision D08.
	FailurePushFailed FailureReason = "push_failed"
)

// ValidFailureReason reports whether r is a recognised failure reason.
func ValidFailureReason(r FailureReason) bool {
	switch r {
	case FailureCloneFailed, FailureSetupFailed, FailureAgentError, FailureTimeout, FailurePushFailed:
		return true
	}
	return false
}

// RunResult is the machine-readable OUTCOME of a SUCCEEDED run, recorded on
// Run.Result (D18). It is orthogonal to Status: the Job still drives the
// terminal status (an empty-diff run exits 0 → succeeded), while Result
// classifies WHAT that success produced. Left an open string for
// forward-compatible outcomes; only "no_changes" is defined today.
type RunResult string

const (
	// RunResultNoChanges: the agent ran to completion but produced an EMPTY diff
	// (no code changes). The run is a first-class success, not a failure — before
	// D18 the runner exited non-zero here and the run showed failed(agent_error),
	// which was misleading. Nothing is pushed and no diff artifact is uploaded.
	RunResultNoChanges RunResult = "no_changes"
)

// ValidRunResult reports whether r is a recognised run result.
func ValidRunResult(r RunResult) bool {
	switch r {
	case RunResultNoChanges:
		return true
	}
	return false
}

// NoChanges reports whether the run finished with the first-class "no changes"
// outcome (empty diff; D18) rather than producing a diff.
func (r *Run) NoChanges() bool {
	return r.Result != nil && *r.Result == RunResultNoChanges
}

// RunArtifact is a blob produced by a run. Text artifacts (diff) use Content;
// binary artifacts (bundle) use Bytes. Exactly one is populated per kind.
type RunArtifact struct {
	RunID   string       `json:"run_id"`
	Kind    ArtifactKind `json:"kind"`
	Content string       `json:"content"`
	// Bytes carries binary artifact payloads (kind=bundle). Never serialised to
	// API clients (fetched only over the internal RUN_TOKEN path).
	Bytes     []byte    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// Model is one entry in the cluster model catalog (D21): an OpenAI-compatible
// LLM endpoint a cluster admin registered and may grant to projects. It
// supersedes the single-row cluster_model_config — the effective model for a run
// is now resolved per project (see internal/modelcfg). Name is a unique,
// human-facing display label. APIKeyEnc is the AES-256-GCM ciphertext of the API
// key (nil/empty when the endpoint needs no key); the plaintext is NEVER
// serialised to API clients — hence `json:"-"` on the encrypted blob — and the
// base_url/api key are never exposed to non-admins (only to cluster-admins).
type Model struct {
	// ID is the catalog primary key (referenced by services.default_model_id,
	// runs.model_id and model_grants).
	ID string `json:"id"`
	// Name is the unique display name (e.g. "GPT-4o").
	Name string `json:"name"`
	// BaseURL is the OpenAI-compatible base URL (http/https).
	BaseURL string `json:"base_url"`
	// ModelName is the "provider/model" id written into the runner's jcode config.
	ModelName string `json:"model_name"`
	// APIKeyEnc is the encrypted API key (nonce||ciphertext), or nil when absent.
	APIKeyEnc []byte `json:"-"`
	// CreatedAt / UpdatedAt / UpdatedBy record audit metadata (UpdatedBy is a user
	// id or "" for the service principal).
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// APIKeySet reports whether the model carries an (encrypted) API key. Used to
// echo api_key_set to admins without ever exposing the plaintext.
func (m *Model) APIKeySet() bool { return len(m.APIKeyEnc) > 0 }

// CredType classifies an Integration's credential shape (D19). Only PAT is
// implemented this cycle; GithubApp is an accepted-but-inert expansion slot.
type CredType string

const (
	// CredTypePAT is a personal/organization access token (the only kind wired
	// today). Gitea org PAT / GitLab group token / GitHub PAT.
	CredTypePAT CredType = "pat"
	// CredTypeGithubApp is the future GitHub App installation credential. Accepted
	// by the schema CHECK so the column can hold it later, but NOT implemented now.
	CredTypeGithubApp CredType = "github_app"
)

// ValidCredType reports whether c is a recognised credential type.
func ValidCredType(c CredType) bool {
	switch c {
	case CredTypePAT, CredTypeGithubApp:
		return true
	}
	return false
}

// Integration is a project-level git host binding with a BOT service credential
// (D19 / F5). A service bound to it (Service.IntegrationID) performs every git
// operation as this bot identity — never the triggering user's personal OAuth —
// so the credential survives an individual leaving and keeps a single audit
// subject. The PR body annotates the real trigger for traceability (see the
// reconciler). TokenEnc is the AES-256-GCM ciphertext of the service token
// (nonce||ciphertext, AUTH_TOKEN_KEY); the plaintext is NEVER serialised to an API
// client — hence json:"-" — and is decrypted only in the credentials resolver.
type Integration struct {
	// ID is the primary key (referenced by services.integration_id).
	ID string `json:"id"`
	// ProjectID is the owning project (grants cascade-delete with it).
	ProjectID string `json:"project_id"`
	// Name is a human label, UNIQUE within the project (default "default").
	Name string `json:"name"`
	// Provider is the git host kind: gitea | github | gitlab.
	Provider GitProvider `json:"provider"`
	// Host is the git host — a bare host (github.com) or a full base URL
	// (http://gitea.jcloud.svc.cluster.local:3000). Validated against the cluster
	// git-host allowlist (D20) at create time.
	Host string `json:"host"`
	// CredType is the credential shape (only "pat" is wired; see CredType).
	CredType CredType `json:"cred_type"`
	// TokenEnc is the sealed service token (never serialised — json:"-").
	TokenEnc []byte `json:"-"`
	// BotUsername is the token's current user, best-effort discovered from the
	// provider at create time (empty when discovery failed / not yet done).
	BotUsername string `json:"bot_username"`
	// CreatedBy is the user id that created it ("" for the service principal).
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TokenSet reports whether the integration carries a (sealed) token. Always true
// for a stored integration (token_enc is NOT NULL) — kept for symmetry with the
// write-only token convention used across the API views.
func (i *Integration) TokenSet() bool { return len(i.TokenEnc) > 0 }

// KanbanLink binds a jtype board column to a project/service so that a card
// dragged into TriggerColumn dispatches an agent run, and (when DoneColumn is
// set) a finished run's card is moved to DoneColumn after the result is written
// back as a comment (Feature E / architecture "kanban = trigger + writeback").
//
// One cluster-wide jtype PAT (env JTYPE_TOKEN) authorises every link's reads &
// writes; each link names its own WorkspaceID (jtype workspace id) and BoardRef
// (the board's id within `.board`). UNIQUE(WorkspaceID, BoardRef) — one link per
// board; to trigger multiple columns off one board, widen TriggerColumn later.
type KanbanLink struct {
	ID string `json:"id"`
	// WorkspaceID is the jtype workspace id (a uuid) the board lives in.
	WorkspaceID string `json:"workspace_id"`
	// BoardRef is the board id inside the workspace (the `.board` document's
	// board id, e.g. "jcloud-dev"); cards carry it in their frontmatter `board`.
	BoardRef string `json:"board_ref"`
	// ProjectID / ServiceID name the target the poller dispatches runs against.
	// The service's repo + the project's guardrails (Feature B) apply to kanban
	// runs exactly as they do to console/webhook runs.
	ProjectID string `json:"project_id"`
	ServiceID string `json:"service_id"`
	// TriggerColumn is the frontmatter `status` value that dispatches a run
	// (e.g. "ai"). A card already in this column when the poller starts is also
	// dispatched (claims dedup, so restart-safe).
	TriggerColumn string `json:"trigger_column"`
	// DoneColumn, when non-empty, is the status the writeback pass moves a
	// finished run's card to. Empty => the card stays put after the result
	// comment is posted (no auto-move).
	DoneColumn string `json:"done_column,omitempty"`
	// Enabled gates the poller. A disabled link is retained (its claims persist)
	// but never scanned.
	Enabled bool `json:"enabled"`
	// TokenEnc is the per-link jtype PAT, AES-256-GCM sealed (nonce||ciphertext)
	// with AUTH_TOKEN_KEY — the same scheme the model catalog uses (D25 / F6).
	// nil => the poller/writeback fall back to the cluster JTYPE_TOKEN env. Never
	// serialized (write-only via the API; the view echoes only TokenSet()).
	TokenEnc  []byte    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TokenSet reports whether the link carries a per-link (encrypted) jtype PAT.
// Used to echo token_set to owners without ever exposing the plaintext; false
// means the link falls back to the cluster JTYPE_TOKEN env.
func (l KanbanLink) TokenSet() bool { return len(l.TokenEnc) > 0 }

// KanbanClaim is the idempotency row for kanban-triggered dispatch (Feature E).
// UNIQUE(LinkId, DocumentID) means a given card is dispatched at most once per
// link: the poller Ensures a claim row the first time it sees the card in the
// trigger column, then — only while RunID is empty — dispatches a run and stamps
// RunID. Restart-safe: a claim with a non-empty RunID is never re-dispatched.
//
// RunID is NULL until dispatch succeeds. This doubles as the "card seen" marker:
// when the LLM is not configured (fail-visible gate), the poller leaves RunID
// NULL, posts ONE "LLM not configured" comment (NotifiedNotConfiguredAt), and
// retries on subsequent ticks — so the moment an admin configures the model the
// pending card auto-dispatches, with no re-spam.
type KanbanClaim struct {
	ID string `json:"id"`
	// LinkID + DocumentID identify the card within a link (the jtype document id
	// of the `.md` card).
	LinkID     string `json:"link_id"`
	DocumentID string `json:"document_id"`
	// DocumentPath is the card's relative path in the workspace (e.g.
	// "cards/add-health-banner.md"), captured at claim time for logging and to
	// spare a list round-trip on writeback.
	DocumentPath string `json:"document_path,omitempty"`
	// RunID is the dispatched run's id. Empty until dispatch commits; the writeback
	// pass joins claims→runs on it.
	RunID string `json:"run_id,omitempty"`
	// NotifiedNotConfiguredAt is stamped once when the poller posted an
	// "LLM not configured" comment for this card (throttle: at most one such
	// notice per card). Nil otherwise.
	NotifiedNotConfiguredAt *time.Time `json:"notified_not_configured_at,omitempty"`
	// WritebackAt is stamped once the reconciler has posted the result comment
	// (and moved the card, if configured). Nil until then; the writeback scan
	// reads "claim with a terminal run and WritebackAt nil".
	WritebackAt *time.Time `json:"writeback_at,omitempty"`
	ClaimedAt   time.Time  `json:"claimed_at"`
}

// Schedule is a service-level cron trigger (F11 / D24). On each matching cron
// tick the schedule poller dispatches a headless agent run (origin=schedule)
// against the service, with the service's default model (the F4/D21 resolution
// chain) and Prompt as the task. It mirrors the kanban poller's poll/idempotency
// philosophy: level-based, restart-safe, and driven off DB state (LastFiredAt)
// so no external cron daemon is needed.
type Schedule struct {
	ID string `json:"id"`
	// ServiceID is the target service; the run inherits its repo, project
	// guardrails and default model exactly as a console/kanban run does. The
	// project is derived from the service at dispatch time (no denormalized
	// project_id column — the poller loads the service anyway for its model/host
	// gates).
	ServiceID string `json:"service_id"`
	// CronExpr is a standard 5-field cron expression (minute hour dom month dow;
	// no seconds, no @descriptors). Validated at create/update; a schedule whose
	// next two fires are closer than the min-interval guard is rejected.
	CronExpr string `json:"cron_expr"`
	// Prompt is the task handed to the agent on each fire (the run's prompt).
	Prompt string `json:"prompt"`
	// Enabled gates the poller. A disabled schedule is retained but never scanned.
	Enabled bool `json:"enabled"`
	// LastFiredAt is the instant the poller last CLAIMED a due window for this
	// schedule (nil => never fired; the first Next() is computed from CreatedAt).
	// It is advanced with a conditional UPDATE (WHERE last_fired_at IS NOT DISTINCT
	// FROM $old) so two poller instances cannot double-dispatch a single window,
	// and it is advanced to the CURRENT time — a restart never backfills the
	// windows missed while the process was down.
	LastFiredAt *time.Time `json:"last_fired_at,omitempty"`
	// LastError, when non-empty, is why the most recent due window was ABANDONED
	// without dispatching: the model gate failed (no/ambiguous model) or the
	// service's integration host is no longer cluster-allowed. Fail-visible
	// (CLAUDE.md red line #1): the window is still advanced (not retried forever)
	// AND the owner sees the reason in the console. Cleared on the next successful
	// dispatch.
	LastError string `json:"last_error,omitempty"`
	// CreatedBy is the user who created the schedule (nil for the service
	// principal). Audit trail for the automated runs it dispatches.
	CreatedBy *string   `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
