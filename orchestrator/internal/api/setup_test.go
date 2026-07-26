package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func newSetupServer(t *testing.T) (*httptest.Server, *store.MemStore, *providerStub) {
	t.Helper()
	stub := newProviderStub()
	upstream := httptest.NewServer(stub.handler())
	stub.baseURL = upstream.URL
	t.Cleanup(upstream.Close)
	st := store.NewMemStore()
	srv := New(st, &config.Config{ConsoleToken: consoleToken, ConsoleURL: "http://localhost:5173", MasterKey: validTokenKey(t), SessionTTL: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, stub
}

func TestFirstVisitorSetupPersistsLoginProviderAndEnablesDynamicAuth(t *testing.T) {
	ts, st, upstream := newSetupServer(t)

	request, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/setup", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "cloud.example.test"
	request.Header.Set("X-Forwarded-Proto", "https")
	before, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if before.StatusCode != http.StatusOK {
		t.Fatalf("GET setup status=%d", before.StatusCode)
	}
	var initial struct {
		SetupRequired      bool   `json:"setup_required"`
		PublicURL          string `json:"public_url"`
		LoginProviderCount int    `json:"login_provider_count"`
	}
	decode(t, before, &initial)
	if !initial.SetupRequired || initial.LoginProviderCount != 0 || initial.PublicURL != "https://cloud.example.test" {
		t.Fatalf("initial setup=%+v", initial)
	}

	missing := do(t, http.MethodPut, ts.URL+"/api/v1/setup", "", map[string]any{"public_url": "http://localhost:5173"})
	if missing.StatusCode != http.StatusConflict {
		t.Fatalf("setup without provider=%d want 409", missing.StatusCode)
	}
	missing.Body.Close()

	complete := do(t, http.MethodPut, ts.URL+"/api/v1/setup", "", map[string]any{
		"public_url": "http://localhost:5173",
		"provider":   map[string]any{"provider": "gitea", "base_url": upstream.baseURL, "login_enabled": true, "plugin_enabled": true, "client_id": "setup-client", "client_secret": "setup-secret"},
	})
	if complete.StatusCode != http.StatusOK {
		t.Fatalf("setup complete=%d", complete.StatusCode)
	}
	var done struct {
		SetupRequired      bool `json:"setup_required"`
		LoginProviderCount int  `json:"login_provider_count"`
	}
	decode(t, complete, &done)
	if done.SetupRequired || done.LoginProviderCount != 1 {
		t.Fatalf("completed setup=%+v", done)
	}

	cfg, err := st.GetProviderConfig(context.Background(), "gitea")
	if err != nil || len(cfg.ClientSecretEnc) == 0 {
		t.Fatalf("provider config was not encrypted/persisted: cfg=%+v err=%v", cfg, err)
	}
	if cfg.Capabilities == nil || len(cfg.Capabilities) != 0 {
		t.Fatalf("setup must persist an empty capability array, got %#v", cfg.Capabilities)
	}

	providers := do(t, http.MethodGet, ts.URL+"/auth/providers", "", nil)
	var listed struct {
		Providers []authProviderInfo `json:"providers"`
	}
	decode(t, providers, &listed)
	if len(listed.Providers) != 1 || listed.Providers[0].ID != "gitea" {
		t.Fatalf("auth providers=%+v", listed.Providers)
	}

	start, err := noRedirectClient().Get(ts.URL + "/auth/login/gitea")
	if err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	if start.StatusCode != http.StatusFound {
		t.Fatalf("dynamic login start=%d", start.StatusCode)
	}
	if got := start.Header.Get("Location"); got == "" || got == ts.URL {
		t.Fatalf("missing provider redirect: %q", got)
	}
}

func TestSetupRequestOriginUsesConfiguredHTTPSAcrossInternalHTTPProxy(t *testing.T) {
	srv := &Server{cfg: &config.Config{ConsoleURL: "https://cloud.example.test"}}
	req := httptest.NewRequest(http.MethodGet, "http://orchestrator.internal/api/v1/setup", nil)
	req.Host = "cloud.example.test"
	req.Header.Set("X-Forwarded-Proto", "http")
	if got := srv.setupRequestOrigin(req); got != "https://cloud.example.test" {
		t.Fatalf("setup origin=%q want https browser entry", got)
	}
}

func TestSetupEndpointIsClosedAfterCompletion(t *testing.T) {
	ts, _, upstream := newSetupServer(t)
	request := map[string]any{"public_url": "http://localhost:5173", "provider": map[string]any{"provider": "gitea", "base_url": upstream.baseURL, "login_enabled": true, "plugin_enabled": true, "client_id": "id", "client_secret": "secret"}}
	first := do(t, http.MethodPut, ts.URL+"/api/v1/setup", "", request)
	first.Body.Close()
	second := do(t, http.MethodPut, ts.URL+"/api/v1/setup", "", request)
	if second.StatusCode != http.StatusForbidden {
		t.Fatalf("second setup=%d want 403", second.StatusCode)
	}
	second.Body.Close()
}

func TestSetupRejectsNonEmptyDatabaseWithoutSettings(t *testing.T) {
	ts, st, upstream := newSetupServer(t)
	_ = mkUser(t, st, "existing-admin")
	request := map[string]any{
		"public_url": "http://localhost:5173",
		"provider": map[string]any{
			"provider": "gitea", "base_url": upstream.baseURL,
			"login_enabled": true, "client_id": "id", "client_secret": "secret",
		},
	}
	response := do(t, http.MethodPut, ts.URL+"/api/v1/setup", "", request)
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("setup on non-empty database=%d want 409", response.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "database_recovery_required" {
		t.Fatalf("error code=%q", body.Error.Code)
	}
}

func TestDatabaseProviderConfigurationDisablesEnvOAuthFallback(t *testing.T) {
	ts, st, _ := newAuthServer(t) // this fixture intentionally has env Gitea OAuth
	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{Provider: domain.PluginJType, BaseURL: "https://jtype.example.test", PluginEnabled: true}); err != nil {
		t.Fatal(err)
	}
	providers := do(t, http.MethodGet, ts.URL+"/auth/providers", "", nil)
	var listed struct {
		Providers []authProviderInfo `json:"providers"`
	}
	decode(t, providers, &listed)
	if len(listed.Providers) != 0 {
		t.Fatalf("DB configuration must suppress env fallback, got %+v", listed.Providers)
	}
	login := do(t, http.MethodGet, ts.URL+"/auth/login/gitea", "", nil)
	if login.StatusCode != http.StatusNotFound {
		t.Fatalf("DB configuration must reject stale env login, got %d", login.StatusCode)
	}
	login.Body.Close()
}
