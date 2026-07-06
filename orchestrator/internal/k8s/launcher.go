// Package k8s wraps client-go behind a small JobLauncher interface so the
// reconciler can be unit-tested with a fake and no cluster is required for
// `go test ./...`.
package k8s

import "context"

// JobName returns the deterministic Job name for a run. Deterministic naming is
// what makes CreateJob idempotent across reconcile passes and crashes.
func JobName(runID string) string {
	return "jcloud-run-" + runID
}

// JobState is the reconciler-facing summary of a Job's status, decoupled from
// batchv1 types so the reconciler never imports client-go.
type JobState int

const (
	// JobUnknown: the launcher could not determine state (transient error).
	JobUnknown JobState = iota
	// JobMissing: no Job with the given name exists.
	JobMissing
	// JobPending: Job exists but no pod is active or complete yet.
	JobPending
	// JobRunning: a pod is active.
	JobRunning
	// JobSucceeded: the Job completed successfully.
	JobSucceeded
	// JobFailed: the Job failed (backoffLimit exhausted or pod error).
	JobFailed
	// JobDeadlineExceeded: the Job's activeDeadlineSeconds was exceeded — the
	// anti-zombie timeout guardrail. Distinguished from JobFailed so the
	// reconciler can classify failure_reason=timeout (PRD AC-12).
	JobDeadlineExceeded
)

// String renders a JobState for logs.
func (s JobState) String() string {
	switch s {
	case JobMissing:
		return "missing"
	case JobPending:
		return "pending"
	case JobRunning:
		return "running"
	case JobSucceeded:
		return "succeeded"
	case JobFailed:
		return "failed"
	case JobDeadlineExceeded:
		return "deadline_exceeded"
	default:
		return "unknown"
	}
}

// JobSpec is the reconciler's request to launch a runner Job. It carries only
// the domain-level knobs; the launcher owns the batchv1 translation.
type JobSpec struct {
	// Name is the deterministic Job name (e.g. "jcloud-run-<id>"). Creating a
	// Job with a name that already exists is treated as success (idempotent).
	Name string
	// RunID is stamped into the jcloud.run-id label and the RUN_ID env var.
	RunID string

	// Env is the full environment injected into the runner container. The
	// reconciler assembles the runner-contract vars; see reconciler.jobEnv.
	Env map[string]string

	// TimeoutSeconds maps to activeDeadlineSeconds (anti-zombie guardrail).
	TimeoutSeconds int64
}

// JobLauncher is the cluster-facing seam the reconciler depends on.
type JobLauncher interface {
	// CreateJob creates the runner Job. It MUST be idempotent: if a Job with
	// spec.Name already exists it returns nil (no error), so a reconcile that
	// crashed after creating the Job but before persisting status is safe.
	CreateJob(ctx context.Context, spec JobSpec) error
	// GetJobState returns the current state of the named Job.
	GetJobState(ctx context.Context, name string) (JobState, error)
	// DeleteJob removes the named Job (and its pods via propagation). Deleting a
	// missing Job is not an error.
	DeleteJob(ctx context.Context, name string) error
}
