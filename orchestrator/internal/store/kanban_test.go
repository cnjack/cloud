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

// TestKanbanLinkTokenAndByProject covers F6 / D25: the per-link token_enc blob
// roundtrips (stored opaque, TokenSet reflects presence) and ListKanbanLinksByProject
// scopes to one project.
func TestKanbanLinkTokenAndByProject(t *testing.T) {
	ctx := context.Background()
	m, p, svc := newKanbanTestStore(t)
	// A second project + service so ByProject has something to exclude.
	p2 := &domain.Project{ID: domain.NewID(), Name: "other", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, p2); err != nil {
		t.Fatal(err)
	}
	svc2 := &domain.Service{ID: domain.NewID(), ProjectID: p2.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := m.CreateService(ctx, svc2); err != nil {
		t.Fatal(err)
	}

	withTok := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws1", BoardRef: "b1",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", Enabled: true,
		TokenEnc: []byte{0x01, 0x02, 0x03}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	noTok := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws1", BoardRef: "b2",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	otherProj := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws2", BoardRef: "b3",
		ProjectID: p2.ID, ServiceID: svc2.ID, TriggerColumn: "ai", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	for _, l := range []*domain.KanbanLink{withTok, noTok, otherProj} {
		if err := m.CreateKanbanLink(ctx, l); err != nil {
			t.Fatalf("create %s: %v", l.BoardRef, err)
		}
	}

	got, err := m.GetKanbanLink(ctx, withTok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TokenSet() || string(got.TokenEnc) != "\x01\x02\x03" {
		t.Fatalf("token_enc did not roundtrip: set=%v enc=%v", got.TokenSet(), got.TokenEnc)
	}
	if none, _ := m.GetKanbanLink(ctx, noTok.ID); none.TokenSet() {
		t.Fatal("link without token must report TokenSet()=false")
	}

	byProj, err := m.ListKanbanLinksByProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byProj) != 2 {
		t.Fatalf("ByProject want 2 links for p, got %d", len(byProj))
	}
	for _, l := range byProj {
		if l.ProjectID != p.ID {
			t.Fatalf("ByProject leaked a foreign project's link: %+v", l)
		}
	}
}

// TestSetKanbanLinkToken pins the P2 rotation contract: only token_enc changes
// (claims survive a rotation — no re-dispatch), nil clears back to the cluster
// fallback, and an unknown link is ErrNotFound.
func TestSetKanbanLinkToken(t *testing.T) {
	ctx := context.Background()
	m, p, svc := newKanbanTestStore(t)

	link := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := m.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	// A dispatched claim exists BEFORE the rotation.
	if _, err := m.EnsureKanbanClaim(ctx, link.ID, "docA", "cards/a.md"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetKanbanClaimRun(ctx, link.ID, "docA", "run-1"); err != nil {
		t.Fatal(err)
	}

	// Rotate WITH a device-flow expiry (D28): token stored, expiry roundtrips,
	// claims untouched (run_id still stamped).
	exp := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := m.SetKanbanLinkToken(ctx, link.ID, []byte("NEW"), &exp); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	got, _ := m.GetKanbanLink(ctx, link.ID)
	if !got.TokenSet() || string(got.TokenEnc) != "NEW" {
		t.Fatalf("token not rotated: %v", got.TokenEnc)
	}
	if got.TokenExpiresAt == nil || !got.TokenExpiresAt.Equal(exp) {
		t.Fatalf("token_expires_at did not roundtrip: %v want %v", got.TokenExpiresAt, exp)
	}
	claim, _ := m.EnsureKanbanClaim(ctx, link.ID, "docA", "cards/a.md")
	if claim.RunID != "run-1" {
		t.Fatalf("rotation must retain claims; run_id=%q want run-1", claim.RunID)
	}

	// Non-aliasing: mutating the returned copy's blob/expiry must NOT reach the
	// stored row (GetKanbanLink deep-copies, matching GetClusterKanbanConfig).
	alias, _ := m.GetKanbanLink(ctx, link.ID)
	alias.TokenEnc[0] = 'X'
	*alias.TokenExpiresAt = alias.TokenExpiresAt.Add(time.Hour)
	fresh, _ := m.GetKanbanLink(ctx, link.ID)
	if string(fresh.TokenEnc) != "NEW" || !fresh.TokenExpiresAt.Equal(exp) {
		t.Fatalf("GetKanbanLink must deep-copy: enc=%q exp=%v", fresh.TokenEnc, fresh.TokenExpiresAt)
	}

	// Clear: nil token_enc + nil expiry => back to the cluster fallback, expiry NULL.
	if err := m.SetKanbanLinkToken(ctx, link.ID, nil, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = m.GetKanbanLink(ctx, link.ID)
	if got.TokenSet() {
		t.Fatalf("clear did not remove the token: %v", got.TokenEnc)
	}
	if got.TokenExpiresAt != nil {
		t.Fatalf("clear must null token_expires_at, got %v", got.TokenExpiresAt)
	}

	// Unknown link => ErrNotFound.
	if err := m.SetKanbanLinkToken(ctx, "nope", []byte("x"), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown link want ErrNotFound, got %v", err)
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
