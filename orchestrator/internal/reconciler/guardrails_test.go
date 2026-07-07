package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/store"
)

// seedGuardrailProject creates a project (with whatever guardrail fields the
// caller set on p) + a readonly raw 'default' service + nRuns queued runs against
// it. p.ID/CreatedAt are filled when empty. Returns the project id and the run
// ids in creation order.
func seedGuardrailProject(t *testing.T, st *store.MemStore, p *domain.Project, nRuns int) (string, []string) {
	t.Helper()
	ctx := context.Background()
	if p.ID == "" {
		p.ID = domain.NewID()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.Name == "" {
		p.Name = "guard"
	}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://git/x.git",
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, nRuns)
	for i := 0; i < nRuns; i++ {
		run := &domain.Run{
			ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "do the thing",
			Status: domain.StatusQueued, Attempt: 1,
			// Stagger created_at so ListRunsByStatus has a deterministic order.
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}
		if err := st.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	return p.ID, ids
}

// countByStatus returns how many of runIDs are currently in status.
func countByStatus(t *testing.T, st *store.MemStore, runIDs []string, status domain.RunStatus) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	for _, id := range runIDs {
		r, err := st.GetRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == status {
			n++
		}
	}
	return n
}

func intp(n int) *int { return &n }

// TestPerProjectConcurrencyLimit: a project capped at max_concurrent_runs=1 with
// two queued runs schedules only ONE in a tick — the other stays queued (not
// failed). Cluster capacity is generous so it is not the limiter. This also pins
// the "no over-schedule within one tick" invariant.
func TestPerProjectConcurrencyLimit(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10) // cluster max well above the project cap
	_, ids := seedGuardrailProject(t, st, &domain.Project{MaxConcurrentRuns: intp(1)}, 2)

	rec.Tick(ctx)

	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("scheduled %d runs in one tick, want 1 (per-project cap)", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 1 {
		t.Fatalf("queued %d runs, want 1 (the capped-out run stays queued)", got)
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created %d jobs, want 1", len(fake.Created))
	}

	// Later tick with the first run still active (Pending) must NOT schedule the
	// second — the project is still at its limit.
	rec.Tick(ctx)
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("second tick scheduled %d, want still 1 (first run still active)", got)
	}

	// Complete the first run; its slot frees and the second schedules next tick.
	first, _ := st.GetRun(ctx, firstScheduled(t, st, ids))
	fake.SetState(first.K8sJobName, k8s.JobSucceeded)
	rec.Tick(ctx) // observes success -> succeeded (frees the slot)
	rec.Tick(ctx) // now schedules the second run
	if got := countByStatus(t, st, ids, domain.StatusSucceeded); got != 1 {
		t.Fatalf("succeeded=%d want 1", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 1 {
		t.Fatalf("after the slot freed, scheduling=%d want 1 (second run)", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 0 {
		t.Fatalf("queued=%d want 0 (both runs progressed)", got)
	}
}

// firstScheduled returns the id of whichever run currently has a Job (the one the
// per-project gate let through first).
func firstScheduled(t *testing.T, st *store.MemStore, ids []string) string {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		r, _ := st.GetRun(ctx, id)
		if r.Status == domain.StatusScheduling && r.K8sJobName != "" {
			return id
		}
	}
	t.Fatal("no scheduled run found")
	return ""
}

// TestPerProjectLimitsAreIndependent: two projects reconciled in the same tick —
// project A capped at 1 (2 queued) and project B uncapped (2 queued). A schedules
// 1, B schedules both. One project's cap never starves another.
func TestPerProjectLimitsAreIndependent(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 10) // generous cluster capacity
	_, aIDs := seedGuardrailProject(t, st, &domain.Project{Name: "A", MaxConcurrentRuns: intp(1)}, 2)
	_, bIDs := seedGuardrailProject(t, st, &domain.Project{Name: "B"}, 2) // nil = inherit (uncapped here)

	rec.Tick(ctx)

	if got := countByStatus(t, st, aIDs, domain.StatusScheduling); got != 1 {
		t.Fatalf("project A scheduled %d, want 1 (cap)", got)
	}
	if got := countByStatus(t, st, bIDs, domain.StatusScheduling); got != 2 {
		t.Fatalf("project B scheduled %d, want 2 (uncapped, independent of A)", got)
	}
}

// TestNilProjectLimitInheritsClusterCap: a project with a nil max_concurrent_runs
// falls back to the CLUSTER limit — 3 queued runs, cluster max 2 => 2 scheduled,
// exactly the pre-Feature-B behaviour (no per-project throttle applied).
func TestNilProjectLimitInheritsClusterCap(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 2) // cluster cap = 2
	_, ids := seedGuardrailProject(t, st, &domain.Project{MaxConcurrentRuns: nil}, 3)

	rec.Tick(ctx)

	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 2 {
		t.Fatalf("scheduled %d, want 2 (cluster cap governs when project cap is nil)", got)
	}
	if got := countByStatus(t, st, ids, domain.StatusQueued); got != 1 {
		t.Fatalf("queued %d, want 1", got)
	}
}

// TestZeroProjectLimitInherits: max_concurrent_runs=0 (or negative) means
// "inherit", NOT "block everything". With a generous cluster cap all runs
// schedule.
func TestZeroProjectLimitInherits(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 10)
	_, ids := seedGuardrailProject(t, st, &domain.Project{MaxConcurrentRuns: intp(0)}, 2)

	rec.Tick(ctx)
	if got := countByStatus(t, st, ids, domain.StatusScheduling); got != 2 {
		t.Fatalf("scheduled %d, want 2 (0 means inherit, not block)", got)
	}
}

