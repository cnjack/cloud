package reconciler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/store"
)

func testRec(t *testing.T, maxConcurrent int) (*Reconciler, *store.MemStore, *k8s.FakeLauncher) {
	t.Helper()
	st := store.NewMemStore()
	fake := k8s.NewFakeLauncher()
	cfg := &config.Config{
		ReconcileInterval: time.Millisecond,
		MaxConcurrentRuns: maxConcurrent,
		RunTimeoutSecs:    1800,
		OrchBaseURL:       "http://orch",
		RunnerImage:       "runner:test",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, fake, cfg, log, nil), st, fake
}

func seedProjectAndRun(t *testing.T, st *store.MemStore) domain.Run {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", RepoURL: "https://git/x.git", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, Prompt: "do the thing",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	return *run
}

func TestReconcileFullLifecycle(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	run := seedProjectAndRun(t, st)

	// Tick 1: queued -> scheduling, Job created.
	rec.Tick(ctx)
	got, _ := st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusScheduling {
		t.Fatalf("after tick1 status=%s want scheduling", got.Status)
	}
	if got.K8sJobName != k8s.JobName(run.ID) {
		t.Fatalf("job name = %q", got.K8sJobName)
	}
	if got.TokenHash == "" {
		t.Fatal("token hash should be set at job creation")
	}
	if len(fake.CreatedNames()) != 1 {
		t.Fatalf("expected 1 job created, got %d", len(fake.CreatedNames()))
	}

	// Verify runner-contract env was injected.
	env := fake.Created[0].Env
	for _, k := range []string{"RUN_ID", "TASK_PROMPT", "ORCH_BASE_URL", "RUN_TOKEN", "REPO_URL"} {
		if env[k] == "" {
			t.Errorf("job env missing %s", k)
		}
	}
	if fake.Created[0].TimeoutSeconds != 1800 {
		t.Errorf("timeout = %d want 1800", fake.Created[0].TimeoutSeconds)
	}

	// Tick 2 (job still pending): no change (idempotent, no duplicate Job).
	rec.Tick(ctx)
	if len(fake.CreatedNames()) != 1 {
		t.Fatalf("duplicate job created: %d", len(fake.CreatedNames()))
	}

	// Job goes running.
	fake.SetState(got.K8sJobName, k8s.JobRunning)
	rec.Tick(ctx)
	got, _ = st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusRunning {
		t.Fatalf("status=%s want running", got.Status)
	}
	if got.StartedAt == nil {
		t.Fatal("started_at should be set")
	}

	// Job succeeds.
	fake.SetState(got.K8sJobName, k8s.JobSucceeded)
	rec.Tick(ctx)
	got, _ = st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusSucceeded {
		t.Fatalf("status=%s want succeeded", got.Status)
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at should be set")
	}

	// run.status events should have been emitted for each transition.
	events, _ := st.ListEvents(ctx, run.ID, 0, 100)
	if len(events) < 3 {
		t.Fatalf("expected >=3 status events, got %d", len(events))
	}
}

func TestReconcileTimeoutClassification(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	run := seedProjectAndRun(t, st)

	rec.Tick(ctx) // create
	got, _ := st.GetRun(ctx, run.ID)
	fake.SetState(got.K8sJobName, k8s.JobDeadlineExceeded)
	rec.Tick(ctx)

	got, _ = st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusFailed {
		t.Fatalf("status=%s want failed", got.Status)
	}
	if got.FailureReason != domain.FailureTimeout {
		t.Fatalf("failure reason=%s want timeout", got.FailureReason)
	}
	if got.FailureMessage == "" {
		t.Fatal("failure message must be non-empty (AC-9)")
	}

	// A run.failure event must have been emitted.
	events, _ := st.ListEvents(ctx, run.ID, 0, 100)
	var sawFailure bool
	for _, e := range events {
		if e.Type == domain.EventRunFailure {
			sawFailure = true
			if e.Payload["reason"] != string(domain.FailureTimeout) {
				t.Errorf("run.failure reason payload = %v", e.Payload["reason"])
			}
		}
	}
	if !sawFailure {
		t.Error("expected a run.failure event")
	}
}

func TestReconcilePreservesRunnerReportedFailure(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	run := seedProjectAndRun(t, st)
	rec.Tick(ctx)
	got, _ := st.GetRun(ctx, run.ID)

	// Simulate runner-reported clone failure recorded while still scheduling.
	if _, err := st.SetRunnerFailure(ctx, got.ID, domain.FailureCloneFailed, "fatal: repository not found"); err != nil {
		t.Fatal(err)
	}

	// Now the Job fails; reconciler must NOT overwrite the specific reason.
	fake.SetState(got.K8sJobName, k8s.JobFailed)
	rec.Tick(ctx)
	got, _ = st.GetRun(ctx, run.ID)
	if got.FailureReason != domain.FailureCloneFailed {
		t.Fatalf("reason=%s want clone_failed (runner-reported must win)", got.FailureReason)
	}
}

