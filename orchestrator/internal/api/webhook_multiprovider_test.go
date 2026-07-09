package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// --- signature / token unit tests (F13) -------------------------------------

func TestValidGitHubSignature(t *testing.T) {
	body := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !validGitHubSignature(webhookSecret, body, good) {
		t.Error("valid signature rejected")
	}
	if validGitHubSignature(webhookSecret, body, good[:len(good)-2]+"00") {
		t.Error("tampered signature accepted")
	}
	if validGitHubSignature(webhookSecret, append(body, '!'), good) {
		t.Error("signature accepted over a tampered body")
	}
	if validGitHubSignature("", body, good) {
		t.Error("empty secret accepted")
	}
	if validGitHubSignature(webhookSecret, body, "") {
		t.Error("empty header accepted")
	}
	// Missing the "sha256=" prefix (a bare hex digest) must fail.
	if validGitHubSignature(webhookSecret, body, good[len("sha256="):]) {
		t.Error("prefixless digest accepted")
	}
	if validGitHubSignature(webhookSecret, body, "sha256=not-hex-zz") {
		t.Error("non-hex digest accepted")
	}
}

func TestValidGitLabToken(t *testing.T) {
	if !validGitLabToken(webhookSecret, webhookSecret) {
		t.Error("matching token rejected")
	}
	if validGitLabToken(webhookSecret, "wrong") {
		t.Error("mismatched token accepted")
	}
	if validGitLabToken("", "") {
		t.Error("empty secret accepted")
	}
	if validGitLabToken(webhookSecret, "") {
		t.Error("empty header accepted")
	}
}

func TestWebhookURLForProvider(t *testing.T) {
	cases := []struct {
		base string
		prov domain.GitProvider
		want string
	}{
		// A WEBHOOK_URL pointing at the gitea receiver yields sibling paths.
		{"http://orch:8080/webhooks/gitea", domain.ProviderGitea, "http://orch:8080/webhooks/gitea"},
		{"http://orch:8080/webhooks/gitea", domain.ProviderGitHub, "http://orch:8080/webhooks/github"},
		{"http://orch:8080/webhooks/gitea", domain.ProviderGitLab, "http://orch:8080/webhooks/gitlab"},
		// A bare base (no known trailing segment) gets the path appended.
		{"http://orch:8080", domain.ProviderGitHub, "http://orch:8080/webhooks/github"},
		{"http://orch:8080/", domain.ProviderGitLab, "http://orch:8080/webhooks/gitlab"},
		{"", domain.ProviderGitHub, ""},
	}
	for _, tc := range cases {
		if got := webhookURLForProvider(tc.base, tc.prov); got != tc.want {
			t.Errorf("webhookURLForProvider(%q,%s)=%q want %q", tc.base, tc.prov, got, tc.want)
		}
	}
}

// --- multi-provider handler harness -----------------------------------------

