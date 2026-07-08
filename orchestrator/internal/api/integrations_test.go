package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// fakeGitea stands up an httptest server emulating the two Gitea endpoints the
// integration flow calls: GET /api/v1/user (connectivity + bot_username) and
// GET /api/v1/repos/search (repo listing / reachability). bots maps an accepted
// token to the bot login it reports — any other token gets a 401, exercising the
// "bad token" path; a rotation test maps old and new tokens to different logins
// on ONE host (host_mismatch pins each server to a single gitea host now).
func fakeGitea(t *testing.T, bots map[string]string, repos []map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	login := func(r *http.Request) (string, bool) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
		l, ok := bots[tok]
		return l, ok
	}
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		l, ok := login(r)
		if !ok {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": l})
	})
	mux.HandleFunc("/api/v1/repos/search", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := login(r); !ok {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": repos})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// newCipherServer builds a server WITH a token cipher (AUTH_TOKEN_KEY), the
// given cluster git-host allowlist, and GITEA_URL pointing at giteaURL (the
// host_mismatch wiring constraint requires a gitea integration's host to match
// the cluster GITEA_URL). Returns the cfg too so a test can TIGHTEN the
// allowlist after setup (the dispatch-time gate).
func newCipherServer(t *testing.T, allowed []string, giteaURL string) (*httptest.Server, *store.MemStore, *config.Config) {
	t.Helper()
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{
		ConsoleToken:    consoleToken,
		AuthTokenKey:    validTokenKey(t),
		AllowedGitHosts: allowed,
		GiteaURL:        giteaURL,
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, cfg
}

// createIntegration is a helper: POST an integration and return its view.
func createIntegration(t *testing.T, ts *httptest.Server, tok, pid string, body map[string]any) (*http.Response, integrationView) {
	t.Helper()
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/integrations", tok, body)
	var iv integrationView
	if resp.StatusCode == http.StatusCreated {
		decode(t, resp, &iv)
	}
	return resp, iv
}

// TestIntegrationCreateVerifiesConnectivity: a create verifies the token against
// the provider (discovering bot_username), never echoes the token, and rejects a
// token the provider refuses with a fail-visible 400 (CLAUDE.md red line #1).
func TestIntegrationCreateVerifiesConnectivity(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "jcloud-bot"}, nil)
	ts, _, _ := newCipherServer(t, nil, gitea.URL)
	pid := newProject(t, ts, "integ")

	// Happy path.
	resp, iv := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create integration: status=%d want 201", resp.StatusCode)
	}
	if iv.BotUsername != "jcloud-bot" {
		t.Fatalf("bot_username=%q want jcloud-bot (discovered from provider)", iv.BotUsername)
	}
	if iv.Name != "default" || iv.Provider != "gitea" || !iv.TokenSet {
		t.Fatalf("view=%+v", iv)
	}

	// The token must NEVER be echoed anywhere in the response body.
	raw := do(t, "GET", ts.URL+"/api/v1/projects/"+pid+"/integrations", consoleToken, nil)
	b, _ := io.ReadAll(raw.Body)
	raw.Body.Close()
	if strings.Contains(string(b), "good-pat") {
		t.Fatalf("TOKEN LEAK: integration list contains the token: %s", b)
	}

	// A token the provider refuses is a fail-visible 400 integration_unreachable.
	resp2, _ := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"name": "bad", "provider": "gitea", "host": gitea.URL, "token": "wrong-pat",
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad token: status=%d want 400", resp2.StatusCode)
	}
	var e errorBody
	decode(t, resp2, &e)
	if e.Error.Code != "integration_unreachable" {
		t.Fatalf("code=%q want integration_unreachable", e.Error.Code)
	}
}

// TestIntegrationCipherRequired: without AUTH_TOKEN_KEY the token cannot be
// sealed, so create is a typed 409 (never store a secret in the clear). The
// server's GITEA_URL matches the host so the check under test — the cipher gate
// — is the one that fires (host_mismatch is evaluated before it).
func TestIntegrationCipherRequired(t *testing.T) {
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{
		ConsoleToken: consoleToken,
		GiteaURL:     "https://gitea.example.com",
		// No AuthTokenKey => no cipher.
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	pid := newProject(t, ts, "nocipher")
	resp, _ := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": "gitea.example.com", "token": "x",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("no cipher: status=%d want 409", resp.StatusCode)
	}
	var e errorBody
	decode(t, resp, &e)
	if e.Error.Code != "cipher_not_configured" {
		t.Fatalf("code=%q want cipher_not_configured", e.Error.Code)
	}
}

