package store

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// seedSvcWithRun creates a project + service + one run at runAge ago with the
// given status, returning the service id. Used to exercise ListArchiveCandidates.
func seedSvcWithRun(t *testing.T, st *MemStore, status domain.RunStatus, runAge time.Duration) string {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x", DefaultBranch: "main",
		GitMode: domain.GitModeReadonly, CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "x",
		Status: status, Attempt: 1, CreatedAt: time.Now().Add(-runAge)}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	return svc.ID
}

func TestMemArchiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc := seedSvcWithRun(t, st, domain.StatusSucceeded, 30*24*time.Hour)

	// Fresh service: not archived.
	got, err := st.GetService(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt != nil || got.ArchiveKey != "" {
		t.Fatalf("new service should not be archived: %+v", got)
	}

	// Mark archived.
	at := time.Now().Truncate(time.Second)
	if err := st.MarkServiceArchived(ctx, svc, "workspaces/"+svc+".tar.zst", at); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetService(ctx, svc)
	if got.ArchivedAt == nil || !got.ArchivedAt.Equal(at) {
		t.Fatalf("ArchivedAt = %v, want %v", got.ArchivedAt, at)
	}
	if got.ArchiveKey != "workspaces/"+svc+".tar.zst" {
		t.Fatalf("ArchiveKey = %q", got.ArchiveKey)
	}

	// Clear.
	if err := st.ClearServiceArchive(ctx, svc); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetService(ctx, svc)
	if got.ArchivedAt != nil || got.ArchiveKey != "" {
		t.Fatalf("archive not cleared: %+v", got)
	}
	// Clearing again is idempotent.
	if err := st.ClearServiceArchive(ctx, svc); err != nil {
		t.Fatalf("clear should be idempotent, got %v", err)
	}
}

func TestMemArchiveMissingService(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	if err := st.MarkServiceArchived(ctx, "nope", "k", time.Now()); err != ErrNotFound {
		t.Fatalf("MarkServiceArchived(missing) = %v, want ErrNotFound", err)
	}
	if err := st.ClearServiceArchive(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("ClearServiceArchive(missing) = %v, want ErrNotFound", err)
	}
}

func TestMemListArchiveCandidates(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	idle := seedSvcWithRun(t, st, domain.StatusSucceeded, 30*24*time.Hour) // eligible
	recent := seedSvcWithRun(t, st, domain.StatusSucceeded, 1*24*time.Hour) // too recent
	live := seedSvcWithRun(t, st, domain.StatusRunning, 30*24*time.Hour)     // idle age but live run
	noRuns := func() string {                                               // no runs => never a candidate
		p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
		_ = st.CreateProject(ctx, p)
		s := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
			RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x", DefaultBranch: "main",
			GitMode: domain.GitModeReadonly, CreatedAt: time.Now()}
		_ = st.CreateService(ctx, s)
		return s.ID
	}()

	idleBefore := time.Now().Add(-14 * 24 * time.Hour)
	cands, err := st.ListArchiveCandidates(ctx, idleBefore)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range cands {
		got[c.ServiceID] = true
	}
	if !got[idle] {
		t.Error("idle service should be a candidate")
	}
	if got[recent] {
		t.Error("recently-active service should not be a candidate")
	}
	if got[live] {
		t.Error("service with a live run should not be a candidate")
	}
	if got[noRuns] {
		t.Error("service with no runs should not be a candidate")
	}

	// Archiving the idle one removes it from the candidate set.
	if err := st.MarkServiceArchived(ctx, idle, "k", time.Now()); err != nil {
		t.Fatal(err)
	}
	cands, _ = st.ListArchiveCandidates(ctx, idleBefore)
	for _, c := range cands {
		if c.ServiceID == idle {
			t.Error("already-archived service must not be a candidate")
		}
	}
}
