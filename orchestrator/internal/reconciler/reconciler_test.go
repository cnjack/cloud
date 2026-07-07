package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/modelcfg"
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
		// Feature A: the env-source model config so createJob's fail-visible gate
		// treats the cluster as configured and schedules (mirrors the e2e rig).
		ModelBaseURL: "http://model.test/v1",
		ModelName:    "mock/mock-model",
		ModelAPIKey:  "test-key",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, fake, cfg, log, nil), st, fake
}

// seedProjectAndRun creates a project + a readonly raw 'default' service + a
// queued run against it — the default (diff-only) shape.
func seedProjectAndRun(t *testing.T, st *store.MemStore) domain.Run {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
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
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "do the thing",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	return *run
}

// jobEnvForService seeds a project + a 'default' service with the given repo
// config + a queued run, sets cfg.GiteaURL/GiteaToken, and returns the jobEnv
// the reconciler would inject — the seam for the git-env token/push tests.
func jobEnvForService(t *testing.T, svc domain.Service, tokenCfg, urlCfg string) map[string]string {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemStore()
	fake := k8s.NewFakeLauncher()
	cfg := &config.Config{
		ReconcileInterval: time.Millisecond,
		MaxConcurrentRuns: 4,
		RunTimeoutSecs:    1800,
		OrchBaseURL:       "http://orch",
		RunnerImage:       "runner:test",
		GiteaToken:        tokenCfg,
		GiteaURL:          urlCfg,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := New(st, fake, cfg, log, nil)
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc.ID = domain.NewID()
	svc.ProjectID = p.ID
	svc.Name = "default"
	if svc.DefaultBranch == "" {
		svc.DefaultBranch = "main"
	}
	svc.CreatedAt = time.Now()
	if err := st.CreateService(ctx, &svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "do the thing",
		Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	return rec.jobEnv(ctx, run, "run-token", envModel())
}

// envModel is a resolved env-source model config for the jobEnv unit tests
// (Feature A): jobEnv now takes the effective model config, not cfg fields.
func envModel() modelcfg.Resolved {
	return modelcfg.Resolved{
		Source: modelcfg.SourceEnv, BaseURL: "http://model.test/v1",
		ModelName: "mock/mock-model", APIKey: "test-key", APIKeySet: true,
	}
}

// TestCreateJobFailsVisiblyWhenModelNotConfigured is the fail-visible gate at
// Job-launch (CLAUDE.md red line #1): a run queued while the model was
// configured but scheduled after it was cleared must be marked FAILED with a
// visible run.failure event — never launched against a mock/empty model.
func TestCreateJobFailsVisiblyWhenModelNotConfigured(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	// Model becomes unconfigured (env cleared, no DB row).
	rec.cfg.ModelBaseURL, rec.cfg.ModelName, rec.cfg.ModelAPIKey = "", "", ""
	run := seedProjectAndRun(t, st)

	rec.Tick(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusFailed {
		t.Fatalf("status=%q want failed", got.Status)
	}
	if got.FailureReason != domain.FailureSetupFailed {
		t.Fatalf("failure_reason=%q want setup_failed", got.FailureReason)
	}
	if len(fake.Created) != 0 {
		t.Fatalf("created %d jobs, want 0 (must not launch a runner with no model)", len(fake.Created))
	}
	events, _ := st.ListEvents(ctx, run.ID, 0, 100)
	var sawFailure bool
	for _, e := range events {
		if e.Type == domain.EventRunFailure {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatalf("expected a visible run.failure event; got %d events", len(events))
	}
}

// erroringModelStore wraps a MemStore so GetModelConfig fails — the transient
// (DB blip / key rotation) shape, distinct from "no row" (not configured).
type erroringModelStore struct {
	*store.MemStore
}

func (e *erroringModelStore) GetModelConfig(context.Context) (*domain.ModelConfig, error) {
	return nil, errTransientModel
}

var errTransientModel = errors.New("transient db error")

// TestCreateJobRetriesOnModelResolveError: a TRANSIENT model-config resolve
// error must NOT permanently fail the run — the tick logs and skips (run stays
// queued, no Job), and once the store recovers the next tick schedules it,
// consistent with the other transient-error paths in Tick.
func TestCreateJobRetriesOnModelResolveError(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemStore()
	st := &erroringModelStore{MemStore: inner}
	fake := k8s.NewFakeLauncher()
	cfg := &config.Config{
		ReconcileInterval: time.Millisecond,
		MaxConcurrentRuns: 4,
		RunTimeoutSecs:    1800,
		OrchBaseURL:       "http://orch",
		RunnerImage:       "runner:test",
		ModelBaseURL:      "http://model.test/v1",
		ModelName:         "mock/mock-model",
		ModelAPIKey:       "test-key",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := New(st, fake, cfg, log, nil)
	run := seedProjectAndRun(t, inner)

	// While the store errors: no Job, and the run is NOT failed — still queued.
	rec.Tick(ctx)
	got, _ := inner.GetRun(ctx, run.ID)
	if got.Status != domain.StatusQueued {
		t.Fatalf("status=%q want queued (transient error must not fail the run)", got.Status)
	}
	if len(fake.Created) != 0 {
		t.Fatalf("created %d jobs, want 0 while resolve errors", len(fake.Created))
	}

	// Store recovers (point the resolver at the healthy inner store): the very
	// next tick schedules the run. Errors were never cached, so no TTL wait.
	rec.st = inner
	rec.models = modelcfg.NewResolver(inner, nil, cfg)
	rec.Tick(ctx)
	got, _ = inner.GetRun(ctx, run.ID)
	if got.Status != domain.StatusScheduling {
		t.Fatalf("status=%q want scheduling after recovery", got.Status)
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created %d jobs, want 1 after recovery", len(fake.Created))
	}
}

// assertNoToken asserts the M3 credential-free contract: NO provider token of
// any name is ever injected into the runner env.
func assertNoToken(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range []string{"GIT_TOKEN", "GIT_PUSH_URL"} {
		if _, ok := env[k]; ok {
			t.Fatalf("M3: %s must never be injected into the runner (got %q)", k, env[k])
		}
	}
}

// TestJobEnvProviderReadonlyUsesFetch is the M3 contract for a provider service:
// SOURCE_MODE=fetch (the orchestrator serves a source bundle over the RUN_TOKEN
// — no credential in the pod, and public/private is not guessed), GIT_MODE stays
// readonly, and NO token is injected. REPO_URL is omitted (the runner fetches).
func TestJobEnvProviderReadonlyUsesFetch(t *testing.T) {
	env := jobEnvForService(t, domain.Service{
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "ai/priv", GitMode: domain.GitModeReadonly,
	}, "secret-pat", "https://git.example.com")
	assertNoToken(t, env)
	if env["SOURCE_MODE"] != "fetch" {
		t.Fatalf("SOURCE_MODE=%q want fetch (unified provider path)", env["SOURCE_MODE"])
	}
	if env["GIT_MODE"] != string(domain.GitModeReadonly) {
		t.Fatalf("GIT_MODE=%q want readonly", env["GIT_MODE"])
	}
	if env["BASE_BRANCH"] != "main" {
		t.Fatalf("BASE_BRANCH=%q want main", env["BASE_BRANCH"])
	}
	if _, ok := env["REPO_URL"]; ok {
		t.Fatalf("fetch mode must not set REPO_URL (runner fetches a bundle); got %q", env["REPO_URL"])
	}
	if _, ok := env["BRANCH_NAME"]; ok {
		t.Fatalf("readonly must not carry a push BRANCH_NAME; got %q", env["BRANCH_NAME"])
	}
}

// TestJobEnvGithubProviderUsesFetchNoToken verifies a github provider service
// also takes the unified fetch path and never receives a token.
func TestJobEnvGithubProviderUsesFetchNoToken(t *testing.T) {
	env := jobEnvForService(t, domain.Service{
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitHub,
		RepoOwnerName: "someone/other", GitMode: domain.GitModeReadonly,
	}, "secret-pat", "https://git.example.com")
	assertNoToken(t, env)
	if env["SOURCE_MODE"] != "fetch" {
		t.Fatalf("SOURCE_MODE=%q want fetch", env["SOURCE_MODE"])
	}
}

// TestJobEnvRawRepoClonesDirectly verifies raw services (git://, file://, opaque
// http) use SOURCE_MODE=clone and clone their raw URL as-is with no token.
func TestJobEnvRawRepoClonesDirectly(t *testing.T) {
	env := jobEnvForService(t, domain.Service{
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://git.jcloud/seed.git",
		GitMode: domain.GitModeReadonly,
	}, "secret-pat", "https://git.example.com")
	assertNoToken(t, env)
	if env["SOURCE_MODE"] != "clone" {
		t.Fatalf("SOURCE_MODE=%q want clone (raw repo)", env["SOURCE_MODE"])
	}
	if env["REPO_URL"] != "git://git.jcloud/seed.git" {
		t.Fatalf("REPO_URL=%q want the raw url as-is", env["REPO_URL"])
	}
}

// TestJobEnvDraftPRGiteaProducesBundle verifies draft_pr on a gitea provider
// service tells the runner to produce a bundle: GIT_MODE=draft_pr + a
// deterministic jcode/run-<8> BRANCH_NAME, fetch source, and NO token.
func TestJobEnvDraftPRGiteaProducesBundle(t *testing.T) {
	env := jobEnvForService(t, domain.Service{
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "ai/priv", GitMode: domain.GitModeDraftPR,
	}, "secret-pat", "https://git.example.com")
	assertNoToken(t, env)
	if env["GIT_MODE"] != string(domain.GitModeDraftPR) {
		t.Fatalf("GIT_MODE=%q want draft_pr", env["GIT_MODE"])
	}
	if env["SOURCE_MODE"] != "fetch" {
		t.Fatalf("SOURCE_MODE=%q want fetch", env["SOURCE_MODE"])
	}
	if !strings.HasPrefix(env["BRANCH_NAME"], "jcode/run-") {
		t.Fatalf("BRANCH_NAME=%q want jcode/run-<id> prefix", env["BRANCH_NAME"])
	}
	if env["BASE_BRANCH"] != "main" {
		t.Fatalf("BASE_BRANCH=%q want main", env["BASE_BRANCH"])
	}
}

// TestJobEnvDraftPRNonGiteaAlsoProducesBundle verifies draft_pr on ANY provider
// (github here) also asks the runner to bundle — M3 no longer restricts the
// push flow to gitea (the orchestrator pushes with the user's OAuth token).
func TestJobEnvDraftPRNonGiteaAlsoProducesBundle(t *testing.T) {
	env := jobEnvForService(t, domain.Service{
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitHub,
		RepoOwnerName: "someone/other", GitMode: domain.GitModeDraftPR,
	}, "secret-pat", "https://git.example.com")
	assertNoToken(t, env)
	if env["GIT_MODE"] != string(domain.GitModeDraftPR) {
		t.Fatalf("GIT_MODE=%q want draft_pr (any provider produces a bundle in M3)", env["GIT_MODE"])
	}
	if env["BRANCH_NAME"] == "" {
		t.Fatalf("draft_pr must set BRANCH_NAME")
	}
}

// TestJobEnvRawDraftPRStaysReadonly verifies a raw service can only be readonly
// (draft_pr requires a provider) — it never produces a bundle.
func TestJobEnvRawDraftPRStaysReadonly(t *testing.T) {
	env := jobEnvForService(t, domain.Service{
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://git/x.git",
		GitMode: domain.GitModeDraftPR, // draft_pr on a raw repo is not eligible
	}, "secret-pat", "https://git.example.com")
	if env["GIT_MODE"] != string(domain.GitModeReadonly) {
		t.Fatalf("GIT_MODE=%q want readonly (raw cannot draft_pr)", env["GIT_MODE"])
	}
	if _, ok := env["BRANCH_NAME"]; ok {
		t.Fatalf("raw readonly must not set BRANCH_NAME")
	}
}

// TestJobEnvReviewInjectsPRRefs verifies a review run carries RUN_KIND=review and
// the PR_HEAD/PR_BASE refs the runner diffs.
func TestJobEnvReviewInjectsPRRefs(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	rec, _, _ := testRec(t, 4)
	rec.st = st
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "o/r", DefaultBranch: "main", GitMode: domain.GitModeDraftPR,
		CreatedAt: time.Now(),
	}
	_ = st.CreateService(ctx, svc)
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "review it",
		Status: domain.StatusQueued, Kind: domain.RunKindReview,
		PRHeadBranch: "jcode/run-abc", PRBaseBranch: "main", CreatedAt: time.Now(),
	}
	_ = st.CreateRun(ctx, run)
	env := rec.jobEnv(ctx, run, "tok", envModel())
	if env["RUN_KIND"] != string(domain.RunKindReview) {
		t.Fatalf("RUN_KIND=%q want review", env["RUN_KIND"])
	}
	if env["PR_HEAD"] != "jcode/run-abc" || env["PR_BASE"] != "main" {
		t.Fatalf("review PR refs wrong: head=%q base=%q", env["PR_HEAD"], env["PR_BASE"])
	}
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
	// Seed 4 queued runs on one project/service.
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "git://git/x.git", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = st.CreateService(ctx, svc)
	for i := 0; i < 4; i++ {
		_ = st.CreateRun(ctx, &domain.Run{
			ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "t",
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
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "git://git/x.git", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = st.CreateService(ctx, svc)
	for i := 0; i < 5; i++ {
		_ = st.CreateRun(ctx, &domain.Run{
			ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "t",
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
	rec.Tick(ctx)                                // queued -> scheduling
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