// TestIntegrationHostAllowlist: with a non-empty cluster allowlist a disallowed
// host is a 400 host_not_allowed BEFORE any network call; an allowlisted host is
// accepted — and the comparison is PORT-SENSITIVE (SSRF review C1②): an entry
// without the port does NOT open a host serving on an explicit port.
func TestIntegrationHostAllowlist(t *testing.T) {
	// Allow only "github.com": a gitea host on 127.0.0.1 is rejected.
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, nil)
	ts, _, _ := newCipherServer(t, []string{"github.com"}, gitea.URL)
	pid := newProject(t, ts, "allow")

	resp, _ := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("disallowed host: status=%d want 400", resp.StatusCode)
	}
	var e errorBody
	decode(t, resp, &e)
	if e.Error.Code != "host_not_allowed" {
		t.Fatalf("code=%q want host_not_allowed", e.Error.Code)
	}

	// Port-sensitivity: an allowlist entry of the BARE host must NOT match the
	// fake's host:port (different port = different service).
	gitea2 := fakeGitea(t, map[string]string{"good-pat": "bot"}, nil)
	ts2, _, _ := newCipherServer(t, []string{"127.0.0.1"}, gitea2.URL)
	pid2 := newProject(t, ts2, "allow-port")
	resp2, _ := createIntegration(t, ts2, consoleToken, pid2, map[string]any{
		"provider": "gitea", "host": gitea2.URL, "token": "good-pat",
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("bare-host allowlist vs host:port integration: status=%d want 400 (port-sensitive)", resp2.StatusCode)
	}
	resp2.Body.Close()

	// The exact host:port entry is accepted.
	gitea3 := fakeGitea(t, map[string]string{"good-pat": "bot"}, nil)
	ts3, _, _ := newCipherServer(t, []string{strings.TrimPrefix(gitea3.URL, "http://")}, gitea3.URL)
	pid3 := newProject(t, ts3, "allow-exact")
	resp3, _ := createIntegration(t, ts3, consoleToken, pid3, map[string]any{
		"provider": "gitea", "host": gitea3.URL, "token": "good-pat",
	})
	if resp3.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp3.Body)
		t.Fatalf("exact host:port allowlist: status=%d want 201; body=%s", resp3.StatusCode, b)
	}
	resp3.Body.Close()
}

// TestIntegrationHostMismatch (F5 review P2): the integration host must match
// where this deployment actually performs git operations — gitea via the
// cluster GITEA_URL, github/gitlab via their public hosts. A mismatch is a
// typed 400 host_mismatch BEFORE any network round-trip.
func TestIntegrationHostMismatch(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, nil)
	ts, _, _ := newCipherServer(t, nil, gitea.URL)
	pid := newProject(t, ts, "mismatch")

	// A gitea host that differs from the cluster GITEA_URL → 400 host_mismatch.
	resp, _ := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": "http://other-gitea.example.com:3000", "token": "good-pat",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("gitea host mismatch: status=%d want 400", resp.StatusCode)
	}
	var e errorBody
	decode(t, resp, &e)
	if e.Error.Code != "host_mismatch" {
		t.Fatalf("code=%q want host_mismatch", e.Error.Code)
	}

	// A github integration must target github.com (public host only for now).
	resp2, _ := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"name": "ghe", "provider": "github", "host": "ghe.corp.example.com", "token": "x",
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("github enterprise host: status=%d want 400", resp2.StatusCode)
	}
	var e2 errorBody
	decode(t, resp2, &e2)
	if e2.Error.Code != "host_mismatch" {
		t.Fatalf("code=%q want host_mismatch", e2.Error.Code)
	}

	// With NO cluster GITEA_URL a gitea integration cannot operate → host_mismatch.
	ts2, _, _ := newCipherServer(t, nil, "")
	pid2 := newProject(t, ts2, "no-gitea-url")
	resp3, _ := createIntegration(t, ts2, consoleToken, pid2, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("gitea integration without GITEA_URL: status=%d want 400", resp3.StatusCode)
	}
	resp3.Body.Close()
}

