package store

import (
	"context"
	"errors"
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

	p := &domain.Project{ID: domain.NewID(), Name: "pgtest", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatalf("create service: %v", err)
	}
	r := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "x", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, p.ID) }) // cascades runs/events
	return st, r.ID
}

// TestPGIntegrationRoundTrip exercises the D19/F5 integration pgx paths that the
// memory store cannot: the bytea token blob round-trip, unique(project_id,name),
// UpdateIntegration rotation, the services.integration_id FK, and ON DELETE SET
// NULL unbinding a bound service. Requires JCLOUD_PG_DSN.
func TestPGIntegrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed store test")
	}
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	p := &domain.Project{ID: domain.NewID(), Name: "integ-pg", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, p.ID) })

	in := &domain.Integration{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		Provider: domain.ProviderGitea, Host: "gitea.example.com", CredType: domain.CredTypePAT,
		TokenEnc: []byte{0x01, 0x02, 0x03, 0xff}, BotUsername: "bot1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.CreateIntegration(ctx, in); err != nil {
		t.Fatalf("create integration: %v", err)
	}
	// unique(project_id, name).
	dup := *in
	dup.ID = domain.NewID()
	if err := st.CreateIntegration(ctx, &dup); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("dup create err=%v want ErrAlreadyExists", err)
	}

	got, err := st.GetIntegration(ctx, in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.TokenEnc) != string(in.TokenEnc) || got.BotUsername != "bot1" || got.CredType != domain.CredTypePAT {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Rotate token + bot_username.
	in.TokenEnc = []byte{0xaa, 0xbb}
	in.BotUsername = "bot2"
	if err := st.UpdateIntegration(ctx, in); err != nil {
		t.Fatalf("update: %v", err)
	}
	rot, _ := st.GetIntegration(ctx, in.ID)
	if string(rot.TokenEnc) != "\xaa\xbb" || rot.BotUsername != "bot2" {
		t.Fatalf("rotated mismatch: %+v", rot)
	}

	// Bind a service, then delete the integration → FK SET NULL unbinds it.
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "widget",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "acme/widget",
		DefaultBranch: "main", IntegrationID: &in.ID, CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatalf("create service: %v", err)
	}
	if n, _ := st.CountServicesUsingIntegration(ctx, in.ID); n != 1 {
		t.Fatalf("count services=%d want 1", n)
	}
	if err := st.DeleteIntegration(ctx, in.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	unbound, err := st.GetService(ctx, svc.ID)
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if unbound.IntegrationID != nil {
		t.Fatalf("integration_id=%v want nil after delete (FK SET NULL)", *unbound.IntegrationID)
	}
}