func TestReconcileCapacityGating(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 2) // cap = 2
	// Seed 4 queued runs on one project.
	p := &domain.Project{ID: domain.NewID(), Name: "p", RepoURL: "https://git/x.git", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	for i := 0; i < 4; i++ {
		_ = st.CreateRun(ctx, &domain.Run{
			ID: domain.NewID(), ProjectID: p.ID, Prompt: "t",
			Status: domain.StatusQueued, Attempt: 1,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		})
	}

	// One tick should schedule exactly 2 (the cap).
	rec.Tick(ctx)
	if n := len(fake.CreatedNames()); n != 2 {
		t.Fatalf("scheduled %d jobs, want 2 (capacity)", n)
	}
	active, _ := st.CountActiveRuns(ctx)
	if active != 2 {
		t.Fatalf("active=%d want 2", active)
	}

	// Advance the two scheduled runs to succeeded, freeing capacity.
	for name := range fake.States {
		fake.SetState(name, k8s.JobSucceeded)
	}
	rec.Tick(ctx) // marks 2 succeeded, schedules... none yet? succeeded frees slots same tick
	// After succeeded, active drops; a follow-up tick schedules the rest.
	rec.Tick(ctx)
	if n := len(fake.CreatedNames()); n < 3 {
		t.Fatalf("expected more jobs scheduled after capacity freed, got %d", n)
	}
}

func TestReconcileUnlimitedCapacity(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 0) // 0 => unlimited
	p := &domain.Project{ID: domain.NewID(), Name: "p", RepoURL: "https://git/x.git", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	for i := 0; i < 5; i++ {
		_ = st.CreateRun(ctx, &domain.Run{
			ID: domain.NewID(), ProjectID: p.ID, Prompt: "t",
			Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now(),
		})
	}
	rec.Tick(ctx)
	if n := len(fake.CreatedNames()); n != 5 {
		t.Fatalf("unlimited: scheduled %d, want 5", n)
	}
}

// TestReconcileStaleCopyDoesNotClobberRunnerReason is the regression for the
// root-cause lost-update finding as it manifests in the reconciler: Tick lists a
// run with an empty FailureReason, the runner then ingests a specific reason,
// and the SAME tick's ActionMarkFailed must NOT overwrite it with the generic
// cluster-derived reason. It reproduces the exact interleaving by handing apply a
// STALE run copy (empty reason) after the store row has the runner reason.
func TestReconcileStaleCopyDoesNotClobberRunnerReason(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	run := seedProjectAndRun(t, st)
	rec.Tick(ctx) // queued -> scheduling
	staleAtListTime, _ := st.GetRun(ctx, run.ID) // FailureReason == "" here
	// Advance to running so MarkFailed is a legal transition.
	fake.SetState(staleAtListTime.K8sJobName, k8s.JobRunning)
	rec.Tick(ctx)
	staleAtListTime, _ = st.GetRun(ctx, run.ID)
	staleAtListTime.FailureReason = "" // ensure the snapshot is empty (as at list time)

	// Runner ingest records a specific reason AFTER the reconciler's list read.
	if _, err := st.SetRunnerFailure(ctx, run.ID, domain.FailureCloneFailed, "fatal: repo not found"); err != nil {
		t.Fatal(err)
	}

	// The reconciler now applies MarkFailed using its STALE copy.
	rec.apply(ctx, staleAtListTime, Decision{
		Action:        ActionMarkFailed,
		FailureReason: domain.FailureAgentError,
		FailureMsg:    "runner Job failed",
	})

	got, _ := st.GetRun(ctx, run.ID)
	if got.FailureReason != domain.FailureCloneFailed {
		t.Fatalf("reason=%s want clone_failed (runner-reported must survive stale reconciler copy)", got.FailureReason)
	}
	if got.FailureMessage != "fatal: repo not found" {
		t.Fatalf("message=%q want runner message", got.FailureMessage)
	}
}

