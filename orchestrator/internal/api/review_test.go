package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// fakePRFactory returns a fixed Provider (or an error) regardless of the token,
// so the PR-status lookup in GET /pr is unit-tested without a real git host.
type fakePRFactory struct {
	prov provider.Provider
	err  error
}

func (f *fakePRFactory) PRClient(domain.GitProvider, string, string) (provider.Provider, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.prov, nil
}

// newReviewServer builds a test server over st with an injectable PR-status
// factory (nil => keep the real one) and a gitea PAT so a user-less run resolves
// a credential. Returns the httptest server + the underlying *Server.
func newReviewServer(t *testing.T, st *store.MemStore, factory provider.Factory) (*httptest.Server, *Server) {
	t.Helper()
	hub := sse.NewHub()
	cfg := withTestModel(&config.Config{
		ConsoleToken:    consoleToken,
		GiteaURL:        "http://gitea.test",
		GiteaToken:      "gitea-pat",
		SourceBundleTTL: time.Minute,
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	if factory != nil {
		srv.factory = factory
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv
}

// reviewFixture: one project with an owner/member/viewer + a stranger + admin, a
// gitea draft_pr default service, and a succeeded agent run that opened a PR.
type reviewFixture struct {
	ts        *httptest.Server
	st        *store.MemStore
	projectID string
	svc       *domain.Service
	agentRun  *domain.Run
	member    *domain.User
	tokens    map[string]string
}

func setupReview(t *testing.T, factory provider.Factory) reviewFixture {
	t.Helper()
	st := store.NewMemStore()
	ctx := context.Background()

	admin := mkUser(t, st, "admin") // first user => cluster admin
	owner := mkUser(t, st, "owner")
	member := mkUser(t, st, "member")
	viewer := mkUser(t, st, "viewer")
	stranger := mkUser(t, st, "stranger")

	p := &domain.Project{ID: domain.NewID(), Name: "rev", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	for uid, role := range map[string]domain.Role{
		owner.ID: domain.RoleOwner, member.ID: domain.RoleMember, viewer.ID: domain.RoleViewer,
	} {
		if err := st.UpsertMember(ctx, &domain.ProjectMember{
			ProjectID: p.ID, UserID: uid, Role: role, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "jcloud/seed", DefaultBranch: "main", GitMode: domain.GitModeDraftPR,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}

	agent := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "add hello",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent,
		GitBranch: "jcode/run-abc12345", PRURL: "http://gitea.test/jcloud/seed/pulls/7", PRNumber: 7,
		Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, agent); err != nil {
		t.Fatal(err)
	}

	ts, _ := newReviewServer(t, st, factory)
	return reviewFixture{
		ts: ts, st: st, projectID: p.ID, svc: svc, agentRun: agent, member: member,
		tokens: map[string]string{
			"admin":    mkSession(t, st, admin.ID),
			"owner":    mkSession(t, st, owner.ID),
			"member":   mkSession(t, st, member.ID),
			"viewer":   mkSession(t, st, viewer.ID),
			"stranger": mkSession(t, st, stranger.ID),
			"service":  consoleToken,
		},
	}
}

// TestRequestReviewRBAC: viewer/stranger are forbidden; member/owner/admin/service
// may request a review of a succeeded agent run with a PR.
func TestRequestReviewRBAC(t *testing.T) {
	f := setupReview(t, nil)
	url := f.ts.URL + "/api/v1/runs/" + f.agentRun.ID + "/review"

	want := map[string]int{
		"admin": 201, "owner": 201, "member": 201, "service": 201,
		"viewer": http.StatusForbidden, "stranger": http.StatusForbidden,
	}
	for role, exp := range want {
		r := do(t, "POST", url, f.tokens[role], nil)
		if r.StatusCode != exp {
			t.Errorf("role=%s request review: status=%d want %d", role, r.StatusCode, exp)
		}
		r.Body.Close()
	}
}

// TestRequestReviewCreatesReviewRun: the created run is kind=review, carries the
// PR head/base branches, records the triggering user, and its prompt names the PR.
func TestRequestReviewCreatesReviewRun(t *testing.T) {
	f := setupReview(t, nil)
	r := do(t, "POST", f.ts.URL+"/api/v1/runs/"+f.agentRun.ID+"/review", f.tokens["member"], nil)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("request review: status=%d want 201", r.StatusCode)
	}
	var run domain.Run
	decode(t, r, &run)
	if run.Kind != domain.RunKindReview {
		t.Errorf("kind=%q want review", run.Kind)
	}
	if run.PRHeadBranch != f.agentRun.GitBranch {
		t.Errorf("pr_head_branch=%q want %q", run.PRHeadBranch, f.agentRun.GitBranch)
	}
	if run.PRBaseBranch != f.svc.DefaultBranch {
		t.Errorf("pr_base_branch=%q want %q", run.PRBaseBranch, f.svc.DefaultBranch)
	}
	if run.Status != domain.StatusQueued {
		t.Errorf("status=%q want queued", run.Status)
	}
	if run.Prompt == "" || run.Prompt == f.agentRun.Prompt {
		t.Errorf("review prompt=%q should be a review placeholder naming the PR", run.Prompt)
	}
	stored, _ := f.st.GetRun(context.Background(), run.ID)
	if stored.TriggeredByUserID == nil || *stored.TriggeredByUserID != f.member.ID {
		t.Errorf("triggered_by=%v want member id %s", stored.TriggeredByUserID, f.member.ID)
	}
}

// TestRequestReviewPreconditions: non-succeeded, no-PR, and reviewing a review
// run are all 409 conflicts.
func TestRequestReviewPreconditions(t *testing.T) {
	f := setupReview(t, nil)
	ctx := context.Background()

	// Running agent run (not succeeded).
	running := &domain.Run{
		ID: domain.NewID(), ProjectID: f.projectID, ServiceID: f.svc.ID, Prompt: "x",
		Status: domain.StatusRunning, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := f.st.CreateRun(ctx, running); err != nil {
		t.Fatal(err)
	}
	// Succeeded agent run with no PR.
	noPR := &domain.Run{
		ID: domain.NewID(), ProjectID: f.projectID, ServiceID: f.svc.ID, Prompt: "x",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := f.st.CreateRun(ctx, noPR); err != nil {
		t.Fatal(err)
	}
	// A review run (cannot review a review).
	rev := &domain.Run{
		ID: domain.NewID(), ProjectID: f.projectID, ServiceID: f.svc.ID, Prompt: "x",
		Status: domain.StatusSucceeded, Kind: domain.RunKindReview, Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := f.st.CreateRun(ctx, rev); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{running.ID, noPR.ID, rev.ID} {
		r := do(t, "POST", f.ts.URL+"/api/v1/runs/"+id+"/review", f.tokens["member"], nil)
		if r.StatusCode != http.StatusConflict {
			t.Errorf("precondition run %s: status=%d want 409", id, r.StatusCode)
		}
		r.Body.Close()
	}

	// A missing run is a 404.
	r := do(t, "POST", f.ts.URL+"/api/v1/runs/does-not-exist/review", f.tokens["member"], nil)
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("missing run: status=%d want 404", r.StatusCode)
	}
	r.Body.Close()
}

// TestGetPRVisibilityAndState: a viewer can read the PR view; the state comes
// from the provider (open here); a stranger is forbidden.
func TestGetPRVisibilityAndState(t *testing.T) {
	f := setupReview(t, &fakePRFactory{prov: provider.NewFakeProvider()})
	url := f.ts.URL + "/api/v1/runs/" + f.agentRun.ID + "/pr"

	// Stranger: 403.
	sr := do(t, "GET", url, f.tokens["stranger"], nil)
	if sr.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger get pr: status=%d want 403", sr.StatusCode)
	}
	sr.Body.Close()

	// Viewer: 200 with a live state and the PR link + head branch.
	vr := do(t, "GET", url, f.tokens["viewer"], nil)
	if vr.StatusCode != http.StatusOK {
		t.Fatalf("viewer get pr: status=%d want 200", vr.StatusCode)
	}
	var pv prView
	decode(t, vr, &pv)
	if pv.State != "open" {
		t.Errorf("state=%q want open", pv.State)
	}
	if pv.URL != f.agentRun.PRURL {
		t.Errorf("url=%q want %q", pv.URL, f.agentRun.PRURL)
	}
	if pv.HeadBranch != f.agentRun.GitBranch {
		t.Errorf("head_branch=%q want %q", pv.HeadBranch, f.agentRun.GitBranch)
	}
}

// TestGetPRUnknownStateDegrades: a provider error degrades state to "unknown"
// and still returns 200 (never 500).
func TestGetPRUnknownStateDegrades(t *testing.T) {
	f := setupReview(t, &fakePRFactory{err: errors.New("provider down")})
	r := do(t, "GET", f.ts.URL+"/api/v1/runs/"+f.agentRun.ID+"/pr", f.tokens["member"], nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("get pr: status=%d want 200", r.StatusCode)
	}
	var pv prView
	decode(t, r, &pv)
	if pv.State != "unknown" {
		t.Errorf("state=%q want unknown", pv.State)
	}
}

// TestGetPRReviewRunsFilter: review_runs contains only same-service kind=review
// runs whose head branch matches the run's pushed branch, newest first, enriched
// with the triggering user's display name. An unrelated-branch review and an
// agent run are excluded.
func TestGetPRReviewRunsFilter(t *testing.T) {
	f := setupReview(t, &fakePRFactory{prov: provider.NewFakeProvider()})
	ctx := context.Background()

	// A matching review run (created via the endpoint, so triggered_by=member).
	cr := do(t, "POST", f.ts.URL+"/api/v1/runs/"+f.agentRun.ID+"/review", f.tokens["member"], nil)
	if cr.StatusCode != http.StatusCreated {
		t.Fatalf("create review: status=%d", cr.StatusCode)
	}
	var matching domain.Run
	decode(t, cr, &matching)

	// A review run on a DIFFERENT head branch (same service) — must be excluded.
	other := &domain.Run{
		ID: domain.NewID(), ProjectID: f.projectID, ServiceID: f.svc.ID, Prompt: "other",
		Status: domain.StatusSucceeded, Kind: domain.RunKindReview,
		PRHeadBranch: "jcode/run-zzz", Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := f.st.CreateRun(ctx, other); err != nil {
		t.Fatal(err)
	}
	// An agent run on the SAME branch — kind=agent, must be excluded.
	agent2 := &domain.Run{
		ID: domain.NewID(), ProjectID: f.projectID, ServiceID: f.svc.ID, Prompt: "agent2",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent,
		GitBranch: f.agentRun.GitBranch, Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := f.st.CreateRun(ctx, agent2); err != nil {
		t.Fatal(err)
	}

	r := do(t, "GET", f.ts.URL+"/api/v1/runs/"+f.agentRun.ID+"/pr", f.tokens["member"], nil)
	var pv prView
	decode(t, r, &pv)
	if len(pv.ReviewRuns) != 1 {
		t.Fatalf("review_runs len=%d want 1 (%+v)", len(pv.ReviewRuns), pv.ReviewRuns)
	}
	got := pv.ReviewRuns[0]
	if got.ID != matching.ID {
		t.Errorf("review run id=%q want %q", got.ID, matching.ID)
	}
	if got.TriggeredByDisplayName != "member" {
		t.Errorf("triggered_by_display_name=%q want member", got.TriggeredByDisplayName)
	}
}
