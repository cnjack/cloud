package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/scmevent"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func TestProviderConfigCompleteRequiresEveryEnabledGitHubCapability(t *testing.T) {
	cfg := &domain.ProviderConfig{
		Provider: domain.PluginGitHub, BaseURL: "https://github.com",
		LoginEnabled: true, PluginEnabled: true,
		ClientID: "oauth-client", ClientSecretEnc: []byte("encrypted"),
	}
	if providerConfigComplete(cfg) {
		t.Fatal("GitHub login credentials alone must not mark the App plugin configured")
	}
	cfg.AppID = "4395162"
	cfg.AppPrivateKeyEnc = []byte("encrypted")
	if providerConfigComplete(cfg) {
		t.Fatal("GitHub App without a webhook secret must remain incomplete")
	}
	cfg.WebhookSecretEnc = []byte("encrypted")
	if !providerConfigComplete(cfg) {
		t.Fatal("complete GitHub OAuth and App credentials should be configured")
	}
}

func TestProviderConfigCompleteRequiresPluginOAuthClient(t *testing.T) {
	for _, kind := range []domain.ProviderKind{domain.PluginGitLab, domain.PluginGitea, domain.PluginJType} {
		cfg := &domain.ProviderConfig{Provider: kind, BaseURL: "https://provider.example", PluginEnabled: true}
		if providerConfigComplete(cfg) {
			t.Fatalf("%s plugin without OAuth client reported configured", kind)
		}
		cfg.ClientID = "client"
		cfg.ClientSecretEnc = []byte("sealed")
		if !providerConfigComplete(cfg) {
			t.Fatalf("%s plugin with OAuth client reported incomplete", kind)
		}
	}
}

func TestProviderIdentityChangesRequireMatchingSecretReplacement(t *testing.T) {
	ts, st, _ := newCipherServer(t, nil, "")
	ctx := context.Background()
	put := func(provider string, body map[string]any) *http.Response {
		return do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/"+provider, consoleToken, body)
	}
	initial := put("gitea", map[string]any{
		"base_url": "https://old-gitea.example.test", "plugin_enabled": true,
		"client_id": "client-a", "client_secret": "secret-a",
	})
	if initial.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(initial.Body)
		t.Fatalf("initial Gitea config=%d body=%s", initial.StatusCode, body)
	}
	initial.Body.Close()
	before, _ := st.GetProviderConfig(ctx, domain.PluginGitea)

	for name, mutation := range map[string]map[string]any{
		"authority": {
			"base_url": "https://attacker.example.test", "plugin_enabled": true, "client_id": "client-a",
		},
		"client id": {
			"base_url": "https://old-gitea.example.test", "plugin_enabled": true, "client_id": "client-b",
		},
	} {
		resp := put("gitea", mutation)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s change without secret status=%d want 400", name, resp.StatusCode)
		}
		resp.Body.Close()
		current, _ := st.GetProviderConfig(ctx, domain.PluginGitea)
		if current.BaseURL != before.BaseURL || current.ClientID != before.ClientID ||
			current.ConfigRevision != before.ConfigRevision {
			t.Fatalf("%s rejected update mutated config: before=%+v after=%+v", name, before, current)
		}
	}

	// Canonical URL normalization is a semantic no-op and keeps the write-only
	// secret without manufacturing a new configuration identity.
	same := put("gitea", map[string]any{
		"base_url": "https://old-gitea.example.test/", "plugin_enabled": true, "client_id": "client-a",
	})
	if same.StatusCode != http.StatusOK {
		t.Fatalf("same-origin no-op status=%d", same.StatusCode)
	}
	same.Body.Close()
	afterSame, _ := st.GetProviderConfig(ctx, domain.PluginGitea)
	if afterSame.ConfigRevision != before.ConfigRevision {
		t.Fatalf("same-origin no-op revision=%d want %d", afterSame.ConfigRevision, before.ConfigRevision)
	}

	github := put("github", map[string]any{
		"base_url": "https://github.com", "plugin_enabled": true,
		"app_id": "app-a", "app_private_key": "private-a", "webhook_secret": "hook-a",
	})
	if github.StatusCode != http.StatusOK {
		t.Fatalf("initial GitHub config=%d", github.StatusCode)
	}
	github.Body.Close()
	githubChange := put("github", map[string]any{
		"base_url": "https://github.com", "plugin_enabled": true, "app_id": "app-b",
	})
	if githubChange.StatusCode != http.StatusBadRequest {
		t.Fatalf("GitHub App ID change without private key=%d want 400", githubChange.StatusCode)
	}
	githubChange.Body.Close()

	jtype := put("jtype", map[string]any{
		"base_url": "https://jtype-a.example.test", "plugin_enabled": true,
		"client_id": "jtype-client", "client_secret": "jtype-secret",
	})
	if jtype.StatusCode != http.StatusOK {
		t.Fatalf("initial JType config=%d", jtype.StatusCode)
	}
	jtype.Body.Close()
	jtypeChange := put("jtype", map[string]any{
		"base_url": "https://jtype-b.example.test", "plugin_enabled": true,
		"client_id": "jtype-client",
	})
	if jtypeChange.StatusCode != http.StatusBadRequest {
		t.Fatalf("JType URL change without secret=%d want 400", jtypeChange.StatusCode)
	}
	jtypeChange.Body.Close()
}

