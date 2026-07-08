package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/store"
)

// fakeKanbanWriter captures MoveCard + AddComment calls (and can fail on demand).
type fakeKanbanWriter struct {
	comments   []commentCall
	moves      []moveCall
	commentErr error
	moveErr    error
}

type commentCall struct {
	ws, docID, body string
}
type moveCall struct {
	ws, docID, status string
}

func (f *fakeKanbanWriter) AddComment(_ context.Context, ws, docID, body string) error {
	if f.commentErr != nil {
		return f.commentErr
	}
	f.comments = append(f.comments, commentCall{ws, docID, body})
	return nil
}

func (f *fakeKanbanWriter) MoveCard(_ context.Context, ws, docID, status string) error {
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moves = append(f.moves, moveCall{ws, docID, status})
	return nil
}

// seedKanbanTerminal sets up a project/service/link + a terminal kanban-origin
// run with a claim, returning the pieces a writeback test asserts against.
func seedKanbanTerminal(t *testing.T, st *store.MemStore, status domain.RunStatus, doneColumn string) (*domain.Run, *domain.KanbanLink, *domain.KanbanClaim) {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	link := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", DoneColumn: doneColumn,
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "p",
		Status: status, Attempt: 1, CreatedAt: time.Now(), Origin: domain.RunOriginKanban}
	if status == domain.StatusFailed {
		run.FailureReason = domain.FailureAgentError
		run.FailureMessage = "boom"
	}
	if status == domain.StatusSucceeded {
		run.PRURL = "http://gitea/pr/1"
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	claim, err := st.EnsureKanbanClaim(ctx, link.ID, "doc1", "cards/x.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKanbanClaimRun(ctx, link.ID, "doc1", run.ID); err != nil {
		t.Fatal(err)
	}
	return run, link, claim
}

func newWritebackRec(st store.Store) *Reconciler {
	cfg := &config.Config{ReconcileInterval: time.Millisecond, MaxConcurrentRuns: 4, OrchBaseURL: "http://orch"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := New(st, k8s.NewFakeLauncher(), cfg, log, nil)
	return rec
}

func TestWritebackSucceededPostsAndMoves(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	run, link, _ := seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	fk := &fakeKanbanWriter{}
	rec := newWritebackRec(st).WithKanban(fk, "http://console")

	rec.Tick(ctx)

	if len(fk.comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(fk.comments))
	}
	if !strings.Contains(fk.comments[0].body, "finished") || !strings.Contains(fk.comments[0].body, run.PRURL) {
		t.Fatalf("succeeded comment = %q", fk.comments[0].body)
	}
	if !strings.Contains(fk.comments[0].body, "http://console/runs/"+run.ID) {
		t.Fatalf("console link missing: %q", fk.comments[0].body)
	}
	if len(fk.moves) != 1 || fk.moves[0].status != "done" {
		t.Fatalf("want move to done, got %+v", fk.moves)
	}
	if fk.moves[0].ws != link.WorkspaceID {
		t.Fatalf("move used wrong workspace: %q", fk.moves[0].ws)
	}
	// writeback_at stamped → second tick is a no-op.
	rec.Tick(ctx)
	if len(fk.comments) != 1 || len(fk.moves) != 1 {
		t.Fatalf("second tick re-wrote; comments=%d moves=%d", len(fk.comments), len(fk.moves))
	}
}

func TestWritebackFailedPostsReasonNoMove(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	run, _, _ := seedKanbanTerminal(t, st, domain.StatusFailed, "done")
	fk := &fakeKanbanWriter{}
	rec := newWritebackRec(st).WithKanban(fk, "http://console")

	rec.Tick(ctx)

	if len(fk.comments) != 1 || !strings.Contains(fk.comments[0].body, "failed") ||
		!strings.Contains(fk.comments[0].body, "boom") || !strings.Contains(fk.comments[0].body, string(run.FailureReason)) {
		t.Fatalf("failed comment = %q", fk.comments[0].body)
	}
	if len(fk.moves) != 0 {
		t.Fatalf("failed run must still move to done when configured; got %+v", fk.moves)
	}
}

func TestWritebackNoDoneColumnSkipsMove(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "") // no done column
	fk := &fakeKanbanWriter{}
	rec := newWritebackRec(st).WithKanban(fk, "")

	rec.Tick(ctx)
	if len(fk.comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(fk.comments))
	}
	if len(fk.moves) != 0 {
		t.Fatalf("no done column => no move; got %+v", fk.moves)
	}
}

// A transient jtype error leaves the claim unmarked so the next tick retries
// (and, having retried, succeeds exactly once).
func TestWritebackRetriesOnTransientError(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	fk := &fakeKanbanWriter{}
	rec := newWritebackRec(st).WithKanban(fk, "")

	fk.moveErr = errors.New("jtype down")
	rec.Tick(ctx) // move fails → nothing committed
	if len(fk.comments) != 0 || len(fk.moves) != 0 {
		t.Fatalf("first tick should commit nothing on move error")
	}
	fk.moveErr = nil
	rec.Tick(ctx) // now it succeeds
	if len(fk.comments) != 1 || len(fk.moves) != 1 {
		t.Fatalf("retry should post+move once; comments=%d moves=%d", len(fk.comments), len(fk.moves))
	}
}

// When the kanban client is nil (integration off) the pass is a silent no-op.
func TestWritebackNilClientNoop(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	rec := newWritebackRec(st) // no WithKanban
	rec.Tick(ctx)                // must not panic / error
}