// TestIntegrationTokenRotation: PATCH rotates the token (re-verifying + refreshing
// bot_username); the token stays write-only; an empty token is rejected. One fake
// host maps old + rotated tokens to different bot logins.
func TestIntegrationTokenRotation(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"old-pat": "old-bot", "rotated-pat": "rotated-bot"}, nil)
	ts, _, _ := newCipherServer(t, nil, gitea.URL)
	pid := newProject(t, ts, "rotate")

	_, iv := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "old-pat",
	})
	if iv.ID == "" {
		t.Fatal("integration not created")
	}
	if iv.BotUsername != "old-bot" {
		t.Fatalf("bot=%q want old-bot", iv.BotUsername)
	}

	// Rotate: re-verifies, refreshes the bot username.
	resp := do(t, "PATCH", ts.URL+"/api/v1/integrations/"+iv.ID, consoleToken, map[string]any{"token": "rotated-pat"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate: status=%d want 200", resp.StatusCode)
	}
	var rotated integrationView
	decode(t, resp, &rotated)
	if !rotated.TokenSet || rotated.BotUsername != "rotated-bot" {
		t.Fatalf("rotated view=%+v want token_set + refreshed bot", rotated)
	}

	// An empty token on PATCH is rejected (an integration always needs a credential).
	resp = do(t, "PATCH", ts.URL+"/api/v1/integrations/"+iv.ID, consoleToken, map[string]any{"token": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty token rotate: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestIntegrationRBAC: create/rotate/delete are owner+, list/repos are member+, a
// viewer is denied writes, and a cross-project PATCH is denied.
func TestIntegrationRBAC(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, []map[string]any{
		{"id": 42, "full_name": "acme/widget", "default_branch": "main"},
	})
	ts, st, _ := newCipherServer(t, nil, gitea.URL)

	owner := mkUser(t, st, "iowner")
	member := mkUser(t, st, "imember")
	viewer := mkUser(t, st, "iviewer")
	stranger := mkUser(t, st, "istranger")
	tokOwner := mkSession(t, st, owner.ID)
	tokMember := mkSession(t, st, member.ID)
	tokViewer := mkSession(t, st, viewer.ID)
	tokStranger := mkSession(t, st, stranger.ID)

	// Owner creates a project + adds member/viewer.
	resp := do(t, "POST", ts.URL+"/api/v1/projects", tokOwner, map[string]any{"name": "rbac-integ"})
	var pv projectView
	decode(t, resp, &pv)
	pid := pv.ID
	for uid, role := range map[string]string{member.ID: "member", viewer.ID: "viewer"} {
		do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/members", tokOwner,
			map[string]any{"user_id": uid, "role": role}).Body.Close()
	}

	body := map[string]any{"provider": "gitea", "host": gitea.URL, "token": "good-pat"}

	// Member/viewer cannot create (owner-only).
	if r, _ := createIntegration(t, ts, tokMember, pid, body); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member create: status=%d want 403", r.StatusCode)
	}
	if r, _ := createIntegration(t, ts, tokViewer, pid, body); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create: status=%d want 403", r.StatusCode)
	}
	// Owner creates.
	r, iv := createIntegration(t, ts, tokOwner, pid, body)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("owner create: status=%d want 201", r.StatusCode)
	}

	// Member CAN list + list repos.
	if lr := do(t, "GET", ts.URL+"/api/v1/projects/"+pid+"/integrations", tokMember, nil); lr.StatusCode != http.StatusOK {
		t.Fatalf("member list: status=%d want 200", lr.StatusCode)
	} else {
		lr.Body.Close()
	}
	repoResp := do(t, "GET", ts.URL+"/api/v1/projects/"+pid+"/integrations/"+iv.ID+"/repos", tokMember, nil)
	if repoResp.StatusCode != http.StatusOK {
		t.Fatalf("member list repos: status=%d want 200", repoResp.StatusCode)
	}
	repoResp.Body.Close()

	// Stranger cannot list.
	if lr := do(t, "GET", ts.URL+"/api/v1/projects/"+pid+"/integrations", tokStranger, nil); lr.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger list: status=%d want 403", lr.StatusCode)
	} else {
		lr.Body.Close()
	}
	// Member cannot delete (owner-only).
	if dr := do(t, "DELETE", ts.URL+"/api/v1/integrations/"+iv.ID, tokMember, nil); dr.StatusCode != http.StatusForbidden {
		t.Fatalf("member delete: status=%d want 403", dr.StatusCode)
	} else {
		dr.Body.Close()
	}

	// Cross-project: a SECOND project's owner cannot PATCH the first's
	// integration (403 — the stranger is not a member of its project).
	resp2 := do(t, "POST", ts.URL+"/api/v1/projects", tokStranger, map[string]any{"name": "other"})
	resp2.Body.Close()
	if pr := do(t, "PATCH", ts.URL+"/api/v1/integrations/"+iv.ID, tokStranger, map[string]any{"name": "x"}); pr.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-project patch: status=%d want 403", pr.StatusCode)
	} else {
		pr.Body.Close()
	}

	// Owner deletes.
	if dr := do(t, "DELETE", ts.URL+"/api/v1/integrations/"+iv.ID, tokOwner, nil); dr.StatusCode != http.StatusOK {
		t.Fatalf("owner delete: status=%d want 200", dr.StatusCode)
	} else {
		dr.Body.Close()
	}
}

