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
	case StatusQueued, StatusScheduling, StatusRunning,
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

// GitProvider identifies the git host the draft-PR flow targets. Gitea first
// (D09); GitHub/GitLab are future work.
type GitProvider string

const (
	// ProviderGitea is the only provider wired in the MVP (decision D09).
	ProviderGitea GitProvider = "gitea"
)

// Project is a repository configuration that runs are created against.
type Project struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	RepoURL       string    `json:"repo_url"`
	DefaultBranch string    `json:"default_branch"`
	CreatedAt     time.Time `json:"created_at"`

	// Git integration (ST-1). GitMode defaults to readonly (diff-only). When
	// draft_pr, Provider/ProviderURL/ProviderRepo point at the git host the
	// orchestrator opens the draft PR on. ProviderRepo is "owner/name".
	GitMode      GitMode     `json:"git_mode"`
	Provider     GitProvider `json:"provider,omitempty"`
	ProviderURL  string      `json:"provider_url,omitempty"`
	ProviderRepo string      `json:"provider_repo,omitempty"`
}

// Run is a single agent invocation against a project.
type Run struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Prompt     string    `json:"prompt"`
	Status     RunStatus `json:"status"`
	Phase      string    `json:"phase,omitempty"`
	Error      string    `json:"error,omitempty"`
	K8sJobName string    `json:"k8s_job_name,omitempty"`
	// RetriedFrom links a retry run to the original run it was created from
	// (PRD J2-S4 / AC-10). Nil for first-attempt runs.
	RetriedFrom *string `json:"retried_from,omitempty"`
	// FailureReason / FailureMessage are set together whenever Status ==
	// StatusFailed. FailureMessage is always human-readable and non-empty on
	// failure (PRD J2-S3 / AC-9).
	FailureReason  FailureReason `json:"failure_reason,omitempty"`
	FailureMessage string        `json:"failure_message,omitempty"`
	Attempt        int           `json:"attempt"`
	CreatedAt      time.Time     `json:"created_at"`
	StartedAt      *time.Time    `json:"started_at,omitempty"`
	FinishedAt     *time.Time    `json:"finished_at,omitempty"`
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

	// TokenHash is the SHA-256 (hex) of the per-run bearer token injected into
	// the Job. Never serialised to API clients.
	TokenHash string `json:"-"`
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
)

// ArtifactKind enumerates the kinds of artifact a run can produce.
type ArtifactKind string

const (
	// ArtifactDiff is the unified diff the runner produced.
	ArtifactDiff ArtifactKind = "diff"
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

// RunArtifact is a blob produced by a run (currently only the diff).
type RunArtifact struct {
	RunID     string       `json:"run_id"`
	Kind      ArtifactKind `json:"kind"`
	Content   string       `json:"content"`
	CreatedAt time.Time    `json:"created_at"`
}
