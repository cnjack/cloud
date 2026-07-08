package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// assertConcurrentOfferSingleWinner drives N truly-concurrent OfferNextMessage
// calls (real goroutines) against a queue holding at least TWO messages and
// asserts the C1 invariant on any Store implementation: every call returns the
// SAME message (the oldest), exactly one call is the fresh offer, and no second
// message is ever put in flight. Shared by the MemStore test below and the
// PG-gated test in session_pg_test.go.
func assertConcurrentOfferSingleWinner(t *testing.T, st Store, runID string) {
	t.Helper()
	ctx := context.Background()
	const workers = 8

	type result struct {
		id    string
		seq   int64
		fresh bool
		err   error
	}
	results := make([]result, workers)
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait() // maximise overlap: all goroutines fire together
			m, fresh, err := st.OfferNextMessage(ctx, runID, time.Now().UTC())
			if err != nil {
				results[i] = result{err: err}
				return
			}
			results[i] = result{id: m.ID, seq: m.Seq, fresh: fresh}
		}(i)
	}
	start.Done()
	done.Wait()

	freshCount := 0
	ids := map[string]bool{}
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("worker %d: offer err=%v", i, r.err)
		}
		ids[r.id] = true
		if r.fresh {
			freshCount++
		}
		if r.seq != 1 {
			t.Fatalf("worker %d offered seq=%d — a SECOND message went in flight", i, r.seq)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent offers handed out %d different messages, want exactly 1", len(ids))
	}
	if freshCount != 1 {
		t.Fatalf("fresh offers = %d, want exactly 1 (the single winner)", freshCount)
	}
}

// memSessionRun seeds a MemStore with one awaiting_input session run.
func memSessionRun(t *testing.T) (*MemStore, string) {
	t.Helper()
	ctx := context.Background()
	st := NewMemStore()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "chat",
		Status: domain.StatusAwaitingInput, Kind: domain.RunKindAgent, Session: true,
		Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	return st, run.ID
}

// TestMemConcurrentOfferSingleWinner is the MemStore leg of the C1 concurrency
// invariant (the PG leg lives in session_pg_test.go, gated on JCLOUD_PG_DSN).
func TestMemConcurrentOfferSingleWinner(t *testing.T) {
	ctx := context.Background()
	st, runID := memSessionRun(t)
	if _, err := st.AppendRunMessage(ctx, runID, "m1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendRunMessage(ctx, runID, "m2", ""); err != nil {
		t.Fatal(err)
	}
	assertConcurrentOfferSingleWinner(t, st, runID)
}

// TestMemOfferConsumeTwoPhase pins the MemStore offer/consume semantics:
// re-offer before consume returns the SAME message; consume unlocks the next;
// empty queue is ErrNotFound; a consume with nothing offered is (false, nil).
func TestMemOfferConsumeTwoPhase(t *testing.T) {
	ctx := context.Background()
	st, runID := memSessionRun(t)
	if _, err := st.AppendRunMessage(ctx, runID, "m1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendRunMessage(ctx, runID, "m2", ""); err != nil {
		t.Fatal(err)
	}

	m1, fresh, err := st.OfferNextMessage(ctx, runID, time.Now())
	if err != nil || !fresh || m1.Prompt != "m1" || m1.OfferedAt == nil {
		t.Fatalf("offer m1: %+v fresh=%v err=%v", m1, fresh, err)
	}
	re, fresh, err := st.OfferNextMessage(ctx, runID, time.Now())
	if err != nil || fresh || re.ID != m1.ID {
		t.Fatalf("re-offer: %+v fresh=%v err=%v (want same as m1)", re, fresh, err)
	}
	if consumed, err := st.ConsumeOfferedMessage(ctx, runID, time.Now()); err != nil || !consumed {
		t.Fatalf("consume m1: consumed=%v err=%v", consumed, err)
	}
	m2, fresh, err := st.OfferNextMessage(ctx, runID, time.Now())
	if err != nil || !fresh || m2.Prompt != "m2" {
		t.Fatalf("offer m2: %+v fresh=%v err=%v", m2, fresh, err)
	}
	if consumed, err := st.ConsumeOfferedMessage(ctx, runID, time.Now()); err != nil || !consumed {
		t.Fatalf("consume m2: consumed=%v err=%v", consumed, err)
	}
	if _, _, err := st.OfferNextMessage(ctx, runID, time.Now()); err != ErrNotFound {
		t.Fatalf("empty-queue offer err=%v want ErrNotFound", err)
	}
	if consumed, err := st.ConsumeOfferedMessage(ctx, runID, time.Now()); err != nil || consumed {
		t.Fatalf("no-op consume: consumed=%v err=%v (want false,nil)", consumed, err)
	}
}

// TestMemFinalizeIdleSessionConditional pins the P1 conditional finalize: only
// an awaiting_input run idle at-or-past cutoff flips; a resumed (running) run,
// a fresh one, or an already-finalizing one returns false.
func TestMemFinalizeIdleSessionConditional(t *testing.T) {
	ctx := context.Background()
	st, runID := memSessionRun(t)

	// No awaiting_since recorded → false.
	if ok, err := st.FinalizeIdleSession(ctx, runID, time.Now()); err != nil || ok {
		t.Fatalf("no-epoch finalize: ok=%v err=%v (want false)", ok, err)
	}
	// Stamp an old idle epoch (already awaiting_input; no-op transition allowed).
	if _, err := st.SetRunAwaitingInput(ctx, runID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Not idle long enough (cutoff before awaiting_since) → false.
	if ok, _ := st.FinalizeIdleSession(ctx, runID, time.Now().Add(-2*time.Hour)); ok {
		t.Fatal("finalized a not-yet-idle run")
	}
	// Resumed between list and act (the TOCTOU the conditional op closes) → false.
	if _, err := st.ResumeRun(ctx, runID, "StreamingTurn"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.FinalizeIdleSession(ctx, runID, time.Now()); ok {
		t.Fatal("finalized a run that was resumed to running")
	}
	// Back to awaiting + idle past cutoff → exactly one true, then false.
	if _, err := st.SetRunAwaitingInput(ctx, runID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.FinalizeIdleSession(ctx, runID, time.Now()); err != nil || !ok {
		t.Fatalf("finalize: ok=%v err=%v (want true)", ok, err)
	}
	if ok, _ := st.FinalizeIdleSession(ctx, runID, time.Now()); ok {
		t.Fatal("second finalize must be false (already finalizing)")
	}
}
