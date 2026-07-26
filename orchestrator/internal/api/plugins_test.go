package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
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
