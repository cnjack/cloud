package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtypeoauth"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// mustJSON unmarshals a raw body captured for a secret-leak scan, failing the
// test on a decode error (with the body for context).
func mustJSON(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, raw)
	}
}

// fakeOAuthClient is an in-process oauthClient stand-in with a poll spy. mode
// switches the token poll's outcome so a test can drive pending → complete (and
// the terminal variants); polls counts jtype token hits so the interval gate can
// be asserted. startErr/unsupported drive the start-endpoint edges. onPoll, when
// set, runs DURING the token poll (outside the fake's own lock) — simulating a
// concurrent write landing inside the network window (the TOCTOU test).
type fakeOAuthClient struct {
	mu         sync.Mutex
	mode       jtypeoauth.Status
	token      *jtypeoauth.Token
	polls      int
	startErr   error // returned by StartDeviceAuthorization when non-nil
	pollErr    error // returned by PollToken when non-nil (transient/unsupported)
	deviceCode string
	onPoll     func() // invoked mid-poll (in-flight network window)
}

func (f *fakeOAuthClient) StartDeviceAuthorization(context.Context) (*jtypeoauth.DeviceAuth, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	dc := f.deviceCode
	if dc == "" {
		dc = "dev-secret"
	}
	return &jtypeoauth.DeviceAuth{
		DeviceCode:              dc,
		UserCode:                "482913",
		VerificationURI:         "http://jtype.test/oauth/device",
		VerificationURIComplete: "http://jtype.test/oauth/device?code=482913",
		ExpiresIn:               600,
		Interval:                2,
	}, nil
}

func (f *fakeOAuthClient) PollToken(_ context.Context, deviceCode string) (*jtypeoauth.Token, jtypeoauth.Status, error) {
	_ = deviceCode
	f.mu.Lock()
	f.polls++
	mode, perr, tok, hook := f.mode, f.pollErr, f.token, f.onPoll
	f.mu.Unlock()
	if hook != nil {
		hook() // the "network window": a concurrent write can land here
	}
	if perr != nil {
		return nil, 0, perr
	}
	if mode == jtypeoauth.StatusComplete {
		if tok == nil {
			tok = &jtypeoauth.Token{AccessToken: "minted-mcp-token", ExpiresIn: 7776000}
		}
		return tok, jtypeoauth.StatusComplete, nil
	}
	return nil, mode, nil
}

func (f *fakeOAuthClient) setMode(m jtypeoauth.Status) {
	f.mu.Lock()
	f.mode = m
	f.mu.Unlock()
}

func (f *fakeOAuthClient) setOnPoll(fn func()) {
	f.mu.Lock()
	f.onPoll = fn
	f.mu.Unlock()
}

func (f *fakeOAuthClient) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}

// testClock is an injectable, frozen-until-advanced clock so the interval gate +
// token-expiry stamping are deterministic (no real-time sleeps).
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// connectFixture is a server with a cipher, a cluster admin, a plain member, a
// scoped API key, and a project owned by the admin — plus the injected fake
// oauth client seam. It drives both the cluster and per-link connect surfaces.
type connectFixture struct {
	ts        *httptest.Server
	srv       *Server
	st        *store.MemStore
	fake      *fakeOAuthClient
	clock     *testClock
	adminTok  string
	memberTok string
	apiKey    string
	projectID string
	serviceID string
}

func setupConnect(t *testing.T, withCipher bool) connectFixture {
	t.Helper()
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, JtypePollInterval: 15 * time.Second})
	if withCipher {
		cfg.AuthTokenKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	fake := &fakeOAuthClient{mode: jtypeoauth.StatusPending}
	srv.oauthClientFor = func(string) oauthClient { return fake }
	clock := &testClock{t: time.Now().UTC()}
	srv.connects.now = clock.now
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	admin := mkUser(t, st, "cn-admin") // first user => cluster admin
	member := mkUser(t, st, "cn-member")
	adminTok := mkSession(t, st, admin.ID)
	memberTok := mkSession(t, st, member.ID)

	// A project owned by the admin + a default service + a scoped API key.
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects", adminTok, map[string]any{"name": "cn"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	sresp := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+pv.ID+"/services", adminTok,
		map[string]any{"name": "default", "repo_url": "https://git/x.git"})
	var svc domain.Service
	decode(t, sresp, &svc)
	kr := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+pv.ID+"/apikeys", adminTok, map[string]any{"name": "ci"})
	var key createAPIKeyResponse
	decode(t, kr, &key)

	return connectFixture{
		ts: ts, srv: srv, st: st, fake: fake, clock: clock,
		adminTok: adminTok, memberTok: memberTok, apiKey: key.Key,
		projectID: pv.ID, serviceID: svc.ID,
	}
}

