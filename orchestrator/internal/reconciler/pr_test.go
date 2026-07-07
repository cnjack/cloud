package reconciler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// --- test doubles for the M3 push stack -------------------------------------

// fakePusher records the branches it "pushed" and can inject an error. It stands
// in for the git-CLI Pusher so the PR pass is tested without git/a remote.
type fakePusher struct {
	pushed []string
	err    error

	// ff-only (M7 update mode) fakes.
	ffPushed         []string
	ffErr            error
	ffAlreadyPresent bool
}

func (f *fakePusher) PushBundleBranch(_ context.Context, _ /*remoteURL*/, _ /*bundlePath*/, branch string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.pushed = append(f.pushed, branch)
	return "pushedsha1234", nil
}

func (f *fakePusher) PushBundleBranchFFOnly(_ context.Context, _ /*remoteURL*/, _ /*bundlePath*/, branch string) (string, bool, error) {
	if f.ffErr != nil {
		return "", false, f.ffErr
	}
	if f.ffAlreadyPresent {
		return "ffsha0000", true, nil
	}
	f.ffPushed = append(f.ffPushed, branch)
	return "ffsha1234", false, nil
}

// fakeFactory returns a fixed Provider regardless of the resolved token.
type fakeFactory struct {
	p   provider.Provider
	err error
}

func (f *fakeFactory) PRClient(_ domain.GitProvider, _ /*token*/, _ /*scheme*/ string) (provider.Provider, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.p, nil
}

// wirePRStack attaches a fake factory (over the given provider), a fresh fake
// pusher, and a gitea-PAT credential resolver (so a user-less run resolves the
// fallback token) to rec. Returns the pusher so a test can assert push behavior.
func wirePRStack(rec *Reconciler, st *store.MemStore, prov provider.Provider) *fakePusher {
	pusher := &fakePusher{}
	rec.cfg.GiteaURL = "http://gitea.test"
	rec.cfg.GiteaToken = "gitea-pat"
	creds := credentials.NewResolver(st, nil, nil, "gitea-pat", nil)
	rec.WithPRStack(&fakeFactory{p: prov}, pusher, creds)
	return pusher
}

// seedDraftPRRun creates a project + a draft_pr gitea-provider 'default' service
// + a succeeded agent run that has uploaded a bundle (git_branch recorded), i.e.
// exactly the state the runner+bundle-ingest leaves behind for the PR pass.
func seedDraftPRRun(t *testing.T, st *store.MemStore, branch string) (domain.Service, domain.Run) {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "gp", CreatedAt: time.Now()}
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
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "add a Hello line to README",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	// The bundle-ingest handler stores the bundle AND records the branch.
	if err := st.PutRunBundle(ctx, run.ID, []byte("PACK bundle bytes")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunGit(ctx, run.ID, branch, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRun(ctx, run.ID)
	return *svc, *got
}

// TestReconcilePRCreation covers the happy path: a succeeded draft_pr run with a
// stored bundle gets its branch pushed and exactly one draft PR opened, pr_url /
// pr_number persisted, and a run.status event carrying pr_url emitted.
func TestReconcilePRCreation(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)

	_, run := seedDraftPRRun(t, st, "jcode/run-abc")

	rec.reconcilePRs(ctx)

	if len(pusher.pushed) != 1 || pusher.pushed[0] != "jcode/run-abc" {
		t.Fatalf("pushed=%v want [jcode/run-abc]", pusher.pushed)
	}
	if fake.CreatedCount() != 1 {
		t.Fatalf("created %d PRs, want 1", fake.CreatedCount())
	}
	in := fake.Created[0]
	if in.Head != "jcode/run-abc" {
		t.Errorf("PR head=%q want jcode/run-abc", in.Head)
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
	if got.CommitSHA != "pushedsha1234" {
		t.Errorf("commit_sha=%q want the pushed sha", got.CommitSHA)
	}

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

// TestReconcilePRCreationIdempotent proves the pass never double-opens or
// double-pushes across ticks.
func TestReconcilePRCreationIdempotent(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)

	seedDraftPRRun(t, st, "jcode/run-idem")

	rec.reconcilePRs(ctx)
	rec.reconcilePRs(ctx)
	rec.reconcilePRs(ctx)

	if fake.CreatedCount() != 1 {
		t.Fatalf("created %d PRs across 3 ticks, want 1 (idempotent)", fake.CreatedCount())
	}
	if len(pusher.pushed) != 1 {
		t.Fatalf("pushed %d times across 3 ticks, want 1 (idempotent)", len(pusher.pushed))
	}
}

// TestReconcilePRPreExisting covers the crash-between-push-and-persist path: the
// provider already has an open PR for the head branch. The reconciler adopts it
// (records url/number) and does NOT push or create a second PR.
func TestReconcilePRPreExisting(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)

	_, run := seedDraftPRRun(t, st, "jcode/run-adopt")
	fake.Seed("jcloud", "seed", "jcode/run-adopt", provider.PR{Number: 7, URL: "http://gitea.test/jcloud/seed/pulls/7"})

	rec.reconcilePRs(ctx)

	if fake.CreatedCount() != 0 {
		t.Fatalf("created %d PRs, want 0 (must adopt the existing one)", fake.CreatedCount())
	}
	if len(pusher.pushed) != 0 {
		t.Fatalf("pushed %d times, want 0 (PR already exists → skip push)", len(pusher.pushed))
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.PRNumber != 7 || got.PRURL != "http://gitea.test/jcloud/seed/pulls/7" {
		t.Fatalf("did not adopt existing PR: url=%q num=%d", got.PRURL, got.PRNumber)
	}
}

// TestReconcilePRSkipsWhenNoStack proves the flow degrades cleanly to diff-only
// when the draft-PR stack is not configured: no panic, no PR, run untouched.
func TestReconcilePRSkipsWhenNoStack(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4) // no WithPRStack

	_, run := seedDraftPRRun(t, st, "jcode/run-nostack")
	rec.reconcilePRs(ctx) // must be a no-op

	got, _ := st.GetRun(ctx, run.ID)
	if got.PRURL != "" {
		t.Fatalf("pr_url set with no stack: %q", got.PRURL)
	}
}

