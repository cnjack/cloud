package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

const webhookSecret = "s3cr3t-webhook-key"

// --- pure parser (table-driven) ---------------------------------------------

func TestParseMentionCommand(t *testing.T) {
	cases := []struct {
		name string
		body string
		kind commandKind
		task string
	}{
		{"review exact", "@jcode review", cmdReview, ""},
		{"review case-insensitive mention + word", "@JCODE Review", cmdReview, ""},
		{"review leading whitespace", "   \n @jcode review\n", cmdReview, ""},
		{"task", "@jcode Add a CONTRIBUTING.md with guidelines", cmdTask, "Add a CONTRIBUTING.md with guidelines"},
		{"task multiword starting review", "@jcode review the auth flow and refactor", cmdTask, "review the auth flow and refactor"},
		{"task trims surrounding space", "@jcode   do the thing  ", cmdTask, "do the thing"},
		{"no mention", "looks good to me", cmdNone, ""},
		{"mention mid-comment (not a command)", "cc @jcode please", cmdNone, ""},
		{"bare mention", "@jcode", cmdNone, ""},
		{"bare mention trailing space", "@jcode   ", cmdNone, ""},
		{"not our handle", "@jcoder do it", cmdNone, ""},
		{"empty", "", cmdNone, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMentionCommand(tc.body)
			if got.kind != tc.kind || got.task != tc.task {
				t.Fatalf("parseMentionCommand(%q) = {%d,%q} want {%d,%q}", tc.body, got.kind, got.task, tc.kind, tc.task)
			}
		})
	}
}

func TestValidGiteaSignature(t *testing.T) {
	body := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))

	if !validGiteaSignature(webhookSecret, body, good) {
		t.Error("valid signature rejected")
	}
	if validGiteaSignature(webhookSecret, body, good[:len(good)-2]+"00") {
		t.Error("tampered signature accepted")
	}
	if validGiteaSignature(webhookSecret, append(body, '!'), good) {
		t.Error("signature accepted over a tampered body")
	}
	if validGiteaSignature("", body, good) {
		t.Error("empty secret accepted")
	}
	if validGiteaSignature(webhookSecret, body, "") {
		t.Error("empty signature accepted")
	}
	if validGiteaSignature(webhookSecret, body, "not-hex-zz") {
		t.Error("non-hex signature accepted")
	}
}

// --- handler harness --------------------------------------------------------

type webhookFixture struct {
	ts   *httptest.Server
	st   *store.MemStore
	prov *provider.FakeProvider
	svc  *domain.Service
}

// newWebhookServer builds a test server with the webhook route enabled (secret
// set), a gitea PAT (so the user-less receipt/reply resolves a credential), and a
// fake provider capturing PR-detail reads + issue-comment receipts.
func newWebhookServer(t *testing.T, st *store.MemStore, secret string) (*httptest.Server, *Server, *provider.FakeProvider) {
	return newWebhookServerModel(t, st, secret, true)
}

