package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// kanbanConfigFixture is a server plus a cluster-admin, a plain member, and a
// project-scoped API key — the three principals the D27 /system/kanban authz
// matrix needs — over a config the caller controls (cipher on/off, env fallback).
type kanbanConfigFixture struct {
	ts        *httptest.Server
	srv       *Server
	st        *store.MemStore
	adminTok  string
	memberTok string
	apiKey    string // scoped API key plaintext (jck_...)
}

// kanbanTestCfg builds a config for the fixture: a 32-byte AUTH_TOKEN_KEY when
// withCipher, and the given JTYPE_* env fallback.
func kanbanTestCfg(withCipher bool, envBaseURL, envToken string) *config.Config {
	c := withTestModel(&config.Config{
		ConsoleToken:      consoleToken,
		JtypeBaseURL:      envBaseURL,
		JtypeToken:        envToken,
		JtypePollInterval: 15 * time.Second,
	})
	if withCipher {
		c.AuthTokenKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	return c
}

func setupKanbanConfig(t *testing.T, cfg *config.Config) kanbanConfigFixture {
	t.Helper()
	st := store.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	admin := mkUser(t, st, "kc-admin") // first user => cluster admin
	member := mkUser(t, st, "kc-member")
	adminTok := mkSession(t, st, admin.ID)
	memberTok := mkSession(t, st, member.ID)

	// A project owned by the admin, plus a scoped API key on it (capped at
	// RoleMember on its own project — never cluster-admin).
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects", adminTok, map[string]any{"name": "kc"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	kr := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+pv.ID+"/apikeys", adminTok, map[string]any{"name": "ci"})
	if kr.StatusCode != http.StatusCreated {
		t.Fatalf("mint api key: %d", kr.StatusCode)
	}
	var key createAPIKeyResponse
	decode(t, kr, &key)

	return kanbanConfigFixture{ts: ts, srv: srv, st: st, adminTok: adminTok, memberTok: memberTok, apiKey: key.Key}
}

func (f kanbanConfigFixture) url() string { return f.ts.URL + "/api/v1/system/kanban" }

// putConfig PUTs body and decodes the response view (asserting the status).
func (f kanbanConfigFixture) putConfig(t *testing.T, body map[string]any, wantStatus int) kanbanConfigView {
	t.Helper()
	resp := do(t, http.MethodPut, f.url(), f.adminTok, body)
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("PUT %v: status=%d want %d body=%s", body, resp.StatusCode, wantStatus, b)
	}
	var v kanbanConfigView
	decode(t, resp, &v)
	return v
}

// Test 1: PUT with base_url + token => 200; GET reflects source=db, token_set,
// effective_enabled; the plaintext token never appears in any response body.
func TestKanbanConfigPutAndGet(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(true, "", ""))
	const secret = "super-secret-pat-value"

	// PUT: read the RAW body for the no-leak scan.
	resp := do(t, http.MethodPut, f.url(), f.adminTok, map[string]any{"base_url": "http://jtype.db", "token": secret})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d want 200", resp.StatusCode)
	}
	putRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(putRaw), secret) {
		t.Fatalf("SECRET LEAK: PUT response contains the plaintext token: %s", putRaw)
	}

	// GET returns the same shape (source=db, token_set, effective on), no plaintext.
	gr := do(t, http.MethodGet, f.url(), f.adminTok, nil)
	getRaw, _ := io.ReadAll(gr.Body)
	gr.Body.Close()
	if strings.Contains(string(getRaw), secret) {
		t.Fatalf("SECRET LEAK: GET response contains the plaintext token: %s", getRaw)
	}
	var v kanbanConfigView
	if err := json.Unmarshal(getRaw, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.BaseURL != "http://jtype.db" || !v.TokenSet || v.Source != "db" ||
		!v.EffectiveEnabled || v.EffectiveBaseURL != "http://jtype.db" || !v.ClusterTokenSet {
		t.Fatalf("GET view = %+v", v)
	}
}