// TestMemberBuildsServiceViaIntegration: a MEMBER may create a service by binding
// a project's existing integration (D19 RBAC opening), the repo must be reachable
// by the bot token, and the service records the integration + provider; a BARE
// create (no integration) by a member is still owner-only (403).
func TestMemberBuildsServiceViaIntegration(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, []map[string]any{
		{"id": 42, "full_name": "acme/widget", "default_branch": "main"},
	})
	ts, st, _ := newCipherServer(t, nil, gitea.URL)

	owner := mkUser(t, st, "bowner")
	member := mkUser(t, st, "bmember")
	tokOwner := mkSession(t, st, owner.ID)
	tokMember := mkSession(t, st, member.ID)

	resp := do(t, "POST", ts.URL+"/api/v1/projects", tokOwner, map[string]any{"name": "build"})
	var pv projectView
	decode(t, resp, &pv)
	pid := pv.ID
	do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/members", tokOwner,
		map[string]any{"user_id": member.ID, "role": "member"}).Body.Close()

	_, iv := createIntegration(t, ts, tokOwner, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})

	// Member creates a service off the integration (reachable repo) → 201.
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", tokMember, map[string]any{
		"name": "widget", "owner_name": "acme/widget", "integration_id": iv.ID,
		"provider_repo_id": 42, "git_mode": "draft_pr",
	})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("member build via integration: status=%d want 201; body=%s", resp.StatusCode, b)
	}
	var svc domain.Service
	decode(t, resp, &svc)
	if svc.IntegrationID == nil || *svc.IntegrationID != iv.ID {
		t.Fatalf("service integration_id=%v want %s", svc.IntegrationID, iv.ID)
	}
	if svc.Provider != domain.ProviderGitea || svc.RepoOwnerName != "acme/widget" {
		t.Fatalf("service repo config not from integration: %+v", svc)
	}

	// Member BARE create (no integration) is still owner-only → 403.
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", tokMember, map[string]any{
		"name": "bare", "repo_url": "https://git/x.git",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member bare create: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// A repo NOT reachable by the integration is rejected fail-visibly.
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", tokMember, map[string]any{
		"name": "sneaky", "owner_name": "secret/private", "integration_id": iv.ID, "git_mode": "draft_pr",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unreachable repo: status=%d want 400", resp.StatusCode)
	}
	var e errorBody
	decode(t, resp, &e)
	if e.Error.Code != "repo_not_reachable" {
		t.Fatalf("code=%q want repo_not_reachable", e.Error.Code)
	}
}

