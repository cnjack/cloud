package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// seedRun creates a project+run so the event methods (which require the run to
// exist for the FOR UPDATE lock analogue) can operate.
func seedRun(t *testing.T, m *MemStore) string {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", RepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	r := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, Prompt: "x", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := m.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// TestRunnerSeqIsServerAllocated proves the runner's client seq is NOT used as
// the durable seq: the store assigns a monotonic global seq starting at 1.
func TestRunnerSeqIsServerAllocated(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	runID := seedRun(t, m)

	// Runner sends its own seq starting high; the store must renumber from 1.
	stored, err := m.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 100, Type: domain.EventAgentText, Payload: map[string]any{"n": 1}},
		{Seq: 101, Type: domain.EventAgentText, Payload: map[string]any{"n": 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d want 2", len(stored))
	}
	if stored[0].Seq != 1 || stored[1].Seq != 2 {
		t.Fatalf("server seqs = %d,%d want 1,2", stored[0].Seq, stored[1].Seq)
	}
}

// TestRunnerDedupeByClientSeq proves a re-sent batch (same client seq) is a
// no-op and consumes no new global seq.
func TestRunnerDedupeByClientSeq(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	runID := seedRun(t, m)

	batch := []EventInput{
		{Seq: 1, Type: domain.EventAgentText, Payload: map[string]any{"t": "a"}},
		{Seq: 2, Type: domain.EventAgentText, Payload: map[string]any{"t": "b"}},
	}
	if s, _ := m.AppendRunnerEvents(ctx, runID, batch); len(s) != 2 {
		t.Fatalf("first send inserted %d want 2", len(s))
	}
	// Re-send identical batch: idempotent, nothing new.
	if s, _ := m.AppendRunnerEvents(ctx, runID, batch); len(s) != 0 {
		t.Fatalf("replay inserted %d want 0", len(s))
	}
	// Partial overlap: seq 2 dup, seq 3 new -> only 3 inserted.
	s, _ := m.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 2, Type: domain.EventAgentText},
		{Seq: 3, Type: domain.EventAgentText},
	})
	if len(s) != 1 || s[0].Seq != 3 {
		t.Fatalf("partial replay = %+v want one event seq 3", s)
	}
}

// TestNoCollisionRunnerVsInternal is the regression test for the original
// hazard: interleaving runner ingest with internal emission must produce a
// gapless, unique, monotonic seq sequence with NO dropped events.
func TestNoCollisionRunnerVsInternal(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	runID := seedRun(t, m)

	// Interleave: internal, runner(x2), internal, runner.
	if _, err := m.AppendInternalEvent(ctx, runID, domain.EventRunStatus, map[string]any{"status": "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 1, Type: domain.EventAgentText}, {Seq: 2, Type: domain.EventAgentToolCall},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendInternalEvent(ctx, runID, domain.EventRunArtifact, map[string]any{"kind": "diff"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendRunnerEvents(ctx, runID, []EventInput{{Seq: 3, Type: domain.EventAgentToolResult}}); err != nil {
		t.Fatal(err)
	}

	events, _ := m.ListEvents(ctx, runID, 0, 100)
	if len(events) != 5 {
		t.Fatalf("total events = %d want 5 (none dropped)", len(events))
	}
	// seqs must be exactly 1..5, unique and monotonic.
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d want %d (gap/dup)", i, e.Seq, i+1)
		}
	}
}

// TestConcurrentIngestOrderingAndUniqueness hammers the store with concurrent
// runner ingests and internal emissions and asserts the durable log has unique,
// gapless seqs and preserves every accepted event.
func TestConcurrentIngestOrderingAndUniqueness(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	runID := seedRun(t, m)

	const runnerBatches = 50
	const internalEmits = 50

	var wg sync.WaitGroup
	wg.Add(2)

	// Runner goroutine: 50 batches, each with a unique client seq.
	go func() {
		defer wg.Done()
		for i := 0; i < runnerBatches; i++ {
			_, err := m.AppendRunnerEvents(ctx, runID, []EventInput{
				{Seq: int64(i + 1), Type: domain.EventAgentText, Payload: map[string]any{"i": i}},
			})
			if err != nil {
				t.Errorf("runner ingest %d: %v", i, err)
				return
			}
		}
	}()

	// Internal goroutine: 50 status emissions.
	go func() {
		defer wg.Done()
		for i := 0; i < internalEmits; i++ {
			if _, err := m.AppendInternalEvent(ctx, runID, domain.EventRunStatus, map[string]any{"i": i}); err != nil {
				t.Errorf("internal emit %d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	events, _ := m.ListEvents(ctx, runID, 0, 10000)
	want := runnerBatches + internalEmits
	if len(events) != want {
		t.Fatalf("durable events = %d want %d (collisions dropped some)", len(events), want)
	}
	seen := map[int64]bool{}
	for i, e := range events {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
		if e.Seq != int64(i+1) {
			t.Fatalf("non-monotonic/gap at index %d: seq %d", i, e.Seq)
		}
	}
}
