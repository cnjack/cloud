// Package reconciler holds the idempotent control loop that drives runs toward
// their desired state by creating/observing/deleting K8s Jobs.
//
// Design (mirrors a Kubernetes controller; see docs D06):
//   - The loop is tick-driven (interval from config).
//   - Every decision is derived purely from DB state + observed cluster state,
//     so a crash is safe: on restart the loop re-derives everything.
//   - The pure decision function `decide` is separated from side effects so it
//     is exhaustively unit-tested with a table of cases and no store/cluster.
//
// Divergences from Symphony (documented for the record):
//   - Retry creates a NEW run linked via retried_from, rather than an in-place
//     RetryQueued->Running transition on the same claim. Simpler to reason about
//     with a Job-per-run model and a REST trigger source.
//   - Auto-retry with the Symphony backoff formula is NOT wired in this MVP
//     (retries are operator-triggered). The backoff value and attempt counter
//     are carried forward so a future auto-retry reconciler can enforce it.
package reconciler

import (
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
)

// Action is the side effect the reconciler should perform for one run.
type Action int

const (
	// ActionNone: nothing to do this tick.
	ActionNone Action = iota
	// ActionCreateJob: create the runner Job and move to scheduling.
	ActionCreateJob
	// ActionMarkRunning: the Job's pod is active; move to running.
	ActionMarkRunning
	// ActionMarkSucceeded: the Job completed; move to succeeded.
	ActionMarkSucceeded
	// ActionMarkFailed: the Job failed/missing/deadline; move to failed.
	ActionMarkFailed
	// ActionDeleteJob: delete the Job (used when finalising a cancel).
	ActionDeleteJob
)

// String renders an Action for logs/tests.
func (a Action) String() string {
	switch a {
	case ActionCreateJob:
		return "create_job"
	case ActionMarkRunning:
		return "mark_running"
	case ActionMarkSucceeded:
		return "mark_succeeded"
	case ActionMarkFailed:
		return "mark_failed"
	case ActionDeleteJob:
		return "delete_job"
	default:
		return "none"
	}
}

// Decision is the output of the pure decision function.
type Decision struct {
	Action        Action
	FailureReason domain.FailureReason // set when Action == ActionMarkFailed
	FailureMsg    string               // human-readable, non-empty on failure
}

// decide computes the action for a single run given the observed Job state and
// whether there is capacity to schedule new work. It is pure: no I/O.
//
//   - jobState is only meaningful once the run has a Job (scheduling/running);
//     for queued runs it is ignored.
//   - hasCapacity gates queued -> scheduling only.
func decide(run domain.Run, jobState k8s.JobState, hasCapacity bool) Decision {
	switch run.Status {
	case domain.StatusQueued:
		if hasCapacity {
			return Decision{Action: ActionCreateJob}
		}
		return Decision{Action: ActionNone}

	case domain.StatusScheduling:
		switch jobState {
		case k8s.JobRunning:
			return Decision{Action: ActionMarkRunning}
		case k8s.JobSucceeded:
			return Decision{Action: ActionMarkSucceeded}
		case k8s.JobFailed:
			return Decision{Action: ActionMarkFailed,
				FailureReason: domain.FailureAgentError,
				FailureMsg:    "runner Job failed before reporting progress"}
		case k8s.JobDeadlineExceeded:
			return Decision{Action: ActionMarkFailed,
				FailureReason: domain.FailureTimeout,
				FailureMsg:    "run exceeded its time limit (activeDeadlineSeconds) before starting"}
		case k8s.JobMissing:
			// We believe we created a Job but it is gone: treat as failure.
			return Decision{Action: ActionMarkFailed,
				FailureReason: domain.FailureAgentError,
				FailureMsg:    "runner Job disappeared while scheduling"}
		default: // JobPending, JobUnknown
			return Decision{Action: ActionNone}
		}

	case domain.StatusRunning:
		switch jobState {
		case k8s.JobSucceeded:
			return Decision{Action: ActionMarkSucceeded}
		case k8s.JobFailed:
			return Decision{Action: ActionMarkFailed,
				FailureReason: domain.FailureAgentError,
				FailureMsg:    "runner Job failed"}
		case k8s.JobDeadlineExceeded:
			return Decision{Action: ActionMarkFailed,
				FailureReason: domain.FailureTimeout,
				FailureMsg:    "run exceeded its time limit (activeDeadlineSeconds)"}
		case k8s.JobMissing:
			return Decision{Action: ActionMarkFailed,
				FailureReason: domain.FailureAgentError,
				FailureMsg:    "runner Job disappeared while running"}
		default: // JobRunning, JobPending, JobUnknown
			return Decision{Action: ActionNone}
		}

	default:
		// Terminal (succeeded/failed/canceled) or blocked: reconciler leaves it.
		return Decision{Action: ActionNone}
	}
}