// seedLink inserts a blank (tokenless) kanban link directly, bypassing the
// create-time board-column validation (exercised elsewhere) — the connect tests
// care about the CONNECT flow, not link creation.
func (f connectFixture) seedLink(t *testing.T) *domain.KanbanLink {
	t.Helper()
	l := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b", ProjectID: f.projectID,
		ServiceID: f.serviceID, TriggerColumn: "ai", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := f.st.CreateKanbanLink(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	return l
}

func (f connectFixture) clusterConnectURL() string { return f.ts.URL + "/api/v1/system/kanban/connect" }

// setClusterBaseURL PUTs a DB base_url override (admin) so the cluster connect
// precondition (base_url_not_configured) is satisfied.
func (f connectFixture) setClusterBaseURL(t *testing.T, base string) {
	t.Helper()
	r := do(t, http.MethodPut, f.ts.URL+"/api/v1/system/kanban", f.adminTok, map[string]any{"base_url": base})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("set base_url: %d", r.StatusCode)
	}
	r.Body.Close()
}

// Test 1: cluster start — no DB base_url → 409 base_url_not_configured; with a
// base_url → 200 carrying user_code + verification_uri_complete, device_code
// WITHHELD from the response.
func TestClusterConnectStart(t *testing.T) {
	f := setupConnect(t, true)

	// No base_url yet.
	resp := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("no base_url: status=%d want 409", resp.StatusCode)
	}
	var eb errorBody
	decode(t, resp, &eb)
	if eb.Error.Code != "base_url_not_configured" {
		t.Fatalf("code=%q want base_url_not_configured", eb.Error.Code)
	}

	// With a base_url: 200 start view, no device_code leak.
	f.setClusterBaseURL(t, "http://jtype.db")
	resp = do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start status=%d want 200 body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "dev-secret") {
		t.Fatalf("SECRET LEAK: start response contains the device_code: %s", raw)
	}
	var sv kanbanConnectStartView
	mustJSON(t, raw, &sv)
	if sv.ConnectID == "" || sv.UserCode != "482913" ||
		sv.VerificationURIComplete != "http://jtype.test/oauth/device?code=482913" {
		t.Fatalf("start view = %+v", sv)
	}
	if sv.ExpiresIn != 600 || sv.Interval != 2 {
		t.Fatalf("expires_in=%d interval=%d", sv.ExpiresIn, sv.Interval)
	}
}