// newMPWebhookServer builds a webhook test server with a LIVE cipher (so an
// integration bot token can be sealed/resolved — the github/gitlab reply
// credential path) and a fake provider. modelConfigured=false leaves MODEL_* empty
// to exercise the fail-visible model gate.
func newMPWebhookServer(t *testing.T, st store.Store, secret string, modelConfigured bool) (*httptest.Server, *Server, *provider.FakeProvider) {
	t.Helper()
	cfg := &config.Config{
		ConsoleToken:    consoleToken,
		ConsoleURL:      "http://console.test",
		GiteaURL:        "http://gitea.test",
		GiteaToken:      "gitea-pat",
		WebhookSecret:   secret,
		AuthTokenKey:    validTokenKey(t),
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

// mkProviderUser creates a user with an identity on the given provider whose
// provider_uid is uid (the stringified numeric id a webhook payload carries).
func mkProviderUser(t *testing.T, st *store.MemStore, prov domain.GitProvider, name, uid string) *domain.User {
	t.Helper()
	u := &domain.User{ID: domain.NewID(), DisplayName: name, CreatedAt: time.Now().UTC()}
	id := &domain.UserIdentity{
		ID: domain.NewID(), Provider: prov, ProviderUID: uid,
		Username: name, AccessTokenEnc: []byte("enc"), CreatedAt: time.Now().UTC(),
	}
	if _, err := st.CreateUserWithIdentity(context.Background(), u, id); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetUser(context.Background(), u.ID)
	return got
}

// seedMPWebhookProject makes a draft_pr github/gitlab service tracking
// repoOwnerName, bound to an integration whose bot token is sealed with the live
// cipher (so the github/gitlab reply credential resolves), with `member` at the
// given role, and seeds the PR-by-number head/base for the fake.
func seedMPWebhookProject(t *testing.T, st *store.MemStore, srv *Server, fake *provider.FakeProvider, prov domain.GitProvider, repoOwnerName string, member *domain.User, role domain.Role) *domain.Service {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "wh", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	host := "github.com"
	if prov == domain.ProviderGitLab {
		host = "gitlab.com"
	}
	enc, err := srv.Cipher().EncryptString("bot-token")
	if err != nil {
		t.Fatal(err)
	}
	integ := &domain.Integration{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		Provider: prov, Host: host, CredType: domain.CredTypePAT,
		TokenEnc: enc, BotUsername: "jcode-bot", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateIntegration(ctx, integ); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: prov,
		RepoOwnerName: repoOwnerName, DefaultBranch: "main",
		GitMode: domain.GitModeDraftPR, IntegrationID: &integ.ID, CreatedAt: time.Now().UTC(),
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
	owner, repo, _ := provider.SplitRepo(repoOwnerName)
	fake.SeedByNumber(owner, repo, 7, provider.PR{
		Number: 7, URL: "http://host/" + repoOwnerName + "/pulls/7",
		State: "open", HeadRef: "feature-x", BaseRef: "main",
	})
	return svc
}

// postGitHubWebhook posts body to /webhooks/github with a valid X-Hub-Signature-256
// over signWith (pass a different secret to forge a bad signature).
func postGitHubWebhook(t *testing.T, ts *httptest.Server, signWith, event string, body []byte) *http.Response {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(signWith))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// postGitLabWebhook posts body to /webhooks/gitlab with the given X-Gitlab-Token.
func postGitLabWebhook(t *testing.T, ts *httptest.Server, token, event string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/gitlab", bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Event", event)
	req.Header.Set("X-Gitlab-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// gitlabNoteBytes builds a GitLab "Note Hook" payload (real-shape subset). A
// noteableType != "MergeRequest" exercises the non-MR ignore path.
func gitlabNoteBytes(noteableType string, noteID, userID int64, note, pathWithNamespace string, mrIID int) []byte {
	m := map[string]any{
		"object_kind": "note",
		"event_type":  "note",
		"user":        map[string]any{"id": userID, "username": "dev"},
		"project":     map[string]any{"path_with_namespace": pathWithNamespace},
		"object_attributes": map[string]any{
			"id":            noteID,
			"note":          note,
			"noteable_type": noteableType,
			"url":           "https://gitlab.test/" + pathWithNamespace + "/-/merge_requests/" + strconv.Itoa(mrIID) + "#note_" + strconv.FormatInt(noteID, 10),
		},
		"merge_request": map[string]any{
			"iid": mrIID, "source_branch": "feature-x", "target_branch": "main",
		},
	}
	b, _ := json.Marshal(m)
	return b
}

// --- route registration -----------------------------------------------------

func TestMultiProviderWebhookRoutesAbsentWithoutSecret(t *testing.T) {
	st := store.NewMemStore()
	ts, _, _ := newMPWebhookServer(t, st, "", true) // no secret => routes absent
	for _, path := range []string{"/webhooks/github", "/webhooks/gitlab"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader([]byte(`{}`)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404 (route must be absent without a secret)", path, resp.StatusCode)
		}
	}
}

// --- GitHub handler ---------------------------------------------------------

func TestGitHubWebhookBadSignature(t *testing.T) {
	st := store.NewMemStore()
	ts, _, _ := newMPWebhookServer(t, st, webhookSecret, true)
	body := commentPayload("created", true, 100, 1, "@jcode review", "org/seed", 7)
	resp := postGitHubWebhook(t, ts, "wrong-secret", "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 for a bad signature", resp.StatusCode)
	}
}

func TestGitHubWebhookNonPRCommentIgnored(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newMPWebhookServer(t, st, webhookSecret, true)
	body := commentPayload("created", false /*not a PR*/, 101, 1, "@jcode review", "org/seed", 7)
	resp := postGitHubWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if fake.CommentCount() != 0 {
		t.Fatal("a non-PR comment must not reply or create a run")
	}
}

func TestGitHubWebhookReviewCommandCreatesReviewRun(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, true)
	admin := mkProviderUser(t, st, domain.ProviderGitHub, "admin", "999") // first user => cluster admin
	_ = admin
	member := mkProviderUser(t, st, domain.ProviderGitHub, "dev", "1001")
	svc := seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitHub, "org/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 200, 1001, "@jcode review", "org/seed", 7)
	resp := postGitHubWebhook(t, ts, webhookSecret, "issue_comment", body)
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
	// The github de-dup key is provider-prefixed so it can never collide with a
	// gitea / gitlab comment id.
	if r.Origin != domain.RunOriginWebhook || r.OriginCommentID != "github:200" {
		t.Errorf("origin=%q comment=%q want webhook/github:200", r.Origin, r.OriginCommentID)
	}
	if r.PRHeadBranch != "feature-x" || r.PRBaseBranch != "main" || r.PRNumber != 7 {
		t.Errorf("PR head/base/num = %q/%q/%d want feature-x/main/7", r.PRHeadBranch, r.PRBaseBranch, r.PRNumber)
	}
	if r.TriggeredByUserID == nil || *r.TriggeredByUserID != member.ID {
		t.Errorf("triggered_by = %v want the mapped member", r.TriggeredByUserID)
	}
	if fake.CommentCount() != 1 || !bytes.Contains([]byte(fake.Comments[0].Body), []byte("run started")) {
		t.Fatalf("expected 1 receipt with 'run started', got %+v", fake.Comments)
	}
}

func TestGitHubWebhookTaskCommandCreatesAgentRun(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, true)
	member := mkProviderUser(t, st, domain.ProviderGitHub, "dev", "1001") // first user => cluster admin (fine)
	svc := seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitHub, "org/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 201, 1001, "@jcode Add a CONTRIBUTING.md", "org/seed", 7)
	resp := postGitHubWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()

	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("created %d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.Kind != domain.RunKindAgent || r.Prompt != "Add a CONTRIBUTING.md" {
		t.Errorf("kind=%q prompt=%q want agent/'Add a CONTRIBUTING.md'", r.Kind, r.Prompt)
	}
	if r.PRHeadBranch != "feature-x" || r.PRURL == "" || r.PRNumber != 7 {
		t.Errorf("run not associated with the PR: head=%q url=%q num=%d", r.PRHeadBranch, r.PRURL, r.PRNumber)
	}
}

func TestGitHubWebhookUnmappedIdentityReplies(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, true)
	member := mkProviderUser(t, st, domain.ProviderGitHub, "dev", "1001")
	svc := seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitHub, "org/seed", member, domain.RoleMember)

	// Comment from a github uid with NO jcloud identity.
	body := commentPayload("created", true, 202, 55555, "@jcode review", "org/seed", 7)
	resp := postGitHubWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (unmapped identity)", len(runs))
	}
	if fake.CommentCount() != 1 || !bytes.Contains([]byte(fake.Comments[0].Body), []byte("jcloud account")) {
		t.Fatalf("expected an explanatory reply naming the GitHub account, got %+v", fake.Comments)
	}
	if !bytes.Contains([]byte(fake.Comments[0].Body), []byte("GitHub")) {
		t.Fatalf("reply should name the provider (GitHub); got %q", fake.Comments[0].Body)
	}
}

