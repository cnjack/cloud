package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/store"
)

// addServiceWithRuns creates one raw readonly service under projectID plus nRuns
// queued runs against it. Returns the service id and the run ids (creation order).
func addServiceWithRuns(t *testing.T, st *store.MemStore, projectID string, nRuns int) (string, []string) {
	t.Helper()
	ctx := context.Background()
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: projectID, Name: "svc-" + domain.NewID()[:6],
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://git/x.git",
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, nRuns)
	for i := 0; i < nRuns; i++ {
		run := &domain.Run{
			ID: domain.NewID(), ProjectID: projectID, ServiceID: svc.ID, Prompt: "do the thing",
			Status: domain.StatusQueued, Attempt: 1,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}
		if err := st.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	return svc.ID, ids
}

// TestPersistentWorkspaceOffNoPVC pins the default (OFF) path: no PVC is ensured,
// the JobSpec carries no WorkspacePVC, and PERSISTENT_WORKSPACE is not injected.
func TestPersistentWorkspaceOffNoPVC(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10) // PersistentWorkspace defaults to false
	seedGuardrailProject(t, st, &domain.Project{}, 1)

	rec.Tick(ctx)

	if len(fake.EnsuredPVCs) != 0 {
		t.Fatalf("ensured %d PVCs with the flag OFF, want 0", len(fake.EnsuredPVCs))
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created %d jobs, want 1", len(fake.Created))
	}
	if fake.Created[0].WorkspacePVC != "" {
		t.Fatalf("JobSpec.WorkspacePVC=%q want empty (ephemeral)", fake.Created[0].WorkspacePVC)
	}
	if _, ok := fake.Created[0].Env["PERSISTENT_WORKSPACE"]; ok {
		t.Fatal("PERSISTENT_WORKSPACE env set with the flag OFF")
	}
}

// TestPersistentWorkspaceEnsuresPVCAndEnv: with the flag ON a queued run ensures
// its service PVC, mounts it (spec.WorkspacePVC = ws-<svc>), and injects the
// runner switch PERSISTENT_WORKSPACE=1.
func TestPersistentWorkspaceEnsuresPVCAndEnv(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.PersistentWorkspace = true
	seedGuardrailProject(t, st, &domain.Project{}, 1)

	rec.Tick(ctx)

	if len(fake.Created) != 1 {
		t.Fatalf("created %d jobs, want 1", len(fake.Created))
	}
	spec := fake.Created[0]
	if len(fake.EnsuredPVCs) != 1 {
		t.Fatalf("ensured %d PVCs, want 1", len(fake.EnsuredPVCs))
	}
	wantPVC := k8s.WorkspacePVCName(fake.EnsuredPVCs[0])
	if spec.WorkspacePVC != wantPVC {
		t.Fatalf("JobSpec.WorkspacePVC=%q want %q", spec.WorkspacePVC, wantPVC)
	}
	if spec.Env["PERSISTENT_WORKSPACE"] != "1" {
		t.Fatalf("PERSISTENT_WORKSPACE=%q want 1", spec.Env["PERSISTENT_WORKSPACE"])
	}
	// The ensured PVC is keyed by the run's service id.
	run, _ := firstScheduledRun(t, st)
	if fake.EnsuredPVCs[0] != run.ServiceID {
		t.Fatalf("ensured PVC for service %q, want %q", fake.EnsuredPVCs[0], run.ServiceID)
	}
}

// firstScheduledRun returns the single run currently in scheduling with a Job.
func firstScheduledRun(t *testing.T, st *store.MemStore) (domain.Run, bool) {
	t.Helper()
	ctx := context.Background()
	runs, err := st.ListRunsByStatus(ctx, domain.StatusScheduling)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.K8sJobName != "" {
			return r, true
		}
	}
	return domain.Run{}, false
}