func TestSelfHostedProviderRejectsClusterWebhookSecret(t *testing.T) {
	ts, _, _ := newCipherServer(t, nil, "")
	resp := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": "https://gitea.example.test", "plugin_enabled": true,
		"client_id": "client", "client_secret": "secret", "webhook_secret": "misleading-shared-secret",
	})
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("self-hosted webhook_secret status=%d want 400 body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "per-Service") {
		t.Fatalf("self-hosted webhook secret error is not actionable: %s", body)
	}
}

func TestPluginOAuthCallbackRejectsMidFlightProviderChangeBeforeExchange(t *testing.T) {
	var replacementTokenHits atomic.Int32
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			replacementTokenHits.Add(1)
		}
		http.Error(w, "must not be reached", http.StatusTeapot)
	}))
	defer replacement.Close()
	original := httptest.NewServer(http.NotFoundHandler())
	defer original.Close()

	ts, _, _ := newCipherServer(t, nil, "")
	configure := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": original.URL, "plugin_enabled": true,
		"client_id": "client-a", "client_secret": "secret-a",
	})
	if configure.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(configure.Body)
		t.Fatalf("initial provider config=%d body=%s", configure.StatusCode, body)
	}
	configure.Body.Close()
	projectID := newProject(t, ts, "oauth-config-binding")
	start := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/plugins/gitea/connect", consoleToken, map[string]any{
		"consent_accepted": true, "consent_version": pluginConsentVersion,
	})
	if start.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(start.Body)
		start.Body.Close()
		t.Fatalf("start plugin OAuth=%d body=%s", start.StatusCode, body)
	}
	stateCookie := findCookie(start, stateCookieName)
	pluginCookie := findCookie(start, pluginOAuthCookieName)
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	decode(t, start, &started)
	authorizeURL, err := url.Parse(started.AuthorizeURL)
	if err != nil || stateCookie == nil || pluginCookie == nil {
		t.Fatalf("invalid OAuth start url=%q state=%v plugin=%v err=%v", started.AuthorizeURL, stateCookie, pluginCookie, err)
	}

	changed := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": replacement.URL, "plugin_enabled": true,
		"client_id": "client-b", "client_secret": "secret-b",
	})
	if changed.StatusCode != http.StatusOK {
		t.Fatalf("replacement provider config=%d", changed.StatusCode)
	}
	changed.Body.Close()

	cbReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/auth/callback/gitea?code=stolen&state="+
		url.QueryEscape(authorizeURL.Query().Get("state")), nil)
	cbReq.AddCookie(stateCookie)
	cbReq.AddCookie(pluginCookie)
	cb, err := noRedirectClient().Do(cbReq)
	if err != nil {
		t.Fatal(err)
	}
	cb.Body.Close()
	if cb.StatusCode != http.StatusConflict {
		t.Fatalf("mid-flight callback=%d want 409", cb.StatusCode)
	}
	if replacementTokenHits.Load() != 0 {
		t.Fatalf("callback sent old authorization code to replacement token endpoint %d times", replacementTokenHits.Load())
	}
	if cleared := findCookie(cb, pluginOAuthCookieName); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatal("rejected callback did not clear pending plugin OAuth cookie")
	}
}