// TestReconcilePRPushErrorRetries proves a transient push error leaves the run in
// the awaiting scan (no pr_url, no PR), so the next tick retries.
func TestReconcilePRPushErrorRetries(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)
	pusher.err = errors.New("git push: connection reset")

	_, run := seedDraftPRRun(t, st, "jcode/run-pushfail")
	rec.reconcilePRs(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.PRURL != "" {
		t.Fatalf("pr_url set despite push error: %q", got.PRURL)
	}
	if fake.CreatedCount() != 0 {
		t.Fatalf("PR created despite push error: %d", fake.CreatedCount())
	}
	// Recover: clear the error; next tick pushes and opens the PR.
	pusher.err = nil
	rec.reconcilePRs(ctx)
	got, _ = st.GetRun(ctx, run.ID)
	if got.PRURL == "" {
		t.Fatal("pr_url not set after push recovered")
	}
}

// TestReconcilePRCreateErrorRetries proves a transient provider (create) error
// leaves the run for the next tick.
func TestReconcilePRCreateErrorRetries(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	fake.CreateErr = errors.New("gitea 503")
	wirePRStack(rec, st, fake)

	_, run := seedDraftPRRun(t, st, "jcode/run-503")
	rec.reconcilePRs(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.PRURL != "" {
		t.Fatalf("pr_url set despite create error: %q", got.PRURL)
	}
	fake.CreateErr = nil
	rec.reconcilePRs(ctx)
	got, _ = st.GetRun(ctx, run.ID)
	if got.PRURL == "" {
		t.Fatal("pr_url not set after provider recovered")
	}
}

// --- review pass ------------------------------------------------------------

// seedReviewRun creates a project + draft_pr provider service + a succeeded
// review run whose output is set and target PR head recorded.
func seedReviewRun(t *testing.T, st *store.MemStore, head, output string) domain.Run {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "rp", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "jcloud/seed", DefaultBranch: "main",
		GitMode: domain.GitModeDraftPR, CreatedAt: time.Now(),
	}
	_ = st.CreateService(ctx, svc)
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "review the PR",
		Status: domain.StatusSucceeded, Kind: domain.RunKindReview,
		PRHeadBranch: head, PRBaseBranch: "main", CreatedAt: time.Now(),
	}
	_ = st.CreateRun(ctx, run)
	if _, err := st.SetReviewOutput(ctx, run.ID, output); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRun(ctx, run.ID)
	return *got
}

