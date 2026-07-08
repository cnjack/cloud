package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// newKanbanTestStore returns a MemStore seeded with one project + service so the
// kanban_link FKs (memory enforces them loosely) resolve. Reused across the
// kanban store tests.
func newKanbanTestStore(t *testing.T) (*MemStore, *domain.Project, *domain.Service) {
	t.Helper()
	m := NewMemStore()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "kan", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := m.CreateService(ctx, svc); err != nil {
		t.Fatalf("create service: %v", err)
	}
	return m, p, svc
}

func TestKanbanLinkCRUD(t *testing.T) {
	ctx := context.Background()
	m, p, svc := newKanbanTestStore(t)

	l := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: "ws1", BoardRef: "board-a",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", DoneColumn: "done",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := m.CreateKanbanLink(ctx, l); err != nil {
		t.Fatalf("create: %v", err)
	}
	// UNIQUE(workspace_id, board_ref): a second link on the same board conflicts.
	dup := *l
	dup.ID = domain.NewID()
	if err := m.CreateKanbanLink(ctx, &dup); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("dup create want ErrAlreadyExists, got %v", err)
	}
	// A different board on the same workspace is fine.
	dup.BoardRef = "board-b"
	if err := m.CreateKanbanLink(ctx, &dup); err != nil {
		t.Fatalf("create second board: %v", err)
	}

	got, err := m.GetKanbanLink(ctx, l.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DoneColumn != "done" || got.TriggerColumn != "ai" {
		t.Fatalf("get roundtrip = %+v", got)
	}

	all, err := m.ListKanbanLinks(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("list want 2, got %d", len(all))
	}

	enabled, err := m.ListEnabledKanbanLinks(ctx)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if len(enabled) != 2 {
		t.Fatalf("enabled want 2, got %d", len(enabled))
	}

	if err := m.DeleteKanbanLink(ctx, l.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetKanbanLink(ctx, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete want ErrNotFound, got %v", err)
	}
	if err := m.DeleteKanbanLink(ctx, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-delete want ErrNotFound, got %v", err)
	}
}

// TestKanbanClaimSemantics pins the idempotency contract: EnsureKanbanClaim is
// idempotent, SetKanbanClaimRun commits once, and a second dispatch is a no-op.
func TestKanbanClaimSemantics(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newKanbanTestStore(t)

	c1, err := m.EnsureKanbanClaim(ctx, "link1", "docA", "cards/a.md")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if c1.RunID != "" {
		t.Fatalf("fresh claim must have empty RunID, got %q", c1.RunID)
	}
	// Second ensure returns the SAME row (idempotent — no duplicate dispatch).
	c2, err := m.EnsureKanbanClaim(ctx, "link1", "docA", "cards/a.md")
	if err != nil {
		t.Fatalf("ensure2: %v", err)
	}
	if c2.ID != c1.ID {
		t.Fatalf("ensure not idempotent: %s != %s", c2.ID, c1.ID)
	}

	if err := m.SetKanbanClaimRun(ctx, "link1", "docA", "run-1"); err != nil {
		t.Fatalf("set run: %v", err)
	}
	c3, err := m.EnsureKanbanClaim(ctx, "link1", "docA", "cards/a.md")
	if err != nil {
		t.Fatalf("ensure3: %v", err)
	}
	if c3.RunID != "run-1" {
		t.Fatalf("RunID not stamped, got %q", c3.RunID)
	}
	// A second SetKanbanClaimRun must NOT overwrite run-1 (no double-dispatch).
	if err := m.SetKanbanClaimRun(ctx, "link1", "docA", "run-2"); err != nil {
		t.Fatalf("set run2: %v", err)
	}
	c4, _ := m.EnsureKanbanClaim(ctx, "link1", "docA", "cards/a.md")
	if c4.RunID != "run-1" {
		t.Fatalf("RunID overwritten to %q; want run-1", c4.RunID)
	}

	// Notify-once: first stamp returns true, second false.
	at := time.Now().UTC()
	if ok, err := m.MarkKanbanNotConfiguredNotified(ctx, "link1", "docA", at); err != nil || !ok {
		t.Fatalf("first notify want ok=true, got %v %v", ok, err)
	}
	if ok, err := m.MarkKanbanNotConfiguredNotified(ctx, "link1", "docA", at); err != nil || ok {
		t.Fatalf("second notify want ok=false, got %v %v", ok, err)
	}
}

// TestKanbanWritebackScan covers the writeback scan + MarkKanbanWriteback
// idempotency: only terminal runs surface, non-terminal do not, and the marker
// is first-writer-wins.
func TestKanbanWritebackScan(t *testing.T) {
	ctx := context.Background()
	m, p, svc := newKanbanTestStore(t)

	link := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", DoneColumn: "done",
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := m.CreateKanbanLink(ctx, link); err != nil {
		t.Fatalf("create link: %v", err)
	}

	// A succeeded run with a claim → surfaces as pending writeback.
	rOK := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "p",
		Status: domain.StatusSucceeded, Attempt: 1, CreatedAt: time.Now(), Origin: domain.RunOriginKanban}
	if err := m.CreateRun(ctx, rOK); err != nil {
		t.Fatalf("create run ok: %v", err)
	}
	if _, err := m.EnsureKanbanClaim(ctx, link.ID, "docOK", "cards/ok.md"); err != nil {
		t.Fatalf("ensure ok: %v", err)
	}
	if err := m.SetKanbanClaimRun(ctx, link.ID, "docOK", rOK.ID); err != nil {
		t.Fatalf("set run ok: %v", err)
	}

	// A queued (non-terminal) run with a claim → must NOT surface.
	rRun := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "p",
		Status: domain.StatusRunning, Attempt: 1, CreatedAt: time.Now(), Origin: domain.RunOriginKanban}
	if err := m.CreateRun(ctx, rRun); err != nil {
		t.Fatalf("create run running: %v", err)
	}
	if _, err := m.EnsureKanbanClaim(ctx, link.ID, "docRun", "cards/run.md"); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	if err := m.SetKanbanClaimRun(ctx, link.ID, "docRun", rRun.ID); err != nil {
		t.Fatalf("set run running: %v", err)
	}

	pending, err := m.ListKanbanRunsAwaitingWriteback(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Run.ID != rOK.ID {
		t.Fatalf("pending want only the succeeded run, got %+v", pending)
	}
	if pending[0].Link.ID != link.ID || pending[0].Claim.DocumentID != "docOK" {
		t.Fatalf("writeback join miswired: %+v", pending[0])
	}

	// Mark once → true; mark again → false (idempotent, no double-post).
	at := time.Now().UTC()
	if ok, err := m.MarkKanbanWriteback(ctx, link.ID, "docOK", at); err != nil || !ok {
		t.Fatalf("first mark want true, got %v %v", ok, err)
	}
	if ok, err := m.MarkKanbanWriteback(ctx, link.ID, "docOK", at); err != nil || ok {
		t.Fatalf("second mark want false, got %v %v", ok, err)
	}
	// After writeback, the succeeded run drops out of the scan.
	pending2, _ := m.ListKanbanRunsAwaitingWriteback(ctx)
	if len(pending2) != 0 {
		t.Fatalf("after writeback want empty scan, got %d", len(pending2))
	}
}