// TestPGProjectGuardrailsAndServiceRoundTrip exercises the pgx codec paths that
// the memory store cannot: jsonb injected_env <-> map[string]string, text[]
// provider_allowlist <-> []string, nullable int/bigint guardrails, and the
// provider/raw service columns (nullable provider/owner/raw). It also covers
// ListRunsByService.
func TestPGProjectGuardrailsAndServiceRoundTrip(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed store test")
	}
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	maxRuns := 7
	timeout := int64(1234)
	p := &domain.Project{
		ID: domain.NewID(), Name: "guard", CreatedAt: time.Now(),
		MaxConcurrentRuns: &maxRuns, RunTimeoutSecs: &timeout,
		ProviderAllowlist: []string{"gitea", "github"},
		InjectedEnv:       map[string]string{"FOO": "bar", "BAZ": "qux"},
	}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, p.ID) })

	gotP, err := st.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if gotP.MaxConcurrentRuns == nil || *gotP.MaxConcurrentRuns != 7 {
		t.Fatalf("max_concurrent_runs = %v want 7", gotP.MaxConcurrentRuns)
	}
	if gotP.RunTimeoutSecs == nil || *gotP.RunTimeoutSecs != 1234 {
		t.Fatalf("run_timeout_secs = %v want 1234", gotP.RunTimeoutSecs)
	}
	if len(gotP.ProviderAllowlist) != 2 || gotP.ProviderAllowlist[0] != "gitea" {
		t.Fatalf("provider_allowlist = %v", gotP.ProviderAllowlist)
	}
	if gotP.InjectedEnv["FOO"] != "bar" || gotP.InjectedEnv["BAZ"] != "qux" {
		t.Fatalf("injected_env = %v", gotP.InjectedEnv)
	}

	// A project with nil guardrails round-trips as nil/empty (inherit global).
	p2 := &domain.Project{ID: domain.NewID(), Name: "bare", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p2); err != nil {
		t.Fatalf("create bare project: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, p2.ID) })
	gotP2, _ := st.GetProject(ctx, p2.ID)
	if gotP2.MaxConcurrentRuns != nil || gotP2.RunTimeoutSecs != nil || len(gotP2.ProviderAllowlist) != 0 {
		t.Fatalf("bare guardrails not nil: %+v", gotP2)
	}

	// Provider service round-trip (nullable provider/owner set, raw NULL).
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "jcloud/seed", DefaultBranch: "main", GitMode: domain.GitModeDraftPR,
		CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatalf("create provider service: %v", err)
	}
	// Raw service round-trip (provider/owner NULL, raw set).
	rawSvc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "seed",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/seed.git",
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, rawSvc); err != nil {
		t.Fatalf("create raw service: %v", err)
	}

	gotSvc, err := st.GetDefaultService(ctx, p.ID)
	if err != nil {
		t.Fatalf("get default service: %v", err)
	}
	if gotSvc.Provider != domain.ProviderGitea || gotSvc.RepoOwnerName != "jcloud/seed" ||
		gotSvc.RawRepoURL != "" || gotSvc.GitMode != domain.GitModeDraftPR {
		t.Fatalf("provider service round-trip wrong: %+v", gotSvc)
	}
	gotRaw, _ := st.GetService(ctx, rawSvc.ID)
	if gotRaw.RepoKind != domain.RepoKindRaw || gotRaw.RawRepoURL != "git://x/seed.git" || gotRaw.Provider != "" {
		t.Fatalf("raw service round-trip wrong: %+v", gotRaw)
	}
	if svcs, _ := st.ListServices(ctx, p.ID); len(svcs) != 2 {
		t.Fatalf("ListServices len=%d want 2", len(svcs))
	}

	// ListRunsByService returns only the service's runs.
	r1 := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "a", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	r2 := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: rawSvc.ID, Prompt: "b", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	_ = st.CreateRun(ctx, r1)
	_ = st.CreateRun(ctx, r2)
	bySvc, err := st.ListRunsByService(ctx, svc.ID, 100)
	if err != nil {
		t.Fatalf("list runs by service: %v", err)
	}
	if len(bySvc) != 1 || bySvc[0].ID != r1.ID {
		t.Fatalf("ListRunsByService = %+v want [%s]", bySvc, r1.ID)
	}
	if bySvc[0].Kind != domain.RunKindAgent {
		t.Fatalf("run kind = %q want agent (default)", bySvc[0].Kind)
	}
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

