package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// seedSessionProject creates a project + a service (draft_pr provider by
// default) so session runs have somewhere to live. Returns the ids.
func seedSessionProject(t *testing.T, st *store.MemStore, p *domain.Project) (string, string) {
	t.Helper()
	ctx := context.Background()
	if p.ID == "" {
		p.ID = domain.NewID()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.Name == "" {
		p.Name = "sess"
	}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "jcloud/seed", DefaultBranch: "main",
		GitMode: domain.GitModeDraftPR, CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	return p.ID, svc.ID
}

// liveSession creates a session run already in awaiting_input with a live Job
// (so the reconciler counts it and decide() leaves it alone).
func liveSession(t *testing.T, st *store.MemStore, fake *k8s.FakeLauncher, projectID, serviceID string) string {
	t.Helper()
	ctx := context.Background()
	run := &domain.Run{ID: domain.NewID(), ProjectID: projectID, ServiceID: serviceID, Prompt: "s",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	job := k8s.JobName(run.ID)
	if _, err := st.ScheduleRun(ctx, run.ID, job, auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunAwaitingInput(ctx, run.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	fake.SetState(job, k8s.JobRunning)
	return run.ID
}

// TestSessionJobEnvAndTTL: a queued session run is scheduled with RUN_SESSION=1
// and a RUN_TIMEOUT / activeDeadlineSeconds driven by the session TTL, while a
// non-session run gets neither.
func TestSessionJobEnvAndTTL(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.SessionTTLSecs = 7200
	pid, sid := seedSessionProject(t, st, &domain.Project{})

	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	rec.Tick(ctx)

	if len(fake.Created) != 1 {
		t.Fatalf("created %d jobs want 1", len(fake.Created))
	}
	spec := fake.Created[0]
	if spec.Env["RUN_SESSION"] != "1" {
		t.Errorf("RUN_SESSION=%q want 1", spec.Env["RUN_SESSION"])
	}
	if spec.Env["RUN_TIMEOUT"] != "7200s" {
		t.Errorf("RUN_TIMEOUT=%q want 7200s (session TTL)", spec.Env["RUN_TIMEOUT"])
	}
	// Job deadline = TTL + grace (max(120, ttl/10)) = 7200 + 720.
	if spec.TimeoutSeconds != 7200+720 {
		t.Errorf("activeDeadlineSeconds=%d want 7920", spec.TimeoutSeconds)
	}
}

// TestNonSessionRunNoRunSessionEnv: a plain run never gets RUN_SESSION.
func TestNonSessionRunNoRunSessionEnv(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	pid, sid := seedSessionProject(t, st, &domain.Project{})
	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "one shot",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	rec.Tick(ctx)
	if _, ok := fake.Created[0].Env["RUN_SESSION"]; ok {
		t.Fatal("non-session run must not set RUN_SESSION")
	}
}

// TestMaxLiveSessionsGate: with the cluster live-session cap at 2 and two live
// sessions already running, a third queued session run stays queued.
func TestMaxLiveSessionsGate(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10) // cluster concurrency well above the session cap
	rec.cfg.MaxLiveSessions = 2
	pid, sid := seedSessionProject(t, st, &domain.Project{})

	liveSession(t, st, fake, pid, sid)
	liveSession(t, st, fake, pid, sid)

	// A third session run, queued.
	third := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "third",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true, Attempt: 1, CreatedAt: time.Now().Add(time.Second)}
	if err := st.CreateRun(ctx, third); err != nil {
		t.Fatal(err)
	}

	before := len(fake.Created)
	rec.Tick(ctx)
	got, _ := st.GetRun(ctx, third.ID)
	if got.Status != domain.StatusQueued {
		t.Fatalf("third session status=%q want queued (over live-session cap)", got.Status)
	}
	if len(fake.Created) != before {
		t.Fatalf("scheduled a job over the cap (created went %d -> %d)", before, len(fake.Created))
	}

	// Raise the project override to 3 → the third schedules next tick.
	three := 3
	proj, _ := st.GetProject(ctx, pid)
	proj.MaxLiveSessions = &three
	if err := st.UpdateProject(ctx, proj); err != nil {
		t.Fatal(err)
	}
	rec.Tick(ctx)
	got, _ = st.GetRun(ctx, third.ID)
	if got.Status != domain.StatusScheduling {
		t.Fatalf("after raising cap, third status=%q want scheduling", got.Status)
	}
}

// TestSessionIdleTimeoutFinalizes: an awaiting_input run idle past the timeout is
// finalized (flag set + session.finish event); a fresh one is left alone.
func TestSessionIdleTimeoutFinalizes(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 10)
	rec.cfg.SessionIdleTimeoutSecs = 60
	pid, sid := seedSessionProject(t, st, &domain.Project{})

	old := time.Now().Add(-120 * time.Second)
	stale := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "idle",
		Status: domain.StatusAwaitingInput, Kind: domain.RunKindAgent, Session: true,
		AwaitingSince: &old, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, stale); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now()
	freshRun := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "active",
		Status: domain.StatusAwaitingInput, Kind: domain.RunKindAgent, Session: true,
		AwaitingSince: &fresh, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, freshRun); err != nil {
		t.Fatal(err)
	}

	rec.reconcileSessionIdle(ctx)

	gotStale, _ := st.GetRun(ctx, stale.ID)
	if !gotStale.SessionFinalizing {
		t.Fatal("stale session not finalized")
	}
	gotFresh, _ := st.GetRun(ctx, freshRun.ID)
	if gotFresh.SessionFinalizing {
		t.Fatal("fresh session must not be finalized")
	}
	// A session.finish(idle_timeout) event was appended for the stale run.
	evs, _ := st.ListEvents(ctx, stale.ID, 0, 100)
	found := false
	for _, e := range evs {
		if e.Type == domain.EventSessionFinish && e.Payload["reason"] == "idle_timeout" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no session.finish(idle_timeout) event: %+v", evs)
	}
}