// Test 2: token pointer three-state — absent KEEPS, "" CLEARS, a value ROTATES.
func TestKanbanConfigTokenThreeState(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(true, "", ""))
	ctx := context.Background()

	// Set base + token.
	if v := f.putConfig(t, map[string]any{"base_url": "http://a", "token": "tok1"}, http.StatusOK); !v.TokenSet {
		t.Fatalf("set: token_set=false, want true")
	}
	// Absent token + new base => token KEPT, base changed.
	v := f.putConfig(t, map[string]any{"base_url": "http://b"}, http.StatusOK)
	if !v.TokenSet || v.BaseURL != "http://b" {
		t.Fatalf("keep: view=%+v (want token_set true, base http://b)", v)
	}
	if row, err := f.st.GetClusterKanbanConfig(ctx); err != nil {
		t.Fatal(err)
	} else if got, _ := f.srv.Cipher().DecryptString(row.TokenEnc); got != "tok1" {
		t.Fatalf("keep must retain the token; decrypt=%q want tok1", got)
	}
	// "" CLEARS.
	if v := f.putConfig(t, map[string]any{"base_url": "http://b", "token": ""}, http.StatusOK); v.TokenSet {
		t.Fatalf("clear: token_set=true, want false")
	}
	// A value ROTATES.
	f.putConfig(t, map[string]any{"base_url": "http://b", "token": "tok2"}, http.StatusOK)
	if row, err := f.st.GetClusterKanbanConfig(ctx); err != nil {
		t.Fatal(err)
	} else if got, _ := f.srv.Cipher().DecryptString(row.TokenEnc); got != "tok2" {
		t.Fatalf("rotate: decrypt=%q want tok2", got)
	}
}

// Test 3: a token with no cipher => 409 cipher_not_configured; a base_url-only
// PUT still succeeds (the base URL needs no cipher).
func TestKanbanConfigTokenNeedsCipher(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(false, "", "")) // no AUTH_TOKEN_KEY

	// base_url-only is fine.
	f.putConfig(t, map[string]any{"base_url": "http://a"}, http.StatusOK)

	// A token is a typed 409.
	resp := do(t, http.MethodPut, f.url(), f.adminTok, map[string]any{"base_url": "http://a", "token": "x"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("token w/o cipher: status=%d want 409", resp.StatusCode)
	}
	var eb errorBody
	decode(t, resp, &eb)
	if eb.Error.Code != "cipher_not_configured" {
		t.Fatalf("error code=%q want cipher_not_configured", eb.Error.Code)
	}
	// Nothing was stored (the base_url-only row from above still has no token).
	if row, err := f.st.GetClusterKanbanConfig(context.Background()); err != nil || row.TokenSet() {
		t.Fatalf("409 must not store a token: row=%+v err=%v", row, err)
	}
}

