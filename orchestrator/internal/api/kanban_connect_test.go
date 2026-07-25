//go:build legacy_api

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
	"github.com/cnjack/jcloud/internal/jtype"
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
			tok = &jtypeoauth.Token{AccessToken: "minted-full-token", ExpiresIn: 7776000, Scope: "full"}
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
// oauth client seam. Since D36 the only connect surface is per-link.
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
		cfg.MasterKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	fake := &fakeOAuthClient{mode: jtypeoauth.StatusPending}
	srv.oauthClientFor = func(string) oauthClient { return fake }
	srv.validateJtypeToken = func(context.Context, string) error { return nil }
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

// linkConnectURL is the per-link connect base (POST start / GET poll).
func (f connectFixture) linkConnectURL(linkID string) string {
	return f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/links/" + linkID + "/connect"
}

// setClusterBaseURL PUTs a DB base_url override (admin) so the per-link connect
// precondition (integration enabled) is satisfied.
func (f connectFixture) setClusterBaseURL(t *testing.T, base string) {
	t.Helper()
	r := do(t, http.MethodPut, f.ts.URL+"/api/v1/system/kanban", f.adminTok, map[string]any{"base_url": base})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("set base_url: %d", r.StatusCode)
	}
	r.Body.Close()
}

// Test 1: link start — no integration configured → 409 kanban_not_configured;
// configured → 200 carrying user_code + verification_uri_complete, device_code
// WITHHELD from the response.
func TestLinkConnectStart(t *testing.T) {
	f := setupConnect(t, true)
	link := f.seedLink(t)

	// Integration off.
	resp := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("no integration: status=%d want 409", resp.StatusCode)
	}
	var eb errorBody
	decode(t, resp, &eb)
	if eb.Error.Code != "kanban_not_configured" {
		t.Fatalf("code=%q want kanban_not_configured", eb.Error.Code)
	}

	// Integration on: 200 start view, no device_code leak.
	f.setClusterBaseURL(t, "http://jtype.db")
	resp = do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
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

