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

// WorkspacePVCName is the deterministic PersistentVolumeClaim name for a
// service's persistent workspace (Feature C / D05). Keyed by serviceID (a
// NewID, so it never collides across services), it lets EnsureWorkspacePVC be
// idempotent and DeleteWorkspacePVC target exactly one service's volume.
func WorkspacePVCName(serviceID string) string {
	return "ws-" + serviceID
}

// ArchiveJobName is the deterministic Job name for a service's one-shot
// workspace-archive Job (F10 / D23 ③). Its distinct prefix keeps it from
// colliding with a run's JobName ("jcloud-run-<runID>"), and the deterministic
// name is what makes the archive pass's CreateJob idempotent across ticks.
func ArchiveJobName(serviceID string) string {
	return "jcloud-archive-" + serviceID
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

	// WorkspacePVC, when non-empty, is the name of the service's persistent
	// workspace PVC (Feature C / D05). buildJob then mounts it at /workspace
	// (subPath work/) and $HOME/.jcode (subPath home/), so the git checkout and
	// jcode memory survive across runs of the same service. Empty => the ephemeral
	// path (no volume; the runner clones fresh into an emptyDir-like container FS),
	// exactly the pre-Feature-C behaviour.
	WorkspacePVC string
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

	// EnsureWorkspacePVC idempotently creates the service's persistent workspace
	// PVC (ws-<serviceID>) before its Job is launched (Feature C / D05). It is a
	// no-op when the PVC already exists (AlreadyExists swallowed). projectID is
	// stamped as a label for tenant attribution / cleanup. Launchers without a
	// cluster (process/local) implement it as a no-op — persistence is a
	// Kubernetes-only capability there.
	EnsureWorkspacePVC(ctx context.Context, serviceID, projectID string) error
	// DeleteWorkspacePVC best-effort deletes a service's workspace PVC when the
	// service is deleted (D05 tenant-erasure guardrail). Deleting a missing PVC is
	// not an error.
	DeleteWorkspacePVC(ctx context.Context, serviceID string) error

	// WorkspacePVCExists reports whether the service's workspace PVC currently
	// exists (F10 / D23 ③). The archive pass consults it before launching an
	// archive Job so it never tries to tar a service that has runs but no PVC
	// (e.g. persistent workspace was toggled on only recently). Launchers without a
	// cluster (process/local) return (false, nil): there are no PVCs to archive.
	WorkspacePVCExists(ctx context.Context, serviceID string) (bool, error)
}

// ImagePrewarmer is an OPTIONAL launcher capability (kubernetes launcher only):
// it keeps the runner image cached on every node (a DaemonSet of sleeper pods,
// see prewarm.go) so runs start without a cold pull. Launchers without a
// cluster (process/local) do NOT implement it; callers type-assert and surface
// "not supported" (D14 fail-visible) rather than silently no-op'ing.
type ImagePrewarmer interface {
	// PrewarmRunnerImage (re)asserts the prewarm DaemonSet on the current
	// runner image and restarts its pods, forcing a fresh pull on every node.
	PrewarmRunnerImage(ctx context.Context) error
	// RunnerImagePrewarmStatus reports the prewarm DaemonSet's desired/ready
	// counts, effective image, and last API-triggered sync time. A never-synced
	// state is not an error.
	RunnerImagePrewarmStatus(ctx context.Context) (PrewarmStatus, error)
}