// TestServiceBindMatchesByRepoID (F5 review C3): the reachability match keys off
// the RENAME-PROOF numeric repo id, not just the owner/name string — a repo the
// picker saw before a rename still binds, and the service's owner_name is
// canonicalised to the provider's current full_name. This only works because the
// create handler populates svc.ProviderRepoID BEFORE the bind.
func TestServiceBindMatchesByRepoID(t *testing.T) {
	// The provider reports the repo under its RENAMED full_name, same id 42.
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, []map[string]any{
		{"id": 42, "full_name": "acme/widget-renamed", "default_branch": "main"},
	})
	ts, _, _ := newCipherServer(t, nil, gitea.URL)
	pid := newProject(t, ts, "idmatch")
	_, iv := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})

	// The request still carries the OLD name but the picker's id.
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "widget", "owner_name": "acme/widget", "integration_id": iv.ID,
		"provider_repo_id": 42, "git_mode": "draft_pr",
	})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("id-match bind: status=%d want 201; body=%s", resp.StatusCode, b)
	}
	var svc domain.Service
	decode(t, resp, &svc)
	if svc.RepoOwnerName != "acme/widget-renamed" {
		t.Fatalf("owner_name=%q want canonicalised acme/widget-renamed (id match)", svc.RepoOwnerName)
	}
	if svc.ProviderRepoID == nil || *svc.ProviderRepoID != 42 {
		t.Fatalf("provider_repo_id=%v want 42", svc.ProviderRepoID)
	}
}

// TestServicePatchBindChecksReachability (F5 review C3): PATCH-binding an
// integration onto an existing service runs the SAME validation as create —
// including the "repo is in the bot's reachable set" gate.
func TestServicePatchBindChecksReachability(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, []map[string]any{
		{"id": 42, "full_name": "acme/widget", "default_branch": "main"},
	})
	ts, _, _ := newCipherServer(t, nil, gitea.URL)
	pid := newProject(t, ts, "patch-bind")
	_, iv := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})

	// An unbound service pointing at a repo the bot CANNOT see.
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "hidden", "owner_name": "secret/private", "provider": "gitea", "git_mode": "draft_pr",
	})
	var hidden domain.Service
	decode(t, resp, &hidden)

	// PATCH-bind → 400 repo_not_reachable (same gate as create).
	resp = do(t, "PATCH", ts.URL+"/api/v1/services/"+hidden.ID, consoleToken, map[string]any{
		"integration_id": iv.ID,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch-bind unreachable: status=%d want 400", resp.StatusCode)
	}
	var e errorBody
	decode(t, resp, &e)
	if e.Error.Code != "repo_not_reachable" {
		t.Fatalf("code=%q want repo_not_reachable", e.Error.Code)
	}

	// A reachable service PATCH-binds fine (and canonicalises).
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "widget", "owner_name": "acme/widget", "provider": "gitea", "git_mode": "draft_pr",
	})
	var widget domain.Service
	decode(t, resp, &widget)
	resp = do(t, "PATCH", ts.URL+"/api/v1/services/"+widget.ID, consoleToken, map[string]any{
		"integration_id": iv.ID,
	})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("patch-bind reachable: status=%d want 200; body=%s", resp.StatusCode, b)
	}
	var bound domain.Service
	decode(t, resp, &bound)
	if bound.IntegrationID == nil || *bound.IntegrationID != iv.ID {
		t.Fatalf("integration_id=%v want %s", bound.IntegrationID, iv.ID)
	}
}

