package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// kanbanConfigFixture is a server plus a cluster-admin, a plain member, and a
// project-scoped API key — the three principals the D27 /system/kanban authz
// matrix needs — over a config the caller controls (env fallback on/off).
// D36: the cluster config is base-URL-only, so no cipher is involved at all.
type kanbanConfigFixture struct {
	ts        *httptest.Server
	srv       *Server
	st        *store.MemStore
	adminTok  string
	memberTok string
	apiKey    string // scoped API key plaintext (jck_...)
}

// kanbanTestCfg builds a config for the fixture with the given JTYPE_BASE_URL
// env fallback ("" = no env fallback).
func kanbanTestCfg(envBaseURL string) *config.Config {
	return withTestModel(&config.Config{
		ConsoleToken:      consoleToken,
		JtypeBaseURL:      envBaseURL,
		JtypePollInterval: 15 * time.Second,
	})
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

// Test 1 (D36): PUT base_url => 200; GET reflects source=db + effective_enabled.
// The view carries NO token fields at all (the cluster config is base-URL-only).
func TestKanbanConfigPutAndGet(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(""))

	f.putConfig(t, map[string]any{"base_url": "http://jtype.db"}, http.StatusOK)

	gr := do(t, http.MethodGet, f.url(), f.adminTok, nil)
	getRaw, _ := io.ReadAll(gr.Body)
	gr.Body.Close()
	var v kanbanConfigView
	if err := json.Unmarshal(getRaw, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.BaseURL != "http://jtype.db" || v.Source != "db" ||
		!v.EffectiveEnabled || v.EffectiveBaseURL != "http://jtype.db" {
		t.Fatalf("GET view = %+v", v)
	}
	// No credential fields on the wire (D36): token_set / cluster_token_set /
	// token_expires_at must not appear in the raw JSON.
	var raw map[string]any
	if err := json.Unmarshal(getRaw, &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, k := range []string{"token_set", "cluster_token_set", "token_expires_at"} {
		if _, present := raw[k]; present {
			t.Fatalf("D36: response must not carry credential field %q: %s", k, getRaw)
		}
	}
}

// Test 2: an invalid/empty base_url is a fail-visible 400.
func TestKanbanConfigInvalidBaseURL(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(""))
	for _, bad := range []string{"", "not-a-url", "ftp://x"} {
		resp := do(t, http.MethodPut, f.url(), f.adminTok, map[string]any{"base_url": bad})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("base_url=%q: status=%d want 400", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Test 3: DELETE removes the override; the effective state honestly falls back to
// env (when set) or none — never a stale "still db".
func TestKanbanConfigDelete(t *testing.T) {
	// With an env fallback: DELETE => source env, effective on.
	f := setupKanbanConfig(t, kanbanTestCfg("http://env"))
	f.putConfig(t, map[string]any{"base_url": "http://db"}, http.StatusOK)
	resp := do(t, http.MethodDelete, f.url(), f.adminTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status=%d want 200", resp.StatusCode)
	}
	var v kanbanConfigView
	decode(t, resp, &v)
	if v.Source != "env" || !v.EffectiveEnabled || v.EffectiveBaseURL != "http://env" || v.BaseURL != "" {
		t.Fatalf("delete-with-env view = %+v", v)
	}

	// Without an env fallback: DELETE => source none, effective off.
	f2 := setupKanbanConfig(t, kanbanTestCfg(""))
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

// Test 4: DB > env precedence is reflected on BOTH /system/kanban and the /system
// snapshot's kanban block (and the snapshot carries no credential fields, D36).
func TestKanbanConfigDBOverridesEnv(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg("http://env"))

	// No DB row yet => the env fallback is effective on both surfaces.
	if v := f.getConfig(t); v.Source != "env" || v.EffectiveBaseURL != "http://env" {
		t.Fatalf("pre-override /system/kanban = %+v (want env)", v)
	}
	if k := f.systemKanbanBlock(t); k.Source != "env" || k.BaseURL != "http://env" || !k.Enabled {
		t.Fatalf("pre-override /system kanban = %+v (want env)", k)
	}

	// A DB override wins.
	f.putConfig(t, map[string]any{"base_url": "http://db"}, http.StatusOK)
	if v := f.getConfig(t); v.Source != "db" || v.EffectiveBaseURL != "http://db" {
		t.Fatalf("post-override /system/kanban = %+v (want db)", v)
	}
	if k := f.systemKanbanBlock(t); k.Source != "db" || k.BaseURL != "http://db" || !k.Enabled {
		t.Fatalf("post-override /system kanban = %+v (want db)", k)
	}
}

// Test 5: authz — GET/PUT/DELETE require cluster-admin. A member and a scoped API
// key are 403; no token is 401.
func TestKanbanConfigAuthz(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(""))
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

// Test 6 (D36): the cluster-surface connect routes are GONE — a POST to the old
// /system/kanban/connect path is a 404 (route removed with the cluster fallback
// token; per-link connect lives under /projects/...).
func TestKanbanClusterConnectRouteGone(t *testing.T) {
	f := setupKanbanConfig(t, kanbanTestCfg(""))
	f.putConfig(t, map[string]any{"base_url": "http://jtype.db"}, http.StatusOK)

	resp := do(t, http.MethodPost, f.url()+"/connect", f.adminTok, nil)
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("D36: cluster connect route must be gone; status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()
	resp2 := do(t, http.MethodGet, f.url()+"/connect/abc", f.adminTok, nil)
	if resp2.StatusCode != http.StatusNotFound && resp2.StatusCode != http.StatusMethodNotAllowed {
		resp2.Body.Close()
		t.Fatalf("D36: cluster connect poll route must be gone; status=%d", resp2.StatusCode)
	}
	resp2.Body.Close()
}
