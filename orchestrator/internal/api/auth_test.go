package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func validTokenKey(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// providerStub serves an OAuth2 token + user endpoint (gitea shape). The token
// endpoint echoes access_token="at-"+code; the user endpoint returns the JSON
// registered under that access token, so a test drives a specific identity by
// choosing the callback `code`.
type providerStub struct {
	mu      sync.Mutex
	users   map[string]map[string]any // keyed by access token
	baseURL string
}

func newProviderStub() *providerStub { return &providerStub{users: map[string]map[string]any{}} }

// setUser registers the profile returned for callback `code`.
func (p *providerStub) setUser(code string, user map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users["at-"+code] = user
}

func (p *providerStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.PostForm.Get("code")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + code, "token_type": "bearer"})
	})
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		at := ""
		if h := r.Header.Get("Authorization"); len(h) > 7 {
			at = h[7:]
		}
		p.mu.Lock()
		u, ok := p.users[at]
		p.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(u)
	})
	return mux
}

// newAuthServer builds an API server wired to a gitea OAuth provider stub.
func newAuthServer(t *testing.T) (*httptest.Server, *store.MemStore, *providerStub) {
	t.Helper()
	stub := newProviderStub()
	psrv := httptest.NewServer(stub.handler())
	stub.baseURL = psrv.URL
	t.Cleanup(psrv.Close)

	st := store.NewMemStore()
	cfg := &config.Config{
		ConsoleToken: consoleToken,
		ConsoleURL:   "http://console.test",
		AuthTokenKey: validTokenKey(t),
		SessionTTL:   24 * time.Hour,
		GiteaURL:     psrv.URL,
		OAuthProviders: []config.OAuthProviderConfig{{
			ID: "gitea", ClientID: "cid", ClientSecret: "sec",
			ExternalURL: psrv.URL, InternalURL: psrv.URL,
		}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, stub
}

// TestProjectIntegrationOAuthFlow covers the owner-managed alternative to a
// pasted bot token: the owner supplies an OAuth app client, authorizes it at the
// git host, and the callback stores the resulting access token as the project's
// unattended integration credential. The client secret and token never appear
// in the redirect or integration API view.
func TestProjectIntegrationOAuthFlow(t *testing.T) {
	ts, _, stub := newAuthServer(t)
	stub.setUser("owner", map[string]any{"id": 1, "login": "owner", "full_name": "Project owner"})
	stub.setUser("bot", map[string]any{"id": 42, "login": "jcode-bot", "full_name": "jcode bot"})

	login := doOAuthFlow(t, ts, "/auth/login/gitea", "owner", "")
	login.Body.Close()
	session := findCookie(login, sessionCookieName)
	if session == nil {
		t.Fatal("login did not set a session cookie")
	}

	created := do(t, "POST", ts.URL+"/api/v1/projects", session.Value, map[string]any{"name": "oauth-project"})
	var project projectView
	decode(t, created, &project)

	form := url.Values{
		"project_id":    {project.ID},
		"name":          {"automation-bot"},
		"host":          {stub.baseURL},
		"client_id":     {"project-client"},
		"client_secret": {"project-secret"},
		"return_to":     {"/projects/" + project.ID + "?view=project-settings"},
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/integrations/gitea", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	start, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	if start.StatusCode != http.StatusFound {
		t.Fatalf("start status=%d want 302", start.StatusCode)
	}
	state := redirectQuery(t, start).Get("state")
	if state == "" || strings.Contains(start.Header.Get("Location"), "project-secret") {
		t.Fatalf("unsafe authorize redirect: %s", start.Header.Get("Location"))
	}
	stateCookie := findCookie(start, stateCookieName)
	pendingCookie := findCookie(start, integrationOAuthCookieName)
	if stateCookie == nil || pendingCookie == nil || strings.Contains(pendingCookie.Value, "project-secret") {
		t.Fatal("OAuth start did not set opaque state and integration cookies")
	}

	callback, _ := http.NewRequest(http.MethodGet, ts.URL+"/auth/callback/gitea?code=bot&state="+url.QueryEscape(state), nil)
	callback.AddCookie(stateCookie)
	callback.AddCookie(pendingCookie)
	cb, err := noRedirectClient().Do(callback)
	if err != nil {
		t.Fatal(err)
	}
	cb.Body.Close()
	if cb.StatusCode != http.StatusFound || redirectQuery(t, cb).Get("integration_connected") != "gitea" {
		t.Fatalf("callback status=%d location=%s", cb.StatusCode, cb.Header.Get("Location"))
	}

	listed := do(t, "GET", ts.URL+"/api/v1/projects/"+project.ID+"/integrations", session.Value, nil)
	var envelope struct {
		Integrations []integrationView `json:"integrations"`
	}
	decode(t, listed, &envelope)
	if len(envelope.Integrations) != 1 || envelope.Integrations[0].CredType != "oauth" || envelope.Integrations[0].BotUsername != "jcode-bot" {
		t.Fatalf("integrations=%+v", envelope.Integrations)
	}
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func redirectQuery(t *testing.T, resp *http.Response) url.Values {
	t.Helper()
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location %q: %v", resp.Header.Get("Location"), err)
	}
	return u.Query()
}

// doOAuthFlow runs login (or link) + callback for `code` and returns the callback
// response. For a link flow, pass the linking user's session token as bearer.
func doOAuthFlow(t *testing.T, ts *httptest.Server, path, code, bearer string) *http.Response {
	t.Helper()
	client := noRedirectClient()

	// 1. Start: GET /auth/login|link/gitea.
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	start, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	if start.StatusCode != http.StatusFound {
		t.Fatalf("start %s: status=%d want 302", path, start.StatusCode)
	}
	state := redirectQuery(t, start).Get("state")
	if state == "" {
		t.Fatal("authorize redirect carried no state")
	}
	stateCookie := findCookie(start, stateCookieName)
	if stateCookie == nil {
		t.Fatal("no state cookie set on login start")
	}

	// 2. Callback: GET /auth/callback/gitea?code=..&state=..
	cbReq, _ := http.NewRequest("GET", ts.URL+"/auth/callback/gitea?code="+code+"&state="+url.QueryEscape(state), nil)
	cbReq.AddCookie(stateCookie)
	cb, err := client.Do(cbReq)
	if err != nil {
		t.Fatal(err)
	}
	return cb
}

func TestAuthProvidersListed(t *testing.T) {
	ts, _, _ := newAuthServer(t)
	resp := do(t, "GET", ts.URL+"/auth/providers", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("providers: status=%d", resp.StatusCode)
	}
	var body struct {
		Providers []authProviderInfo `json:"providers"`
	}
	decode(t, resp, &body)
	if len(body.Providers) != 1 || body.Providers[0].ID != "gitea" || body.Providers[0].LoginURL != "/auth/login/gitea" {
		t.Fatalf("providers = %+v", body.Providers)
	}
}

func TestAuthProvidersEmptyWhenUnconfigured(t *testing.T) {
	// The default newTestServer has no OAuth providers.
	ts, _, _ := newTestServer(t)
	resp := do(t, "GET", ts.URL+"/auth/providers", "", nil)
	var body struct {
		Providers []authProviderInfo `json:"providers"`
	}
	decode(t, resp, &body)
	if len(body.Providers) != 0 {
		t.Fatalf("expected no providers, got %+v", body.Providers)
	}
	// And CONSOLE_TOKEN still works everywhere (backward compatible).
	r2 := do(t, "GET", ts.URL+"/api/v1/projects", consoleToken, nil)
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("console token path: status=%d want 200", r2.StatusCode)
	}
	r2.Body.Close()
}

// TestOAuthFirstUserBecomesAdmin covers the full callback flow: first user =>
// cluster admin + ?welcome=first-admin + session cookie; second user => normal
// user + ?welcome=new.
func TestOAuthFirstUserBecomesAdmin(t *testing.T) {
	ts, st, stub := newAuthServer(t)
	ctx := context.Background()

	// First user.
	stub.setUser("alice", map[string]any{"id": 1, "login": "alice", "full_name": "Alice"})
	cb := doOAuthFlow(t, ts, "/auth/login/gitea", "alice", "")
	cb.Body.Close()
	if cb.StatusCode != http.StatusFound {
		t.Fatalf("callback: status=%d want 302", cb.StatusCode)
	}
	if got := redirectQuery(t, cb).Get("welcome"); got != "first-admin" {
		t.Fatalf("welcome=%q want first-admin", got)
	}
	sess := findCookie(cb, sessionCookieName)
	if sess == nil || sess.Value == "" {
		t.Fatal("no session cookie set on first login")
	}
	// The user exists and is cluster admin.
	id, err := st.GetIdentity(ctx, domain.ProviderGitea, "1")
	if err != nil {
		t.Fatalf("identity not stored: %v", err)
	}
	u, _ := st.GetUser(ctx, id.UserID)
	if !u.IsClusterAdmin || u.DisplayName != "Alice" {
		t.Fatalf("first user = %+v want cluster admin 'Alice'", u)
	}
	// The stored access token is encrypted (not the plaintext).
	if string(id.AccessTokenEnc) == "at-alice" || len(id.AccessTokenEnc) == 0 {
		t.Fatal("identity access token must be stored encrypted")
	}

	// Second user is NOT admin, welcome=new.
	stub.setUser("bob", map[string]any{"id": 2, "login": "bob", "full_name": "Bob"})
	cb2 := doOAuthFlow(t, ts, "/auth/login/gitea", "bob", "")
	cb2.Body.Close()
	if got := redirectQuery(t, cb2).Get("welcome"); got != "new" {
		t.Fatalf("second user welcome=%q want new", got)
	}
	id2, _ := st.GetIdentity(ctx, domain.ProviderGitea, "2")
	u2, _ := st.GetUser(ctx, id2.UserID)
	if u2.IsClusterAdmin {
		t.Fatal("second user must NOT be cluster admin")
	}

	// Returning login (alice again) carries no welcome param.
	cb3 := doOAuthFlow(t, ts, "/auth/login/gitea", "alice", "")
	cb3.Body.Close()
	if got := redirectQuery(t, cb3).Get("welcome"); got != "" {
		t.Fatalf("returning login welcome=%q want empty", got)
	}
	if n, _ := st.CountUsers(ctx); n != 2 {
		t.Fatalf("user count=%d want 2 (no duplicate on re-login)", n)
	}
}

// TestOAuthLinkAndConflict covers /auth/link: success => ?linked=gitea, and an
// identity already owned by another user => ?link_error=taken.
func TestOAuthLinkAndConflict(t *testing.T) {
	ts, st, stub := newAuthServer(t)
	ctx := context.Background()

	// Alice logs in and captures her session token (for the authed link start).
	stub.setUser("alice", map[string]any{"id": 1, "login": "alice"})
	cb := doOAuthFlow(t, ts, "/auth/login/gitea", "alice", "")
	cb.Body.Close()
	aliceSession := findCookie(cb, sessionCookieName).Value

	// Bob logs in (owns identity uid=2).
	stub.setUser("bob", map[string]any{"id": 2, "login": "bob"})
	doOAuthFlow(t, ts, "/auth/login/gitea", "bob", "").Body.Close()

	aliceID, _ := st.GetIdentity(ctx, domain.ProviderGitea, "1")

	// Alice links a brand-new identity (uid=3) => linked=gitea.
	stub.setUser("alice-extra", map[string]any{"id": 3, "login": "alice2"})
	link := doOAuthFlow(t, ts, "/auth/link/gitea", "alice-extra", aliceSession)
	link.Body.Close()
	if got := redirectQuery(t, link).Get("linked"); got != "gitea" {
		t.Fatalf("link success param=%q want gitea", got)
	}
	ids, _ := st.ListIdentities(ctx, aliceID.UserID)
	if len(ids) != 2 {
		t.Fatalf("alice should have 2 identities after link, got %d", len(ids))
	}

	// Alice tries to link bob's identity (uid=2) => link_error=taken.
	stub.setUser("take-bob", map[string]any{"id": 2, "login": "bob"})
	conflict := doOAuthFlow(t, ts, "/auth/link/gitea", "take-bob", aliceSession)
	conflict.Body.Close()
	if got := redirectQuery(t, conflict).Get("link_error"); got != "taken" {
		t.Fatalf("link conflict param=%q want taken", got)
	}
}

// A webhook setup CTA may send a linked user through OAuth again to grant the
// repository-hook scope. The callback must refresh that same identity and land
// back on the exact in-app Automation location, never an arbitrary URL.
func TestOAuthLinkReauthorizationReturnsToSafeProjectLocation(t *testing.T) {
	ts, _, stub := newAuthServer(t)

	stub.setUser("alice", map[string]any{"id": 1, "login": "alice"})
	login := doOAuthFlow(t, ts, "/auth/login/gitea", "alice", "")
	login.Body.Close()
	session := findCookie(login, sessionCookieName)
	if session == nil {
		t.Fatal("missing session cookie")
	}

	returnTo := "/projects/project-1?service=service-1&tab=automations&webhook=oauth"
	stub.setUser("alice-refresh", map[string]any{"id": 1, "login": "alice"})
	linked := doOAuthFlow(t, ts, "/auth/link/gitea?return_to="+url.QueryEscape(returnTo), "alice-refresh", session.Value)
	linked.Body.Close()
	location, err := url.Parse(linked.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Host != "console.test" || location.Path != "/projects/project-1" {
		t.Fatalf("redirect=%q want console project route", linked.Header.Get("Location"))
	}
	if q := location.Query(); q.Get("service") != "service-1" || q.Get("tab") != "automations" || q.Get("webhook") != "oauth" || q.Get("linked") != "gitea" {
		t.Fatalf("redirect query=%v", q)
	}

	unsafe := doOAuthFlow(t, ts, "/auth/link/gitea?return_to="+url.QueryEscape("https://evil.example/steal"), "alice-refresh", session.Value)
	unsafe.Body.Close()
	unsafeLocation, err := url.Parse(unsafe.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if unsafeLocation.Host != "console.test" || unsafeLocation.Path != "" {
		t.Fatalf("unsafe return_to escaped console origin: %q", unsafe.Header.Get("Location"))
	}
}

// TestCallbackRejectsBadState proves CSRF protection: a callback with a
// mismatched/absent state cookie is a 400.
func TestCallbackRejectsBadState(t *testing.T) {
	ts, _, stub := newAuthServer(t)
	stub.setUser("x", map[string]any{"id": 1, "login": "x"})
	client := noRedirectClient()

	// Start to get a valid signed state, but DON'T send the matching cookie.
	start, err := client.Get(ts.URL + "/auth/login/gitea")
	if err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	state := redirectQuery(t, start).Get("state")

	cb, err := client.Get(ts.URL + "/auth/callback/gitea?code=x&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Body.Close()
	if cb.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback without state cookie: status=%d want 400", cb.StatusCode)
	}
}

// --- middleware three-state + session lifecycle ------------------------------

// mkUser creates a user (+ minimal identity) in the store. The first user is
// cluster admin (store semantics), so callers create the admin first if needed.
func mkUser(t *testing.T, st *store.MemStore, name string) *domain.User {
	t.Helper()
	u := &domain.User{ID: domain.NewID(), DisplayName: name, CreatedAt: time.Now().UTC()}
	id := &domain.UserIdentity{
		ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: name + "-uid",
		Username: name, AccessTokenEnc: []byte("enc"), CreatedAt: time.Now().UTC(),
	}
	if _, err := st.CreateUserWithIdentity(context.Background(), u, id); err != nil {
		t.Fatal(err)
	}
	return u
}

// mkSession creates a valid session for userID and returns the plaintext token.
func mkSession(t *testing.T, st *store.MemStore, userID string) string {
	t.Helper()
	tok, _ := auth.GenerateRunToken()
	now := time.Now().UTC()
	if err := st.CreateSession(context.Background(), &domain.Session{
		ID: domain.NewID(), UserID: userID, TokenHash: auth.HashToken(tok),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestMiddlewareThreeStates(t *testing.T) {
	ts, st, _ := newTestServer(t)
	admin := mkUser(t, st, "admin") // first user => cluster admin
	tok := mkSession(t, st, admin.ID)

	// 1. Bearer session token.
	resp := do(t, "GET", ts.URL+"/api/v1/me", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer session: status=%d want 200", resp.StatusCode)
	}
	var me meResponse
	decode(t, resp, &me)
	if me.User.ID != admin.ID || me.IsService {
		t.Fatalf("bearer session me = %+v", me)
	}

	// 2. Session cookie.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})
	cresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cresp.StatusCode != http.StatusOK {
		t.Fatalf("cookie session: status=%d want 200", cresp.StatusCode)
	}
	cresp.Body.Close()

	// 3. CONSOLE_TOKEN => service principal.
	sresp := do(t, "GET", ts.URL+"/api/v1/me", consoleToken, nil)
	var sme meResponse
	decode(t, sresp, &sme)
	if !sme.IsService || !sme.User.IsClusterAdmin || sme.User.DisplayName != "console token" {
		t.Fatalf("service me = %+v want is_service cluster admin 'console token'", sme)
	}
	if sme.Identities == nil {
		t.Fatal("service identities must be [] (non-nil)")
	}

	// 4. Unauthenticated => 401.
	uresp := do(t, "GET", ts.URL+"/api/v1/me", "", nil)
	if uresp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth: status=%d want 401", uresp.StatusCode)
	}
	uresp.Body.Close()
}

func TestSessionExpiryAndRevocation(t *testing.T) {
	ts, st, _ := newTestServer(t)
	u := mkUser(t, st, "user")
	ctx := context.Background()

	// Expired session => 401.
	expTok, _ := auth.GenerateRunToken()
	past := time.Now().Add(-2 * time.Hour)
	_ = st.CreateSession(ctx, &domain.Session{
		ID: domain.NewID(), UserID: u.ID, TokenHash: auth.HashToken(expTok),
		CreatedAt: past, ExpiresAt: past.Add(time.Hour), // expired 1h ago
	})
	if r := do(t, "GET", ts.URL+"/api/v1/me", expTok, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session: status=%d want 401", r.StatusCode)
	}

	// Valid session works, then logout revokes it.
	tok := mkSession(t, st, u.ID)
	if r := do(t, "GET", ts.URL+"/api/v1/me", tok, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("valid session: status=%d want 200", r.StatusCode)
	}
	if r := do(t, "POST", ts.URL+"/auth/logout", tok, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("logout: status=%d want 200", r.StatusCode)
	}
	if r := do(t, "GET", ts.URL+"/api/v1/me", tok, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout session: status=%d want 401 (revoked)", r.StatusCode)
	}
}