func TestGitHubWebhookDedupe(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, true)
	member := mkProviderUser(t, st, domain.ProviderGitHub, "dev", "1001")
	svc := seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitHub, "org/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 300, 1001, "@jcode review", "org/seed", 7)
	for i := 0; i < 3; i++ {
		resp := postGitHubWebhook(t, ts, webhookSecret, "issue_comment", body)
		resp.Body.Close()
	}
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("redelivered comment created %d runs, want 1 (dedup)", len(runs))
	}
	if fake.CommentCount() != 1 {
		t.Fatalf("posted %d receipts across 3 deliveries, want 1", fake.CommentCount())
	}
}

// TestGitHubWebhookModelNotConfiguredReplies is the fail-visible model gate on the
// github path: no LLM configured → no run, one explanatory reply linking the
// console.
func TestGitHubWebhookModelNotConfiguredReplies(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, false /* no model */)
	member := mkProviderUser(t, st, domain.ProviderGitHub, "dev", "1001")
	svc := seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitHub, "org/seed", member, domain.RoleMember)

	body := commentPayload("created", true, 250, 1001, "@jcode Add a CONTRIBUTING.md", "org/seed", 7)
	resp := postGitHubWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (model not configured)", len(runs))
	}
	if fake.CommentCount() != 1 {
		t.Fatalf("posted %d comments, want 1 explanatory reply", fake.CommentCount())
	}
	got := fake.Comments[0].Body
	if !bytes.Contains([]byte(got), []byte("no LLM is configured")) || !bytes.Contains([]byte(got), []byte("http://console.test")) {
		t.Fatalf("reply should explain + link the console; got %q", got)
	}
}

