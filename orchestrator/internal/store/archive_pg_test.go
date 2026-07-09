package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// TestPGArchiveRoundTrip exercises the F10 archive columns end-to-end on real
// Postgres: the archived_at/archive_key round-trip through the shared service
// SELECT columns, MarkServiceArchived/ClearServiceArchive, and
// ListArchiveCandidates' idle/live/archived filters. Requires JCLOUD_PG_DSN.
func TestPGArchiveRoundTrip(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed store test")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	p := &domain.Project{ID: domain.NewID(), Name: "arch-pg", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, p.ID) })

	mkSvc := func() string {
		s := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: domain.NewID(),
			RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
		if err := st.CreateService(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	mkRun := func(svc string, status domain.RunStatus, age time.Duration) {
		r := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc, Prompt: "x",
			Status: status, Attempt: 1, CreatedAt: time.Now().Add(-age)}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	idle := mkSvc()
	mkRun(idle, domain.StatusSucceeded, 30*24*time.Hour)
	recent := mkSvc()
	mkRun(recent, domain.StatusSucceeded, 1*24*time.Hour)
	live := mkSvc()
	mkRun(live, domain.StatusRunning, 30*24*time.Hour)

	idleBefore := time.Now().Add(-14 * 24 * time.Hour)
	cands, err := st.ListArchiveCandidates(ctx, idleBefore)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, c := range cands {
		set[c.ServiceID] = true
	}
	if !set[idle] {
		t.Error("idle service should be a candidate")
	}
	if set[recent] || set[live] {
		t.Error("recent/live services must not be candidates")
	}

	// Mark archived => round-trips through GetService and leaves the candidate set.
	at := time.Now().UTC().Truncate(time.Millisecond)
	key := "workspaces/" + idle + ".tar.zst"
	if err := st.MarkServiceArchived(ctx, idle, key, at); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetService(ctx, idle)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt == nil || got.ArchiveKey != key {
		t.Fatalf("archive not persisted: archived_at=%v key=%q", got.ArchivedAt, got.ArchiveKey)
	}
	cands, _ = st.ListArchiveCandidates(ctx, idleBefore)
	for _, c := range cands {
		if c.ServiceID == idle {
			t.Error("archived service must drop out of the candidate set")
		}
	}

	// Clear => GetService shows unarchived again.
	if err := st.ClearServiceArchive(ctx, idle); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetService(ctx, idle)
	if got.ArchivedAt != nil || got.ArchiveKey != "" {
		t.Fatalf("archive not cleared: %+v", got)
	}
}

// TestPGMigration0021Idempotent re-applies the migration set twice and asserts
// the archive columns exist and a second apply is a clean no-op.
func TestPGMigration0021Idempotent(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed migration test")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Re-apply: no-op (schema_migrations gate + IF NOT EXISTS).
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("second migrate should be a no-op, got: %v", err)
	}
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name='services' AND column_name IN ('archived_at','archive_key')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected archived_at + archive_key columns, found %d", n)
	}
}
