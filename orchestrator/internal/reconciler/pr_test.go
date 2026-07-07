package reconciler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// seedDraftPRRun creates a draft_pr project + a succeeded run that reported a
// pushed branch, mirroring the state the runner leaves behind for the PR pass.
func seedDraftPRRun(t *testing.T, st *store.MemStore, branch string) (domain.Project, domain.Run) {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{
		ID: domain.NewID(), Name: "gp", RepoURL: "http://git/x.git", DefaultBranch: "main",
		GitMode: domain.GitModeDraftPR, Provider: domain.ProviderGitea,
		ProviderURL: "http://gitea.test", ProviderRepo: "jcloud/seed",
		CreatedAt: time.Now(),
	}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, Prompt: "add a Hello line to README",
		Status: domain.StatusSucceeded, Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunGit(ctx, run.ID, branch, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRun(ctx, run.ID)
	return *p, *got
}

// TestReconcilePRCreation covers the happy-path PR pass: a succeeded draft_pr
// run with a pushed branch and no PR gets exactly one draft PR opened, pr_url /
// pr_number are persisted, and a run.status event carrying pr_url is emitted.
func TestReconcilePRCreation(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	rec.WithProvider(fake)

	_, run := seedDraftPRRun(t, st, "agent/run-abc")

	rec.reconcilePRs(ctx)

	if fake.CreatedCount() != 1 {
		t.Fatalf("created %d PRs, want 1", fake.CreatedCount())
	}
	// The created PR must be a draft targeting the reported branch and base.
	in := fake.Created[0]
	if in.Head != "agent/run-abc" {
		t.Errorf("PR head=%q want agent/run-abc", in.Head)
	}
	if in.Base != "main" {
		t.Errorf("PR base=%q want main", in.Base)
	}
	if !strings.HasPrefix(in.Title, "[jcode] ") {
		t.Errorf("PR title=%q want '[jcode] ' prefix", in.Title)
	}
	if !strings.Contains(in.Body, run.ID) {
		t.Errorf("PR body must link the run id; got %q", in.Body)
	}

	got, _ := st.GetRun(ctx, run.ID)
	if got.PRURL == "" || got.PRNumber == 0 {
		t.Fatalf("pr_url/pr_number not persisted: url=%q num=%d", got.PRURL, got.PRNumber)
	}

	// A run.status event carrying pr_url must have been emitted for the console.
	events, _ := st.ListEvents(ctx, run.ID, 0, 100)
	var sawPRStatus bool
	for _, e := range events {
		if e.Type == domain.EventRunStatus && e.Payload["pr_url"] != nil {
			sawPRStatus = true
		}
	}
	if !sawPRStatus {
		t.Error("expected a run.status event carrying pr_url")
	}
}

// TestReconcilePRCreationIdempotent proves the reconcile pass never double-opens
// a PR: a second tick finds pr_url set (run no longer in the awaiting scan) and
// even a forced re-run against a fresh store row wouldn't re-create because
// FindOpenPRByHead returns the existing one.
func TestReconcilePRCreationIdempotent(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	rec.WithProvider(fake)

	_, run := seedDraftPRRun(t, st, "agent/run-idem")

	rec.reconcilePRs(ctx)
	rec.reconcilePRs(ctx)
	rec.reconcilePRs(ctx)

	if fake.CreatedCount() != 1 {
		t.Fatalf("created %d PRs across 3 ticks, want 1 (idempotent)", fake.CreatedCount())
	}
	_ = run
}

// TestReconcilePRPreExisting covers the crash-between-create-and-persist path:
// the provider already has an open PR for the head branch (created by a prior
// tick that crashed before MarkPRCreated). The reconciler must ADOPT it — record
// its url/number — and NOT create a second PR.
func TestReconcilePRPreExisting(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	rec.WithProvider(fake)

	_, run := seedDraftPRRun(t, st, "agent/run-adopt")
	// Simulate a PR that already exists for this head (prior crashed tick).
	fake.Seed("jcloud", "seed", "agent/run-adopt", provider.PR{Number: 7, URL: "http://gitea.test/jcloud/seed/pulls/7"})

	rec.reconcilePRs(ctx)

	if fake.CreatedCount() != 0 {
		t.Fatalf("created %d PRs, want 0 (must adopt the existing one)", fake.CreatedCount())
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.PRNumber != 7 || got.PRURL != "http://gitea.test/jcloud/seed/pulls/7" {
		t.Fatalf("did not adopt existing PR: url=%q num=%d", got.PRURL, got.PRNumber)
	}
}

// TestReconcilePRSkipsWhenNoProvider proves the flow degrades cleanly to
// diff-only when no provider is configured (nil): no panic, no PR, run untouched.
func TestReconcilePRSkipsWhenNoProvider(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4) // no WithProvider

	_, run := seedDraftPRRun(t, st, "agent/run-noprov")
	rec.reconcilePRs(ctx) // must be a no-op

	got, _ := st.GetRun(ctx, run.ID)
	if got.PRURL != "" {
		t.Fatalf("pr_url set with no provider: %q", got.PRURL)
	}
}

// TestReconcilePRTransientErrorRetries proves a transient provider error leaves
// the run in the awaiting scan (no pr_url), so the next tick retries.
func TestReconcilePRTransientErrorRetries(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	fake.CreateErr = errors.New("gitea 503")
	rec.WithProvider(fake)

	_, run := seedDraftPRRun(t, st, "agent/run-retry")
	rec.reconcilePRs(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.PRURL != "" {
		t.Fatalf("pr_url set despite create error: %q", got.PRURL)
	}
	// Recover: clear the error; next tick opens the PR.
	fake.CreateErr = nil
	rec.reconcilePRs(ctx)
	got, _ = st.GetRun(ctx, run.ID)
	if got.PRURL == "" {
		t.Fatal("pr_url not set after provider recovered")
	}
}

// TestShouldOpenPR is the exhaustive table for the pure PR-creation gate.
func TestShouldOpenPR(t *testing.T) {
	base := func() (domain.Run, domain.Project) {
		return domain.Run{
				Status: domain.StatusSucceeded, GitBranch: "agent/run-x", PRURL: "",
			}, domain.Project{
				GitMode: domain.GitModeDraftPR, Provider: domain.ProviderGitea, ProviderRepo: "o/r",
			}
	}
	cases := []struct {
		name          string
		mutate        func(*domain.Run, *domain.Project)
		providerReady bool
		want          bool
	}{
		{"happy path", func(*domain.Run, *domain.Project) {}, true, true},
		{"no provider ready", func(*domain.Run, *domain.Project) {}, false, false},
		{"not succeeded", func(r *domain.Run, _ *domain.Project) { r.Status = domain.StatusRunning }, true, false},
		{"failed run", func(r *domain.Run, _ *domain.Project) { r.Status = domain.StatusFailed }, true, false},
		{"readonly project", func(_ *domain.Run, p *domain.Project) { p.GitMode = domain.GitModeReadonly }, true, false},
		{"no branch reported", func(r *domain.Run, _ *domain.Project) { r.GitBranch = "" }, true, false},
		{"pr already set", func(r *domain.Run, _ *domain.Project) { r.PRURL = "http://x/1" }, true, false},
		{"empty repo", func(_ *domain.Run, p *domain.Project) { p.ProviderRepo = "" }, true, false},
		{"non-gitea provider", func(_ *domain.Run, p *domain.Project) { p.Provider = "github" }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, p := base()
			tc.mutate(&r, &p)
			if got := shouldOpenPR(r, p, tc.providerReady); got != tc.want {
				t.Fatalf("shouldOpenPR = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPRTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"add a Hello line", "[jcode] add a Hello line"},
		{"first line\nsecond line", "[jcode] first line"},
		{"  trim me  ", "[jcode] trim me"},
		{"", "[jcode] agent run"},
	}
	for _, tc := range cases {
		if got := prTitle(tc.in); got != tc.want {
			t.Errorf("prTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Long prompt is truncated with an ellipsis.
	long := strings.Repeat("x", 200)
	got := prTitle(long)
	if !strings.HasPrefix(got, "[jcode] ") || len([]rune(got)) > 8+72 {
		t.Errorf("prTitle(long) not truncated: len=%d", len([]rune(got)))
	}
}

// TestJobEnvDraftPRInjection proves the runner env carries the push contract for
// a draft_pr project (with a token configured) and stays diff-only otherwise.
func TestJobEnvDraftPRInjection(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.GiteaURL = "http://gitea.test"
	rec.cfg.GiteaToken = "tok-123"

	proj, run := seedDraftPRRun(t, st, "agent/run-env")
	// Reset run to queued-like for env building; env only needs the ids.
	_ = proj

	env := rec.jobEnv(ctx, &run, "run-token")
	if env["GIT_MODE"] != string(domain.GitModeDraftPR) {
		t.Fatalf("GIT_MODE=%q want draft_pr", env["GIT_MODE"])
	}
	if env["GIT_BRANCH"] != "agent/run-"+run.ID {
		t.Errorf("GIT_BRANCH=%q want agent/run-%s", env["GIT_BRANCH"], run.ID)
	}
	if env["GIT_PUSH_URL"] != "http://gitea.test/jcloud/seed.git" {
		t.Errorf("GIT_PUSH_URL=%q", env["GIT_PUSH_URL"])
	}
	if env["GIT_TOKEN"] != "tok-123" {
		t.Errorf("GIT_TOKEN not injected")
	}
	if env["GIT_BASE_BRANCH"] != "main" {
		t.Errorf("GIT_BASE_BRANCH=%q want main", env["GIT_BASE_BRANCH"])
	}

	// With no token, a draft_pr project degrades to diff-only (GIT_MODE=readonly).
	rec.cfg.GiteaToken = ""
	env2 := rec.jobEnv(ctx, &run, "run-token")
	if env2["GIT_MODE"] != string(domain.GitModeReadonly) {
		t.Fatalf("GIT_MODE=%q want readonly when token unset", env2["GIT_MODE"])
	}
	if env2["GIT_PUSH_URL"] != "" {
		t.Errorf("GIT_PUSH_URL should be empty with no token")
	}
}

// TestReadonlyProjectStaysDiffOnly proves the DEFAULT (readonly) project injects
// no push env and the PR pass never touches it — the unchanged J1-J3 behavior.
func TestReadonlyProjectStaysDiffOnly(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	rec.cfg.GiteaToken = "tok"
	prov := provider.NewFakeProvider()
	rec.WithProvider(prov)

	// A plain readonly run (default mode) that succeeded with a branch somehow set
	// must NOT get a PR (mode gate) and its env must be diff-only.
	run := seedProjectAndRun(t, st) // readonly project (no git config)
	rec.Tick(ctx)
	got, _ := st.GetRun(ctx, run.ID)
	env := rec.jobEnv(ctx, got, "tok")
	if env["GIT_MODE"] != string(domain.GitModeReadonly) {
		t.Fatalf("readonly project GIT_MODE=%q want readonly", env["GIT_MODE"])
	}
	if _, ok := env["GIT_PUSH_URL"]; ok {
		t.Error("readonly project must not get GIT_PUSH_URL")
	}
	// Drive it to succeeded; PR pass must not create anything.
	fake.SetState(got.K8sJobName, k8s.JobSucceeded)
	rec.Tick(ctx)
	if prov.CreatedCount() != 0 {
		t.Fatalf("readonly project got a PR: created=%d", prov.CreatedCount())
	}
}