// TestReconcileReviewPost covers the happy path: the review output is posted as a
// comment on the target PR (found by head) and the run is stamped posted.
func TestReconcileReviewPost(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	fake.Seed("jcloud", "seed", "jcode/run-pr1", provider.PR{Number: 12, URL: "http://gitea.test/jcloud/seed/pulls/12"})

	run := seedReviewRun(t, st, "jcode/run-pr1", "## Review\nconclusion: approve")

	rec.reconcileReviews(ctx)

	if fake.ReviewCount() != 1 {
		t.Fatalf("posted %d reviews, want 1", fake.ReviewCount())
	}
	if fake.Reviews[0].Number != 12 || !strings.Contains(fake.Reviews[0].Body, "approve") {
		t.Fatalf("review posted to wrong PR/body: %+v", fake.Reviews[0])
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.ReviewPostedAt == nil {
		t.Fatal("review_posted_at not stamped")
	}
}

// TestReconcileReviewIdempotent proves the review comment is posted at most once
// across ticks (review_posted_at gate).
func TestReconcileReviewIdempotent(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	fake.Seed("jcloud", "seed", "jcode/run-pr2", provider.PR{Number: 5, URL: "http://gitea.test/jcloud/seed/pulls/5"})

	seedReviewRun(t, st, "jcode/run-pr2", "needs-work: fix the tests")

	rec.reconcileReviews(ctx)
	rec.reconcileReviews(ctx)
	rec.reconcileReviews(ctx)

	if fake.ReviewCount() != 1 {
		t.Fatalf("posted %d reviews across 3 ticks, want 1 (idempotent)", fake.ReviewCount())
	}
}

// TestReconcileReviewNoTargetPR proves a review run whose target PR is not found
// stays unposted (retried next tick), not crashed.
func TestReconcileReviewNoTargetPR(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider() // no PR seeded
	wirePRStack(rec, st, fake)

	run := seedReviewRun(t, st, "jcode/run-missing", "approve")
	rec.reconcileReviews(ctx)

	if fake.ReviewCount() != 0 {
		t.Fatalf("posted %d reviews, want 0 (no target PR)", fake.ReviewCount())
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.ReviewPostedAt != nil {
		t.Fatal("review_posted_at stamped despite no PR")
	}
}

// --- M7 webhook update-push pass --------------------------------------------

// seedWebhookUpdateRun creates a project + draft_pr gitea service + a succeeded
// WEBHOOK agent run whose bundle was received onto an EXISTING PR head branch
// (git_branch = head, pr_url pre-filled, commit_sha empty) — the state the
// bundle-ingest leaves for the update-push pass.
func seedWebhookUpdateRun(t *testing.T, st *store.MemStore, head string) (domain.Service, domain.Run) {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "wh", CreatedAt: time.Now()}
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
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "add a CONTRIBUTING.md",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent,
		Origin: domain.RunOriginWebhook, OriginCommentID: "c-" + head,
		PRURL: "http://gitea.test/jcloud/seed/pulls/9", PRNumber: 9,
		PRHeadBranch: head, PRBaseBranch: "main", Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRunBundle(ctx, run.ID, []byte("PACK update bundle")); err != nil {
		t.Fatal(err)
	}
	// Bundle-ingest records git_branch = the run's push branch (= PR head branch).
	if _, err := st.SetRunGit(ctx, run.ID, domain.RunPushBranch(run), ""); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRun(ctx, run.ID)
	return *svc, *got
}

// TestReconcileUpdatePush covers the happy path: a webhook task bundle is ff-only
// pushed onto its existing PR head branch, commit_sha is stamped, and NO new PR
// is opened.
func TestReconcileUpdatePush(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)

	_, run := seedWebhookUpdateRun(t, st, "feature-x")
	rec.reconcileUpdatePushes(ctx)

	if len(pusher.ffPushed) != 1 || pusher.ffPushed[0] != "feature-x" {
		t.Fatalf("ff-pushed=%v want [feature-x]", pusher.ffPushed)
	}
	if fake.CreatedCount() != 0 {
		t.Fatalf("update mode opened %d PRs, want 0", fake.CreatedCount())
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.CommitSHA != "ffsha1234" {
		t.Errorf("commit_sha=%q want the ff-pushed sha", got.CommitSHA)
	}
}

// TestReconcileUpdatePushIdempotent proves the branch is pushed at most once
// across ticks (commit_sha gate).
func TestReconcileUpdatePushIdempotent(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)

	seedWebhookUpdateRun(t, st, "feature-idem")
	rec.reconcileUpdatePushes(ctx)
	rec.reconcileUpdatePushes(ctx)
	rec.reconcileUpdatePushes(ctx)

	if len(pusher.ffPushed) != 1 {
		t.Fatalf("ff-pushed %d times across 3 ticks, want 1 (idempotent)", len(pusher.ffPushed))
	}
}