// Test 2: link poll pending → complete seals the token into kanban_links
// (roundtrip), stamps token_expires_at ≈ now+90d, flips credential_status to
// per_link, and the plaintext token NEVER appears in any response body. A
// repeat poll is idempotent (single-use; no re-mint).
func TestLinkConnectPollToComplete(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	link := f.seedLink(t)

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	decode(t, start, &sv)
	pollURL := f.linkConnectURL(link.ID) + "/" + sv.ConnectID

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
	if strings.Contains(string(raw), "minted-full-token") || strings.Contains(string(raw), "dev-secret") {
		t.Fatalf("SECRET LEAK: poll response contains a secret: %s", raw)
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

	// The token is sealed on the link row, decrypts to the minted value, never plaintext.
	stored, err := f.st.GetKanbanLink(context.Background(), link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.TokenSet() || stored.TokenExpiresAt == nil {
		t.Fatalf("per-link token/expiry not stored: %+v", stored)
	}
	if got, _ := f.srv.Cipher().DecryptString(stored.TokenEnc); got != "minted-full-token" {
		t.Fatalf("per-link token decrypts to %q", got)
	}

	// The project links list flips credential_status → per_link (no secret leak).
	lr := do(t, http.MethodGet, f.ts.URL+"/api/v1/projects/"+f.projectID+"/kanban/links", f.adminTok, nil)
	lraw, _ := io.ReadAll(lr.Body)
	lr.Body.Close()
	if strings.Contains(string(lraw), "minted-full-token") || strings.Contains(string(lraw), "dev-secret") {
		t.Fatalf("SECRET LEAK: links list contains a secret: %s", lraw)
	}
	var list struct {
		Links []kanbanLinkView `json:"links"`
	}
	mustJSON(t, lraw, &list)
	if len(list.Links) != 1 || list.Links[0].CredentialStatus != "per_link" || list.Links[0].TokenExpiresAt == "" {
		t.Fatalf("link view after connect = %+v", list.Links)
	}

	// A later poll is idempotent: still complete (single-use; no re-mint).
	pr = do(t, http.MethodGet, pollURL, f.adminTok, nil)
	var st3 kanbanConnectStatusView
	decode(t, pr, &st3)
	if st3.Status != "complete" || !st3.TokenSet {
		t.Fatalf("repeat poll = %+v want complete", st3)
	}
}

func TestLinkConnectRejectsTokenThatCannotListWorkspaces(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	link := f.seedLink(t)
	f.srv.validateJtypeToken = func(context.Context, string) error {
		return fmt.Errorf("forbidden")
	}

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	decode(t, start, &sv)
	f.fake.setMode(jtypeoauth.StatusComplete)

	poll := do(t, http.MethodGet, f.linkConnectURL(link.ID)+"/"+sv.ConnectID, f.adminTok, nil)
	var status kanbanConnectStatusView
	decode(t, poll, &status)
	if status.Status != "denied" || status.TokenSet {
		t.Fatalf("status=%+v want denied without token", status)
	}
	stored, err := f.st.GetKanbanLink(context.Background(), link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenSet() {
		t.Fatal("failed-capability token must not be stored")
	}
}

// Test 3: an unsupported jtype (start returns ErrOAuthUnsupported) → 409
// jtype_oauth_unsupported; JCLOUD_MASTER_KEY unset → cipher_not_configured AT START
// (no jtype call at all).
func TestLinkConnectUnsupportedAndNoCipher(t *testing.T) {
	// Unsupported.
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	link := f.seedLink(t)
	f.fake.startErr = jtypeoauth.ErrOAuthUnsupported
	resp := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	var eb errorBody
	decode(t, resp, &eb)
	if resp.StatusCode != http.StatusConflict || eb.Error.Code != "jtype_oauth_unsupported" {
		t.Fatalf("unsupported: status=%d code=%q", resp.StatusCode, eb.Error.Code)
	}

	// No cipher: 409 cipher_not_configured, and NO jtype call was made.
	f2 := setupConnect(t, false)
	f2.setClusterBaseURL(t, "http://jtype.db")
	link2 := f2.seedLink(t)
	resp = do(t, http.MethodPost, f2.linkConnectURL(link2.ID), f2.adminTok, nil)
	var eb2 errorBody
	decode(t, resp, &eb2)
	if resp.StatusCode != http.StatusConflict || eb2.Error.Code != "cipher_not_configured" {
		t.Fatalf("no cipher: status=%d code=%q want 409 cipher_not_configured", resp.StatusCode, eb2.Error.Code)
	}
	if f2.fake.pollCount() != 0 {
		t.Fatal("cipher check must precede any jtype call")
	}
}

// Test 4: a DIFFERENT project's link path → 404 (the link is not in that project).
func TestLinkConnectCrossProject404(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	link := f.seedLink(t)

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

// Test 5: the authz matrix — link connect: a member (non-owner) is 403 on start
// AND poll, a scoped API key (capped at RoleMember on its own project) is 403,
// unauth is 401.
func TestConnectAuthzMatrix(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	link := f.seedLink(t)
	base := f.linkConnectURL(link.ID)

	for _, tc := range []struct {
		name string
		tok  string
		want int
	}{
		{"member", f.memberTok, http.StatusForbidden},
		{"api-key", f.apiKey, http.StatusForbidden},
		{"unauth", "", http.StatusUnauthorized},
	} {
		if r := do(t, http.MethodPost, base, tc.tok, nil); r.StatusCode != tc.want {
			t.Errorf("link start by %s status=%d want %d", tc.name, r.StatusCode, tc.want)
			r.Body.Close()
		} else {
			r.Body.Close()
		}
		if tc.want == http.StatusUnauthorized {
			continue // the poll route's auth middleware answers 401 the same way
		}
		if r := do(t, http.MethodGet, base+"/deadbeef", tc.tok, nil); r.StatusCode != tc.want {
			t.Errorf("link poll by %s status=%d want %d", tc.name, r.StatusCode, tc.want)
			r.Body.Close()
		} else {
			r.Body.Close()
		}
	}
}

// Test 6a: an unknown connect_id → 404 connect_expired (also the restart-drops
// case), and a DIFFERENT principal (another owner user) cannot poll someone
// else's flow (a leaked connect_id is unusable).
func TestConnectRegistryUnknownAndPrincipalMismatch(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	link := f.seedLink(t)

	// Unknown id.
	r := do(t, http.MethodGet, f.linkConnectURL(link.ID)+"/deadbeef", f.adminTok, nil)
	var eb errorBody
	decode(t, r, &eb)
	if r.StatusCode != http.StatusNotFound || eb.Error.Code != "connect_expired" {
		t.Fatalf("unknown id: status=%d code=%q", r.StatusCode, eb.Error.Code)
	}

	// The admin (user) starts a flow; ANOTHER owner user (a DIFFERENT identity,
	// same RoleOwner authority on the project) must not be able to poll it.
	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	decode(t, start, &sv)
	other := mkUser(t, f.st, "cn-other-owner")
	if err := f.st.UpsertMember(context.Background(), &domain.ProjectMember{
		ProjectID: f.projectID, UserID: other.ID, Role: domain.RoleOwner,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	otherTok := mkSession(t, f.st, other.ID)
	mism := do(t, http.MethodGet, f.linkConnectURL(link.ID)+"/"+sv.ConnectID, otherTok, nil)
	var eb2 errorBody
	decode(t, mism, &eb2)
	if mism.StatusCode != http.StatusNotFound || eb2.Error.Code != "connect_expired" {
		t.Fatalf("principal mismatch: status=%d code=%q want 404 connect_expired", mism.StatusCode, eb2.Error.Code)
	}
	// The original principal still can (proves the record survived the mismatch poll).
	ok := do(t, http.MethodGet, f.linkConnectURL(link.ID)+"/"+sv.ConnectID, f.adminTok, nil)
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
	link := f.seedLink(t)

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	decode(t, start, &sv)
	pollURL := f.linkConnectURL(link.ID) + "/" + sv.ConnectID

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
	link := f.seedLink(t)

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	decode(t, start, &sv)

	// The admin edits the base URL mid-flow, then jtype "approves".
	f.setClusterBaseURL(t, "http://jtype.b")
	f.fake.setMode(jtypeoauth.StatusComplete)

	pr := do(t, http.MethodGet, f.linkConnectURL(link.ID)+"/"+sv.ConnectID, f.adminTok, nil)
	var st kanbanConnectStatusView
	decode(t, pr, &st)
	if st.Status != "expired" || st.TokenSet {
		t.Fatalf("mid-flow base change poll = %+v want expired/no-token", st)
	}
	// No token was stored despite the "approval".
	stored, err := f.st.GetKanbanLink(context.Background(), link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenSet() {
		t.Fatal("a base_url change mid-flow must NOT store a token")
	}
	// jtype was never polled for the token (the mid-flow guard precedes the poll).
	if f.fake.pollCount() != 0 {
		t.Fatalf("expired-by-base-change must not poll jtype, got %d polls", f.fake.pollCount())
	}
}

// Test 6e (anti-TOCTOU): the base_url changes BETWEEN the guard read and
// completion — i.e. INSIDE the token poll's network window. The re-check in
// completeConnectLocked must refuse: status expired and no token is stored.
func TestConnectBaseURLChangedDuringCompletingPoll(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.a")
	link := f.seedLink(t)

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	decode(t, start, &sv)

	// jtype "approves", and the admin's re-point lands INSIDE the poll (after the
	// guard read) — simulated by mutating the row from the fake's mid-poll hook.
	f.fake.setMode(jtypeoauth.StatusComplete)
	f.fake.setOnPoll(func() {
		if err := f.st.UpsertClusterKanbanConfig(context.Background(),
			&domain.KanbanConfig{BaseURL: "http://jtype.b", UpdatedBy: "admin"}); err != nil {
			t.Errorf("mid-poll upsert: %v", err)
		}
		// The resolver caches; the mid-flow re-check must see the NEW base.
		f.srv.kanban.Invalidate()
	})

	pr := do(t, http.MethodGet, f.linkConnectURL(link.ID)+"/"+sv.ConnectID, f.adminTok, nil)
	var st kanbanConnectStatusView
	decode(t, pr, &st)
	if st.Status != "expired" || st.TokenSet {
		t.Fatalf("in-poll base change = %+v want expired/no-token", st)
	}
	// The guard passed (jtype WAS polled) — the completion-time re-check refused.
	if f.fake.pollCount() != 1 {
		t.Fatalf("guard should have passed (1 jtype poll), got %d", f.fake.pollCount())
	}
	stored, err := f.st.GetKanbanLink(context.Background(), link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenSet() {
		t.Fatal("a base_url change inside the completing poll must NOT store a token")
	}
}

// Test 6f: a slow_down poll backs the interval off (+5s) — the next poll inside
// the widened window is gated (no jtype hit); past it, the poll goes through.
func TestConnectSlowDownBackoff(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	link := f.seedLink(t)

	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
	decode(t, start, &sv)
	pollURL := f.linkConnectURL(link.ID) + "/" + sv.ConnectID

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
	linkA := f.seedLink(t)
	linkB := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b2", ProjectID: f.projectID,
		ServiceID: f.serviceID, TriggerColumn: "ai", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := f.st.CreateKanbanLink(context.Background(), linkB); err != nil {
		t.Fatal(err)
	}
	block := &blockingOAuthClient{
		blockOn: "dev-1", entered: make(chan struct{}), release: make(chan struct{}),
	}
	f.srv.oauthClientFor = func(string) oauthClient { return block }

	// Flow A starts (gets dev-1) and its poll blocks server-side.
	var svA kanbanConnectStartView
	startA := do(t, http.MethodPost, f.linkConnectURL(linkA.ID), f.adminTok, nil)
	decode(t, startA, &svA)
	pollADone := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, f.linkConnectURL(linkA.ID)+"/"+svA.ConnectID, nil)
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

	// Flow B (start + poll, on ANOTHER link) must complete promptly despite A
	// being stuck.
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		var svB kanbanConnectStartView
		startB := do(t, http.MethodPost, f.linkConnectURL(linkB.ID), f.adminTok, nil)
		if startB.StatusCode != http.StatusOK {
			t.Errorf("flow B start status=%d want 200", startB.StatusCode)
			return
		}
		decode(t, startB, &svB)
		pollB := do(t, http.MethodGet, f.linkConnectURL(linkB.ID)+"/"+svB.ConnectID, f.adminTok, nil)
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
		{"scope_mismatch", 0, jtypeoauth.ErrOAuthScopeMismatch, "denied"},
		{"unsupported", 0, jtypeoauth.ErrOAuthUnsupported, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupConnect(t, true)
			f.setClusterBaseURL(t, "http://jtype.db")
			link := f.seedLink(t)
			var sv kanbanConnectStartView
			start := do(t, http.MethodPost, f.linkConnectURL(link.ID), f.adminTok, nil)
			decode(t, start, &sv)
			f.fake.setMode(tc.mode)
			f.fake.pollErr = tc.perr
			pr := do(t, http.MethodGet, f.linkConnectURL(link.ID)+"/"+sv.ConnectID, f.adminTok, nil)
			var st kanbanConnectStatusView
			decode(t, pr, &st)
			if st.Status != tc.want || st.TokenSet {
				t.Fatalf("%s poll = %+v want %s", tc.name, st, tc.want)
			}
		})
	}
}

// --- D37: project-surface connect (connect BEFORE the first link exists) -----

// Test D37-1: the project flow mints a token bound to NO link — the poll returns
// the SEALED blob (decryptable server-side, plaintext never on the wire), repeat
// polls keep returning it, and NO link row is created/touched.
func TestProjectConnectSealedBlobRoundtrip(t *testing.T) {
	f := setupConnect(t, true)

	// Integration off → 409 kanban_not_configured.
	linkless := do(t, http.MethodPost, f.ts.URL+"/api/v1/projects/"+f.projectID+"/kanban/connect", f.adminTok, nil)
	var eb errorBody
	decode(t, linkless, &eb)
	if linkless.StatusCode != http.StatusConflict || eb.Error.Code != "kanban_not_configured" {
		t.Fatalf("off: status=%d code=%q", linkless.StatusCode, eb.Error.Code)
	}

	f.setClusterBaseURL(t, "http://jtype.db")
	base := f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/connect"
	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, base, f.adminTok, nil)
	if start.StatusCode != http.StatusOK {
		t.Fatalf("start: %d", start.StatusCode)
	}
	decode(t, start, &sv)

	f.clock.advance(3 * time.Second)
	f.fake.setMode(jtypeoauth.StatusComplete)
	pr := do(t, http.MethodGet, base+"/"+sv.ConnectID, f.adminTok, nil)
	raw, _ := io.ReadAll(pr.Body)
	pr.Body.Close()
	if strings.Contains(string(raw), "minted-full-token") || strings.Contains(string(raw), "dev-secret") {
		t.Fatalf("SECRET LEAK: project poll response contains a secret: %s", raw)
	}
	var st kanbanConnectStatusView
	mustJSON(t, raw, &st)
	if st.Status != "complete" || !st.TokenSet || st.TokenExpiresAt == "" {
		t.Fatalf("complete poll = %+v", st)
	}
	if st.TokenEnc == "" {
		t.Fatal("D37: project-surface complete poll must carry token_enc")
	}
	// The blob is valid ciphertext under THIS orchestrator's cipher and opens to
	// the minted token (the console never learns this — the test server does).
	blob, err := base64.StdEncoding.DecodeString(st.TokenEnc)
	if err != nil {
		t.Fatalf("token_enc is not base64: %v", err)
	}
	if got, err := f.srv.Cipher().DecryptString(blob); err != nil || got != "minted-full-token" {
		t.Fatalf("token_enc decrypts to %q err=%v, want minted-full-token", got, err)
	}

	// A repeat poll keeps returning the blob (idempotent; no re-mint).
	pr = do(t, http.MethodGet, base+"/"+sv.ConnectID, f.adminTok, nil)
	var st2 kanbanConnectStatusView
	decode(t, pr, &st2)
	if st2.TokenEnc != st.TokenEnc {
		t.Fatal("repeat poll must return the same sealed blob")
	}

	// Nothing was stored: the project has no link rows at all.
	if links, _ := f.st.ListKanbanLinksByProject(context.Background(), f.projectID); len(links) != 0 {
		t.Fatalf("project-surface connect must not create/touch link rows: %+v", links)
	}
}

// Test D37-2: the blob DRIVES discovery without any link in the project
// (X-Jtype-Token-Enc), and a tampered/foreign blob is a typed 400 bad_token_enc.
func TestProjectConnectBlobDrivesDiscovery(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	f.srv.jtypeDiscoveryFor = func(_ *jtype.Factory, token string) jtypeDiscovery {
		if token != "minted-full-token" {
			t.Errorf("discovery resolved token %q want minted-full-token", token)
		}
		return fakeDiscovery{workspaces: []jtype.Workspace{{ID: "ws-1", Name: "Team"}}}
	}

	base := f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/connect"
	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, base, f.adminTok, nil)
	decode(t, start, &sv)
	f.fake.setMode(jtypeoauth.StatusComplete)
	pr := do(t, http.MethodGet, base+"/"+sv.ConnectID, f.adminTok, nil)
	var st kanbanConnectStatusView
	decode(t, pr, &st)

	// No link exists (seedLink was never called) — without the header this is the
	// D36 typed 409; with it, discovery lists.
	wsURL := f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/jtype/workspaces"
	noHdr := do(t, http.MethodGet, wsURL, f.adminTok, nil)
	if noHdr.StatusCode != http.StatusConflict {
		t.Fatalf("no header want 409 kanban_token_required, got %d", noHdr.StatusCode)
	}
	noHdr.Body.Close()

	req, err := http.NewRequest(http.MethodGet, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.adminTok)
	req.Header.Set("X-Jtype-Token-Enc", st.TokenEnc)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("discovery with blob want 200, got %d body=%s", resp.StatusCode, b)
	}

	// A garbage blob → 400 bad_token_enc (never a silent empty list).
	req2, _ := http.NewRequest(http.MethodGet, wsURL, nil)
	req2.Header.Set("Authorization", "Bearer "+f.adminTok)
	req2.Header.Set("X-Jtype-Token-Enc", "aW50ZWdyaXR5LXRhbXBlcmVk")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad blob want 400, got %d", resp2.StatusCode)
	}
	var eb errorBody
	decode(t, resp2, &eb)
	if eb.Error.Code != "bad_token_enc" {
		t.Fatalf("code=%q want bad_token_enc", eb.Error.Code)
	}
}

// Test D37-3: authz + scoping — a member/api-key is 403 on the project flow; a
// flow started in project A cannot be polled via project B's path (404).
func TestProjectConnectAuthzAndScoping(t *testing.T) {
	f := setupConnect(t, true)
	f.setClusterBaseURL(t, "http://jtype.db")
	baseA := f.ts.URL + "/api/v1/projects/" + f.projectID + "/kanban/connect"

	for _, tc := range []struct {
		name string
		tok  string
		want int
	}{
		{"member", f.memberTok, http.StatusForbidden},
		{"api-key", f.apiKey, http.StatusForbidden},
		{"unauth", "", http.StatusUnauthorized},
	} {
		r := do(t, http.MethodPost, baseA, tc.tok, nil)
		if r.StatusCode != tc.want {
			t.Errorf("project connect start by %s status=%d want %d", tc.name, r.StatusCode, tc.want)
		}
		r.Body.Close()
	}

	// Cross-project poll path → 404 connect_expired (never an existence leak).
	var sv kanbanConnectStartView
	start := do(t, http.MethodPost, baseA, f.adminTok, nil)
	decode(t, start, &sv)
	p2 := do(t, http.MethodPost, f.ts.URL+"/api/v1/projects", f.adminTok, map[string]any{"name": "p2"})
	var pv2 projectView
	decode(t, p2, &pv2)
	cross := do(t, http.MethodGet,
		f.ts.URL+"/api/v1/projects/"+pv2.ID+"/kanban/connect/"+sv.ConnectID, f.adminTok, nil)
	if cross.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project poll want 404, got %d", cross.StatusCode)
	}
	cross.Body.Close()
}