// TestSessionPushOpensThenUpdates: the first turn's bundle opens the draft PR,
// a later turn ff-updates the same branch; idempotent when nothing new.
func TestSessionPushOpensThenUpdates(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)
	pid, sid := seedSessionProject(t, st, &domain.Project{})

	branch := "jcode/run-sess1"
	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "chat build",
		Status: domain.StatusAwaitingInput, Kind: domain.RunKindAgent, Session: true,
		GitBranch: branch, BundleRev: 1, PushedRev: 0, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRunBundle(ctx, run.ID, []byte("turn1 bundle")); err != nil {
		t.Fatal(err)
	}

	// Turn 1: open the PR.
	rec.reconcileSessionPushes(ctx)
	if len(pusher.pushed) != 1 || pusher.pushed[0] != branch {
		t.Fatalf("turn1 pushed=%v want [%s]", pusher.pushed, branch)
	}
	if fake.CreatedCount() != 1 {
		t.Fatalf("turn1 opened %d PRs want 1", fake.CreatedCount())
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.PRURL == "" || got.PushedRev != 1 {
		t.Fatalf("turn1 pr_url=%q pushed_rev=%d want set/1", got.PRURL, got.PushedRev)
	}

	// Idempotent: nothing new (bundle_rev == pushed_rev) → no more pushes.
	rec.reconcileSessionPushes(ctx)
	if len(pusher.pushed) != 1 || len(pusher.ffPushed) != 0 {
		t.Fatalf("idempotent re-run pushed again: create=%v ff=%v", pusher.pushed, pusher.ffPushed)
	}

	// Turn 2: a new bundle bumps bundle_rev → ff-update the same branch, no new PR.
	if err := st.PutRunBundle(ctx, run.ID, []byte("turn2 bundle")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BumpBundleRev(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	rec.reconcileSessionPushes(ctx)
	if len(pusher.ffPushed) != 1 || pusher.ffPushed[0] != branch {
		t.Fatalf("turn2 ffPushed=%v want [%s]", pusher.ffPushed, branch)
	}
	if fake.CreatedCount() != 1 {
		t.Fatalf("turn2 opened another PR (%d) — must reuse the branch", fake.CreatedCount())
	}
	got, _ = st.GetRun(ctx, run.ID)
	if got.PushedRev != 2 {
		t.Fatalf("turn2 pushed_rev=%d want 2", got.PushedRev)
	}
}

// TestAwaitingInputJobSucceededMarksSucceeded: when a session's pod exits 0 (a
// finish/idle finalize took effect), the reconciler moves it to succeeded.
func TestAwaitingInputJobSucceededMarksSucceeded(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	pid, sid := seedSessionProject(t, st, &domain.Project{})
	rid := liveSession(t, st, fake, pid, sid)

	// The runner exited (finish → 410 → exit 0): the Job succeeds.
	run, _ := st.GetRun(ctx, rid)
	fake.SetState(run.K8sJobName, k8s.JobSucceeded)
	rec.Tick(ctx)

	got, _ := st.GetRun(ctx, rid)
	if got.Status != domain.StatusSucceeded {
		t.Fatalf("awaiting_input + Job succeeded → status=%q want succeeded", got.Status)
	}
}

// TestAwaitingInputHoldsWorkspaceSlot: with PERSISTENT_WORKSPACE on, a session
// run parked in awaiting_input still holds the service's RWO workspace PVC — a
// queued run of the SAME service must stay queued until the session ends.
func TestAwaitingInputHoldsWorkspaceSlot(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.PersistentWorkspace = true
	pid, sid := seedSessionProject(t, st, &domain.Project{})
	rid := liveSession(t, st, fake, pid, sid)

	queued := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "next",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now().Add(time.Second)}
	if err := st.CreateRun(ctx, queued); err != nil {
		t.Fatal(err)
	}

	rec.Tick(ctx)
	got, _ := st.GetRun(ctx, queued.ID)
	if got.Status != domain.StatusQueued {
		t.Fatalf("same-service run status=%q want queued (awaiting_input session holds the RWO PVC)", got.Status)
	}

	// The session ends (runner exits 0) → the slot frees → the queued run goes.
	sess, _ := st.GetRun(ctx, rid)
	fake.SetState(sess.K8sJobName, k8s.JobSucceeded)
	rec.Tick(ctx) // observes success → succeeded (frees the service slot)
	rec.Tick(ctx) // schedules the queued run
	got, _ = st.GetRun(ctx, queued.ID)
	if got.Status != domain.StatusScheduling {
		t.Fatalf("after session end, same-service run status=%q want scheduling", got.Status)
	}
}

// TestSessionIdleRaceResumedNotFinalized (P1): a run that was awaiting_input
// when the pass LISTED it but got resumed to running (a message arrived) before
// the finalize acts must NOT be finalized — the conditional store op re-checks
// status/awaiting_since atomically, closing the TOCTOU.
func TestSessionIdleRaceResumedNotFinalized(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 10)
	rec.cfg.SessionIdleTimeoutSecs = 60
	pid, sid := seedSessionProject(t, st, &domain.Project{})

	old := time.Now().Add(-120 * time.Second)
	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "idle",
		Status: domain.StatusAwaitingInput, Kind: domain.RunKindAgent, Session: true,
		AwaitingSince: &old, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Simulate the race: the run resumes (message delivered) AFTER the pass
	// would have listed it but BEFORE it acts. Since the conditional finalize
	// re-checks in the store, running the pass now must leave the run alone.
	if _, err := st.ResumeRun(ctx, run.ID, "StreamingTurn"); err != nil {
		t.Fatal(err)
	}
	rec.reconcileSessionIdle(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.SessionFinalizing {
		t.Fatal("resumed session was finalized (TOCTOU not closed)")
	}
	// And no session.finish event was appended.
	evs, _ := st.ListEvents(ctx, run.ID, 0, 100)
	for _, e := range evs {
		if e.Type == domain.EventSessionFinish {
			t.Fatalf("unexpected session.finish on a resumed run: %+v", e)
		}
	}
}

// TestSessionRunNeverStale (P2): a session run is long-lived BY DESIGN — even
// past the stale-escape threshold it keeps holding the per-service RWO slot
// (its pod verifiably owns the PVC), so a queued same-service run stays queued.
func TestSessionRunNeverStale(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.PersistentWorkspace = true
	pid, sid := seedSessionProject(t, st, &domain.Project{})

	// A session run RUNNING for 2h — far past the 30m stale-escape floor.
	run := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "long chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true, Attempt: 1,
		CreatedAt: time.Now().Add(-3 * time.Hour)}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	job := k8s.JobName(run.ID)
	if _, err := st.ScheduleRun(ctx, run.ID, job, auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "StreamingTurn", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fake.SetState(job, k8s.JobRunning)

	queued := &domain.Run{ID: domain.NewID(), ProjectID: pid, ServiceID: sid, Prompt: "next",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, queued); err != nil {
		t.Fatal(err)
	}

	rec.Tick(ctx)
	got, _ := st.GetRun(ctx, queued.ID)
	if got.Status != domain.StatusQueued {
		t.Fatalf("same-service run status=%q want queued (session run is never stale)", got.Status)
	}
}
