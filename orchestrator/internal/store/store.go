// Package store is the persistence layer. The Store interface is the seam the
// API and reconciler depend on; PGStore is the pgx/v5-backed implementation.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// EventInput is a single event to append. For AppendEvents the Seq is the
// authoritative global seq (caller-assigned). For AppendRunnerEvents the Seq is
// only the runner's client-side sequence number, used as a per-source
// idempotency key; the global seq is allocated server-side.
type EventInput struct {
	Seq     int64
	Type    string
	Payload map[string]any
}

// Event source tags. The global seq is allocated server-side per run; the
// client-supplied number is retained per-source as an idempotency key so a
// runner batch re-send is a no-op and never competes with internal events for
// the shared seq space. See migration 0002 and cloud/docs/11-api.md §5.1.
const (
	// SourceRunner tags events ingested from the runner (its own 1..N stream).
	SourceRunner = "runner"
	// SourceInternal tags orchestrator-emitted events (run.status, run.artifact,
	// run.failure).
	SourceInternal = "internal"
)

// Store is the durable persistence contract for the orchestrator.
type Store interface {
	// Projects
	CreateProject(ctx context.Context, p *domain.Project) error
	GetProject(ctx context.Context, id string) (*domain.Project, error)
	ListProjects(ctx context.Context) ([]domain.Project, error)
	UpdateProject(ctx context.Context, p *domain.Project) error
	DeleteProject(ctx context.Context, id string) error

	// Runs
	CreateRun(ctx context.Context, r *domain.Run) error
	GetRun(ctx context.Context, id string) (*domain.Run, error)
	GetRunByTokenHash(ctx context.Context, tokenHash string) (*domain.Run, error)
	ListRuns(ctx context.Context, projectID string, limit int) ([]domain.Run, error)

	// Run mutators. Each of these re-reads the committed row inside a
	// transaction (SELECT ... FOR UPDATE), validates the state-machine
	// transition against the CURRENT stored status (not a caller snapshot), and
	// applies ONLY the fields it names — so two concurrent writers can never
	// clobber each other's untouched fields (the "stale full-row" lost-update
	// hazard). Each returns the freshly-committed row so the caller never has to
	// write a stale in-memory copy back. An illegal transition returns
	// ErrInvalidTransition; a missing run returns ErrNotFound.

	// ScheduleRun moves a queued run to scheduling and is the ONLY method that
	// writes k8s_job_name and token_hash. phase is set to the given value.
	ScheduleRun(ctx context.Context, id, jobName, tokenHash, phase string) (*domain.Run, error)
	// MarkRunning moves a scheduling run to running, stamping started_at only if
	// it is currently null.
	MarkRunning(ctx context.Context, id, phase string, startedAt time.Time) (*domain.Run, error)
	// MarkSucceeded moves a run to succeeded, stamping finished_at if null.
	MarkSucceeded(ctx context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error)
	// MarkFailed moves a run to failed, stamping finished_at if null. It
	// PRESERVES any already-set failure_reason/failure_message (e.g. a specific
	// runner-reported reason): the given reason/message are written only where
	// the stored value is empty. error mirrors the resulting failure_message.
	MarkFailed(ctx context.Context, id, phase string, reason domain.FailureReason, msg string, finishedAt time.Time) (*domain.Run, error)
	// SetRunnerFailure records a runner-reported failure_reason/message WITHOUT
	// changing status, and only while the run is still non-terminal. It is
	// first-writer-wins: it writes only where the stored fields are empty, so a
	// later generic classification cannot overwrite a specific runner reason and
	// vice-versa. A no-op (already terminal, or fields already set) is not an
	// error. Returns the committed row (possibly unchanged).
	SetRunnerFailure(ctx context.Context, id string, reason domain.FailureReason, msg string) (*domain.Run, error)
	// CancelRun moves a run to canceled and stamps finished_at. It does NOT
	// touch k8s_job_name/token_hash, so the committed row it returns still names
	// the Job the caller must delete.
	CancelRun(ctx context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error)
	// MarkJobCleaned stamps job_cleaned_at once the reconciler has confirmed a
	// terminal run's Job is deleted from the cluster. It KEEPS k8s_job_name —
	// the Job name is part of the run's historical record (audit + e2e
	// verification; see 11-api.md Run schema). Idempotent: an already-set
	// job_cleaned_at is preserved. It does not change status and is a no-op if
	// the run is missing.
	MarkJobCleaned(ctx context.Context, id string) error
	// SetRunGit records the branch/commit the runner pushed (from a run.git
	// event) WITHOUT changing status. First-writer-wins per field: it writes only
	// where the stored value is empty, so a duplicate event is a no-op. It never
	// touches status/pr_url and is a no-op on a missing run. Returns the committed
	// row (possibly unchanged). This is the ONLY writer of git_branch/commit_sha.
	SetRunGit(ctx context.Context, id, branch, commitSHA string) (*domain.Run, error)
	// MarkPRCreated stamps pr_url/pr_number once the orchestrator has opened (or
	// found) the draft PR (ST-1). It is IDEMPOTENT and first-writer-wins: it
	// writes only where pr_url is currently empty, so a retry that raced another
	// tick cannot double-open or clobber an already-recorded PR. It does not
	// change status and is a no-op on a missing run. Returns the committed row.
	MarkPRCreated(ctx context.Context, id, prURL string, prNumber int) (*domain.Run, error)

	// Reconciler queries
	ListRunsByStatus(ctx context.Context, statuses ...domain.RunStatus) ([]domain.Run, error)
	// ListTerminalRunsWithJob returns terminal runs whose Job has not yet been
	// confirmed deleted (k8s_job_name != '' AND job_cleaned_at IS NULL), so the
	// reconciler can reap orphaned Jobs — e.g. a cancel that raced Job creation
	// — exactly once. Reaped runs keep their Job name; MarkJobCleaned is what
	// removes them from this scan.
	ListTerminalRunsWithJob(ctx context.Context) ([]domain.Run, error)
	CountActiveRuns(ctx context.Context) (int, error)
	// CountRunsByStatus returns a per-status count for the given statuses across
	// ALL projects, as a map keyed by status. Statuses not present in the store
	// are reported as 0 (every requested status is present as a key). Read-only;
	// used by the admin system snapshot (GET /api/v1/system).
	CountRunsByStatus(ctx context.Context, statuses ...domain.RunStatus) (map[domain.RunStatus]int, error)
	// ListRunsAwaitingPR returns succeeded runs that have a pushed branch
	// (git_branch <> '') but no PR yet (pr_url = ''), so the reconciler can open
	// the draft PR (ST-1). The project-mode gate (draft_pr) is applied by the
	// reconciler after joining the project. Ordered oldest-first.
	ListRunsAwaitingPR(ctx context.Context) ([]domain.Run, error)

	// Events
	// AppendEvents inserts events idempotently by (run_id, seq); duplicates are
	// ignored. Returns the number of newly-inserted rows. The caller owns the
	// global seq. Retained for internal callers/tests that assign seq directly;
	// production ingest and emission use the server-allocating methods below.
	AppendEvents(ctx context.Context, runID string, events []EventInput) (int, error)
	// AppendRunnerEvents ingests a batch of runner events. Each event's Seq is
	// treated as a per-source idempotency key (SourceRunner): events already
	// present under (run_id, SourceRunner, client_seq) are skipped. New events
	// are assigned the next server-allocated global seq (monotonic per run) so
	// they can never collide with internally-emitted events. Returns the
	// newly-inserted events (with their allocated seq/ts) in insertion order.
	AppendRunnerEvents(ctx context.Context, runID string, events []EventInput) ([]domain.RunEvent, error)
	// AppendInternalEvent appends one orchestrator-emitted event (run.status,
	// run.artifact, run.failure), allocating the next global seq transactionally.
	// It replaces the racy NextEventSeq+AppendEvents pattern. Returns the stored
	// event so the caller can publish it to the live hub.
	AppendInternalEvent(ctx context.Context, runID, typ string, payload map[string]any) (domain.RunEvent, error)
	// NextEventSeq returns the next unused seq for a run (max(seq)+1, or 1).
	// Used by tests; production emitters use AppendInternalEvent which allocates
	// atomically.
	NextEventSeq(ctx context.Context, runID string) (int64, error)
	ListEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]domain.RunEvent, error)

	// Artifacts
	PutArtifact(ctx context.Context, a *domain.RunArtifact) error
	GetArtifact(ctx context.Context, runID string, kind domain.ArtifactKind) (*domain.RunArtifact, error)

	// Lifecycle
	Close()
}

// ErrInvalidTransition is returned by the run mutators when a status change is
// not permitted by the domain state machine.
var ErrInvalidTransition = errors.New("invalid run status transition")