// TestReconcileUpdatePushAlreadyPresent proves that when the PR head already
// contains the change, the run is marked done (commit_sha stamped) without a push.
func TestReconcileUpdatePushAlreadyPresent(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)
	pusher.ffAlreadyPresent = true

	_, run := seedWebhookUpdateRun(t, st, "feature-present")
	rec.reconcileUpdatePushes(ctx)

	if len(pusher.ffPushed) != 0 {
		t.Fatalf("pushed despite already-present: %v", pusher.ffPushed)
	}
	got, _ := st.GetRun(ctx, run.ID)
	if got.CommitSHA == "" {
		t.Fatal("commit_sha not stamped for an already-present change (would re-scan forever)")
	}
}

// TestReconcileUpdatePushNonFFRetries proves a non-fast-forward push leaves the
// run in the scan (commit_sha empty) so the next tick retries — no force-push,
// no infinite spin (the scan is bounded by the reconcile interval).
func TestReconcileUpdatePushNonFFRetries(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	fake := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, fake)
	pusher.ffErr = errors.New("! [rejected] feature-x -> feature-x (non-fast-forward)")

	_, run := seedWebhookUpdateRun(t, st, "feature-nff")
	rec.reconcileUpdatePushes(ctx)

	got, _ := st.GetRun(ctx, run.ID)
	if got.CommitSHA != "" {
		t.Fatalf("commit_sha set despite non-ff push: %q", got.CommitSHA)
	}
	// Recover: the divergence resolves (or the change is now present) — next tick
	// completes without opening a PR.
	pusher.ffErr = nil
	pusher.ffAlreadyPresent = true
	rec.reconcileUpdatePushes(ctx)
	got, _ = st.GetRun(ctx, run.ID)
	if got.CommitSHA == "" {
		t.Fatal("commit_sha not stamped after the push path recovered")
	}
}

// TestWebhookTaskJobEnv proves a webhook @mention agent task builds ON the PR head
// branch: BASE_BRANCH == BRANCH_NAME == the head branch (entrypoint then bundles
// startSHA..HEAD onto that same branch).
func TestWebhookTaskJobEnv(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	_, run := seedWebhookUpdateRun(t, st, "feature-env")
	env := rec.jobEnv(ctx, &run, "tok", envModel(), nil, rec.cfg.RunTimeoutSecs)
	if env["BASE_BRANCH"] != "feature-env" {
		t.Errorf("BASE_BRANCH=%q want feature-env (PR head)", env["BASE_BRANCH"])
	}
	if env["BRANCH_NAME"] != "feature-env" {
		t.Errorf("BRANCH_NAME=%q want feature-env (== BASE_BRANCH)", env["BRANCH_NAME"])
	}
	if env["GIT_MODE"] != string(domain.GitModeDraftPR) {
		t.Errorf("GIT_MODE=%q want draft_pr", env["GIT_MODE"])
	}
}