// Test 4: an invalid/empty base_url is a fail-visible 400.
func TestKanbanConfigInvalidBaseURL(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(true, "", ""))
	for _, bad := range []string{"", "not-a-url", "ftp://x"} {
		resp := do(t, http.MethodPut, f.url(), f.adminTok, map[string]any{"base_url": bad})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("base_url=%q: status=%d want 400", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Test 5: DELETE removes the override; the effective state honestly falls back to
// env (when set) or none — never a stale "still db".
func TestKanbanConfigDelete(t *testing.T) {
	// With an env fallback: DELETE => source env, effective on.
	f := setupKanbanConfig(t, kanbanTestCfg(true, "http://env", "envtok"))
	f.putConfig(t, map[string]any{"base_url": "http://db"}, http.StatusOK)
	resp := do(t, http.MethodDelete, f.url(), f.adminTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status=%d want 200", resp.StatusCode)
	}
	var v kanbanConfigView
	decode(t, resp, &v)
	if v.Source != "env" || !v.EffectiveEnabled || v.EffectiveBaseURL != "http://env" || v.BaseURL != "" || v.TokenSet {
		t.Fatalf("delete-with-env view = %+v", v)
	}

	// Without an env fallback: DELETE => source none, effective off.
	f2 := setupKanbanConfig(t, kanbanTestCfg(true, "", ""))
	f2.putConfig(t, map[string]any{"base_url": "http://db"}, http.StatusOK)
	resp2 := do(t, http.MethodDelete, f2.url(), f2.adminTok, nil)
	var v2 kanbanConfigView
	decode(t, resp2, &v2)
	if v2.Source != "none" || v2.EffectiveEnabled {
		t.Fatalf("delete-no-env view = %+v (want source none, off)", v2)
	}
	// Idempotent: a second DELETE is still 200.
	if r := do(t, http.MethodDelete, f2.url(), f2.adminTok, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("second DELETE status=%d want 200", r.StatusCode)
	}
}

// Test 6: DB > env precedence is reflected on BOTH /system/kanban and the /system
// snapshot's kanban block.
func TestKanbanConfigDBOverridesEnv(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(true, "http://env", "envtok"))

	// No DB row yet => the env fallback is effective on both surfaces.
	if v := f.getConfig(t); v.Source != "env" || v.EffectiveBaseURL != "http://env" {
		t.Fatalf("pre-override /system/kanban = %+v (want env)", v)
	}
	if k := f.systemKanbanBlock(t); k.Source != "env" || k.BaseURL != "http://env" || !k.Enabled {
		t.Fatalf("pre-override /system kanban = %+v (want env)", k)
	}

	// A DB override wins.
	f.putConfig(t, map[string]any{"base_url": "http://db", "token": "dbtok"}, http.StatusOK)
	if v := f.getConfig(t); v.Source != "db" || v.EffectiveBaseURL != "http://db" || !v.ClusterTokenSet {
		t.Fatalf("post-override /system/kanban = %+v (want db)", v)
	}
	if k := f.systemKanbanBlock(t); k.Source != "db" || k.BaseURL != "http://db" || !k.Enabled || !k.ClusterTokenSet {
		t.Fatalf("post-override /system kanban = %+v (want db)", k)
	}
}

// Test 7: the effective cluster_token_set is per source — a DB base URL with NO
// DB token does NOT report the env JTYPE_TOKEN as set (source-coupling).
func TestKanbanConfigClusterTokenNotMixed(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(true, "http://env", "envtok"))
	f.putConfig(t, map[string]any{"base_url": "http://db"}, http.StatusOK) // no DB token
	v := f.getConfig(t)
	if v.Source != "db" || v.ClusterTokenSet {
		t.Fatalf("db source must not borrow the env token: %+v", v)
	}
	if k := f.systemKanbanBlock(t); k.Source != "db" || k.ClusterTokenSet {
		t.Fatalf("/system kanban db source must not borrow env token: %+v", k)
	}
}

// Test 8: authz — GET/PUT/DELETE require cluster-admin. A member and a scoped API
// key are 403; no token is 401.
func TestKanbanConfigAuthz(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(true, "", ""))
	body := map[string]any{"base_url": "http://a"}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		// no token => 401.
		if r := do(t, m, f.url(), "", body); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s unauth = %d want 401", m, r.StatusCode)
			r.Body.Close()
		} else {
			r.Body.Close()
		}
		// member => 403.
		if r := do(t, m, f.url(), f.memberTok, body); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s member = %d want 403", m, r.StatusCode)
			r.Body.Close()
		} else {
			r.Body.Close()
		}
		// scoped API key => 403.
		if r := do(t, m, f.url(), f.apiKey, body); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s api-key = %d want 403", m, r.StatusCode)
			r.Body.Close()
		} else {
			r.Body.Close()
		}
	}
	// The admin can GET (sanity: the route exists and admin passes the gate).
	if r := do(t, http.MethodGet, f.url(), f.adminTok, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("admin GET = %d want 200", r.StatusCode)
	}
}

// getConfig GETs the config view (admin), asserting 200.
func (f kanbanConfigFixture) getConfig(t *testing.T) kanbanConfigView {
	t.Helper()
	resp := do(t, http.MethodGet, f.url(), f.adminTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET config status=%d want 200", resp.StatusCode)
	}
	var v kanbanConfigView
	decode(t, resp, &v)
	return v
}

// systemKanbanBlock fetches GET /api/v1/system and returns its kanban block.
func (f kanbanConfigFixture) systemKanbanBlock(t *testing.T) systemKanban {
	t.Helper()
	resp := do(t, http.MethodGet, f.ts.URL+"/api/v1/system", f.adminTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /system status=%d want 200", resp.StatusCode)
	}
	var sr systemResponse
	decode(t, resp, &sr)
	return sr.Kanban
}