func TestPluginOAuthCallbackSurvivesSemanticProviderNoOp(t *testing.T) {
	var tokenHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, _ *http.Request) {
		tokenHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"project-token","token_type":"bearer"}`))
	})
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"login":"owner"}`))
	})
	oauthServer := httptest.NewServer(mux)
	defer oauthServer.Close()

	ts, _, _ := newCipherServer(t, nil, "")
	configure := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": oauthServer.URL, "plugin_enabled": true,
		"client_id": "client", "client_secret": "secret",
	})
	if configure.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(configure.Body)
		t.Fatalf("initial provider config=%d body=%s", configure.StatusCode, body)
	}
	configure.Body.Close()
	projectID := newProject(t, ts, "oauth-no-op")
	start := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/plugins/gitea/connect", consoleToken, map[string]any{
		"consent_accepted": true, "consent_version": pluginConsentVersion,
	})
	stateCookie := findCookie(start, stateCookieName)
	pluginCookie := findCookie(start, pluginOAuthCookieName)
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	decode(t, start, &started)
	authorizeURL, _ := url.Parse(started.AuthorizeURL)

	noOp := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": oauthServer.URL + "/", "plugin_enabled": true, "client_id": "client",
	})
	if noOp.StatusCode != http.StatusOK {
		t.Fatalf("semantic provider no-op=%d", noOp.StatusCode)
	}
	noOp.Body.Close()
	cbReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/auth/callback/gitea?code=ok&state="+
		url.QueryEscape(authorizeURL.Query().Get("state")), nil)
	cbReq.AddCookie(stateCookie)
	cbReq.AddCookie(pluginCookie)
	cb, err := noRedirectClient().Do(cbReq)
	if err != nil {
		t.Fatal(err)
	}
	cb.Body.Close()
	if cb.StatusCode != http.StatusFound || tokenHits.Load() != 1 {
		t.Fatalf("callback after semantic no-op status=%d token_hits=%d", cb.StatusCode, tokenHits.Load())
	}
}

func TestProviderConfigViewMarksGrantlessProbePartial(t *testing.T) {
	now := time.Now().UTC()
	if got := providerConfigViewOf(&domain.ProviderConfig{Provider: domain.PluginGitLab, LastCapabilityCheck: &now}).Health; got != "partial" {
		t.Fatalf("GitLab probe health=%q want partial", got)
	}
	if got := providerConfigViewOf(&domain.ProviderConfig{Provider: domain.PluginGitHub, LastCapabilityCheck: &now}).Health; got != "healthy" {
		t.Fatalf("GitHub App probe health=%q want healthy", got)
	}
}

func TestProviderProbePersistsObservedVersionAndDerivedCapabilities(t *testing.T) {
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t)})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	srv.probeProviderConfig = func(_ context.Context, got *domain.ProviderConfig) (provider.ConfigProbeResult, error) {
		if got.Provider != domain.PluginGitea {
			t.Fatalf("provider=%q", got.Provider)
		}
		return provider.ConfigProbeResult{Version: "1.25.1"}, nil
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{
		Provider: domain.PluginGitea, BaseURL: "https://gitea.example.test", PluginEnabled: true,
		ClientID: "client", ClientSecretEnc: []byte("sealed"),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetProviderConfig(context.Background(), domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: "project", Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, ConfigRevision: before.ConfigRevision,
		ConsentVersion: pluginConsentVersion, ConsentedAt: now, CreatedAt: now,
	}
	if err := st.CreatePluginInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/system/providers/gitea/test", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("probe status=%d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()
	stored, err := st.GetProviderConfig(context.Background(), domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CapabilityVersion != "1.25.1" || stored.LastCapabilityCheck == nil || stored.LastHealthError != "" {
		t.Fatalf("stored probe=%+v", stored)
	}
	if len(stored.Capabilities) == 0 {
		t.Fatal("supported provider did not persist derived capability actions")
	}
	if stored.ConfigRevision != before.ConfigRevision {
		t.Fatalf("successful probe changed revision from %d to %d", before.ConfigRevision, stored.ConfigRevision)
	}
	currentInstallation, err := st.GetPluginInstallation(context.Background(), installation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentInstallation.ConfigRevision != before.ConfigRevision || currentInstallation.Status != domain.PluginStatusEnabled {
		t.Fatalf("successful probe changed installation identity: %+v", currentInstallation)
	}
}

func TestProviderProbeFailureIsObservationOnly(t *testing.T) {
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t)})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	srv.probeProviderConfig = func(_ context.Context, _ *domain.ProviderConfig) (provider.ConfigProbeResult, error) {
		return provider.ConfigProbeResult{}, context.DeadlineExceeded
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	checked := time.Now().Add(-time.Hour).UTC()
	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{
		Provider: domain.PluginGitea, BaseURL: "https://gitea.example.test", PluginEnabled: true,
		ClientID: "client", ClientSecretEnc: []byte("sealed"),
		CapabilityVersion: "1.25.3", Capabilities: []string{"push.updated"}, LastCapabilityCheck: &checked,
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetProviderConfig(context.Background(), domain.PluginGitea)
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: "project", Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, ConfigRevision: before.ConfigRevision,
		ConsentVersion: pluginConsentVersion, ConsentedAt: checked, CreatedAt: checked,
	}
	if err := st.CreatePluginInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodPost, ts.URL+"/api/v1/system/providers/gitea/test", consoleToken, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed probe status=%d want 502", resp.StatusCode)
	}
	resp.Body.Close()
	after, _ := st.GetProviderConfig(context.Background(), domain.PluginGitea)
	if after.ConfigRevision != before.ConfigRevision || after.CapabilityVersion != "1.25.3" ||
		len(after.Capabilities) != 1 || after.LastHealthError == "" ||
		after.LastCapabilityCheck == nil || !after.LastCapabilityCheck.After(checked) {
		t.Fatalf("failed probe changed configured identity or known capabilities: before=%+v after=%+v", before, after)
	}
	currentInstallation, _ := st.GetPluginInstallation(context.Background(), installation.ID)
	if currentInstallation.ConfigRevision != before.ConfigRevision || currentInstallation.Status != domain.PluginStatusEnabled {
		t.Fatalf("failed probe changed installation identity: %+v", currentInstallation)
	}
}