// Test 2: cluster poll pending → complete seals the token into
// cluster_kanban_config (roundtrip), stamps token_expires_at ≈ now+90d,
// invalidates the resolver (following /system/kanban shows token_set:true), and
// the plaintext token NEVER appears in any response body.
func TestClusterConnectPollToComplete(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	decode(t, start, &sv)
	pollURL := f.clusterConnectURL() + "/" + sv.ConnectID

	// Pending first.
	pr := do(t, http.MethodGet, pollURL, f.adminTok, nil)
	var st1 kanbanConnectStatusView
	decode(t, pr, &st1)
	if st1.Status != "pending" || st1.TokenSet {
		t.Fatalf("first poll = %+v want pending", st1)
	}

	// Advance past the interval gate so the next poll actually hits jtype, approve,
	// then poll → complete (raw-scan the body for the secret).
	f.clock.advance(3 * time.Second)
	f.fake.setMode(jtypeoauth.StatusComplete)
	before := f.clock.now()
	pr = do(t, http.MethodGet, pollURL, f.adminTok, nil)
	raw, _ := io.ReadAll(pr.Body)
	pr.Body.Close()
	if strings.Contains(string(raw), "minted-mcp-token") {
		t.Fatalf("SECRET LEAK: poll response contains the plaintext token: %s", raw)
	}
	var st2 kanbanConnectStatusView
	mustJSON(t, raw, &st2)
	if st2.Status != "complete" || !st2.TokenSet || st2.TokenExpiresAt == "" {
		t.Fatalf("complete poll = %+v", st2)
	}
	exp, err := time.Parse(time.RFC3339, st2.TokenExpiresAt)
	if err != nil {
		t.Fatalf("parse token_expires_at: %v", err)
	}
	if want := before.Add(90 * 24 * time.Hour); exp.Before(want.Add(-time.Minute)) || exp.After(want.Add(time.Minute)) {
		t.Fatalf("token_expires_at=%v not ≈ now+90d (%v)", exp, want)
	}

	// The token is sealed in the store, decrypts to the minted value, never plaintext.
	row, err := f.st.GetClusterKanbanConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(row.TokenEnc) == 0 || string(row.TokenEnc) == "minted-mcp-token" {
		t.Fatalf("token not sealed: %q", row.TokenEnc)
	}
	if got, _ := f.srv.Cipher().DecryptString(row.TokenEnc); got != "minted-mcp-token" {
		t.Fatalf("sealed token decrypts to %q want minted-mcp-token", got)
	}
	if row.TokenExpiresAt == nil {
		t.Fatal("token_expires_at not stored on the row")
	}

	// Resolver invalidated: /system/kanban now shows the cluster token set + expiry,
	// never the plaintext.
	sr := do(t, http.MethodGet, f.ts.URL+"/api/v1/system/kanban", f.adminTok, nil)
	sraw, _ := io.ReadAll(sr.Body)
	sr.Body.Close()
	if strings.Contains(string(sraw), "minted-mcp-token") {
		t.Fatalf("SECRET LEAK: /system/kanban contains the plaintext token: %s", sraw)
	}
	var cv kanbanConfigView
	mustJSON(t, sraw, &cv)
	if !cv.ClusterTokenSet || !cv.TokenSet || cv.TokenExpiresAt == "" {
		t.Fatalf("/system/kanban after complete = %+v", cv)
	}

	// A later poll is idempotent: still complete (single-use; no re-mint).
	pr = do(t, http.MethodGet, pollURL, f.adminTok, nil)
	var st3 kanbanConnectStatusView
	decode(t, pr, &st3)
	if st3.Status != "complete" || !st3.TokenSet {
		t.Fatalf("repeat poll = %+v want complete", st3)
	}
}

// Test 3: an unsupported jtype (start returns ErrOAuthUnsupported) → 409
// jtype_oauth_unsupported; AUTH_TOKEN_KEY unset → cipher_not_configured AT START
// (no jtype call at all).
func TestClusterConnectUnsupportedAndNoCipher(t *testing.T) {
	// Unsupported.
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	f.fake.startErr = jtypeoauth.ErrOAuthUnsupported
	resp := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	var eb errorBody
	decode(t, resp, &eb)
	if resp.StatusCode != http.StatusConflict || eb.Error.Code != "jtype_oauth_unsupported" {
		t.Fatalf("unsupported: status=%d code=%q", resp.StatusCode, eb.Error.Code)
	}

	// No cipher: 409 cipher_not_configured, and NO jtype call was made.
	f2 := setupConnect(t, false)
	f2.setClusterBaseURL(t, "http://jtype.db")
	resp = do(t, http.MethodPost, f2.clusterConnectURL(), f2.adminTok, nil)
	var eb2 errorBody
	decode(t, resp, &eb2)
	if resp.StatusCode != http.StatusConflict || eb2.Error.Code != "cipher_not_configured" {
		t.Fatalf("no cipher: status=%d code=%q want 409 cipher_not_configured", resp.StatusCode, eb2.Error.Code)
	}
	if f2.fake.pollCount() != 0 {
		t.Fatal("cipher check must precede any jtype call")
	}
}

