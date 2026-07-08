package store

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// TestPGSessionMutators exercises the D22 session SQL against a real Postgres:
// the message queue (append/claim idempotency), the awaiting_input state machine
// (awaiting_since COALESCE + resume clear), and the per-turn push cursor
// (bundle_rev/pushed_rev + ListSessionRunsAwaitingPush). Skips without a DSN.
func TestPGSessionMutators(t *testing.T) {
	st, _ := pgTestStore(t)
	ctx := context.Background()

	// A fresh session run (draft_pr provider service) in its own project.
	p := &domain.Project{ID: domain.NewID(), Name: "sess", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "o/r",
		GitMode: domain.GitModeDraftPR, DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "chat",
		Status: domain.StatusRunning, Kind: domain.RunKindAgent, Session: true, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// --- awaiting_input: stamp awaiting_since once, resume clears it -----------
	got, err := st.SetRunAwaitingInput(ctx, run.ID, time.Now())
	if err != nil || got.Status != domain.StatusAwaitingInput || got.AwaitingSince == nil {
		t.Fatalf("SetRunAwaitingInput: run=%+v err=%v", got, err)
	}
	first := *got.AwaitingSince
	// A duplicate keeps awaiting_since (COALESCE).
	got, _ = st.SetRunAwaitingInput(ctx, run.ID, time.Now().Add(time.Hour))
	if !got.AwaitingSince.Equal(first) {
		t.Fatalf("duplicate awaiting_input reset awaiting_since")
	}

	// --- message queue: two-phase offer/consume ---------------------------------
	if _, err := st.AppendRunMessage(ctx, run.ID, "m1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendRunMessage(ctx, run.ID, "m2", ""); err != nil {
		t.Fatal(err)
	}
	// First offer: m1, fresh.
	c1, fresh, err := st.OfferNextMessage(ctx, run.ID, time.Now())
	if err != nil || !fresh || c1.Prompt != "m1" || c1.OfferedAt == nil || c1.ConsumedAt != nil {
		t.Fatalf("offer m1: %+v fresh=%v err=%v", c1, fresh, err)
	}
	// Re-offer before consume: SAME message, fresh=false (lost-response replay).
	re, fresh, err := st.OfferNextMessage(ctx, run.ID, time.Now())
	if err != nil || fresh || re.ID != c1.ID || re.Prompt != "m1" {
		t.Fatalf("re-offer: %+v fresh=%v err=%v (want same as m1)", re, fresh, err)
	}
	// Consume (turn-complete) → next offer hands out m2.
	if consumed, err := st.ConsumeOfferedMessage(ctx, run.ID, time.Now()); err != nil || !consumed {
		t.Fatalf("consume m1: consumed=%v err=%v", consumed, err)
	}
	c2, fresh, err := st.OfferNextMessage(ctx, run.ID, time.Now())
	if err != nil || !fresh || c2.Prompt != "m2" {
		t.Fatalf("offer m2: %+v fresh=%v err=%v", c2, fresh, err)
	}
	if consumed, err := st.ConsumeOfferedMessage(ctx, run.ID, time.Now()); err != nil || !consumed {
		t.Fatalf("consume m2: consumed=%v err=%v", consumed, err)
	}
	// Empty queue → ErrNotFound; consume with nothing offered → (false, nil).
	if _, _, err := st.OfferNextMessage(ctx, run.ID, time.Now()); err != ErrNotFound {
		t.Fatalf("third offer err=%v want ErrNotFound", err)
	}
	if consumed, err := st.ConsumeOfferedMessage(ctx, run.ID, time.Now()); err != nil || consumed {
		t.Fatalf("no-op consume: consumed=%v err=%v (want false,nil)", consumed, err)
	}

	// resume clears awaiting_since.
	res, err := st.ResumeRun(ctx, run.ID, "StreamingTurn")
	if err != nil || res.Status != domain.StatusRunning || res.AwaitingSince != nil {
		t.Fatalf("ResumeRun: %+v err=%v", res, err)
	}

	// --- push cursor: git_branch + bundle_rev>pushed_rev => in the scan --------
	if _, err := st.SetRunGit(ctx, run.ID, "jcode/run-x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BumpBundleRev(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListSessionRunsAwaitingPush(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != run.ID {
		t.Fatalf("ListSessionRunsAwaitingPush=%+v err=%v", pending, err)
	}
	if _, err := st.SetPushedRev(ctx, run.ID, 1, "sha1"); err != nil {
		t.Fatal(err)
	}
	pending, _ = st.ListSessionRunsAwaitingPush(ctx)
	if len(pending) != 0 {
		t.Fatalf("after SetPushedRev, still pending: %+v", pending)
	}
	after, _ := st.GetRun(ctx, run.ID)
	if after.PushedRev != 1 || after.CommitSHA != "sha1" {
		t.Fatalf("pushed_rev=%d commit_sha=%q want 1/sha1", after.PushedRev, after.CommitSHA)
	}
	// P3: an EMPTY sha (PR-already-exists recovery) preserves the stored tip.
	if after, err := st.SetPushedRev(ctx, run.ID, 2, ""); err != nil || after.CommitSHA != "sha1" {
		t.Fatalf("SetPushedRev with empty sha: commit_sha=%q err=%v (want preserved sha1)", after.CommitSHA, err)
	}

	// --- conditional idle finalize (P1) ----------------------------------------
	// run is currently RUNNING (resumed above) → not finalized.
	if ok, err := st.FinalizeIdleSession(ctx, run.ID, time.Now().Add(time.Hour)); err != nil || ok {
		t.Fatalf("FinalizeIdleSession on running run: ok=%v err=%v (want false)", ok, err)
	}
	if _, err := st.SetRunAwaitingInput(ctx, run.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Cutoff BEFORE awaiting_since → not idle long enough → false.
	if ok, _ := st.FinalizeIdleSession(ctx, run.ID, time.Now().Add(-2*time.Hour)); ok {
		t.Fatal("FinalizeIdleSession finalized a not-yet-idle run")
	}
	// Idle past cutoff → exactly one true; the second call is false (already set).
	if ok, err := st.FinalizeIdleSession(ctx, run.ID, time.Now()); err != nil || !ok {
		t.Fatalf("FinalizeIdleSession: ok=%v err=%v (want true)", ok, err)
	}
	if ok, _ := st.FinalizeIdleSession(ctx, run.ID, time.Now()); ok {
		t.Fatal("second FinalizeIdleSession must be false (already finalizing)")
	}

	// --- finalize + session guardrail round-trip ------------------------------
	fin, err := st.MarkSessionFinalizing(ctx, run.ID)
	if err != nil || !fin.SessionFinalizing {
		t.Fatalf("MarkSessionFinalizing: %+v err=%v", fin, err)
	}
	three, ttl := 3, int64(9000)
	p.MaxLiveSessions = &three
	p.SessionTTLSecs = &ttl
	if err := st.UpdateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	rp, _ := st.GetProject(ctx, p.ID)
	if rp.MaxLiveSessions == nil || *rp.MaxLiveSessions != 3 || rp.SessionTTLSecs == nil || *rp.SessionTTLSecs != 9000 {
		t.Fatalf("session guardrails round-trip: %+v", rp)
	}

	_ = st.DeleteProject(ctx, p.ID) // cascade run_messages
}

// TestPGConcurrentOfferSingleWinner (C1): N truly-concurrent OfferNextMessage
// calls with TWO queued messages must all converge on the SAME message (exactly
// one fresh offer; everyone else gets the identical re-delivery) — never two
// different messages in flight.
func TestPGConcurrentOfferSingleWinner(t *testing.T) {
	st, runID := pgTestStore(t)
	ctx := context.Background()
	if _, err := st.AppendRunMessage(ctx, runID, "m1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendRunMessage(ctx, runID, "m2", ""); err != nil {
		t.Fatal(err)
	}
	assertConcurrentOfferSingleWinner(t, st, runID)
}