// TestPGMarkFailedPreservesRunnerReason is the real-DB regression for the
// stale-full-row lost-update finding: a runner-reported specific reason recorded
// via SetRunnerFailure must survive a subsequent generic MarkFailed. Requires
// JCLOUD_PG_DSN.
func TestPGMarkFailedPreservesRunnerReason(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	if _, err := st.ScheduleRun(ctx, runID, "job", "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, runID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Runner records the specific reason first.
	if _, err := st.SetRunnerFailure(ctx, runID, domain.FailureCloneFailed, "fatal: repo not found"); err != nil {
		t.Fatal(err)
	}
	// Reconciler fails from generic cluster state.
	got, err := st.MarkFailed(ctx, runID, "Failed", domain.FailureAgentError, "runner Job failed", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureReason != domain.FailureCloneFailed {
		t.Fatalf("reason=%s want clone_failed (runner-reported must win)", got.FailureReason)
	}
	if got.FailureMessage != "fatal: repo not found" {
		t.Fatalf("message=%q want runner message", got.FailureMessage)
	}
	if got.Error != got.FailureMessage {
		t.Fatalf("error=%q want to mirror failure_message", got.Error)
	}
	// Job name / token hash must not have been wiped.
	if got.K8sJobName != "job" || got.TokenHash != "hash" {
		t.Fatalf("MarkFailed wiped job/token: job=%q token=%q", got.K8sJobName, got.TokenHash)
	}
}

// TestPGSetRunResult proves the nullable runs.result column round-trips: a fresh
// run has a NULL result (nil pointer), SetRunResult stamps it first-writer-wins
// without touching status, a duplicate differing call is a no-op, and the value
// survives a subsequent MarkSucceeded (empty-diff run exits 0 → succeeded; D18).
// Requires JCLOUD_PG_DSN.
func TestPGSetRunResult(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	if r0, err := st.GetRun(ctx, runID); err != nil {
		t.Fatal(err)
	} else if r0.Result != nil {
		t.Fatalf("fresh run already has result %v", *r0.Result)
	}

	if _, err := st.ScheduleRun(ctx, runID, "job", "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, runID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := st.SetRunResult(ctx, runID, domain.RunResultNoChanges)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result == nil || *got.Result != domain.RunResultNoChanges {
		t.Fatalf("result=%v want no_changes", got.Result)
	}
	if got.Status != domain.StatusRunning {
		t.Fatalf("status=%s; SetRunResult must not change status", got.Status)
	}

	// Duplicate differing call: first-writer-wins (no-op).
	if got2, err := st.SetRunResult(ctx, runID, domain.RunResult("something_else")); err != nil {
		t.Fatal(err)
	} else if got2.Result == nil || *got2.Result != domain.RunResultNoChanges {
		t.Fatalf("result changed to %v; first-writer must win", got2.Result)
	}

	// Survives the terminal transition.
	done, err := st.MarkSucceeded(ctx, runID, "Succeeded", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if done.Result == nil || *done.Result != domain.RunResultNoChanges {
		t.Fatalf("result=%v after MarkSucceeded want no_changes preserved", done.Result)
	}
}

// TestPGConcurrentFailPreservesReason races SetRunnerFailure against MarkFailed
// on a real DB (row-lock serialised) and asserts the specific reason is never
// lost. Requires JCLOUD_PG_DSN.
func TestPGConcurrentFailPreservesReason(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)
	if _, err := st.ScheduleRun(ctx, runID, "job", "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, runID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = st.SetRunnerFailure(ctx, runID, domain.FailureCloneFailed, "specific") }()
	go func() {
		defer wg.Done()
		_, _ = st.MarkFailed(ctx, runID, "Failed", domain.FailureAgentError, "generic", time.Now())
	}()
	wg.Wait()

	got, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusFailed {
		t.Fatalf("status=%s want failed", got.Status)
	}
	if got.FailureReason == "" {
		t.Fatal("empty failure reason after concurrent fail")
	}
	if got.FailureReason == domain.FailureCloneFailed && got.FailureMessage != "specific" {
		t.Fatalf("clone_failed but message=%q", got.FailureMessage)
	}
}

// TestPGMarkJobCleanedPreservesJobName is the real-DB regression for the
// job_cleaned_at rework (migration 0003): cleanup bookkeeping must stamp
// job_cleaned_at while KEEPING k8s_job_name (the run's historical record), and
// ListTerminalRunsWithJob must stop returning the run once stamped. Requires
// JCLOUD_PG_DSN.
func TestPGMarkJobCleanedPreservesJobName(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	if _, err := st.ScheduleRun(ctx, runID, "jcloud-run-"+runID, "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelRun(ctx, runID, "CanceledByOperator", time.Now()); err != nil {
		t.Fatal(err)
	}

	// Un-cleaned terminal run with a job: must be listed.
	pending, err := st.ListTerminalRunsWithJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range pending {
		if r.ID == runID {
			found = true
		}
	}
	if !found {
		t.Fatal("terminal run with un-cleaned job not listed for cleanup")
	}

	if err := st.MarkJobCleaned(ctx, runID); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.K8sJobName != "jcloud-run-"+runID {
		t.Fatalf("k8s_job_name=%q want preserved (historical record)", got.K8sJobName)
	}
	if got.JobCleanedAt == nil {
		t.Fatal("job_cleaned_at not stamped")
	}
	first := *got.JobCleanedAt

	// Idempotent: a second stamp must not move the timestamp.
	if err := st.MarkJobCleaned(ctx, runID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetRun(ctx, runID)
	if got.JobCleanedAt == nil || !got.JobCleanedAt.Equal(first) {
		t.Fatalf("job_cleaned_at moved on re-stamp: %v -> %v", first, got.JobCleanedAt)
	}

	// Cleaned run must no longer be listed.
	pending, err = st.ListTerminalRunsWithJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.ID == runID {
			t.Fatal("cleaned run still returned by ListTerminalRunsWithJob")
		}
	}
}

// TestPGMarkPRCreatedIdempotent is the real-DB regression for MarkPRCreated
// (ST-1 migration 0004): first-writer-wins so a retried reconcile cannot
// double-open, and status/other columns are untouched. Requires JCLOUD_PG_DSN.
func TestPGMarkPRCreatedIdempotent(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	// Record a branch first (as the runner would), then open the PR.
	if _, err := st.SetRunGit(ctx, runID, "agent/run-x", "sha1"); err != nil {
		t.Fatal(err)
	}
	got, err := st.MarkPRCreated(ctx, runID, "http://gitea/pulls/5", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "http://gitea/pulls/5" || got.PRNumber != 5 {
		t.Fatalf("first mark: url=%q num=%d", got.PRURL, got.PRNumber)
	}
	// A second (racing/retried) mark with different values must be ignored.
	got, err = st.MarkPRCreated(ctx, runID, "http://gitea/pulls/9", 9)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "http://gitea/pulls/5" || got.PRNumber != 5 {
		t.Fatalf("second mark clobbered PR: url=%q num=%d", got.PRURL, got.PRNumber)
	}
	// Branch/commit preserved; status unchanged.
	if got.GitBranch != "agent/run-x" || got.CommitSHA != "sha1" {
		t.Fatalf("git fields lost: branch=%q commit=%q", got.GitBranch, got.CommitSHA)
	}
}

// TestPGListRunsAwaitingPR proves the awaiting-PR scan filters correctly on a
// real DB. Requires JCLOUD_PG_DSN.
func TestPGListRunsAwaitingPR(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	// The seeded run is queued with no branch: not awaiting.
	if runs, _ := st.ListRunsAwaitingPR(ctx); containsRun(runs, runID) {
		t.Fatal("queued run should not be awaiting PR")
	}
	// Move it to succeeded with a branch.
	if _, err := st.ScheduleRun(ctx, runID, "job", "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, runID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, runID, "Succeeded", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunGit(ctx, runID, "agent/run-x", "sha"); err != nil {
		t.Fatal(err)
	}
	if runs, _ := st.ListRunsAwaitingPR(ctx); !containsRun(runs, runID) {
		t.Fatal("succeeded run with branch + no PR should be awaiting")
	}
	// Once the PR is stamped it drops out of the scan.
	if _, err := st.MarkPRCreated(ctx, runID, "http://gitea/pulls/1", 1); err != nil {
		t.Fatal(err)
	}
	if runs, _ := st.ListRunsAwaitingPR(ctx); containsRun(runs, runID) {
		t.Fatal("run with PR should no longer be awaiting")
	}
}

func containsRun(runs []domain.Run, id string) bool {
	for _, r := range runs {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TestPGModelCatalogRoundTrip validates the D21 catalog against a real Postgres:
// create (unique name), bytea api_key_enc round-trip, update, grants +
// ListModelsForProject / ListProjectIDsForModel, and the ON DELETE cascade/SET
// NULL to grants + service defaults + run refs. Requires JCLOUD_PG_DSN.
func TestPGModelCatalogRoundTrip(t *testing.T) {
	st, _ := pgTestStore(t)
	ctx := context.Background()

	// Own project so the grant cascade assertions are isolated.
	projectID := domain.NewID()
	if err := st.CreateProject(ctx, &domain.Project{ID: projectID, Name: "mcat", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, projectID) })

	m := &domain.Model{
		ID: domain.NewID(), Name: "gpt-" + domain.NewID(), BaseURL: "https://api.openai.com/v1",
		ModelName: "openai/gpt-4o", APIKeyEnc: []byte{0x00, 0x01, 0x02, 0xff}, UpdatedBy: "admin-user",
	}
	if err := st.CreateModel(ctx, m); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteModel(ctx, m.ID) })

	// Duplicate name => ErrAlreadyExists.
	if err := st.CreateModel(ctx, &domain.Model{ID: domain.NewID(), Name: m.Name, BaseURL: "http://y/v1", ModelName: "c/d"}); err != ErrAlreadyExists {
		t.Fatalf("dup name: err=%v want ErrAlreadyExists", err)
	}

	got, err := st.GetModel(ctx, m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ModelName != "openai/gpt-4o" || len(got.APIKeyEnc) != 4 || got.APIKeyEnc[3] != 0xff || got.UpdatedBy != "admin-user" {
		t.Fatalf("round-trip mismatch: %+v (enc=%v)", got, got.APIKeyEnc)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("created_at/updated_at should be stamped")
	}

	// Update (rename + keyless).
	m.Name = "renamed-" + domain.NewID()
	m.APIKeyEnc = nil
	if err := st.UpdateModel(ctx, m); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = st.GetModel(ctx, m.ID)
	if got.Name != m.Name || got.APIKeyEnc != nil {
		t.Fatalf("update mismatch: %+v", got)
	}

	// Grants + membership listing.
	if err := st.GrantModel(ctx, m.ID, projectID); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := st.GrantModel(ctx, m.ID, projectID); err != nil {
		t.Fatalf("re-grant should be a no-op: %v", err)
	}
	if list, _ := st.ListModelsForProject(ctx, projectID); len(list) != 1 || list[0].ID != m.ID {
		t.Fatalf("ListModelsForProject mismatch: %+v", list)
	}
	if pids, _ := st.ListProjectIDsForModel(ctx, m.ID); len(pids) != 1 || pids[0] != projectID {
		t.Fatalf("ListProjectIDsForModel mismatch: %+v", pids)
	}

	// A bad model/project id trips the FK and normalises to ErrNotFound.
	if err := st.GrantModel(ctx, "no-such-model", projectID); err != ErrNotFound {
		t.Fatalf("grant bad model: err=%v want ErrNotFound", err)
	}

	// Delete => grants cascade, ListModelsForProject empty.
	if err := st.DeleteModel(ctx, m.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := st.ListModelsForProject(ctx, projectID); len(list) != 0 {
		t.Fatalf("grants should cascade on delete: %+v", list)
	}
	if _, err := st.GetModel(ctx, m.ID); err != ErrNotFound {
		t.Fatalf("get after delete: err=%v want ErrNotFound", err)
	}
}