// TestReconcileCleansUpTerminalJobs is the regression for the cleanup half of
// the cancel-racing-reconciler finding: a terminal (canceled) run that still
// carries an un-reaped Job — because a cancel raced Job creation, or its
// best-effort DeleteJob failed transiently — must have its Job reaped by the
// reconciler. Bookkeeping is the job_cleaned_at marker: k8s_job_name is
// PRESERVED as the run's historical record (audit + e2e J3-S6 verifies
// independent worker Jobs by name), and the stamped marker keeps the run out of
// subsequent cleanup scans.
func TestReconcileCleansUpTerminalJobs(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	run := seedProjectAndRun(t, st)

	// Put the run into scheduling (job name set), then cancel it while leaving
	// the job name attached (the orphan scenario).
	if _, err := st.ScheduleRun(ctx, run.ID, k8s.JobName(run.ID), "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelRun(ctx, run.ID, "CanceledByOperator", time.Now()); err != nil {
		t.Fatal(err)
	}
	// The Job still exists in the cluster (cancel's delete "failed"/raced).
	fake.SetState(k8s.JobName(run.ID), k8s.JobRunning)

	rec.Tick(ctx)

	// The reconciler must have deleted the Job...
	deleteCount := 0
	for _, name := range fake.Deleted {
		if name == k8s.JobName(run.ID) {
			deleteCount++
		}
	}
	if deleteCount == 0 {
		t.Fatalf("terminal run's Job was not deleted; Deleted=%v", fake.Deleted)
	}
	got, _ := st.GetRun(ctx, run.ID)
	// ...while PRESERVING k8s_job_name as the historical record...
	if got.K8sJobName != k8s.JobName(run.ID) {
		t.Fatalf("k8s_job_name=%q want %q (Job identity must persist after cleanup)", got.K8sJobName, k8s.JobName(run.ID))
	}
	// ...and stamping job_cleaned_at instead.
	if got.JobCleanedAt == nil {
		t.Fatal("job_cleaned_at not set after cleanup")
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status=%s want canceled (cleanup must not change status)", got.Status)
	}

	// The run must not be re-listed for cleanup: another tick must not issue a
	// second DeleteJob for it.
	before := deleteCount
	rec.Tick(ctx)
	after := 0
	for _, name := range fake.Deleted {
		if name == k8s.JobName(run.ID) {
			after++
		}
	}
	if after != before {
		t.Fatalf("cleaned run was re-processed: delete count %d -> %d (job_cleaned_at filter broken)", before, after)
	}
	// And listing must exclude it.
	pending, err := st.ListTerminalRunsWithJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.ID == run.ID {
			t.Fatal("cleaned run still returned by ListTerminalRunsWithJob")
		}
	}
}

// TestReconcileTokenStableAcrossFailedPersist is the regression for the
// "token regen + idempotent CreateJob mismatch" finding. It models the
// unrecoverable crash-before-commit state: a PRIOR process created the runner
// Job with token1 and crashed before persisting the hash, so the run is still
// queued with NO token_hash while a token1 Job lingers in the cluster. The next
// reconcile's createJob must make the persisted hash match the token the LIVE
// Job carries — otherwise every runner request 401s (constant-time hash compare
// fails). The fix deletes the leftover Job before CreateJob, so CreateJob is
// never a no-op against the stale-token Job and the fresh Job carries the token
// whose hash we persist.
//
// The FakeLauncher faithfully models idempotency-by-name (an existing Job keeps
// its ORIGINAL env), so without the delete-first fix the live Job would still
// carry token1 while the store persists hash(token2).
func TestReconcileTokenStableAcrossFailedPersist(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	run := seedProjectAndRun(t, st)
	jobName := k8s.JobName(run.ID)

	// Simulate the prior crashed process: a token1 Job exists, but the run is
	// still queued with no persisted hash.
	const token1 = "prior-crashed-token"
	if err := fake.CreateJob(ctx, k8s.JobSpec{
		Name:  jobName,
		RunID: run.ID,
		Env:   map[string]string{"RUN_TOKEN": token1},
	}); err != nil {
		t.Fatal(err)
	}

	// One reconcile: queued -> scheduling. createJob must delete the token1 Job
	// and recreate with a fresh token whose hash it persists.
	rec.Tick(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusScheduling {
		t.Fatalf("status=%s want scheduling", got.Status)
	}
	if got.TokenHash == "" {
		t.Fatal("token hash must be persisted after schedule")
	}

	// The LIVE Job's RUN_TOKEN must hash to the persisted token_hash.
	liveSpec, ok := fake.LiveSpec(jobName)
	if !ok {
		t.Fatal("no live Job after reconcile")
	}
	liveToken := liveSpec.Env["RUN_TOKEN"]
	if liveToken == token1 {
		t.Fatal("live Job still carries the stale crashed token (delete-before-create missing)")
	}
	if auth.HashToken(liveToken) != got.TokenHash {
		t.Fatal("persisted token hash does not match the live Job's RUN_TOKEN (runner would 401)")
	}
}
