package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGitHubClient exercises the four Provider ops against an httptest GitHub
// API: find, create (draft:true + Bearer auth), review comment, status.
func TestGitHubClient(t *testing.T) {
	var createBody map[string]any
	var reviewBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls":
			_, _ = w.Write([]byte(`[{"number":3,"html_url":"h3","head":{"ref":"other"}},{"number":9,"html_url":"h9","head":{"ref":"feat"}}]`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":11,"html_url":"http://gh/o/r/pull/11"}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls/11/reviews":
			_ = json.NewDecoder(r.Body).Decode(&reviewBody)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/11":
			_, _ = w.Write([]byte(`{"number":11,"html_url":"http://gh/o/r/pull/11","state":"closed","merged":true}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := NewGitHubClient(srv.URL, "ghtok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pr, err := c.FindOpenPRByHead(ctx, "o", "r", "feat")
	if err != nil || pr == nil || pr.Number != 9 {
		t.Fatalf("find = %+v err=%v want #9", pr, err)
	}

	created, err := c.CreateDraftPR(ctx, CreateDraftPRInput{Owner: "o", Repo: "r", Head: "feat", Base: "main", Title: "[jcode] x", Body: "b"})
	if err != nil || created.Number != 11 {
		t.Fatalf("create = %+v err=%v", created, err)
	}
	if created, _ := createBody["draft"].(bool); !created {
		t.Errorf("create body draft=%v want true", createBody["draft"])
	}
	if gotAuth != "Bearer ghtok" {
		t.Errorf("auth=%q want Bearer ghtok", gotAuth)
	}

	if err := c.CreatePRReview(ctx, "o", "r", 11, "looks good"); err != nil {
		t.Fatalf("review: %v", err)
	}
	if reviewBody["event"] != "COMMENT" || reviewBody["body"] != "looks good" {
		t.Errorf("review body = %+v", reviewBody)
	}

	st, err := c.PRStatus(ctx, "o", "r", 11)
	if err != nil || st.State != "merged" {
		t.Fatalf("status = %+v err=%v want merged", st, err)
	}
}

// TestGitLabClient exercises the four ops against an httptest GitLab API using
// merge-request vocabulary (iid, Draft: prefix, notes, url-encoded project path).
func TestGitLabClient(t *testing.T) {
	var createBody map[string]any
	var noteBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.EscapedPath() // keeps %2F in the project id segment
		switch {
		case r.Method == "GET" && path == "/projects/o%2Fr/merge_requests":
			_, _ = w.Write([]byte(`[{"iid":7,"web_url":"w7","source_branch":"feat"}]`))
		case r.Method == "POST" && path == "/projects/o%2Fr/merge_requests":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_, _ = w.Write([]byte(`{"iid":8,"web_url":"http://gl/o/r/-/mr/8"}`))
		case r.Method == "POST" && path == "/projects/o%2Fr/merge_requests/8/notes":
			_ = json.NewDecoder(r.Body).Decode(&noteBody)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "GET" && path == "/projects/o%2Fr/merge_requests/8":
			_, _ = w.Write([]byte(`{"iid":8,"web_url":"http://gl/o/r/-/mr/8","state":"opened"}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := NewGitLabClient(srv.URL, "gltok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pr, err := c.FindOpenPRByHead(ctx, "o", "r", "feat")
	if err != nil || pr == nil || pr.Number != 7 {
		t.Fatalf("find = %+v err=%v want #7", pr, err)
	}
	created, err := c.CreateDraftPR(ctx, CreateDraftPRInput{Owner: "o", Repo: "r", Head: "feat", Base: "main", Title: "[jcode] x"})
	if err != nil || created.Number != 8 {
		t.Fatalf("create = %+v err=%v", created, err)
	}
	if title, _ := createBody["title"].(string); !strings.HasPrefix(title, GitLabDraftPrefix) {
		t.Errorf("MR title=%q want Draft: prefix", title)
	}
	if err := c.CreatePRReview(ctx, "o", "r", 8, "nice"); err != nil {
		t.Fatalf("note: %v", err)
	}
	if noteBody["body"] != "nice" {
		t.Errorf("note body=%+v", noteBody)
	}
	st, err := c.PRStatus(ctx, "o", "r", 8)
	if err != nil || st.State != "open" {
		t.Fatalf("status=%+v err=%v want open", st, err)
	}
}

// TestOAuthRefresh proves Refresh trades a refresh token for a fresh access
// token via the token endpoint (grant_type=refresh_token).
func TestOAuthRefresh(t *testing.T) {
	var gotGrant, gotRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrant = r.Form.Get("grant_type")
		gotRefresh = r.Form.Get("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer srv.Close()

	p := NewGiteaOAuth(OAuthConfig{ClientID: "id", ClientSecret: "sec", ExternalURL: srv.URL, InternalURL: srv.URL})
	tok, err := p.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if gotGrant != "refresh_token" || gotRefresh != "old-refresh" {
		t.Fatalf("grant=%q refresh=%q", gotGrant, gotRefresh)
	}
	if tok.AccessToken != "new-access" || tok.RefreshToken != "new-refresh" {
		t.Fatalf("tok = %+v", tok)
	}
	if tok.Expiry.IsZero() {
		t.Error("expiry not set from expires_in")
	}

	// Empty refresh token is an error.
	if _, err := p.Refresh(context.Background(), ""); err == nil {
		t.Error("Refresh(\"\") should error")
	}
}

// TestFactoryBuildsClients proves the default Factory builds a client per host
// and errors for an unknown provider.
func TestFactoryBuildsClients(t *testing.T) {
	f := NewFactory("http://gitea.test")
	if _, err := f.PRClient("gitea", "tok", "token"); err != nil {
		t.Errorf("gitea client: %v", err)
	}
	if _, err := f.PRClient("github", "tok", "Bearer"); err != nil {
		t.Errorf("github client: %v", err)
	}
	if _, err := f.PRClient("gitlab", "tok", "Bearer"); err != nil {
		t.Errorf("gitlab client: %v", err)
	}
	if _, err := f.PRClient("svn", "tok", "token"); err == nil {
		t.Error("unknown provider should error")
	}
}

// TestIntegrationClientRefusesRedirects is the SSRF-hardening regression (F5
// review C1①): a user-supplied integration host answering with a 30x must NOT
// have the orchestrator's authenticated request bounced to the redirect target
// (e.g. an internal address). The client built by IntegrationClient refuses to
// follow, the probe fails visibly, and the target is never hit.
func TestIntegrationClientRefusesRedirects(t *testing.T) {
	// The would-be internal target: any hit means the redirect was followed.
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits++
		_, _ = w.Write([]byte(`{"login":"evil"}`))
	}))
	defer target.Close()

	// The malicious integration host: 302 every request at the target.
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer evil.Close()

	// gitea
	c, err := IntegrationClient("gitea", evil.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.(CurrentUser).CurrentUser(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "redirect") {
		t.Fatalf("gitea CurrentUser err=%v want redirect refusal", err)
	}
	// github (enterprise-shaped base so the API path stays on the evil host)
	gh, err := IntegrationClient("github", evil.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gh.(CurrentUser).CurrentUser(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "redirect") {
		t.Fatalf("github CurrentUser err=%v want redirect refusal", err)
	}
	// gitlab
	gl, err := IntegrationClient("gitlab", evil.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gl.(CurrentUser).CurrentUser(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "redirect") {
		t.Fatalf("gitlab CurrentUser err=%v want redirect refusal", err)
	}

	if targetHits != 0 {
		t.Fatalf("redirect TARGET was hit %d times — SSRF bounce not blocked", targetHits)
	}
}
