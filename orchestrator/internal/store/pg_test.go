package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// pgTestStore connects to a real Postgres when JCLOUD_PG_DSN is set, applies
// migrations, and returns a store scoped to a fresh run. Skips otherwise so
// `go test ./...` stays green without a database.
//
//	JCLOUD_PG_DSN=postgres://jcloud:jcloud@localhost:5432/jcloud?sslmode=disable \
//	    go test ./internal/store/ -run PG -v
func pgTestStore(t *testing.T) (*PGStore, string) {
	t.Helper()
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

	p := &domain.Project{ID: domain.NewID(), Name: "pgtest", RepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	r := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, Prompt: "x", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, p.ID) }) // cascades runs/events
	return st, r.ID
}

func TestPGRunnerSeqAllocationAndDedupe(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	// Runner sends high client seqs; server must renumber from 1.
	stored, err := st.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 900, Type: domain.EventAgentText}, {Seq: 901, Type: domain.EventAgentText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Seq != 1 || stored[1].Seq != 2 {
		t.Fatalf("stored seqs = %+v want 1,2", stored)
	}
	// Replay identical batch: idempotent.
	again, _ := st.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 900, Type: domain.EventAgentText}, {Seq: 901, Type: domain.EventAgentText},
	})
	if len(again) != 0 {
		t.Fatalf("replay inserted %d want 0", len(again))
	}
}

// TestPGConcurrentIngestNoCollision is the real-DB regression test for the seq
// hazard: concurrent runner ingest + internal emission must yield a unique,
// gapless seq log with every accepted event preserved.
func TestPGConcurrentIngestNoCollision(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	const n = 40
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := st.AppendRunnerEvents(ctx, runID, []EventInput{
				{Seq: int64(i + 1), Type: domain.EventAgentText},
			}); err != nil {
				t.Errorf("runner %d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := st.AppendInternalEvent(ctx, runID, domain.EventRunStatus, map[string]any{"i": i}); err != nil {
				t.Errorf("internal %d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	events, err := st.ListEvents(ctx, runID, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2*n {
		t.Fatalf("durable events = %d want %d (collision dropped some)", len(events), 2*n)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("gap/dup at index %d: seq %d", i, e.Seq)
		}
	}
}