func TestProviderNoOpPutDoesNotIncrementRevision(t *testing.T) {
	ts, st, _ := newCipherServer(t, nil, "")
	ctx := context.Background()
	checked := time.Now().UTC()
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{
		Provider: domain.PluginGitHub, BaseURL: "https://github.com",
		LoginEnabled: true, PluginEnabled: true, ClientID: "oauth-client",
		ClientSecretEnc: []byte("sealed"), AppID: "4395162",
		AppPrivateKeyEnc: []byte("sealed"), WebhookSecretEnc: []byte("sealed"),
		CapabilityVersion: "github.com", Capabilities: []string{"push.updated"}, LastCapabilityCheck: &checked,
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetProviderConfig(ctx, domain.PluginGitHub)
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: "project", Provider: domain.PluginGitHub,
		Status: domain.PluginStatusEnabled, ConfigRevision: before.ConfigRevision,
		ConsentVersion: pluginConsentVersion, ConsentedAt: checked, CreatedAt: checked,
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/github", consoleToken, map[string]any{
		"base_url": "https://github.com", "login_enabled": true, "plugin_enabled": true,
		"client_id": "oauth-client", "app_id": "4395162",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-op PUT status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	after, _ := st.GetProviderConfig(ctx, domain.PluginGitHub)
	if after.ConfigRevision != before.ConfigRevision || after.CapabilityVersion != before.CapabilityVersion ||
		after.LastCapabilityCheck == nil {
		t.Fatalf("no-op PUT changed revision or observation: before=%+v after=%+v", before, after)
	}
	currentInstallation, _ := st.GetPluginInstallation(ctx, installation.ID)
	if currentInstallation.ConfigRevision != before.ConfigRevision || currentInstallation.Status != domain.PluginStatusEnabled {
		t.Fatalf("no-op PUT changed installation identity: %+v", currentInstallation)
	}
}

func TestProviderCapabilitiesFailClosedForFailedOrTooOldProbe(t *testing.T) {
	ts, st, _ := newCipherServer(t, nil, "")
	now := time.Now().UTC()
	var result struct {
		Capabilities []scmevent.Capability `json:"capabilities"`
	}
	// An unconfigured provider must not advertise the optimistic catalog either.
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/providers/gitea/capabilities", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unconfigured capabilities status=%d", resp.StatusCode)
	}
	decode(t, resp, &result)
	if len(result.Capabilities) != 0 {
		t.Fatalf("unconfigured provider advertised %d capability families", len(result.Capabilities))
	}

	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{
		Provider: domain.PluginGitea, BaseURL: "https://gitea.example.test", PluginEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/providers/gitea/capabilities", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unknown capabilities status=%d", resp.StatusCode)
	}
	decode(t, resp, &result)
	if len(result.Capabilities) != 0 {
		t.Fatalf("unprobed provider advertised %d capability families", len(result.Capabilities))
	}

	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{
		Provider: domain.PluginGitea, BaseURL: "https://gitea.example.test", PluginEnabled: true,
		CapabilityVersion: "1.24.9", LastCapabilityCheck: &now,
	}); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/providers/gitea/capabilities", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capabilities status=%d", resp.StatusCode)
	}
	decode(t, resp, &result)
	if len(result.Capabilities) != 0 {
		t.Fatalf("too-old provider advertised %d capability families", len(result.Capabilities))
	}

	cfg, err := st.GetProviderConfig(context.Background(), domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CapabilityVersion = "1.25.1"
	cfg.LastHealthError = "provider probe failed"
	if err := st.UpsertProviderConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/providers/gitea/capabilities", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed probe capabilities status=%d", resp.StatusCode)
	}
	decode(t, resp, &result)
	if len(result.Capabilities) != 0 {
		t.Fatalf("failed probe advertised %d capability families", len(result.Capabilities))
	}
}