// TestPerServiceSerialization: two queued runs of the SAME service schedule ONE
// at a time when the persistent workspace is on (RWO PVC ⇒ one pod). The second
// only schedules after the first reaches a terminal state.
func TestPerServiceSerialization(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10) // generous cluster capacity — not the limiter
	rec.cfg.PersistentWorkspace = true
	_, ids := seedGuardrailProject(t, st, &domain.Project{}, 2)

	rec.Tick(ctx)

	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("scheduled %d runs, want 1 (per-service serialization)", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 1 {
		t.Fatalf("queued %d, want 1 (the second run waits for the PVC)", got)
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created %d jobs, want 1", len(fake.Created))
	}

	// A later tick with the first run still active must NOT schedule the second.
	rec.Tick(ctx)
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("second tick scheduled %d, want still 1", got)
	}

	// Finish the first run; the service frees and the second schedules.
	first := firstScheduled(t, st, ids)
	fr, _ := st.GetRun(ctx, first)
	fake.SetState(fr.K8sJobName, k8s.JobSucceeded)
	rec.Tick(ctx) // observe success -> succeeded
	rec.Tick(ctx) // now the second run schedules
	if got := countByStatus(t, st, ids, domain.StatusSucceeded); got != 1 {
		t.Fatalf("succeeded=%d want 1", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("after the service freed, scheduling=%d want 1 (second run)", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 0 {
		t.Fatalf("queued=%d want 0 (both runs progressed)", got)
	}
}

// TestPerServiceSerializationOffSchedulesBoth: with the flag OFF the per-service
// gate is inert — two runs of one service both schedule (today's behaviour).
func TestPerServiceSerializationOffSchedulesBoth(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 10) // flag OFF
	_, ids := seedGuardrailProject(t, st, &domain.Project{}, 2)

	rec.Tick(ctx)

	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 2 {
		t.Fatalf("scheduled %d, want 2 (no per-service serialization when OFF)", got)
	}
}

// TestPerServiceIndependentAcrossServices: the per-service cap is per service, not
// global — two services each schedule exactly one of their two queued runs in a
// tick (generous project/cluster caps), so one service never starves another.
func TestPerServiceIndependentAcrossServices(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 10)
	rec.cfg.PersistentWorkspace = true
	// One project, nil cap (inherits the generous cluster cap), two services.
	pid, aIDs := seedGuardrailProject(t, st, &domain.Project{MaxConcurrentRuns: nil}, 2)
	_, bIDs := addServiceWithRuns(t, st, pid, 2)

	rec.Tick(ctx)

	if got := countByStatus(t, st, aIDs, domain.StatusScheduling); got != 1 {
		t.Fatalf("service A scheduled %d, want 1", got)
	}
	if got := countByStatus(t, st, bIDs, domain.StatusScheduling); got != 1 {
		t.Fatalf("service B scheduled %d, want 1 (independent of A)", got)
	}
}

// TestPerServiceSerializationTighterThanProjectCap: the per-service limit of 1 is
// tighter than a permissive project cap — a project capped at 2 with two queued
// runs of ONE service still schedules only one (the PVC gate dominates).
func TestPerServiceSerializationTighterThanProjectCap(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 10)
	rec.cfg.PersistentWorkspace = true
	_, ids := seedGuardrailProject(t, st, &domain.Project{MaxConcurrentRuns: intp(2)}, 2)

	rec.Tick(ctx)

	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("scheduled %d, want 1 (per-service gate tighter than project cap 2)", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 1 {
		t.Fatalf("queued %d, want 1", got)
	}
}

// TestEnsurePVCFailureLeavesRunQueued: an EnsureWorkspacePVC error must NOT launch
// the Job — the run stays queued and is retried next tick (fail-visible; never a
// blind schedule against an unbound volume).
func TestEnsurePVCFailureLeavesRunQueued(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.PersistentWorkspace = true
	fake.EnsurePVCErr = context.DeadlineExceeded // any transient error
	_, ids := seedGuardrailProject(t, st, &domain.Project{}, 1)

	rec.Tick(ctx)

	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 1 {
		t.Fatalf("queued %d, want 1 (ensure failed → stay queued)", got)
	}
	if len(fake.Created) != 0 {
		t.Fatalf("created %d jobs despite ensure failure, want 0", len(fake.Created))
	}

	// Recovery: clear the error and the next tick schedules normally.
	fake.EnsurePVCErr = nil
	rec.Tick(ctx)
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("after recovery scheduled %d, want 1", got)
	}
}