// TestGitHubWebhookNoServiceLogsIgnored: a valid @jcode comment on a repo with NO
// jcloud service (so github has no integration credential to even reply with) is a
// silent log+ignore — never a run, never a fake success.
func TestGitHubWebhookNoServiceLogsIgnored(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newMPWebhookServer(t, st, webhookSecret, true)
	_ = mkProviderUser(t, st, domain.ProviderGitHub, "dev", "1001")

	body := commentPayload("created", true, 260, 1001, "@jcode review", "org/unknown", 7)
	resp := postGitHubWebhook(t, ts, webhookSecret, "issue_comment", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if fake.CommentCount() != 0 {
		t.Fatalf("with no service (no reply credential) nothing should be posted, got %+v", fake.Comments)
	}
}

// --- GitLab handler ---------------------------------------------------------

func TestGitLabWebhookBadToken(t *testing.T) {
	st := store.NewMemStore()
	ts, _, _ := newMPWebhookServer(t, st, webhookSecret, true)
	body := gitlabNoteBytes("MergeRequest", 1244, 1, "@jcode review", "group/seed", 7)
	resp := postGitLabWebhook(t, ts, "wrong-token", "Note Hook", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 for a bad token", resp.StatusCode)
	}
}

func TestGitLabWebhookNonNoteEventIgnored(t *testing.T) {
	st := store.NewMemStore()
	ts, _, fake := newMPWebhookServer(t, st, webhookSecret, true)
	body := gitlabNoteBytes("MergeRequest", 1244, 1, "@jcode review", "group/seed", 7)
	resp := postGitLabWebhook(t, ts, webhookSecret, "Push Hook", body) // wrong event
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200 (non Note Hook ignored)", resp.StatusCode)
	}
	if fake.CommentCount() != 0 {
		t.Fatal("a non-Note event must be a no-op")
	}
}