// TestShouldUpdatePush is the exhaustive table for the pure update-push gate.
func TestShouldUpdatePush(t *testing.T) {
	base := func() (domain.Run, domain.Service) {
		return domain.Run{
				Status: domain.StatusSucceeded, Kind: domain.RunKindAgent,
				Origin: domain.RunOriginWebhook, GitBranch: "feature-x",
				PRURL: "http://x/9", CommitSHA: "",
			}, domain.Service{
				RepoKind: domain.RepoKindProvider, GitMode: domain.GitModeDraftPR,
				Provider: domain.ProviderGitea, RepoOwnerName: "o/r",
			}
	}
	cases := []struct {
		name          string
		mutate        func(*domain.Run, *domain.Service)
		providerReady bool
		want          bool
	}{
		{"happy path", func(*domain.Run, *domain.Service) {}, true, true},
		{"no stack ready", func(*domain.Run, *domain.Service) {}, false, false},
		{"not succeeded", func(r *domain.Run, _ *domain.Service) { r.Status = domain.StatusRunning }, true, false},
		{"review run", func(r *domain.Run, _ *domain.Service) { r.Kind = domain.RunKindReview }, true, false},
		{"api origin", func(r *domain.Run, _ *domain.Service) { r.Origin = domain.RunOriginAPI }, true, false},
		{"readonly service", func(_ *domain.Run, s *domain.Service) { s.GitMode = domain.GitModeReadonly }, true, false},
		{"raw repo", func(_ *domain.Run, s *domain.Service) { s.RepoKind = domain.RepoKindRaw }, true, false},
		{"no branch recorded", func(r *domain.Run, _ *domain.Service) { r.GitBranch = "" }, true, false},
		{"no target PR", func(r *domain.Run, _ *domain.Service) { r.PRURL = "" }, true, false},
		{"already pushed", func(r *domain.Run, _ *domain.Service) { r.CommitSHA = "abc" }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, svc := base()
			tc.mutate(&r, &svc)
			if got := shouldUpdatePush(r, svc, tc.providerReady); got != tc.want {
				t.Fatalf("shouldUpdatePush = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- gates ------------------------------------------------------------------

// TestShouldOpenPR is the exhaustive table for the pure PR-creation gate.
func TestShouldOpenPR(t *testing.T) {
	base := func() (domain.Run, domain.Service) {
		return domain.Run{
				Status: domain.StatusSucceeded, Kind: domain.RunKindAgent,
				GitBranch: "jcode/run-x", PRURL: "",
			}, domain.Service{
				RepoKind: domain.RepoKindProvider, GitMode: domain.GitModeDraftPR,
				Provider: domain.ProviderGitea, RepoOwnerName: "o/r",
			}
	}
	cases := []struct {
		name          string
		mutate        func(*domain.Run, *domain.Service)
		providerReady bool
		want          bool
	}{
		{"happy path", func(*domain.Run, *domain.Service) {}, true, true},
		{"github provider ok", func(_ *domain.Run, s *domain.Service) { s.Provider = domain.ProviderGitHub }, true, true},
		{"no stack ready", func(*domain.Run, *domain.Service) {}, false, false},
		{"not succeeded", func(r *domain.Run, _ *domain.Service) { r.Status = domain.StatusRunning }, true, false},
		{"review run", func(r *domain.Run, _ *domain.Service) { r.Kind = domain.RunKindReview }, true, false},
		{"readonly service", func(_ *domain.Run, s *domain.Service) { s.GitMode = domain.GitModeReadonly }, true, false},
		{"raw repo", func(_ *domain.Run, s *domain.Service) { s.RepoKind = domain.RepoKindRaw }, true, false},
		{"no branch recorded", func(r *domain.Run, _ *domain.Service) { r.GitBranch = "" }, true, false},
		{"pr already set", func(r *domain.Run, _ *domain.Service) { r.PRURL = "http://x/1" }, true, false},
		{"empty repo", func(_ *domain.Run, s *domain.Service) { s.RepoOwnerName = "" }, true, false},
		{"invalid provider", func(_ *domain.Run, s *domain.Service) { s.Provider = "svn" }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, svc := base()
			tc.mutate(&r, &svc)
			if got := shouldOpenPR(r, svc, tc.providerReady); got != tc.want {
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
	long := strings.Repeat("x", 200)
	got := prTitle(long)
	if !strings.HasPrefix(got, "[jcode] ") || len([]rune(got)) > 8+72 {
		t.Errorf("prTitle(long) not truncated: len=%d", len([]rune(got)))
	}
}

// TestReadonlyProjectStaysDiffOnly proves the DEFAULT (readonly) service injects
// no push/bundle env and the PR pass never touches it.
func TestReadonlyProjectStaysDiffOnly(t *testing.T) {
	ctx := context.Background()
	rec, st, fake := testRec(t, 4)
	prov := provider.NewFakeProvider()
	pusher := wirePRStack(rec, st, prov)

	run := seedProjectAndRun(t, st) // readonly raw service
	rec.Tick(ctx)
	got, _ := st.GetRun(ctx, run.ID)
	env := rec.jobEnv(ctx, got, "tok", envModel(), nil, rec.cfg.RunTimeoutSecs)
	if env["GIT_MODE"] != string(domain.GitModeReadonly) {
		t.Fatalf("readonly service GIT_MODE=%q want readonly", env["GIT_MODE"])
	}
	if _, ok := env["BRANCH_NAME"]; ok {
		t.Error("readonly service must not get BRANCH_NAME")
	}
	// Drive it to succeeded; PR pass must not push or create anything.
	fake.SetState(got.K8sJobName, k8s.JobSucceeded)
	rec.Tick(ctx)
	if prov.CreatedCount() != 0 || len(pusher.pushed) != 0 {
		t.Fatalf("readonly service got a PR/push: created=%d pushed=%d", prov.CreatedCount(), len(pusher.pushed))
	}
}