// TestServiceBindRejectsCrossProjectIntegration: binding an integration that
// belongs to a DIFFERENT project is rejected (404).
func TestServiceBindRejectsCrossProjectIntegration(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, []map[string]any{
		{"id": 42, "full_name": "acme/widget", "default_branch": "main"},
	})
	ts, st, _ := newCipherServer(t, nil, gitea.URL)
	owner := mkUser(t, st, "xowner")
	tokOwner := mkSession(t, st, owner.ID)

	// Project A has the integration; project B tries to use it.
	ra := do(t, "POST", ts.URL+"/api/v1/projects", tokOwner, map[string]any{"name": "A"})
	var pa projectView
	decode(t, ra, &pa)
	rb := do(t, "POST", ts.URL+"/api/v1/projects", tokOwner, map[string]any{"name": "B"})
	var pb projectView
	decode(t, rb, &pb)

	_, iv := createIntegration(t, ts, tokOwner, pa.ID, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})

	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pb.ID+"/services", tokOwner, map[string]any{
		"name": "x", "owner_name": "acme/widget", "integration_id": iv.ID, "git_mode": "draft_pr",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project integration bind: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestIntegrationDeleteUnbindsServices: deleting an integration nulls the
// integration_id on any bound service (those services fall back to the legacy
// credential path).
func TestIntegrationDeleteUnbindsServices(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, []map[string]any{
		{"id": 42, "full_name": "acme/widget", "default_branch": "main"},
	})
	ts, st, _ := newCipherServer(t, nil, gitea.URL)
	pid := newProject(t, ts, "unbind")
	_, iv := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "widget", "owner_name": "acme/widget", "integration_id": iv.ID,
		"provider_repo_id": 42, "git_mode": "draft_pr",
	})
	var svc domain.Service
	decode(t, resp, &svc)

	if dr := do(t, "DELETE", ts.URL+"/api/v1/integrations/"+iv.ID, consoleToken, nil); dr.StatusCode != http.StatusOK {
		t.Fatalf("delete integration: status=%d want 200", dr.StatusCode)
	} else {
		dr.Body.Close()
	}
	got, err := st.GetService(context.Background(), svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IntegrationID != nil {
		t.Fatalf("service integration_id=%v want nil after integration delete", *got.IntegrationID)
	}
}

// TestIntegrationDispatchHostGate (F5 adjudication A): tightening the cluster
// allowlist AFTER an integration exists blocks NEW dispatches on services bound
// to it — run create, retry and review all 403 host_not_allowed immediately.
func TestIntegrationDispatchHostGate(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, []map[string]any{
		{"id": 42, "full_name": "acme/widget", "default_branch": "main"},
	})
	ts, st, cfg := newCipherServer(t, nil, gitea.URL) // unrestricted at create time
	pid := newProject(t, ts, "dispatch-gate")
	_, iv := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "widget", "owner_name": "acme/widget", "integration_id": iv.ID,
		"provider_repo_id": 42, "git_mode": "draft_pr",
	})
	var svc domain.Service
	decode(t, resp, &svc)

	// Baseline: dispatch works while the host is allowed.
	r1 := do(t, "POST", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken, map[string]any{"prompt": "go"})
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("dispatch before tighten: status=%d want 201", r1.StatusCode)
	}
	var run domain.Run
	decode(t, r1, &run)

	// TIGHTEN the cluster allowlist (the integration's host falls out of policy).
	cfg.AllowedGitHosts = []string{"github.com"}

	// New run dispatch → 403 host_not_allowed.
	r2 := do(t, "POST", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken, map[string]any{"prompt": "again"})
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("dispatch after tighten: status=%d want 403", r2.StatusCode)
	}
	var e errorBody
	decode(t, r2, &e)
	if e.Error.Code != "host_not_allowed" {
		t.Fatalf("code=%q want host_not_allowed", e.Error.Code)
	}

	// Retry of a terminal run → 403 too.
	ctx := context.Background()
	failed := &domain.Run{
		ID: domain.NewID(), ProjectID: pid, ServiceID: svc.ID, Prompt: "boom",
		Status: domain.StatusFailed, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, failed); err != nil {
		t.Fatal(err)
	}
	r3 := do(t, "POST", ts.URL+"/api/v1/runs/"+failed.ID+"/retry", consoleToken, nil)
	if r3.StatusCode != http.StatusForbidden {
		t.Fatalf("retry after tighten: status=%d want 403", r3.StatusCode)
	}
	r3.Body.Close()

	// Review of a succeeded PR run → 403 too.
	succeeded := &domain.Run{
		ID: domain.NewID(), ProjectID: pid, ServiceID: svc.ID, Prompt: "did it",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent, Attempt: 1,
		GitBranch: "jcode/run-x", PRURL: "http://g/acme/widget/pulls/1", PRNumber: 1,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, succeeded); err != nil {
		t.Fatal(err)
	}
	r4 := do(t, "POST", ts.URL+"/api/v1/runs/"+succeeded.ID+"/review", consoleToken, nil)
	if r4.StatusCode != http.StatusForbidden {
		t.Fatalf("review after tighten: status=%d want 403", r4.StatusCode)
	}
	r4.Body.Close()

	// Widening back re-enables dispatch (level-based, no sticky state).
	cfg.AllowedGitHosts = nil
	r5 := do(t, "POST", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken, map[string]any{"prompt": "ok again"})
	if r5.StatusCode != http.StatusCreated {
		t.Fatalf("dispatch after widen: status=%d want 201", r5.StatusCode)
	}
	r5.Body.Close()
}