// Test 4: per-link — create a link with a blank token, connect it → the minted
// token seals into kanban_links.token_enc and credential_status flips to
// per_link; another project's link path → 404.
func TestLinkConnectSealsPerLinkToken(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db") // enables the integration (eff.Enabled)

	// A blank (tokenless) link (create-then-connect: create is validated elsewhere).
	linkURL := f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/links"
	link := f.seedLink(t)

	connectBase := linkURL + "/" + link.ID + "/connect"
	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, connectBase, f.adminTok, nil)
	if start.StatusCode != http.StatusOK {
		t.Fatalf("link start: %d", start.StatusCode)
	}
	decode(t, start, &sv)

	f.fake.setMode(jtypeoauth.StatusComplete)
	pr := do(t, http.MethodGet, connectBase+"/"+sv.ConnectID, f.adminTok, nil)
	praw, _ := io.ReadAll(pr.Body)
	pr.Body.Close()
	// Raw-body no-leak scan on the PER-LINK poll (mirrors the cluster test).
	if strings.Contains(string(praw), "minted-mcp-token") || strings.Contains(string(praw), "dev-secret") {
		t.Fatalf("SECRET LEAK: link poll response contains a secret: %s", praw)
	}
	var st kanbanConnectStatusView
	mustJSON(t, praw, &st)
	if st.Status != "complete" || !st.TokenSet || st.TokenExpiresAt == "" {
		t.Fatalf("link poll complete = %+v", st)
	}

	// The link now carries a sealed per-link token + expiry; credential_status per_link.
	stored, err := f.st.GetKanbanLink(context.Background(), link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.TokenSet() || stored.TokenExpiresAt == nil {
		t.Fatalf("per-link token/expiry not stored: %+v", stored)
	}
	if got, _ := f.srv.Cipher().DecryptString(stored.TokenEnc); got != "minted-mcp-token" {
		t.Fatalf("per-link token decrypts to %q", got)
	}
	lr := do(t, http.MethodGet, linkURL, f.adminTok, nil)
	lraw, _ := io.ReadAll(lr.Body)
	lr.Body.Close()
	// Raw-body no-leak scan on the project links LIST as well.
	if strings.Contains(string(lraw), "minted-mcp-token") || strings.Contains(string(lraw), "dev-secret") {
		t.Fatalf("SECRET LEAK: links list contains a secret: %s", lraw)
	}
	var list struct {
		Links []kanbanLinkView `json:"links"`
	}
	mustJSON(t, lraw, &list)
	if len(list.Links) != 1 || list.Links[0].CredentialStatus != "per_link" || list.Links[0].TokenExpiresAt == "" {
		t.Fatalf("link view after connect = %+v", list.Links)
	}

	// A DIFFERENT project's link path → 404 (the link is not in that project).
	p2 := do(t, http.MethodPost, f.ts.URL+"/api/v1/projects", f.adminTok, map[string]any{"name": "p2"})
	var pv2 projectView
	decode(t, p2, &pv2)
	other := do(t, http.MethodPost,
		f.ts.URL+"/api/v1/projects/"+pv2.ID+"/kanban/links/"+link.ID+"/connect", f.adminTok, nil)
	if other.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project link connect want 404, got %d", other.StatusCode)
	}
	other.Body.Close()
}