// TestRunTimeoutProjectOverride: a project run_timeout_secs override drives the
// runner's RUN_TIMEOUT (exact) and the Job's activeDeadlineSeconds (RUN_TIMEOUT +
// grace, so the runner's own timeout fires before the hard k8s kill).
func TestRunTimeoutProjectOverride(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10) // cfg.RunTimeoutSecs defaults to 1800
	to := int64(600)
	_, ids := seedGuardrailProject(t, st, &domain.Project{RunTimeoutSecs: &to}, 1)

	rec.Tick(ctx)

	if len(fake.Created) != 1 {
		t.Fatalf("created %d jobs, want 1", len(fake.Created))
	}
	spec := fake.Created[0]
	// RUN_TIMEOUT is the exact per-run budget; the Job deadline adds grace.
	if spec.Env["RUN_TIMEOUT"] != "600s" {
		t.Fatalf("RUN_TIMEOUT=%q want 600s (project override)", spec.Env["RUN_TIMEOUT"])
	}
	wantDeadline := int64(600) + timeoutGrace(600) // 600 + max(120, 60) = 720
	if spec.TimeoutSeconds != wantDeadline {
		t.Fatalf("JobSpec.TimeoutSeconds=%d want %d (RUN_TIMEOUT + grace)", spec.TimeoutSeconds, wantDeadline)
	}
	if spec.TimeoutSeconds <= 600 {
		t.Fatalf("Job deadline %d must strictly exceed RUN_TIMEOUT 600 (grace margin)", spec.TimeoutSeconds)
	}
	_ = ids
}

// TestRunTimeoutClusterDefault: with no project override RUN_TIMEOUT uses the
// cluster default (1800) and the Job deadline is 1800 + grace.
func TestRunTimeoutClusterDefault(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	_, _ = seedGuardrailProject(t, st, &domain.Project{RunTimeoutSecs: nil}, 1)

	rec.Tick(ctx)

	spec := fake.Created[0]
	if spec.Env["RUN_TIMEOUT"] != "1800s" {
		t.Fatalf("RUN_TIMEOUT=%q want 1800s (cluster default)", spec.Env["RUN_TIMEOUT"])
	}
	wantDeadline := int64(1800) + timeoutGrace(1800) // 1800 + max(120, 180) = 1980
	if spec.TimeoutSeconds != wantDeadline {
		t.Fatalf("JobSpec.TimeoutSeconds=%d want %d (RUN_TIMEOUT + grace)", spec.TimeoutSeconds, wantDeadline)
	}
}

// TestTimeoutGrace pins the grace formula: floor of 120s, else timeout/10.
func TestTimeoutGrace(t *testing.T) {
	cases := []struct{ in, want int64 }{
		{60, 120},     // floor
		{600, 120},    // 60 < 120 -> floor
		{1200, 120},   // 120 -> floor boundary
		{1800, 180},   // 180 > 120
		{36000, 3600}, // scales with long runs
	}
	for _, c := range cases {
		if got := timeoutGrace(c.in); got != c.want {
			t.Errorf("timeoutGrace(%d)=%d want %d", c.in, got, c.want)
		}
	}
}

// TestInjectedEnvApplied: a project's injected_env KVs land in the runner Job env.
func TestInjectedEnvApplied(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	_, _ = seedGuardrailProject(t, st, &domain.Project{
		InjectedEnv: map[string]string{"COMPANY_TOKEN": "abc", "HTTP_PROXY": "http://proxy:3128"},
	}, 1)

	rec.Tick(ctx)

	env := fake.Created[0].Env
	if env["COMPANY_TOKEN"] != "abc" {
		t.Fatalf("COMPANY_TOKEN=%q want abc (injected)", env["COMPANY_TOKEN"])
	}
	if env["HTTP_PROXY"] != "http://proxy:3128" {
		t.Fatalf("HTTP_PROXY=%q want the injected proxy", env["HTTP_PROXY"])
	}
	// System variables are still intact.
	if env["RUN_ID"] == "" || env["MODEL_NAME"] == "" {
		t.Fatalf("system env clobbered: RUN_ID=%q MODEL_NAME=%q", env["RUN_ID"], env["MODEL_NAME"])
	}
}

// TestInjectedEnvReservedKeysFilteredDefensively: a stale/legacy injected_env row
// carrying reserved system keys (which the API would reject) must NOT override the
// real system variables at Job launch — jobEnv drops them. A non-reserved key in
// the same row is still applied.
func TestInjectedEnvReservedKeysFilteredDefensively(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 10)
	_, _ = seedGuardrailProject(t, st, &domain.Project{
		InjectedEnv: map[string]string{
			"RUN_TOKEN":  "evil-token",
			"MODEL_NAME": "attacker/model",
			"GIT_MODE":   "draft_pr",
			"OK_FLAG":    "yes",
		},
	}, 1)

	rec.Tick(ctx)

	env := fake.Created[0].Env
	if env["RUN_TOKEN"] == "evil-token" {
		t.Fatal("reserved RUN_TOKEN was overridden by injected_env (must be dropped)")
	}
	if env["MODEL_NAME"] == "attacker/model" {
		t.Fatalf("reserved MODEL_NAME overridden by injected_env: %q", env["MODEL_NAME"])
	}
	if env["GIT_MODE"] == "draft_pr" {
		t.Fatalf("reserved GIT_MODE overridden by injected_env: %q", env["GIT_MODE"])
	}
	if env["OK_FLAG"] != "yes" {
		t.Fatalf("non-reserved OK_FLAG=%q want yes (should still be injected)", env["OK_FLAG"])
	}
}
