// Package store is the persistence layer. The Store interface is the seam the
// API and reconciler depend on; PGStore is the pgx/v5-backed implementation.
package store

import (
	"context"
	"errors"

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
	// UpdateRunStatus atomically transitions status and the associated
	// bookkeeping fields. It enforces the domain state machine and returns the
	// updated run. If the transition is illegal it returns ErrInvalidTransition.
	UpdateRun(ctx context.Context, r *domain.Run) error

	// Reconciler queries
	ListRunsByStatus(ctx context.Context, statuses ...domain.RunStatus) ([]domain.Run, error)
	CountActiveRuns(ctx context.Context) (int, error)

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

// ErrInvalidTransition is returned by UpdateRun when a status change is not
// permitted by the domain state machine.
var ErrInvalidTransition = errors.New("invalid run status transition")