// Test 4b: per-link connect requires the integration to be configured — with no
// cluster base URL it is 409 kanban_not_configured.
func TestLinkConnectRequiresKanbanConfigured(t *testing.T) {
	f := setupConnect(t, true)
	// A link cannot be created without the integration, so seed one directly.
	link := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b", ProjectID: f.projectID,
		ServiceID: f.serviceID, TriggerColumn: "ai", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := f.st.CreateKanbanLink(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	// No cluster base URL set → kanban_not_configured.
	resp := do(t, http.MethodPost,
		f.ts.URL+"/api/v1/projects/"+f.projectID+"/kanban/links/"+link.ID+"/connect", f.adminTok, nil)
	var eb errorBody
	decode(t, resp, &eb)
	if resp.StatusCode != http.StatusConflict || eb.Error.Code != "kanban_not_configured" {
		t.Fatalf("link connect w/o config: status=%d code=%q", resp.StatusCode, eb.Error.Code)
	}
}

// Test 5: the authz matrix — cluster connect: member 403, scoped key 403,
// unauth 401; link connect: non-owner (member) 403.
func TestConnectAuthzMatrix(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")

	// Cluster start: member 403, scoped key 403, unauth 401.
	for _, tc := range []struct {
		tok  string
		want int
	}{
		{f.memberTok, http.StatusForbidden},
		{f.apiKey, http.StatusForbidden},
		{"", http.StatusUnauthorized},
	} {
		r := do(t, http.MethodPost, f.clusterConnectURL(), tc.tok, nil)
		if r.StatusCode != tc.want {
			t.Errorf("cluster start tok=%q status=%d want %d", tc.tok, r.StatusCode, tc.want)
		}
		r.Body.Close()
	}

	// Link connect: a member (non-owner) is forbidden, and a scoped API key —
	// capped at RoleMember on its OWN project — never reaches the RoleOwner gate
	// on either start or poll.
	link := f.seedLink(t)
	linkConnect := f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/links/" + link.ID + "/connect"
	for _, tc := range []struct {
		name string
		tok  string
	}{
		{"member", f.memberTok},
		{"api-key", f.apiKey},
	} {
		if r := do(t, http.MethodPost, linkConnect, tc.tok, nil); r.StatusCode != http.StatusForbidden {
			t.Errorf("link start by %s want 403, got %d", tc.name, r.StatusCode)
			r.Body.Close()
		} else {
			r.Body.Close()
		}
		if r := do(t, http.MethodGet, linkConnect+"/deadbeef", tc.tok, nil); r.StatusCode != http.StatusForbidden {
			t.Errorf("link poll by %s want 403, got %d", tc.name, r.StatusCode)
			r.Body.Close()
		} else {
			r.Body.Close()
		}
	}
}

// Test 6a: an unknown connect_id → 404 connect_expired (also the restart-drops
// case), and a DIFFERENT principal (the service principal) cannot poll another
// subject's flow (leaked connect_id is unusable).
func TestConnectRegistryUnknownAndPrincipalMismatch(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")

	// Unknown id.
	r := do(t, http.MethodGet, f.clusterConnectURL()+"/deadbeef", f.adminTok, nil)
	var eb errorBody
	decode(t, r, &eb)
	if r.StatusCode != http.StatusNotFound || eb.Error.Code != "connect_expired" {
		t.Fatalf("unknown id: status=%d code=%q", r.StatusCode, eb.Error.Code)
	}

	// The admin (user) starts a flow; the service principal (also cluster-admin,
	// but a DIFFERENT identity) must not be able to poll it.
	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	decode(t, start, &sv)
	mism := do(t, http.MethodGet, f.clusterConnectURL()+"/"+sv.ConnectID, consoleToken, nil)
	var eb2 errorBody
	decode(t, mism, &eb2)
	if mism.StatusCode != http.StatusNotFound || eb2.Error.Code != "connect_expired" {
		t.Fatalf("principal mismatch: status=%d code=%q want 404 connect_expired", mism.StatusCode, eb2.Error.Code)
	}
	// The original principal still can (proves the record survived the mismatch poll).
	ok := do(t, http.MethodGet, f.clusterConnectURL()+"/"+sv.ConnectID, f.adminTok, nil)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("owner re-poll status=%d want 200", ok.StatusCode)
	}
	ok.Body.Close()
}

// Test 6b: the interval gate — two rapid polls hit jtype only ONCE (the second is
// served from the cached pending status), asserted via the poll spy.
func TestConnectIntervalGate(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	decode(t, start, &sv)
	pollURL := f.clusterConnectURL() + "/" + sv.ConnectID

	// Two back-to-back polls, well within the 2s interval jtype returned.
	do(t, http.MethodGet, pollURL, f.adminTok, nil).Body.Close()
	do(t, http.MethodGet, pollURL, f.adminTok, nil).Body.Close()
	if got := f.fake.pollCount(); got != 1 {
		t.Fatalf("interval gate: jtype polled %d times, want 1", got)
	}
}

// Test 6c: a base_url changed under an in-flight flow → the poll returns expired
// and NO token is stored, even if jtype would have approved.
func TestConnectBaseURLChangedMidFlow(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.a")

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	decode(t, start, &sv)

	// The admin edits the base URL mid-flow, then jtype "approves".
	f.setClusterBaseURL(t, "http://jtype.b")
	f.fake.setMode(jtypeoauth.StatusComplete)

	pr := do(t, http.MethodGet, f.clusterConnectURL()+"/"+sv.ConnectID, f.adminTok, nil)
	var st kanbanConnectStatusView
	decode(t, pr, &st)
	if st.Status != "expired" || st.TokenSet {
		t.Fatalf("mid-flow base change poll = %+v want expired/no-token", st)
	}
	// No token was stored despite the "approval".
	row, err := f.st.GetClusterKanbanConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row.TokenSet() {
		t.Fatal("a base_url change mid-flow must NOT store a token")
	}
	// jtype was never polled for the token (the mid-flow guard precedes the poll).
	if f.fake.pollCount() != 0 {
		t.Fatalf("expired-by-base-change must not poll jtype, got %d polls", f.fake.pollCount())
	}
}