func TestGitLabWebhookNonMRNoteIgnored(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, true)
	member := mkProviderUser(t, st, domain.ProviderGitLab, "dev", "1001")
	_ = seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitLab, "group/seed", member, domain.RoleMember)

	// A note on an Issue (not a MergeRequest) must be ignored.
	body := gitlabNoteBytes("Issue", 1245, 1001, "@jcode review", "group/seed", 7)
	resp := postGitLabWebhook(t, ts, webhookSecret, "Note Hook", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if fake.CommentCount() != 0 {
		t.Fatal("a non-MR note must not reply or create a run")
	}
}

func TestGitLabWebhookReviewCommandCreatesReviewRun(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, true)
	admin := mkProviderUser(t, st, domain.ProviderGitLab, "admin", "999") // first => cluster admin
	_ = admin
	member := mkProviderUser(t, st, domain.ProviderGitLab, "dev", "1001")
	svc := seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitLab, "group/seed", member, domain.RoleMember)

	// MR iid=7; comment id 1244 → de-dup key "gitlab:1244".
	body := gitlabNoteBytes("MergeRequest", 1244, 1001, "@jcode review", "group/seed", 7)
	resp := postGitLabWebhook(t, ts, webhookSecret, "Note Hook", body)
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
	if r.OriginCommentID != "gitlab:1244" {
		t.Errorf("comment key=%q want gitlab:1244", r.OriginCommentID)
	}
	// MR iid maps to PRNumber; head/base come from the PRByNumber lookup.
	if r.PRNumber != 7 || r.PRHeadBranch != "feature-x" || r.PRBaseBranch != "main" {
		t.Errorf("PR num/head/base = %d/%q/%q want 7/feature-x/main", r.PRNumber, r.PRHeadBranch, r.PRBaseBranch)
	}
	if r.OriginCommentURL == "" {
		t.Error("origin_comment_url should carry the note url")
	}
	if fake.CommentCount() != 1 || !bytes.Contains([]byte(fake.Comments[0].Body), []byte("run started")) {
		t.Fatalf("expected 1 receipt, got %+v", fake.Comments)
	}
}

func TestGitLabWebhookTaskCommandCreatesAgentRun(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, true)
	member := mkProviderUser(t, st, domain.ProviderGitLab, "dev", "1001")
	svc := seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitLab, "group/seed", member, domain.RoleMember)

	body := gitlabNoteBytes("MergeRequest", 1300, 1001, "@jcode Add tests", "group/seed", 7)
	resp := postGitLabWebhook(t, ts, webhookSecret, "Note Hook", body)
	defer resp.Body.Close()
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("created %d runs, want 1", len(runs))
	}
	if runs[0].Kind != domain.RunKindAgent || runs[0].Prompt != "Add tests" {
		t.Errorf("kind=%q prompt=%q want agent/'Add tests'", runs[0].Kind, runs[0].Prompt)
	}
}

func TestGitLabWebhookNonMemberReplies(t *testing.T) {
	st := store.NewMemStore()
	ts, srv, fake := newMPWebhookServer(t, st, webhookSecret, true)
	admin := mkProviderUser(t, st, domain.ProviderGitLab, "admin", "999") // first => cluster admin
	_ = admin
	stranger := mkProviderUser(t, st, domain.ProviderGitLab, "stranger", "1002")
	_ = stranger
	svc := seedMPWebhookProject(t, st, srv, fake, domain.ProviderGitLab, "group/seed", nil, domain.RoleViewer)

	body := gitlabNoteBytes("MergeRequest", 1246, 1002, "@jcode review", "group/seed", 7)
	resp := postGitLabWebhook(t, ts, webhookSecret, "Note Hook", body)
	defer resp.Body.Close()
	runs, _ := st.ListRunsByService(context.Background(), svc.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (non-member)", len(runs))
	}
	if fake.CommentCount() != 1 || !bytes.Contains([]byte(fake.Comments[0].Body), []byte("project")) {
		t.Fatalf("expected a no-access reply, got %+v", fake.Comments)
	}
}
