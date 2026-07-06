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

// EventInput is a single event to append (seq assigned by the caller/runner).
type EventInput struct {
	Seq     int64
	Type    string
	Payload map[string]any
}

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
	// ignored. Returns the number of newly-inserted rows.
	AppendEvents(ctx context.Context, runID string, events []EventInput) (int, error)
	// NextEventSeq returns the next unused seq for a run (max(seq)+1, or 1).
	// Used for internally-emitted events such as run.status.
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