// Test 6e (anti-TOCTOU): the base_url changes BETWEEN the guard read and
// completion — i.e. INSIDE the token poll's network window. The conditional
// store write (SetClusterKanbanToken) must refuse: status expired, the row keeps
// the NEW base_url, and no token is stored.
func TestConnectBaseURLChangedDuringCompletingPoll(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.a")

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	decode(t, start, &sv)

	// jtype "approves", and the admin's re-point lands INSIDE the poll (after the
	// guard read) — simulated by mutating the row from the fake's mid-poll hook.
	f.fake.setMode(jtypeoauth.StatusComplete)
	f.fake.setOnPoll(func() {
		if err := f.st.UpsertClusterKanbanConfig(context.Background(),
			&domain.KanbanConfig{BaseURL: "http://jtype.b", UpdatedBy: "admin"}); err != nil {
			t.Errorf("mid-poll upsert: %v", err)
		}
	})

	pr := do(t, http.MethodGet, f.clusterConnectURL()+"/"+sv.ConnectID, f.adminTok, nil)
	var st kanbanConnectStatusView
	decode(t, pr, &st)
	if st.Status != "expired" || st.TokenSet {
		t.Fatalf("in-poll base change = %+v want expired/no-token", st)
	}
	// The guard passed (jtype WAS polled) — the conditional write is what refused.
	if f.fake.pollCount() != 1 {
		t.Fatalf("guard should have passed (1 jtype poll), got %d", f.fake.pollCount())
	}
	row, err := f.st.GetClusterKanbanConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row.BaseURL != "http://jtype.b" || row.TokenSet() || row.TokenExpiresAt != nil {
		t.Fatalf("row after in-poll base change = %+v (want new base, no token)", row)
	}
}

// Test 6f: a slow_down poll backs the interval off (+5s) — the next poll inside
// the widened window is gated (no jtype hit); past it, the poll goes through.
func TestConnectSlowDownBackoff(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	decode(t, start, &sv)
	pollURL := f.clusterConnectURL() + "/" + sv.ConnectID

	// First poll hits jtype and gets slow_down → interval 2s+5s=7s, still pending.
	f.fake.setMode(jtypeoauth.StatusSlowDown)
	pr := do(t, http.MethodGet, pollURL, f.adminTok, nil)
	var st kanbanConnectStatusView
	decode(t, pr, &st)
	if st.Status != "pending" || f.fake.pollCount() != 1 {
		t.Fatalf("slow_down poll = %+v polls=%d (want pending, 1)", st, f.fake.pollCount())
	}

	// 3s later: past the ORIGINAL 2s interval but inside the backed-off 7s → gated.
	f.fake.setMode(jtypeoauth.StatusComplete)
	f.clock.advance(3 * time.Second)
	pr = do(t, http.MethodGet, pollURL, f.adminTok, nil)
	decode(t, pr, &st)
	if st.Status != "pending" || f.fake.pollCount() != 1 {
		t.Fatalf("inside backoff = %+v polls=%d (want gated pending, still 1)", st, f.fake.pollCount())
	}

	// 5s more (8s ≥ 7s): the gate opens and the flow completes.
	f.clock.advance(5 * time.Second)
	pr = do(t, http.MethodGet, pollURL, f.adminTok, nil)
	decode(t, pr, &st)
	if st.Status != "complete" || f.fake.pollCount() != 2 {
		t.Fatalf("past backoff = %+v polls=%d (want complete, 2)", st, f.fake.pollCount())
	}
}

// blockingOAuthClient hands out sequential device codes and BLOCKS the token
// poll of blockOn until release is closed — pinning the head-of-line regression:
// one flow stuck mid-poll (its rec.mu held across the network call) must not
// stall the registry (reg.mu) for other flows' start/poll. Run under -race.
type blockingOAuthClient struct {
	mu      sync.Mutex
	n       int
	blockOn string        // device code whose poll blocks
	entered chan struct{} // closed once the blocked poll is in flight
	release chan struct{} // close to unblock it
	once    sync.Once
}