// newWebhookServerModel is newWebhookServer with an explicit knob for whether the
// cluster has a model configured (Feature A). modelConfigured=false leaves the
// MODEL_* env empty so processMention hits the fail-visible gate and replies.
// st is the Store interface so a test can wrap MemStore with failure injection.
func newWebhookServerModel(t *testing.T, st store.Store, secret string, modelConfigured bool) (*httptest.Server, *Server, *provider.FakeProvider) {
	t.Helper()
	cfg := &config.Config{
		ConsoleToken:    consoleToken,
		ConsoleURL:      "http://console.test",
		GiteaURL:        "http://gitea.test",
		GiteaToken:      "gitea-pat",
		WebhookSecret:   secret,
		SourceBundleTTL: time.Minute,
	}
	if modelConfigured {
		withTestModel(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	fake := provider.NewFakeProvider()
	srv.factory = &fakePRFactory{prov: fake}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv, fake
}

// mkGiteaUser creates a user with a gitea identity whose provider_uid is the
// (stringified) numeric uid a webhook payload carries. The first user created
// becomes cluster-admin (store semantics), so create the admin first.
func mkGiteaUser(t *testing.T, st *store.MemStore, name, uid string) *domain.User {
	t.Helper()
	u := &domain.User{ID: domain.NewID(), DisplayName: name, CreatedAt: time.Now().UTC()}
	id := &domain.UserIdentity{
		ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: uid,
		Username: name, AccessTokenEnc: []byte("enc"), CreatedAt: time.Now().UTC(),
	}
	if _, err := st.CreateUserWithIdentity(context.Background(), u, id); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetUser(context.Background(), u.ID)
	return got
}

// seedWebhookProject makes a draft_pr gitea service tracking repoOwnerName with
// `member` at the given role, and seeds the PR-by-number head/base for the fake.
func seedWebhookProject(t *testing.T, st *store.MemStore, fake *provider.FakeProvider, repoOwnerName string, member *domain.User, role domain.Role) *domain.Service {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "wh", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: repoOwnerName, DefaultBranch: "main",
		GitMode: domain.GitModeDraftPR, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if member != nil {
		if err := st.UpsertMember(ctx, &domain.ProjectMember{
			ProjectID: p.ID, UserID: member.ID, Role: role, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// PR #7 on this repo: head=feature-x, base=main.
	owner, repo, _ := provider.SplitRepo(repoOwnerName)
	fake.SeedByNumber(owner, repo, 7, provider.PR{
		Number: 7, URL: "http://gitea.test/" + repoOwnerName + "/pulls/7",
		State: "open", HeadRef: "feature-x", BaseRef: "main",
	})
	return svc
}

func commentPayload(action string, isPR bool, commentID, userID int64, body, fullName string, issueNum int) []byte {
	issue := map[string]any{"number": issueNum}
	if isPR {
		issue["pull_request"] = map[string]any{"url": "http://gitea.test/x/pulls/" + strconv.Itoa(issueNum)}
	}
	m := map[string]any{
		"action": action,
		"comment": map[string]any{
			"id": commentID, "body": body,
			"html_url": "http://gitea.test/x/pulls/7#comment-" + strconv.FormatInt(commentID, 10),
			"user":     map[string]any{"id": userID},
		},
		"issue":      issue,
		"repository": map[string]any{"full_name": fullName},
	}
	b, _ := json.Marshal(m)
	return b
}

// postWebhook posts body to /webhooks/gitea with a valid signature over the given
// secret (pass a different secret to forge a bad signature).
func postWebhook(t *testing.T, ts *httptest.Server, signWith, event string, body []byte) *http.Response {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(signWith))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/gitea", bytes.NewReader(body))
	req.Header.Set("X-Gitea-Event", event)
	req.Header.Set("X-Gitea-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// --- handler behaviour ------------------------------------------------------

func TestWebhookRouteAbsentWithoutSecret(t *testing.T) {
	st := store.NewMemStore()
	ts, _, _ := newWebhookServer(t, st, "") // no secret => route not registered
	resp := postWebhook(t, ts, "anything", "issue_comment", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (route must be absent without a secret)", resp.StatusCode)
	}
}

func TestWebhookBadSignature(t *testing.T) {
	st := store.NewMemStore()
	ts, _, _ := newWebhookServer(t, st, webhookSecret)
	body := commentPayload("created", true, 100, 1, "@jcode review", "jcloud/seed", 7)
	resp := postWebhook(t, ts, "wrong-secret", "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 for a bad signature", resp.StatusCode)
	}
}

func TestWebhookNonIssueCommentEventIgnored(t *testing.T) {
	st := store.NewMemStore()
	ts, _, _ := newWebhookServer(t, st, webhookSecret)
	body := []byte(`{"action":"opened"}`)
	resp := postWebhook(t, ts, webhookSecret, "push", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200 (non issue_comment ignored)", resp.StatusCode)
	}
}

func TestWebhookNonPRCommentIgnored(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	body := commentPayload("created", false /*not a PR*/, 101, 1, "@jcode review", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if fake.CommentCount() != 0 {
		t.Fatal("a non-PR comment must not reply or create a run")
	}
}

func TestWebhookReviewCommandCreatesReviewRun(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	admin := mkGiteaUser(t, st, "admin", "999") // first user => cluster admin
	_ = admin
	member := mkGiteaUser(t, st, "dev", "1001")
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 200, 1001, "@jcode review", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("created %d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.Kind != domain.RunKindReview {
		t.Errorf("kind=%q want review", r.Kind)
	}
	if r.Origin != domain.RunOriginWebhook || r.OriginCommentID != "200" {
		t.Errorf("origin=%q comment=%q want webhook/200", r.Origin, r.OriginCommentID)
	}
	if r.PRHeadBranch != "feature-x" || r.PRBaseBranch != "main" {
		t.Errorf("PR head/base = %q/%q want feature-x/main", r.PRHeadBranch, r.PRBaseBranch)
	}
	if r.TriggeredByUserID == nil || *r.TriggeredByUserID != member.ID {
		t.Errorf("triggered_by = %v want the mapped member", r.TriggeredByUserID)
	}
	// A 🚀 receipt was posted on the PR.
	if fake.CommentCount() != 1 {
		t.Fatalf("posted %d comments, want 1 receipt", fake.CommentCount())
	}
	if got := fake.Comments[0]; got.Number != 7 || !bytes.Contains([]byte(got.Body), []byte("run started")) {
		t.Errorf("receipt wrong: %+v", got)
	}
}

func TestWebhookTaskCommandCreatesAgentRun(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	member := mkGiteaUser(t, st, "dev", "1001") // first user => cluster admin (fine)
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 201, 1001, "@jcode Add a CONTRIBUTING.md", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("created %d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.Kind != domain.RunKindAgent {
		t.Errorf("kind=%q want agent", r.Kind)
	}
	if r.Prompt != "Add a CONTRIBUTING.md" {
		t.Errorf("prompt=%q want the task text", r.Prompt)
	}
	if r.PRHeadBranch != "feature-x" || r.PRURL == "" || r.PRNumber != 7 {
		t.Errorf("run not associated with the PR: head=%q url=%q num=%d", r.PRHeadBranch, r.PRURL, r.PRNumber)
	}
	if r.Origin != domain.RunOriginWebhook {
		t.Errorf("origin=%q want webhook", r.Origin)
	}
}

// TestWebhookProviderNotAllowedReplies: when the project's provider_allowlist
// forbids this repo's provider, a valid @jcode command must NOT create a run —
// it replies on the PR explaining the guardrail (fail-visible), same as the model
// gate. Here gitea is excluded from the allowlist.
func TestWebhookProviderNotAllowedReplies(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	member := mkGiteaUser(t, st, "dev", "1001") // first user => cluster admin
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", member, domain.RoleMember)

	// Tighten the project's allowlist so gitea (this service's provider) is denied.
	ctx := context.Background()
	proj, err := st.GetProject(ctx, svc.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	proj.ProviderAllowlist = []string{"github"}
	if err := st.UpdateProject(ctx, proj); err != nil {
		t.Fatal(err)
	}

	body := commentPayload("created", true, 260, 1001, "@jcode Add a CONTRIBUTING.md", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	runs, _ := st.ListRunsByService(ctx, svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (provider not allowed)", len(runs))
	}
	if fake.CommentCount() != 1 {
		t.Fatalf("posted %d comments, want 1 explanatory reply", fake.CommentCount())
	}
	if got := fake.Comments[0].Body; !bytes.Contains([]byte(got), []byte("guardrails")) ||
		!bytes.Contains([]byte(got), []byte("gitea")) {
		t.Fatalf("reply should explain the guardrail + name the provider; got %q", got)
	}
}

// TestWebhookModelNotConfiguredReplies is the fail-visible gate on the webhook
// path (CLAUDE.md red line #1): when no LLM is configured, a valid @jcode
// command must NOT create a run — instead it replies on the PR pointing the user
// at the console to configure the model.
func TestWebhookModelNotConfiguredReplies(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServerModel(t, st, webhookSecret, false /* no model */)
	member := mkGiteaUser(t, st, "dev", "1001") // first user => cluster admin
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 250, 1001, "@jcode Add a CONTRIBUTING.md", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	// No run created.
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (model not configured)", len(runs))
	}
	// A single explanatory reply pointing at the console (ConsoleURL).
	if fake.CommentCount() != 1 {
		t.Fatalf("posted %d comments, want 1 explanatory reply", fake.CommentCount())
	}
	got := fake.Comments[0].Body
	if !bytes.Contains([]byte(got), []byte("no LLM is configured")) ||
		!bytes.Contains([]byte(got), []byte("http://console.test")) {
		t.Fatalf("reply should explain + link the console; got %q", got)
	}
}

// TestWebhookModelNotSelectedReplies is the P5 webhook counterpart: when several
// models are granted but the service has no default, a headless mention can't pick
// — it replies with the DISTINCT "several models / set a default" message (not the
// not-configured notice) and creates no run.
func TestWebhookModelNotSelectedReplies(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServerModel(t, st, webhookSecret, false /* catalog is the only source */)
	member := mkGiteaUser(t, st, "dev", "1001")
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", member, domain.RoleMember)
	ctx := context.Background()
	// Two granted models, no service default → ambiguous.
	for _, n := range []string{"a", "b"} {
		m := &domain.Model{ID: domain.NewID(), Name: n, BaseURL: "http://" + n + "/v1", ModelName: "p/" + n, CreatedAt: time.Now()}
		if err := st.CreateModel(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := st.GrantModel(ctx, m.ID, svc.ProjectID); err != nil {
			t.Fatal(err)
		}
	}

	body := commentPayload("created", true, 251, 1001, "@jcode Add a CONTRIBUTING.md", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	runs, _ := st.ListRunsByService(ctx, svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (model not selected)", len(runs))
	}
	if fake.CommentCount() != 1 {
		t.Fatalf("posted %d comments, want 1 reply", fake.CommentCount())
	}
	got := fake.Comments[0].Body
	if !bytes.Contains([]byte(got), []byte("several models")) {
		t.Fatalf("reply should mention several models / setting a default; got %q", got)
	}
	if bytes.Contains([]byte(got), []byte("no LLM is configured")) {
		t.Fatalf("not-selected reply must NOT read as the not-configured notice; got %q", got)
	}
}

// erroringModelStore wraps a MemStore so the D21 selection chain's grant lookup
// fails — the transient (DB blip) shape, distinct from the definitive
// "no grant / empty catalog" not-configured state.
type erroringModelStore struct {
	*store.MemStore
}

func (e *erroringModelStore) ListModelsForProject(context.Context, string) ([]domain.Model, error) {
	return nil, errTransientModel
}

var errTransientModel = errors.New("transient db error")

// TestWebhookModelResolveErrorRepliesTemporary: a TRANSIENT model-config resolve
// error must NOT be misreported as "not configured" — it replies "temporary
// problem, try again" (and is logged), so the mention isn't silently lost and
// the user isn't sent to reconfigure a model that is actually fine.
func TestWebhookModelResolveErrorRepliesTemporary(t *testing.T) {
	inner := store.NewMemStore()
	st := &erroringModelStore{MemStore: inner}
	ts, _, fake := newWebhookServerModel(t, st, webhookSecret, true /* env-configured, but resolve errors first */)
	member := mkGiteaUser(t, inner, "dev", "1001")
	svc := seedWebhookProject(t, inner, fake, "jcloud/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 251, 1001, "@jcode Add a CONTRIBUTING.md", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	// No run created; ONE reply that says temporary — not "not configured".
	runs, _ := inner.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (resolve errored)", len(runs))
	}
	if fake.CommentCount() != 1 {
		t.Fatalf("posted %d comments, want 1", fake.CommentCount())
	}
	got := fake.Comments[0].Body
	if !bytes.Contains([]byte(got), []byte("temporary")) {
		t.Fatalf("reply should say temporary, got %q", got)
	}
	if bytes.Contains([]byte(got), []byte("not configured")) {
		t.Fatalf("transient error must not be misreported as not-configured: %q", got)
	}
}

func TestWebhookUnmappedIdentityReplies(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	member := mkGiteaUser(t, st, "dev", "1001")
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", member, domain.RoleMember)

	// Comment from a gitea uid with NO jcloud identity.
	body := commentPayload("created", true, 202, 55555, "@jcode review", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (unmapped identity)", len(runs))
	}
	if fake.CommentCount() != 1 || !bytes.Contains([]byte(fake.Comments[0].Body), []byte("jcloud account")) {
		t.Fatalf("expected an explanatory reply, got %+v", fake.Comments)
	}
}

func TestWebhookNonMemberReplies(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	admin := mkGiteaUser(t, st, "admin", "999") // first user => cluster admin
	_ = admin
	// stranger has an identity but is NOT a member of the project.
	stranger := mkGiteaUser(t, st, "stranger", "1002")
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", nil, domain.RoleViewer)

	body := commentPayload("created", true, 203, 1002, "@jcode review", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	_ = stranger
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (non-member)", len(runs))
	}
	if fake.CommentCount() != 1 || !bytes.Contains([]byte(fake.Comments[0].Body), []byte("project")) {
		t.Fatalf("expected a no-access reply, got %+v", fake.Comments)
	}
}

func TestWebhookViewerCannotRun(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	admin := mkGiteaUser(t, st, "admin", "999")
	_ = admin
	viewer := mkGiteaUser(t, st, "viewer", "1003")
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", viewer, domain.RoleViewer)

	body := commentPayload("created", true, 204, 1003, "@jcode Add tests", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("viewer created %d runs, want 0 (running needs member+)", len(runs))
	}
}

func TestWebhookClusterAdminAllowedWithoutMembership(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	admin := mkGiteaUser(t, st, "admin", "999") // first user => cluster admin
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", nil, domain.RoleViewer)

	body := commentPayload("created", true, 205, 999, "@jcode review", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	_ = admin
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("cluster-admin created %d runs, want 1 (admin bypasses membership)", len(runs))
	}
}

func TestWebhookDedupe(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	member := mkGiteaUser(t, st, "dev", "1001")
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 300, 1001, "@jcode review", "jcloud/seed", 7)
	for i := 0; i < 3; i++ {
		resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
		resp.Body.Close()
	}
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("redelivered comment created %d runs, want 1 (dedup)", len(runs))
	}
	// Only the first delivery posts a receipt.
	if fake.CommentCount() != 1 {
		t.Fatalf("posted %d receipts across 3 deliveries, want 1", fake.CommentCount())
	}
}

func TestWebhookNoCommandIgnored(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newWebhookServer(t, st, webhookSecret)
	member := mkGiteaUser(t, st, "dev", "1001")
	svc := seedWebhookProject(t, st, fake, "jcloud/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 400, 1001, "nice work, LGTM", "jcloud/seed", 7)
	resp := postWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 || fake.CommentCount() != 0 {
		t.Fatalf("a non-command comment must be a silent no-op (runs=%d comments=%d)", len(runs), fake.CommentCount())
	}
}