// TestSourceFailsVisiblyOnIntegrationCredential (F5 review C2): the runner's
// source-bundle fetch for an integration-bound service whose bot token cannot be
// decrypted must FAIL with the real reason — a typed 409 (never an anonymous
// clone), and the run's failure classification stamped with the credential cause.
func TestSourceFailsVisiblyOnIntegrationCredential(t *testing.T) {
	gitea := fakeGitea(t, map[string]string{"good-pat": "bot"}, []map[string]any{
		{"id": 42, "full_name": "acme/widget", "default_branch": "main"},
	})
	ts, st, _ := newCipherServer(t, nil, gitea.URL)
	pid := newProject(t, ts, "src-cred")
	_, iv := createIntegration(t, ts, consoleToken, pid, map[string]any{
		"provider": "gitea", "host": gitea.URL, "token": "good-pat",
	})
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "widget", "owner_name": "acme/widget", "integration_id": iv.ID,
		"provider_repo_id": 42, "git_mode": "draft_pr",
	})
	var svc domain.Service
	decode(t, resp, &svc)

	// Corrupt the sealed token so decryption fails (an operator error, e.g. a
	// rotated AUTH_TOKEN_KEY).
	ctx := context.Background()
	integ, err := st.GetIntegration(ctx, iv.ID)
	if err != nil {
		t.Fatal(err)
	}
	integ.TokenEnc = []byte("garbage-not-a-ciphertext")
	if err := st.UpdateIntegration(ctx, integ); err != nil {
		t.Fatal(err)
	}

	// A run with its RUN_TOKEN, as the runner would hold it.
	r1 := do(t, "POST", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken, map[string]any{"prompt": "go"})
	var run domain.Run
	decode(t, r1, &run)
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}

	sresp := do(t, "GET", ts.URL+"/internal/v1/runs/"+run.ID+"/source", tok, nil)
	if sresp.StatusCode != http.StatusConflict {
		b, _ := io.ReadAll(sresp.Body)
		t.Fatalf("source with broken integration credential: status=%d want 409; body=%s", sresp.StatusCode, b)
	}
	var e errorBody
	decode(t, sresp, &e)
	if e.Error.Code != "integration_credential_unavailable" {
		t.Fatalf("code=%q want integration_credential_unavailable", e.Error.Code)
	}

	// The run's failure classification carries the credential cause (clone stage).
	got, _ := st.GetRun(ctx, run.ID)
	if got.FailureReason != domain.FailureCloneFailed {
		t.Fatalf("failure_reason=%q want clone_failed", got.FailureReason)
	}
	if !strings.Contains(got.FailureMessage, "integration") {
		t.Fatalf("failure_message=%q want the integration credential cause", got.FailureMessage)
	}
}

// TestSystemSnapshotAllowedGitHosts: the cluster allowlist is surfaced read-only
// in the admin snapshot (Cluster page card).
func TestSystemSnapshotAllowedGitHosts(t *testing.T) {
	ts, _, _ := newCipherServer(t, []string{"github.com", "gitea.example.com"}, "")
	resp := do(t, "GET", ts.URL+"/api/v1/system", consoleToken, nil)
	var got systemResponse
	decode(t, resp, &got)
	if len(got.Provider.AllowedGitHosts) != 2 {
		t.Fatalf("allowed_git_hosts=%v want 2 entries", got.Provider.AllowedGitHosts)
	}
}