func (b *blockingOAuthClient) StartDeviceAuthorization(context.Context) (*jtypeoauth.DeviceAuth, error) {
	b.mu.Lock()
	b.n++
	n := b.n
	b.mu.Unlock()
	return &jtypeoauth.DeviceAuth{
		DeviceCode: fmt.Sprintf("dev-%d", n), UserCode: "111111",
		VerificationURI: "http://jtype.test/oauth/device", VerificationURIComplete: "http://jtype.test/oauth/device?code=111111",
		ExpiresIn: 600, Interval: 2,
	}, nil
}

func (b *blockingOAuthClient) PollToken(_ context.Context, deviceCode string) (*jtypeoauth.Token, jtypeoauth.Status, error) {
	if deviceCode == b.blockOn {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return nil, jtypeoauth.StatusPending, nil
}

// Test 6g (head-of-line regression, -race): while flow A is stuck inside its
// jtype token poll (holding its record mutex across the "network" call), flow
// B's start + poll must proceed — the registry sweep reads the immutable
// expiresAt lock-free, so reg.mu is never blocked behind a slow poll.
func TestConnectSlowPollDoesNotStallRegistry(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	block := &blockingOAuthClient{
		blockOn: "dev-1", entered: make(chan struct{}), release: make(chan struct{}),
	}
	f.srv.oauthClientFor = func(string) oauthClient { return block }

	// Flow A starts (gets dev-1) and its poll blocks server-side.
	var svA kanbanConnectStartView
	startA := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
	decode(t, startA, &svA)
	pollADone := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, f.clusterConnectURL()+"/"+svA.ConnectID, nil)
		if err != nil {
			pollADone <- err
			return
		}
		req.Header.Set("Authorization", "Bearer "+f.adminTok)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		pollADone <- err
	}()
	select {
	case <-block.entered:
		// Flow A is now mid-poll, holding its record mutex.
	case <-time.After(3 * time.Second):
		close(block.release)
		t.Fatal("flow A's poll never reached the fake client")
	}

	// Flow B (start + poll) must complete promptly despite A being stuck.
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		var svB kanbanConnectStartView
		startB := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
		if startB.StatusCode != http.StatusOK {
			t.Errorf("flow B start status=%d want 200", startB.StatusCode)
			return
		}
		decode(t, startB, &svB)
		pollB := do(t, http.MethodGet, f.clusterConnectURL()+"/"+svB.ConnectID, f.adminTok, nil)
		if pollB.StatusCode != http.StatusOK {
			t.Errorf("flow B poll status=%d want 200", pollB.StatusCode)
		}
		pollB.Body.Close()
	}()
	select {
	case <-otherDone:
		// No stall — the registry stayed responsive.
	case <-time.After(3 * time.Second):
		close(block.release)
		<-pollADone
		t.Fatal("registry stalled: another flow's start/poll blocked behind a slow jtype poll")
	}

	// Unblock A and drain its poll so the test server can shut down cleanly.
	close(block.release)
	if err := <-pollADone; err != nil {
		t.Fatalf("flow A poll transport error: %v", err)
	}
}

// Test 6d: terminal statuses map straight through — expired / denied / unsupported.
func TestConnectTerminalStatuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode jtypeoauth.Status
		perr error
		want string
	}{
		{"expired", jtypeoauth.StatusExpired, nil, "expired"},
		{"denied", jtypeoauth.StatusDenied, nil, "denied"},
		{"unsupported", 0, jtypeoauth.ErrOAuthUnsupported, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupConnect(t, true)
			f.setClusterBaseURL(t, "http://jtype.db")
			var sv kanbanConnectStartView
			start := do(t, http.MethodPost, f.clusterConnectURL(), f.adminTok, nil)
			decode(t, start, &sv)
			f.fake.setMode(tc.mode)
			f.fake.pollErr = tc.perr
			pr := do(t, http.MethodGet, f.clusterConnectURL()+"/"+sv.ConnectID, f.adminTok, nil)
			var st kanbanConnectStatusView
			decode(t, pr, &st)
			if st.Status != tc.want || st.TokenSet {
				t.Fatalf("%s poll = %+v want %s", tc.name, st, tc.want)
			}
		})
	}
}
