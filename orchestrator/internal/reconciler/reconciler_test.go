package reconciler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

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
	got.FailureReason = domain.FailureCloneFailed
	got.FailureMessage = "fatal: repository not found"
	if err := st.UpdateRun(ctx, got); err != nil { // no-op status transition
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