// TestStaleEscapeThreshold pins the formula: 0/disabled => 30m floor; any value
// below 30m => 30m floor; higher values are honoured.
func TestStaleEscapeThreshold(t *testing.T) {
	cases := []struct{ in, want time.Duration }{
		{0, 30 * time.Minute},                // disabled → floor
		{10 * time.Minute, 30 * time.Minute}, // below floor → floor (STALL_TIMEOUT default 10m)
		{29 * time.Minute, 30 * time.Minute}, // just under floor → floor
		{30 * time.Minute, 30 * time.Minute}, // at floor
		{2 * time.Hour, 2 * time.Hour},       // above floor → honoured
	}
	for _, c := range cases {
		if got := staleEscapeThreshold(c.in); got != c.want {
			t.Errorf("staleEscapeThreshold(%v)=%v want %v", c.in, got, c.want)
		}
	}
}

// TestPerServiceStaleEscape: a run stuck in Scheduling for longer than the stall
// threshold no longer blocks the next queued run of the same service (Feature C
// deadlock escape). Without this, a stuck Job (Pending unschedulable, lost
// terminal state) would permanently lock the service's PVC gate.
func TestPerServiceStaleEscape(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.PersistentWorkspace = true

	clock := time.Now()
	rec.now = func() time.Time { return clock }

	_, ids := seedGuardrailProject(t, st, &domain.Project{}, 2)

	// First tick: schedules run A, leaves B queued (per-service serialization).
	rec.Tick(ctx)
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("scheduled %d, want 1", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 1 {
		t.Fatalf("queued %d, want 1", got)
	}

	// Advance the clock PAST the stale threshold. The in-flight run A is now
	// "stale" and must not hold the service gate open.
	threshold := staleEscapeThreshold(rec.cfg.StallTimeout) // 30m with the default 10m STALL_TIMEOUT
	clock = clock.Add(threshold + time.Minute)

	// The stuck run's Job is still pending — simulate it so decide() doesn't
	// transition it to terminal.
	first := firstScheduled(t, st, ids)
	fr, _ := st.GetRun(ctx, first)
	fake.SetState(fr.K8sJobName, k8s.JobPending)

	rec.Tick(ctx)

	// Run B should now schedule (stale escape). Run A stays Scheduling — it will
	// be cleaned up by the job-state polling on a later tick (Missing => failed).
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 2 {
		t.Fatalf("after stale escape, scheduling=%d want 2 (A stuck + B scheduled)", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 0 {
		t.Fatalf("queued=%d want 0 (B escaped the deadlock)", got)
	}
}

// TestPerServiceNoPrematureEscape: a run that is in-flight but NOT yet stale
// (within the threshold) still blocks the second queued run. The escape only
// fires after the threshold is exceeded, not before.
func TestPerServiceNoPrematureEscape(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	rec.cfg.PersistentWorkspace = true

	clock := time.Now()
	rec.now = func() time.Time { return clock }

	_, ids := seedGuardrailProject(t, st, &domain.Project{}, 2)

	rec.Tick(ctx) // schedules A, B queued

	// Advance the clock to JUST UNDER the threshold — A is still fresh.
	threshold := staleEscapeThreshold(rec.cfg.StallTimeout)
	clock = clock.Add(threshold - time.Minute)

	first := firstScheduled(t, st, ids)
	fr, _ := st.GetRun(ctx, first)
	fake.SetState(fr.K8sJobName, k8s.JobPending)

	rec.Tick(ctx)

	// B must still be blocked — A has not exceeded the threshold yet.
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 1 {
		t.Fatalf("queued=%d want 1 (B must wait — A is not stale yet)", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("scheduling=%d want 1 (no premature escape)", got)
	}
}