func TestPluginGrantCapabilityDiscoveryPersistsVersionWithoutChangingConfigRevision(t *testing.T) {
	providerFailure := false
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/version" || r.Header.Get("Authorization") != "Bearer project-grant" {
			http.NotFound(w, r)
			return
		}
		if providerFailure {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"version":"17.11.2"}`))
	}))
	defer providerServer.Close()

	st := store.NewMemStore()
	now := time.Now().UTC()
	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{
		Provider: domain.PluginGitLab, BaseURL: providerServer.URL, PluginEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetProviderConfig(context.Background(), domain.PluginGitLab)
	if err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: "p", Provider: domain.PluginGitLab,
		Status: domain.PluginStatusEnabled, ConfigRevision: cfg.ConfigRevision,
		ConsentVersion: pluginConsentVersion, ConsentedAt: now, CreatedAt: now,
	}
	if err := st.CreatePluginInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}

	// Use an isolated Server value with the same Store so the unit test does not
	// need to manufacture OAuth callback cookies merely to exercise its safe
	// post-grant discovery path.
	apiServer := New(st, withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t)}), slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	apiServer.refreshProviderCapabilitiesFromPluginGrant(context.Background(), installation, "project-grant")

	stored, err := st.GetProviderConfig(context.Background(), domain.PluginGitLab)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CapabilityVersion != "17.11.2" || stored.LastCapabilityCheck == nil || len(stored.Capabilities) == 0 {
		t.Fatalf("stored capabilities=%+v", stored)
	}
	if stored.ConfigRevision != cfg.ConfigRevision {
		t.Fatalf("capability refresh changed config revision from %d to %d", cfg.ConfigRevision, stored.ConfigRevision)
	}

	providerFailure = true
	apiServer.refreshProviderCapabilitiesFromPluginGrant(context.Background(), installation, "project-grant")
	afterFailure, err := st.GetProviderConfig(context.Background(), domain.PluginGitLab)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.CapabilityVersion != "17.11.2" || afterFailure.LastHealthError != "" || afterFailure.ConfigRevision != cfg.ConfigRevision {
		t.Fatalf("failed discovery overwrote known-good capability state: %+v", afterFailure)
	}
}

func TestProjectPluginConsentLifecycle(t *testing.T) {
	ts, _, _ := newCipherServer(t, nil, "")

	// Provider secrets are write-only and require cluster-admin authority.
	put := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": "https://gitea.example.test", "plugin_enabled": true,
		"client_id": "cloud", "client_secret": "never-return-this",
	})
	if put.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(put.Body)
		put.Body.Close()
		t.Fatalf("configure plugin provider: %d body=%s", put.StatusCode, body)
	}
	body, _ := io.ReadAll(put.Body)
	put.Body.Close()
	if strings.Contains(string(body), "never-return-this") || !strings.Contains(string(body), `"client_secret_set":true`) {
		t.Fatalf("provider config leaked or omitted secret state: %s", body)
	}

	projectID := newProject(t, ts, "plugin-project")
	missingConsent := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/plugins/gitea/connect", consoleToken, map[string]any{})
	if missingConsent.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing consent: %d want 400", missingConsent.StatusCode)
	}
	missingConsent.Body.Close()

	created := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/plugins/gitea/connect", consoleToken, map[string]any{
		"consent_accepted": true, "consent_version": pluginConsentVersion,
		"scopes": []string{"full"},
	})
	if created.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(created.Body)
		created.Body.Close()
		t.Fatalf("connect: %d body=%s", created.StatusCode, body)
	}
	var started struct {
		Plugin       pluginInstallationView `json:"plugin"`
		AuthorizeURL string                 `json:"authorize_url"`
	}
	decode(t, created, &started)
	plugin := started.Plugin
	if plugin.Status != string("connecting") || plugin.TokenSet || started.AuthorizeURL == "" {
		t.Fatalf("plugin view=%+v", plugin)
	}
	if strings.Join(plugin.Scopes, " ") != "read:user write:repository" {
		t.Fatalf("server trusted client-supplied scopes instead of canonical OAuth scopes: %v", plugin.Scopes)
	}

	list := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/plugins", consoleToken, nil)
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list project plugins: %d", list.StatusCode)
	}
	body, _ = io.ReadAll(list.Body)
	list.Body.Close()
	if strings.Contains(string(body), "access_token") || strings.Contains(string(body), "refresh_token") {
		t.Fatalf("credential fields leaked from list: %s", body)
	}

	disable := do(t, http.MethodPatch, ts.URL+"/api/v1/plugins/"+plugin.ID, consoleToken, map[string]any{"status": "disabled"})
	if disable.StatusCode != http.StatusOK {
		t.Fatalf("disable plugin: %d", disable.StatusCode)
	}
	disable.Body.Close()

	impact := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/plugins/"+plugin.ID+"/impact", consoleToken, nil)
	if impact.StatusCode != http.StatusOK {
		t.Fatalf("plugin impact: %d", impact.StatusCode)
	}
	impact.Body.Close()

	deleted := do(t, http.MethodDelete, ts.URL+"/api/v1/plugins/"+plugin.ID, consoleToken, map[string]any{"confirmation": "UNINSTALL"})
	if deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("delete plugin: %d", deleted.StatusCode)
	}
	deleted.Body.Close()
}

func TestGitHubProjectPluginRejectsRuntimeOAuthToken(t *testing.T) {
	ts, _, _ := newCipherServer(t, nil, "")
	projectID := newProject(t, ts, "github-plugin")
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/plugins/github/connect", consoleToken, map[string]any{
		"consent_accepted": true, "consent_version": pluginConsentVersion, "github_installation_id": "123",
		"access_token": "oauth-token",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("github OAuth runtime token: %d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestProviderIdentityChangeInvalidatesInstallations(t *testing.T) {
	ts, st, _ := newCipherServer(t, nil, "")
	projectID := newProject(t, ts, "provider-change")
	initial := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": "https://old-gitea.example.test", "plugin_enabled": true,
		"client_id": "old-client", "client_secret": "old-secret",
	})
	if initial.StatusCode != http.StatusOK {
		t.Fatalf("initial provider config status=%d", initial.StatusCode)
	}
	initial.Body.Close()
	cfg, err := st.GetProviderConfig(context.Background(), domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: projectID, Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, AccessTokenEnc: []byte("ciphertext"),
		ConfigRevision: cfg.ConfigRevision, CreatedAt: now,
	}
	if err := st.CreatePluginInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": "https://new-gitea.example.test", "plugin_enabled": true,
		"client_id": "new-client", "client_secret": "new-secret",
	})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("provider update status=%d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()
	got, err := st.GetPluginInstallation(context.Background(), installation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.PluginStatusActionRequired || !strings.Contains(got.LastHealthError, "reconnect") {
		t.Fatalf("installation after provider identity change=%+v", got)
	}
}

func TestPluginReconnectReusesInstallation(t *testing.T) {
	ts, st, _ := newCipherServer(t, nil, "")
	configure := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": "https://gitea.example.test", "plugin_enabled": true,
		"client_id": "cloud", "client_secret": "secret",
	})
	if configure.StatusCode != http.StatusOK {
		t.Fatalf("configure status=%d", configure.StatusCode)
	}
	configure.Body.Close()
	projectID := newProject(t, ts, "reconnect")
	connect := func() pluginInstallationView {
		resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/plugins/gitea/connect", consoleToken, map[string]any{
			"consent_accepted": true, "consent_version": pluginConsentVersion,
		})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("connect status=%d", resp.StatusCode)
		}
		var out struct {
			Plugin pluginInstallationView `json:"plugin"`
		}
		decode(t, resp, &out)
		return out.Plugin
	}
	first := connect()
	installation, err := st.GetPluginInstallation(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	installation.Status = domain.PluginStatusActionRequired
	installation.AccessTokenEnc, installation.RefreshTokenEnc = []byte("old-access"), []byte("old-refresh")
	expires := time.Now().Add(time.Hour)
	installation.TokenExpiresAt = &expires
	if err := st.UpdatePluginInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	second := connect()
	if second.ID != first.ID || second.Status != string(domain.PluginStatusConnecting) {
		t.Fatalf("reconnect created/reported wrong installation: first=%+v second=%+v", first, second)
	}
	stored, err := st.GetPluginInstallation(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.AccessTokenEnc) != 0 || len(stored.RefreshTokenEnc) != 0 || stored.TokenExpiresAt != nil {
		t.Fatalf("reconnect retained an old OAuth grant: %+v", stored)
	}
}

func TestJTypeActionRequiredDiscoveryOnlyAllowsInitialWorkspaceSelection(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	srv := New(st, withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t)}), slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	cfg := &domain.ProviderConfig{Provider: domain.PluginJType, BaseURL: "https://jtype.example.test", PluginEnabled: true}
	if err := st.UpsertProviderConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	token, err := srv.Cipher().EncryptString("jtype-grant")
	if err != nil {
		t.Fatal(err)
	}
	in := &domain.PluginInstallation{ID: "jtype", ProjectID: "p", Provider: domain.PluginJType, Status: domain.PluginStatusActionRequired, AccessTokenEnc: token, ConfigRevision: cfg.ConfigRevision}
	if err := st.CreatePluginInstallation(ctx, in); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := srv.pluginJtypeClient(httptest.NewRecorder(), req, in); !ok {
		t.Fatal("fresh JType authorization with no workspace should allow initial discovery")
	}

	// An action_required row with a previously selected workspace is never an
	// initial selection state; it must reconnect instead of browsing with an old
	// token.
	in.WorkspaceID = "ws-old"
	if err := st.UpdatePluginInstallation(ctx, in); err != nil {
		t.Fatal(err)
	}
	blocked := httptest.NewRecorder()
	if _, ok := srv.pluginJtypeClient(blocked, req, in); ok || blocked.Code != http.StatusConflict {
		t.Fatalf("stale action_required workspace discovery ok=%v status=%d", ok, blocked.Code)
	}

	// Provider revision/health changes also block the exception, so the old
	// grant cannot be sent to the replacement JType URL.
	in.WorkspaceID, in.LastHealthError = "", ""
	if err := st.UpdatePluginInstallation(ctx, in); err != nil {
		t.Fatal(err)
	}
	changed := &domain.ProviderConfig{Provider: domain.PluginJType, BaseURL: "https://new-jtype.example.test", PluginEnabled: true}
	if err := st.UpsertProviderConfigAndInvalidate(ctx, changed, true, "reconnect JType after provider URL change"); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.GetPluginInstallation(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocked = httptest.NewRecorder()
	if _, ok := srv.pluginJtypeClient(blocked, req, fresh); ok || blocked.Code != http.StatusConflict {
		t.Fatalf("changed JType config discovery ok=%v status=%d", ok, blocked.Code)
	}
}

func TestProviderProbeRejectsRedirectsAndMetadataDestinations(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirector.Close()
	if err := probeProviderReachability(context.Background(), redirector.URL); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect probe error=%v", err)
	}
	if err := probeProviderReachability(context.Background(), "http://169.254.169.254"); err == nil {
		t.Fatal("metadata destination probe unexpectedly succeeded")
	}
}

func TestGitHubScopeDigestChangesWithActualPermissions(t *testing.T) {
	base := githubScopeDigest("123", []string{"contents:write", "repository_selection:selected"})
	if base == "" || base != githubScopeDigest("123", []string{"contents:write", "repository_selection:selected"}) {
		t.Fatal("scope digest is empty or unstable")
	}
	if base == githubScopeDigest("123", []string{"contents:read", "repository_selection:selected"}) ||
		base == githubScopeDigest("456", []string{"contents:write", "repository_selection:selected"}) {
		t.Fatal("scope digest did not bind installation and actual permissions")
	}
}
