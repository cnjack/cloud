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

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// newModelServer builds an API server for the model-config endpoints. cipher
// controls whether AUTH_TOKEN_KEY is set (needed for PUT), envModel controls
// whether the MODEL_* env fallback is present (so we can test env/none/db).
func newModelServer(t *testing.T, cipher, envModel bool) (*httptest.Server, *store.MemStore) {
	t.Helper()
	st := store.NewMemStore()
	cfg := &config.Config{ConsoleToken: consoleToken}
	if cipher {
		cfg.AuthTokenKey = validTokenKey(t)
	}
	if envModel {
		withTestModel(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// modelView mirrors the handler's modelConfigView for decoding.
type modelView struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source"`
	BaseURL    string `json:"base_url"`
	ModelName  string `json:"model_name"`
	APIKeySet  bool   `json:"api_key_set"`
}

func TestModelConfigGetNonAdminSeesOnlyConfigured(t *testing.T) {
	ts, st := newModelServer(t, true, true) // env-configured
	admin := mkUser(t, st, "admin")         // first user => cluster admin
	_ = admin
	bob := mkUser(t, st, "bob") // non-admin
	bobTok := mkSession(t, st, bob.ID)

	resp := do(t, "GET", ts.URL+"/api/v1/system/model", bobTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status=%d want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// A non-admin must NOT see the source / base_url / model_name detail.
	for _, leak := range []string{"source", "base_url", "model_name", "model.test"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("non-admin body leaked %q: %s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), `"configured":true`) {
		t.Fatalf("non-admin body should report configured:true, got %s", raw)
	}
}

func TestModelConfigGetAdminEnvSource(t *testing.T) {
	ts, _ := newModelServer(t, true, true) // env-configured
	resp := do(t, "GET", ts.URL+"/api/v1/system/model", consoleToken, nil)
	var v modelView
	decode(t, resp, &v)
	if !v.Configured || v.Source != "env" {
		t.Fatalf("admin env view = %+v want configured/env", v)
	}
	if v.BaseURL != "http://model.test/v1" || v.ModelName != "mock/mock-model" || !v.APIKeySet {
		t.Fatalf("admin env view detail wrong: %+v", v)
	}
}

func TestModelConfigGetNoneWhenUnconfigured(t *testing.T) {
	ts, _ := newModelServer(t, true, false) // no env, no db
	resp := do(t, "GET", ts.URL+"/api/v1/system/model", consoleToken, nil)
	var v modelView
	decode(t, resp, &v)
	if v.Configured || v.Source != "none" {
		t.Fatalf("view = %+v want not configured / none", v)
	}
}

func TestModelConfigPutThenGetReflectsDBAndNeverLeaksKey(t *testing.T) {
	ts, _ := newModelServer(t, true, false)
	const secret = "sk-super-secret-key"
	resp := do(t, "PUT", ts.URL+"/api/v1/system/model", consoleToken, map[string]any{
		"base_url": "https://api.openai.com/v1", "model_name": "openai/gpt-4o", "api_key": secret,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put: status=%d want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), secret) {
		t.Fatalf("PUT response leaked the plaintext api key: %s", raw)
	}

	resp = do(t, "GET", ts.URL+"/api/v1/system/model", consoleToken, nil)
	graw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(graw), secret) {
		t.Fatalf("GET response leaked the plaintext api key: %s", graw)
	}
	var v modelView
	if err := json.Unmarshal(graw, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Source != "db" || !v.Configured || v.BaseURL != "https://api.openai.com/v1" || v.ModelName != "openai/gpt-4o" || !v.APIKeySet {
		t.Fatalf("db view wrong: %+v", v)
	}
}

func TestModelConfigPutEmptyKeyIsKeylessDB(t *testing.T) {
	ts, _ := newModelServer(t, true, false)
	resp := do(t, "PUT", ts.URL+"/api/v1/system/model", consoleToken, map[string]any{
		"base_url": "http://vllm.local/v1", "model_name": "local/llama", "api_key": "",
	})
	var v modelView
	decode(t, resp, &v)
	if v.Source != "db" || !v.Configured || v.APIKeySet {
		t.Fatalf("keyless db view = %+v want db/configured/api_key_set=false", v)
	}
}

func TestModelConfigPutValidation(t *testing.T) {
	ts, _ := newModelServer(t, true, false)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"non-http base_url", map[string]any{"base_url": "ftp://x/v1", "model_name": "a/b", "api_key": "k"}},
		{"empty base_url", map[string]any{"base_url": "", "model_name": "a/b", "api_key": "k"}},
		{"model without slash", map[string]any{"base_url": "http://x/v1", "model_name": "gpt4", "api_key": "k"}},
		{"model empty provider", map[string]any{"base_url": "http://x/v1", "model_name": "/b", "api_key": "k"}},
		{"model empty model", map[string]any{"base_url": "http://x/v1", "model_name": "a/", "api_key": "k"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, "PUT", ts.URL+"/api/v1/system/model", consoleToken, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d want 400", resp.StatusCode)
			}
			resp.Body.Close()
		})
	}
}

// TestModelConfigPutRequiresCipherOnlyForKeys: the cipher gate protects stored
// keys — it must NOT block a keyless save (no key, nothing to encrypt).
func TestModelConfigPutRequiresCipherOnlyForKeys(t *testing.T) {
	ts, _ := newModelServer(t, false, false) // no AUTH_TOKEN_KEY => cipher nil

	// A key WITHOUT a cipher to protect it: typed 409.
	resp := do(t, "PUT", ts.URL+"/api/v1/system/model", consoleToken, map[string]any{
		"base_url": "http://x/v1", "model_name": "a/b", "api_key": "k",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("put key with no cipher: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "cipher_not_configured" {
		t.Fatalf("error code=%q want cipher_not_configured", body.Error.Code)
	}

	// A KEYLESS config needs no encryption — it must save fine without a cipher.
	resp = do(t, "PUT", ts.URL+"/api/v1/system/model", consoleToken, map[string]any{
		"base_url": "http://vllm.local/v1", "model_name": "local/llama", "api_key": "",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("keyless put with no cipher: status=%d want 200", resp.StatusCode)
	}
	var v modelView
	decode(t, resp, &v)
	if v.Source != "db" || !v.Configured || v.APIKeySet {
		t.Fatalf("keyless cipher-less save = %+v want db/configured/api_key_set=false", v)
	}
}

func TestModelConfigMutationRBAC(t *testing.T) {
	ts, st := newModelServer(t, true, false)
	admin := mkUser(t, st, "admin") // first user => cluster admin
	_ = admin
	bob := mkUser(t, st, "bob")
	bobTok := mkSession(t, st, bob.ID)

	// A non-admin cannot PUT or DELETE.
	for _, m := range []string{"PUT", "DELETE"} {
		resp := do(t, m, ts.URL+"/api/v1/system/model", bobTok, map[string]any{
			"base_url": "http://x/v1", "model_name": "a/b", "api_key": "k",
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s as non-admin: status=%d want 403", m, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestModelConfigDeleteFallsBackToEnv(t *testing.T) {
	ts, _ := newModelServer(t, true, true) // env-configured AND we'll set a DB row
	// Set a DB row (wins over env).
	resp := do(t, "PUT", ts.URL+"/api/v1/system/model", consoleToken, map[string]any{
		"base_url": "https://api.openai.com/v1", "model_name": "openai/gpt-4o", "api_key": "k",
	})
	var v modelView
	decode(t, resp, &v)
	if v.Source != "db" {
		t.Fatalf("after put source=%q want db", v.Source)
	}
	// DELETE clears the DB row; the effective config falls back to env.
	resp = do(t, "DELETE", ts.URL+"/api/v1/system/model", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status=%d want 200", resp.StatusCode)
	}
	decode(t, resp, &v)
	if v.Source != "env" || !v.Configured {
		t.Fatalf("after delete source=%q configured=%v want env/true", v.Source, v.Configured)
	}
}

// --- run-creation gate (fail-visible) ---------------------------------------

func TestCreateRun409WhenModelNotConfigured(t *testing.T) {
	// A server with NO model configured (no env, no db).
	st := store.NewMemStore()
	cfg := &config.Config{ConsoleToken: consoleToken}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "do it"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("create run with no model: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "model_not_configured" {
		t.Fatalf("error code=%q want model_not_configured", body.Error.Code)
	}
	// No run should have been created.
	ctx := context.Background()
	runs, _ := st.ListRuns(ctx, p.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("created %d runs, want 0 (gate must not queue)", len(runs))
	}
}

func TestRetryAndReview409WhenModelNotConfigured(t *testing.T) {
	// Server starts configured (so we can create + drive a run), then the model
	// is cleared before retry/review.
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, GiteaURL: "http://gitea.test", GiteaToken: "pat"})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	ctx := context.Background()

	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "do it"})
	var run struct {
		ID string `json:"id"`
	}
	decode(t, resp, &run)
	// Drive to succeeded with a PR so review preconditions pass.
	got, _ := st.GetRun(ctx, run.ID)
	_, _ = st.ScheduleRun(ctx, got.ID, "j", "h", "PreparingWorkspace")
	_, _ = st.MarkRunning(ctx, got.ID, "Running", got.CreatedAt)
	_, _ = st.MarkSucceeded(ctx, got.ID, "Succeeded", got.CreatedAt)
	_, _ = st.SetRunGit(ctx, got.ID, "jcode/run-x", "abc")
	_, _ = st.MarkPRCreated(ctx, got.ID, "http://gitea.test/o/r/pulls/1", 1)

	// Now clear env model config by mutating cfg (mirrors an admin DELETE that
	// leaves nothing configured). The resolver caches successes, so drop its
	// cache the same way the real DELETE handler does.
	cfg.ModelBaseURL, cfg.ModelName, cfg.ModelAPIKey = "", "", ""
	srv.models.Invalidate()

	// Retry -> 409.
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/retry", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry with no model: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
	// Request review -> 409.
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/review", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("review with no model: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "model_not_configured" {
		t.Fatalf("review error code=%q want model_not_configured", body.Error.Code)
	}
}
