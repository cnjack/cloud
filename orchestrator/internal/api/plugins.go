package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/jtypeoauth"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/safehttp"
	"github.com/cnjack/jcloud/internal/store"
)

type providerConfigView struct {
	Provider            string   `json:"provider"`
	BaseURL             string   `json:"base_url"`
	LoginEnabled        bool     `json:"login_enabled"`
	PluginEnabled       bool     `json:"plugin_enabled"`
	ClientID            string   `json:"client_id,omitempty"`
	ClientSecretSet     bool     `json:"client_secret_set"`
	AppID               string   `json:"app_id,omitempty"`
	AppPrivateKeySet    bool     `json:"app_private_key_set"`
	WebhookSecretSet    bool     `json:"webhook_secret_set"`
	CapabilityVersion   string   `json:"capability_version,omitempty"`
	Capabilities        []string `json:"capabilities"`
	ConfigRevision      int64    `json:"config_revision"`
	LastHealthError     string   `json:"last_health_error,omitempty"`
	LastCapabilityCheck string   `json:"last_capability_check,omitempty"`
	// Console-facing summary fields.  Keep the detailed fields above for the
	// provider detail view, but never make a caller infer secret presence from
	// a missing field.
	Configured    bool   `json:"configured"`
	Health        string `json:"health"`
	HealthMessage string `json:"health_message,omitempty"`
	ClientIDSet   bool   `json:"client_id_set"`
	AppIDSet      bool   `json:"app_id_set"`
}

func providerConfigViewOf(cfg *domain.ProviderConfig) providerConfigView {
	health := "unknown"
	if cfg.LastHealthError != "" {
		health = "error"
	} else if cfg.LastCapabilityCheck != nil {
		health = "healthy"
	}
	v := providerConfigView{Provider: string(cfg.Provider), BaseURL: cfg.BaseURL, LoginEnabled: cfg.LoginEnabled,
		PluginEnabled: cfg.PluginEnabled, ClientID: cfg.ClientID, ClientSecretSet: len(cfg.ClientSecretEnc) > 0,
		AppID: cfg.AppID, AppPrivateKeySet: len(cfg.AppPrivateKeyEnc) > 0, WebhookSecretSet: len(cfg.WebhookSecretEnc) > 0,
		CapabilityVersion: cfg.CapabilityVersion, Capabilities: append([]string(nil), cfg.Capabilities...),
		ConfigRevision: cfg.ConfigRevision, LastHealthError: cfg.LastHealthError,
		Configured: providerConfigComplete(cfg), Health: health, HealthMessage: cfg.LastHealthError,
		ClientIDSet: cfg.ClientID != "", AppIDSet: cfg.AppID != ""}
	if cfg.LastCapabilityCheck != nil {
		v.LastCapabilityCheck = cfg.LastCapabilityCheck.UTC().Format(time.RFC3339)
	}
	return v
}

func providerConfigComplete(cfg *domain.ProviderConfig) bool {
	if cfg == nil || strings.TrimSpace(cfg.BaseURL) == "" {
		return false
	}
	if !cfg.LoginEnabled && !cfg.PluginEnabled {
		return false
	}
	// A login-capable SCM provider needs a complete confidential OAuth client.
	// JType currently uses its own device/MCP authorization, so a cluster JType
	// plugin is configured by a base URL alone.
	if cfg.LoginEnabled && cfg.Provider != domain.PluginJType {
		if strings.TrimSpace(cfg.ClientID) == "" || len(cfg.ClientSecretEnc) == 0 {
			return false
		}
	}
	// GitHub's Project Plugin uses App installation credentials, independently
	// of the OAuth client used for Cloud login. Both capabilities must be
	// complete when they are enabled together.
	if cfg.PluginEnabled && cfg.Provider == domain.PluginGitHub {
		return strings.TrimSpace(cfg.AppID) != "" &&
			len(cfg.AppPrivateKeyEnc) > 0 &&
			len(cfg.WebhookSecretEnc) > 0
	}
	return true
}

func (s *Server) handleListProviderConfigs(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	configs, err := s.st.ListProviderConfigs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list provider configuration")
		return
	}
	out := make([]providerConfigView, 0, len(configs))
	for i := range configs {
		out = append(out, providerConfigViewOf(&configs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// Setup is intentionally readable before login so an empty cluster can route
// the first visitor to setup. Mutation is only accepted while setup is open;
// after completion the normal cluster-admin Provider routes are mandatory.
func (s *Server) handleGetSetup(w http.ResponseWriter, r *http.Request) {
	settings, err := s.st.GetClusterSettings(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"setup_required": true, "public_url": s.setupRequestOrigin(r), "login_provider_count": 0, "providers": []providerConfigView{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load setup status")
		return
	}
	configs, listErr := s.st.ListProviderConfigs(r.Context())
	if listErr != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load provider readiness")
		return
	}
	providers := make([]providerConfigView, 0, len(configs))
	loginCount := 0
	for i := range configs {
		providers = append(providers, providerConfigViewOf(&configs[i]))
		if configs[i].LoginEnabled && providerConfigComplete(&configs[i]) {
			loginCount++
		}
	}
	publicURL := settings.PublicURL
	if !settings.SetupComplete && strings.TrimSpace(publicURL) == "" {
		publicURL = s.setupRequestOrigin(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup_required": !settings.SetupComplete, "public_url": publicURL, "login_provider_count": loginCount, "providers": providers})
}

// setupRequestOrigin is only a suggestion shown in the editable setup form. It
// preserves the browser-facing Host forwarded by the console proxy and accepts
// only the two schemes the setup mutation itself permits.
func (s *Server) setupRequestOrigin(r *http.Request) string {
	scheme := requestScheme(r)
	// Some TLS stacks terminate HTTPS before Kong and only forward `http` to
	// the console. When the configured console entry names this same Host, its
	// scheme is a stronger signal than the final proxy hop.
	if configured, err := url.Parse(strings.TrimSpace(s.cfg.ConsoleURL)); err == nil && configured.Host != "" && strings.EqualFold(configured.Host, r.Host) {
		scheme = configured.Scheme
	}
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	return (&url.URL{Scheme: scheme, Host: host}).String()
}

type putSetupReq struct {
	PublicURL string                `json:"public_url"`
	Provider  *putProviderConfigReq `json:"provider"`
}

func (s *Server) handlePutSetup(w http.ResponseWriter, r *http.Request) {
	current, err := s.st.GetClusterSettings(r.Context())
	if err == nil && current.SetupComplete {
		writeError(w, http.StatusForbidden, "setup_complete", "cluster setup is complete; use a cluster admin to change configuration")
		return
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, "internal", "could not load setup status")
		return
	}
	// The accepted "first visitor" bootstrap applies only to a genuinely empty
	// database. An upgrade that has users but no cluster_settings row must never
	// reopen unauthenticated setup and hand control to an arbitrary visitor.
	userCount, err := s.st.CountUsers(r.Context())
	if err != nil {
		writeError(w, 500, "internal", "could not verify setup eligibility")
		return
	}
	if userCount != 0 {
		writeError(w, http.StatusConflict, "database_recovery_required", "setup is incomplete but users already exist; a database administrator must recover cluster setup")
		return
	}
	var req putSetupReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	publicURL := strings.TrimRight(strings.TrimSpace(req.PublicURL), "/")
	if !strings.HasPrefix(publicURL, "https://") && !strings.HasPrefix(publicURL, "http://localhost") {
		writeError(w, 400, "bad_request", "public_url must be https (or localhost for development)")
		return
	}
	// Setup is one screen and one completion request: accepting a URL first and
	// hoping the visitor can later authenticate would strand a fresh cluster.
	if req.Provider == nil {
		writeError(w, http.StatusConflict, "login_provider_required", "configure and enable one login provider in this setup request")
		return
	}
	providerKind, err := providerKindFromString(req.Provider.Provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if providerKind == domain.PluginJType || !req.Provider.LoginEnabled {
		writeError(w, http.StatusBadRequest, "login_provider_required", "setup requires an enabled GitHub, GitLab, or Gitea login provider")
		return
	}
	cfg, err := s.buildProviderConfig(r.Context(), providerKind, *req.Provider, "")
	if err != nil {
		writeProviderConfigError(w, err)
		return
	}
	if !providerConfigComplete(cfg) {
		writeError(w, http.StatusBadRequest, "bad_request", "a login provider requires base_url, client_id, and client_secret")
		return
	}
	if err := probeProviderReachability(r.Context(), cfg.BaseURL); err != nil {
		writeError(w, http.StatusBadGateway, "provider_unreachable", err.Error())
		return
	}
	now := time.Now().UTC()
	cfg.LastCapabilityCheck, cfg.LastHealthError = &now, ""
	if err := s.st.UpsertProviderConfig(r.Context(), cfg); err != nil {
		s.log.Error("save setup provider config", "provider", providerKind, "err", err)
		writeError(w, 500, "internal", "could not save login provider configuration")
		return
	}
	loginReady := true
	if !loginReady {
		writeError(w, http.StatusConflict, "login_provider_required", "configure and enable at least one login provider before completing setup")
		return
	}
	settings := &domain.ClusterSettings{PublicURL: publicURL, SetupComplete: true, UpdatedAt: time.Now().UTC()}
	if err := s.st.UpsertClusterSettings(r.Context(), settings); err != nil {
		writeError(w, 500, "internal", "could not complete setup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup_required": false, "public_url": publicURL, "login_provider_count": 1, "providers": []providerConfigView{providerConfigViewOf(cfg)}})
}

func (s *Server) handleGetProviderConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	provider := domain.ProviderKind(strings.ToLower(strings.TrimSpace(r.PathValue("provider"))))
	if !domain.ValidProviderKind(provider) {
		writeError(w, http.StatusBadRequest, "bad_request", "provider must be github, gitlab, gitea or jtype")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), provider)
	if errors.Is(err, store.ErrNotFound) {
		baseURL := ""
		if provider == domain.PluginGitHub {
			baseURL = "https://github.com"
		}
		writeJSON(w, http.StatusOK, providerConfigViewOf(&domain.ProviderConfig{Provider: provider, BaseURL: baseURL}))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load provider configuration")
		return
	}
	writeJSON(w, http.StatusOK, providerConfigViewOf(cfg))
}

func (s *Server) handleTestProviderConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	provider := domain.ProviderKind(strings.ToLower(strings.TrimSpace(r.PathValue("provider"))))
	if !domain.ValidProviderKind(provider) {
		writeError(w, 400, "bad_request", "provider must be github, gitlab, gitea or jtype")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), provider)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "provider is not configured")
		return
	} else if err != nil {
		writeError(w, 500, "internal", "could not load provider configuration")
		return
	}
	if err := validateProviderConfig(provider, cfg); err != nil {
		cfg.LastHealthError = err.Error()
		now := time.Now().UTC()
		cfg.LastCapabilityCheck = &now
		_ = s.st.UpsertProviderConfig(r.Context(), cfg)
		writeProviderConfigError(w, err)
		return
	}
	if err := probeProviderReachability(r.Context(), cfg.BaseURL); err != nil {
		cfg.LastHealthError = err.Error()
		now := time.Now().UTC()
		cfg.LastCapabilityCheck = &now
		_ = s.st.UpsertProviderConfig(r.Context(), cfg)
		writeError(w, http.StatusBadGateway, "provider_unreachable", err.Error())
		return
	}
	// Credential validation happens at OAuth consent time; this verifies the
	// configured origin and records the capability-check timestamp.
	now := time.Now().UTC()
	cfg.LastCapabilityCheck = &now
	cfg.LastHealthError = ""
	if err := s.st.UpsertProviderConfig(r.Context(), cfg); err != nil {
		writeError(w, 500, "internal", "could not record provider capability check")
		return
	}
	stored, err := s.st.GetProviderConfig(r.Context(), provider)
	if err != nil {
		writeError(w, 500, "internal", "could not reload provider configuration")
		return
	}
	writeJSON(w, http.StatusOK, providerConfigViewOf(stored))
}

func (s *Server) handleProviderConfigImpact(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	provider := domain.ProviderKind(strings.ToLower(strings.TrimSpace(r.PathValue("provider"))))
	if !domain.ValidProviderKind(provider) {
		writeError(w, 400, "bad_request", "provider must be github, gitlab, gitea or jtype")
		return
	}
	count, err := s.st.CountProviderConfigImpact(r.Context(), provider)
	if err != nil {
		writeError(w, 500, "internal", "could not calculate provider impact")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": provider, "installations": count, "on_save": "mark_action_required"})
}

type putProviderConfigReq struct {
	Provider          string   `json:"provider,omitempty"`
	BaseURL           string   `json:"base_url"`
	LoginEnabled      bool     `json:"login_enabled"`
	PluginEnabled     bool     `json:"plugin_enabled"`
	ClientID          string   `json:"client_id"`
	ClientSecret      string   `json:"client_secret"`
	AppID             string   `json:"app_id"`
	AppPrivateKey     string   `json:"app_private_key"`
	WebhookSecret     string   `json:"webhook_secret"`
	CapabilityVersion string   `json:"capability_version"`
	Capabilities      []string `json:"capabilities"`
}

func providerKindFromString(raw string) (domain.ProviderKind, error) {
	p := domain.ProviderKind(strings.ToLower(strings.TrimSpace(raw)))
	if !domain.ValidProviderKind(p) {
		return "", fmt.Errorf("provider must be github, gitlab, gitea or jtype")
	}
	return p, nil
}

func normalizeProviderURL(raw string, provider domain.ProviderKind) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if provider == domain.PluginGitHub && base == "" {
		return "https://github.com", nil
	}
	if provider == domain.PluginGitHub && base != "https://github.com" {
		return "", fmt.Errorf("github provider configuration is fixed to https://github.com")
	}
	if base == "" {
		return "", fmt.Errorf("base_url is required")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("base_url must be an https origin (or localhost for development)")
	}
	return base, nil
}

func validateProviderConfig(provider domain.ProviderKind, cfg *domain.ProviderConfig) error {
	if cfg == nil {
		return fmt.Errorf("provider configuration is required")
	}
	if _, err := normalizeProviderURL(cfg.BaseURL, provider); err != nil {
		return err
	}
	if cfg.LoginEnabled && provider == domain.PluginJType {
		return fmt.Errorf("jtype cannot be enabled as a login provider")
	}
	if cfg.LoginEnabled && (strings.TrimSpace(cfg.ClientID) == "" || len(cfg.ClientSecretEnc) == 0) {
		return fmt.Errorf("a login provider requires client_id and client_secret")
	}
	if !cfg.LoginEnabled && !cfg.PluginEnabled {
		return fmt.Errorf("enable login or plugin capability before saving a provider")
	}
	return nil
}

// probeProviderReachability verifies the configured origin before an empty
// cluster is declared ready. OAuth credentials are checked only during human
// consent, but an unreachable origin would otherwise leave no login path. A
// non-5xx response is sufficient: provider roots commonly answer 401/404.
func probeProviderReachability(ctx context.Context, baseURL string) error {
	if _, parseErr := url.Parse(baseURL); parseErr != nil {
		return fmt.Errorf("invalid provider URL: %w", parseErr)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, baseURL, nil)
	if err != nil {
		return fmt.Errorf("invalid provider URL: %w", err)
	}
	hc := safehttp.NewProviderClient(baseURL, 5*time.Second)
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("provider origin is unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("provider origin returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func writeProviderConfigError(w http.ResponseWriter, err error) {
	if strings.Contains(err.Error(), "JCLOUD_MASTER_KEY") {
		writeError(w, http.StatusConflict, "cipher_not_configured", err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, "bad_request", err.Error())
}

// buildProviderConfig is shared by bootstrap and admin settings. It applies
// full-resource PUT semantics while preserving write-only values when a caller
// intentionally leaves their replacement field empty.
func (s *Server) buildProviderConfig(ctx context.Context, provider domain.ProviderKind, req putProviderConfigReq, updatedBy string) (*domain.ProviderConfig, error) {
	var cfg domain.ProviderConfig
	existing, err := s.st.GetProviderConfig(ctx, provider)
	if err == nil {
		cfg = *existing
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("could not load existing provider configuration: %w", err)
	}
	base, err := normalizeProviderURL(req.BaseURL, provider)
	if err != nil {
		return nil, err
	}
	cfg.Provider, cfg.BaseURL, cfg.LoginEnabled, cfg.PluginEnabled = provider, base, req.LoginEnabled, req.PluginEnabled
	cfg.ClientID, cfg.AppID = strings.TrimSpace(req.ClientID), strings.TrimSpace(req.AppID)
	cfg.CapabilityVersion = strings.TrimSpace(req.CapabilityVersion)
	cfg.Capabilities = []string{}
	cfg.Capabilities = append(cfg.Capabilities, req.Capabilities...)
	if req.ClientSecret != "" || req.AppPrivateKey != "" || req.WebhookSecret != "" {
		if s.cipher == nil {
			return nil, fmt.Errorf("set JCLOUD_MASTER_KEY before storing provider secrets")
		}
		if req.ClientSecret != "" {
			if cfg.ClientSecretEnc, err = s.cipher.EncryptString(req.ClientSecret); err != nil {
				return nil, fmt.Errorf("could not encrypt provider client secret: %w", err)
			}
		}
		if req.AppPrivateKey != "" {
			if cfg.AppPrivateKeyEnc, err = s.cipher.EncryptString(req.AppPrivateKey); err != nil {
				return nil, fmt.Errorf("could not encrypt GitHub App private key: %w", err)
			}
		}
		if req.WebhookSecret != "" {
			if cfg.WebhookSecretEnc, err = s.cipher.EncryptString(req.WebhookSecret); err != nil {
				return nil, fmt.Errorf("could not encrypt webhook secret: %w", err)
			}
		}
	}
	cfg.UpdatedBy = updatedBy
	if err := validateProviderConfig(provider, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// handlePutProviderConfig keeps secrets write-only. Empty secret fields retain
// an existing encrypted value, which prevents a read-modify-save in the Console
// from silently clearing a credential.
func (s *Server) handlePutProviderConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	provider, err := providerKindFromString(r.PathValue("provider"))
	if err != nil {
		writeProviderConfigError(w, err)
		return
	}
	var req putProviderConfigReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	previous, previousErr := s.st.GetProviderConfig(r.Context(), provider)
	if previousErr != nil && !errors.Is(previousErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "could not load current provider configuration")
		return
	}
	cfg, err := s.buildProviderConfig(r.Context(), provider, req, principalFrom(r.Context()).userID())
	if err != nil {
		writeProviderConfigError(w, err)
		return
	}
	invalidate := previousErr == nil && (previous.BaseURL != cfg.BaseURL ||
		previous.ClientID != cfg.ClientID ||
		previous.AppID != cfg.AppID ||
		strings.TrimSpace(req.ClientSecret) != "" ||
		strings.TrimSpace(req.AppPrivateKey) != "" ||
		(previous.PluginEnabled && !cfg.PluginEnabled))
	reason := ""
	if invalidate {
		reason = "Cluster Provider identity, URL, credentials, or Plugin availability changed; reconnect this Project Plugin"
	}
	if err := s.st.UpsertProviderConfigAndInvalidate(r.Context(), cfg, invalidate, reason); err != nil {
		writeError(w, 500, "internal", "could not save provider configuration")
		return
	}
	stored, err := s.st.GetProviderConfig(r.Context(), provider)
	if err != nil {
		writeError(w, 500, "internal", "provider configuration saved but could not be read")
		return
	}
	writeJSON(w, http.StatusOK, providerConfigViewOf(stored))
}

type pluginInstallationView struct {
	ID                string   `json:"id"`
	ProjectID         string   `json:"project_id"`
	Provider          string   `json:"provider"`
	Status            string   `json:"status"`
	ExternalAccountID string   `json:"external_account_id,omitempty"`
	ExternalAccount   string   `json:"external_account,omitempty"`
	WorkspaceID       string   `json:"workspace_id,omitempty"`
	Scopes            []string `json:"scopes"`
	TokenSet          bool     `json:"token_set"`
	ConsentVersion    string   `json:"consent_version"`
	ConsentedAt       string   `json:"consented_at,omitempty"`
	LastHealthError   string   `json:"last_health_error,omitempty"`
	LastHealthyAt     string   `json:"last_healthy_at,omitempty"`
	ServiceCount      int      `json:"service_count"`
	AutomationCount   int      `json:"automation_count"`
}

func pluginInstallationViewOf(in *domain.PluginInstallation) pluginInstallationView {
	v := pluginInstallationView{ID: in.ID, ProjectID: in.ProjectID, Provider: string(in.Provider), Status: string(in.Status), ExternalAccountID: in.ExternalAccountID, ExternalAccount: in.ExternalAccount, WorkspaceID: in.WorkspaceID, Scopes: append([]string(nil), in.Scopes...), TokenSet: in.TokenSet(), ConsentVersion: in.ConsentVersion, LastHealthError: in.LastHealthError}
	if !in.ConsentedAt.IsZero() {
		v.ConsentedAt = in.ConsentedAt.UTC().Format(time.RFC3339)
	}
	if in.LastHealthyAt != nil {
		v.LastHealthyAt = in.LastHealthyAt.UTC().Format(time.RFC3339)
	}
	return v
}

func (s *Server) handleListProjectPlugins(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleViewer) {
		return
	}
	items, err := s.st.ListPluginInstallationsByProject(r.Context(), projectID)
	if err != nil {
		writeError(w, 500, "internal", "could not list project plugins")
		return
	}
	out := make([]pluginInstallationView, 0, len(items))
	for i := range items {
		view := pluginInstallationViewOf(&items[i])
		view.ServiceCount, view.AutomationCount, err = s.st.CountPluginInstallationImpact(r.Context(), items[i].ID)
		if err != nil {
			writeError(w, 500, "internal", "could not calculate project plugin summary")
			return
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": out})
}

// pluginMember resolves a project's installation for read-only resource
// discovery. Viewers can inspect installed resources; only owners can mutate them.
func (s *Server) pluginMember(w http.ResponseWriter, r *http.Request) (*domain.PluginInstallation, bool) {
	in, err := s.st.GetPluginInstallation(r.Context(), r.PathValue("installation"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && in.ProjectID != r.PathValue("id")) {
		writeError(w, 404, "not_found", "project plugin not found")
		return nil, false
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load project plugin")
		return nil, false
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), in.ProjectID, domain.RoleViewer) {
		return nil, false
	}
	if in.Status != domain.PluginStatusEnabled &&
		!s.jtypeInitialWorkspaceDiscoveryAllowed(r.Context(), in) {
		writeError(w, 409, "plugin_unavailable", "this project plugin is not enabled; reconnect or enable it first")
		return nil, false
	}
	return in, true
}

// jtypeInitialWorkspaceDiscoveryAllowed is the sole intentional exception to
// the enabled-only discovery rule: a freshly authorized JType grant must list
// workspaces before its owner can select the one workspace for the Project.
// Never use an action_required Installation after a config revision/error, or
// its old token could be sent to a newly configured JType URL.
func (s *Server) jtypeInitialWorkspaceDiscoveryAllowed(ctx context.Context, in *domain.PluginInstallation) bool {
	if in == nil || in.Provider != domain.PluginJType ||
		in.Status != domain.PluginStatusActionRequired ||
		strings.TrimSpace(in.WorkspaceID) != "" ||
		strings.TrimSpace(in.LastHealthError) != "" {
		return false
	}
	cfg, err := s.st.GetProviderConfig(ctx, domain.PluginJType)
	return err == nil && cfg.PluginEnabled && strings.TrimSpace(cfg.BaseURL) != "" &&
		cfg.ConfigRevision == in.ConfigRevision
}

func (s *Server) pluginAccessToken(in *domain.PluginInstallation) (string, bool) {
	if s.cipher == nil || len(in.AccessTokenEnc) == 0 {
		return "", false
	}
	token, err := s.cipher.DecryptString(in.AccessTokenEnc)
	if err != nil || token == "" {
		return "", false
	}
	return token, true
}

func (s *Server) handleListPluginRepositories(w http.ResponseWriter, r *http.Request) {
	in, ok := s.pluginMember(w, r)
	if !ok {
		return
	}
	if in.Provider == domain.PluginJType {
		writeError(w, 400, "bad_request", "jtype plugins do not expose repositories")
		return
	}
	if in.Provider == domain.PluginGitHub {
		s.listGitHubInstallationRepositories(w, r, in)
		return
	}
	token, tokenOK := s.pluginAccessToken(in)
	if !tokenOK {
		writeError(w, 409, "plugin_credential_unavailable", "the plugin credential is unavailable; reconnect it")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), in.Provider)
	if err != nil {
		writeError(w, 409, "provider_not_configured", "provider configuration is unavailable")
		return
	}
	if s.pluginCredentialIssuer == nil {
		writeError(w, 409, "plugin_credential_unavailable", "the plugin credential issuer is unavailable")
		return
	}
	credential, err := s.pluginCredentialIssuer.IssueRunPluginCredential(r.Context(), in, cfg)
	if err != nil {
		writeError(w, 409, "plugin_credential_unavailable", "the plugin credential is unavailable; reconnect it")
		return
	}
	token = credential.AccessToken
	client, err := provider.IntegrationClientWithScheme(domain.GitProvider(in.Provider), cfg.BaseURL, token, credential.Scheme)
	if err != nil {
		writeError(w, 502, "provider_unavailable", "could not create provider client")
		return
	}
	lister, ok := client.(provider.RepoLister)
	if !ok {
		writeError(w, 500, "internal", "provider client cannot list repositories")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	repos, err := lister.ListRepos(r.Context(), r.URL.Query().Get("q"), page, 50)
	if err != nil {
		writeError(w, 502, "provider_error", "listing repositories failed: "+summarizeProviderErr(err))
		return
	}
	if repos == nil {
		repos = []provider.Repo{}
	}
	writeJSON(w, 200, map[string]any{"repositories": repos})
}

func (s *Server) githubAppIssuer(ctx context.Context) (*provider.GitHubAppIssuer, error) {
	cfg, err := s.st.GetProviderConfig(ctx, domain.PluginGitHub)
	if err != nil {
		return nil, err
	}
	if !cfg.PluginEnabled || strings.TrimSpace(cfg.AppID) == "" || len(cfg.AppPrivateKeyEnc) == 0 {
		return nil, store.ErrNotFound
	}
	if s.cipher == nil {
		return nil, auth.ErrCipherNotConfigured
	}
	key, err := s.cipher.DecryptString(cfg.AppPrivateKeyEnc)
	if err != nil {
		return nil, err
	}
	return provider.NewGitHubAppIssuer(cfg.AppID, []byte(key))
}

func (s *Server) manageableGitHubAppInstallations(ctx context.Context, userID string, issuer *provider.GitHubAppIssuer) ([]provider.AppInstallation, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("a linked GitHub user identity is required")
	}
	token, err := s.creds.ResolveUserOAuth(ctx, domain.ProviderGitHub, userID)
	if err != nil {
		return nil, err
	}
	return issuer.ListUserInstallations(ctx, token.Value)
}

func (s *Server) handleListGitHubAppInstallations(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), pid, domain.RoleOwner) {
		return
	}
	issuer, err := s.githubAppIssuer(r.Context())
	if err != nil {
		writeError(w, 409, "github_app_not_configured", "configure GitHub App id and private key first")
		return
	}
	items, err := s.manageableGitHubAppInstallations(r.Context(), principalFrom(r.Context()).userID(), issuer)
	if err != nil {
		writeError(w, 409, "github_identity_required", "link and sign in with the GitHub account that manages the App Installation")
		return
	}
	writeJSON(w, 200, map[string]any{"installations": items})
}

func githubPermissionScopes(tok *provider.InstallationToken) []string {
	scopes := make([]string, 0, len(tok.Permissions)+1)
	for name, access := range tok.Permissions {
		scopes = append(scopes, name+":"+access)
	}
	sort.Strings(scopes)
	return append(scopes, "repository_selection:"+tok.RepositorySelection)
}

func githubScopeDigest(installationID string, scopes []string) string {
	sum := sha256.Sum256([]byte(pluginConsentVersion + "\x00" + installationID + "\x00" + strings.Join(scopes, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *Server) manageableGitHubInstallation(ctx context.Context, projectID, installationID, userID string) (*provider.AppInstallation, *provider.GitHubAppIssuer, error) {
	issuer, err := s.githubAppIssuer(ctx)
	if err != nil {
		return nil, nil, err
	}
	manageable, err := s.manageableGitHubAppInstallations(ctx, userID, issuer)
	if err != nil {
		return nil, nil, err
	}
	for i := range manageable {
		if manageable[i].ID == installationID {
			return &manageable[i], issuer, nil
		}
	}
	return nil, issuer, store.ErrNotFound
}

func (s *Server) handlePreviewGitHubAppInstallationConsent(w http.ResponseWriter, r *http.Request) {
	projectID, installationID := r.PathValue("id"), r.PathValue("github_installation")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	if err := provider.ValidateGitHubInstallationID(installationID); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	selected, issuer, err := s.manageableGitHubInstallation(r.Context(), projectID, installationID, principalFrom(r.Context()).userID())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusForbidden, "github_installation_forbidden", "the current GitHub user cannot manage the selected App Installation")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "github_identity_required", "link and sign in with the GitHub account that manages the App Installation")
		return
	}
	tok, err := issuer.IssueInstallationToken(r.Context(), installationID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "github_app_error", "could not inspect the selected GitHub App installation")
		return
	}
	scopes := githubPermissionScopes(&tok)
	writeJSON(w, http.StatusOK, map[string]any{
		"installation_id":      installationID,
		"account":              selected.Account,
		"scopes":               scopes,
		"repository_selection": tok.RepositorySelection,
		"scope_digest":         githubScopeDigest(installationID, scopes),
	})
}

func (s *Server) handleSelectGitHubAppInstallation(w http.ResponseWriter, r *http.Request) {
	pid, id := r.PathValue("id"), r.PathValue("github_installation")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), pid, domain.RoleOwner) {
		return
	}
	if err := provider.ValidateGitHubInstallationID(id); err != nil {
		writeError(w, 400, "bad_request", err.Error())
		return
	}
	var req connectProjectPluginReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if !req.ConsentAccepted || strings.TrimSpace(req.ConsentVersion) != pluginConsentVersion {
		writeError(w, 400, "consent_required", "accept the current project plugin consent before selecting an installation")
		return
	}
	selected, issuer, err := s.manageableGitHubInstallation(r.Context(), pid, id, principalFrom(r.Context()).userID())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 403, "github_installation_forbidden", "the current GitHub user cannot manage the selected App Installation")
		return
	}
	if err != nil {
		writeError(w, 409, "github_identity_required", "link and sign in with the GitHub account that manages the App Installation")
		return
	}
	tok, err := issuer.IssueInstallationToken(r.Context(), id)
	if err != nil {
		writeError(w, 502, "github_app_error", "could not validate the selected GitHub App installation")
		return
	}
	scopes := githubPermissionScopes(&tok)
	if req.ScopeDigest == "" || req.ScopeDigest != githubScopeDigest(id, scopes) {
		writeError(w, http.StatusConflict, "consent_scope_changed", "GitHub App permissions changed or were not previewed; review the current permissions and consent again")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), domain.PluginGitHub)
	if err != nil || !cfg.PluginEnabled {
		writeError(w, http.StatusConflict, "github_app_not_configured", "configure and enable the GitHub App first")
		return
	}
	now := time.Now().UTC()
	in, getErr := s.st.GetPluginInstallationForProject(r.Context(), pid, domain.PluginGitHub)
	isReconnect := getErr == nil
	if errors.Is(getErr, store.ErrNotFound) {
		in = &domain.PluginInstallation{ID: domain.NewID(), ProjectID: pid, Provider: domain.PluginGitHub, CreatedAt: now}
	} else if getErr != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load GitHub Plugin")
		return
	}
	// A reconnect must not retain an unrelated OAuth/device grant that happened
	// to be stored on an older installation record.
	in.AccessTokenEnc, in.RefreshTokenEnc, in.TokenExpiresAt = nil, nil, nil
	in.Status, in.GitHubInstallID = domain.PluginStatusEnabled, id
	in.ExternalAccountID, in.ExternalAccount = selected.AccountID, selected.Account
	in.Scopes, in.ConsentVersion = scopes, pluginConsentVersion
	in.ConsentedBy, in.ConsentedAt = principalFrom(r.Context()).userID(), now
	in.ConfigRevision, in.LastHealthError, in.LastHealthyAt = cfg.ConfigRevision, "", &now
	var saveErr error
	if isReconnect {
		saveErr = s.st.UpdatePluginInstallation(r.Context(), in)
	} else {
		saveErr = s.st.CreatePluginInstallation(r.Context(), in)
	}
	if saveErr != nil {
		writeError(w, 500, "internal", "could not save GitHub App selection")
		return
	}
	_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{
		ID: domain.NewID(), ProjectID: pid, InstallationID: in.ID,
		ActorUserID: in.ConsentedBy, EventType: "consent_accepted",
		Detail: pluginConsentVersion, CreatedAt: now,
	})
	_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{
		ID: domain.NewID(), ProjectID: pid, InstallationID: in.ID,
		ActorUserID: in.ConsentedBy, EventType: "connected",
		Detail: "github_installation=" + id, CreatedAt: now,
	})
	if isReconnect {
		_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{
			ID: domain.NewID(), ProjectID: pid, InstallationID: in.ID,
			ActorUserID: in.ConsentedBy, EventType: "reconnected",
			Detail: "github_installation=" + id, CreatedAt: now,
		})
	}
	writeJSON(w, 201, pluginInstallationViewOf(in))
}
func (s *Server) listGitHubInstallationRepositories(w http.ResponseWriter, r *http.Request, in *domain.PluginInstallation) {
	issuer, err := s.githubAppIssuer(r.Context())
	if err != nil {
		writeError(w, 409, "github_app_not_configured", "configure GitHub App first")
		return
	}
	issued, err := issuer.IssueInstallationToken(r.Context(), in.GitHubInstallID)
	if err != nil {
		writeError(w, 502, "github_app_error", "could not mint GitHub installation token")
		return
	}
	client, err := provider.IntegrationClient(domain.ProviderGitHub, "https://github.com", issued.Token)
	if err != nil {
		writeError(w, 500, "internal", "could not create GitHub client")
		return
	}
	lister := client.(provider.RepoLister)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	repos, err := lister.ListRepos(r.Context(), r.URL.Query().Get("q"), page, 50)
	if err != nil {
		writeError(w, 502, "provider_error", "listing repositories failed")
		return
	}
	writeJSON(w, 200, map[string]any{"repositories": repos})
}

func (s *Server) pluginJtypeClient(w http.ResponseWriter, r *http.Request, in *domain.PluginInstallation) (jtypeDiscovery, bool) {
	if in.Provider != domain.PluginJType {
		writeError(w, 400, "bad_request", "this plugin is not jtype")
		return nil, false
	}
	token, tokenOK := s.pluginAccessToken(in)
	if !tokenOK {
		writeError(w, 409, "plugin_credential_unavailable", "the jtype plugin credential is unavailable; reconnect it")
		return nil, false
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), domain.PluginJType)
	if err != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" {
		writeError(w, 409, "provider_not_configured", "jtype provider configuration is unavailable")
		return nil, false
	}
	if cfg.ConfigRevision != in.ConfigRevision ||
		(in.Status != domain.PluginStatusEnabled && !s.jtypeInitialWorkspaceDiscoveryAllowed(r.Context(), in)) {
		writeError(w, 409, "plugin_reconnect_required", "the JType Plugin configuration changed; reconnect before browsing resources")
		return nil, false
	}
	f := jtype.NewFactory(cfg.BaseURL, 20*time.Second)
	return s.jtypeDiscoveryFor(f, token), true
}

func (s *Server) handleListPluginWorkspaces(w http.ResponseWriter, r *http.Request) {
	in, ok := s.pluginMember(w, r)
	if !ok {
		return
	}
	client, ok := s.pluginJtypeClient(w, r, in)
	if !ok {
		return
	}
	wss, err := client.ListWorkspaces(r.Context())
	if err != nil {
		s.writeDiscoveryError(w, "", err)
		return
	}
	out := make([]jtypeWorkspaceView, 0, len(wss))
	for _, ws := range wss {
		out = append(out, jtypeWorkspaceView{ID: ws.ID, Name: ws.Name})
	}
	writeJSON(w, 200, map[string]any{"workspaces": out})
}

func (s *Server) handleListPluginBoards(w http.ResponseWriter, r *http.Request) {
	in, ok := s.pluginMember(w, r)
	if !ok {
		return
	}
	client, ok := s.pluginJtypeClient(w, r, in)
	if !ok {
		return
	}
	workspace := strings.TrimSpace(r.URL.Query().Get("workspace"))
	if workspace == "" {
		workspace = in.WorkspaceID
	}
	if workspace == "" {
		writeError(w, 400, "bad_request", "workspace query parameter is required")
		return
	}
	if in.WorkspaceID != "" && workspace != in.WorkspaceID {
		writeError(w, 403, "forbidden", "workspace is outside this project plugin")
		return
	}
	docs, err := client.ListDocuments(r.Context(), workspace)
	if err != nil {
		s.writeDiscoveryError(w, workspace, err)
		return
	}
	out := []jtypeBoardView{}
	for _, d := range docs {
		if !strings.HasSuffix(strings.ToLower(d.Path), ".board") {
			continue
		}
		board, err := client.GetBoardByDoc(r.Context(), workspace, d.ID)
		if err != nil {
			continue
		}
		view := jtypeBoardView{ID: board.ID, Ref: d.Path, Title: board.Title}
		for _, c := range board.Columns {
			view.Columns = append(view.Columns, jtypeBoardColumnView{Key: c.Key, Name: c.Name})
		}
		out = append(out, view)
	}
	writeJSON(w, 200, map[string]any{"boards": out})
}

type connectProjectPluginReq struct {
	ConsentVersion    string   `json:"consent_version"`
	ConsentAccepted   bool     `json:"consent_accepted"`
	ExternalAccountID string   `json:"external_account_id"`
	ExternalAccount   string   `json:"external_account"`
	GitHubInstallID   string   `json:"github_installation_id"`
	WorkspaceID       string   `json:"workspace_id"`
	Scopes            []string `json:"scopes"`
	ScopeDigest       string   `json:"scope_digest"`
}

const pluginConsentVersion = "plugin-platform-v2-coarse-scope"

const pluginOAuthCookieName = "jcloud_plugin_oauth"

func canonicalPluginConsentScopes(kind domain.ProviderKind) []string {
	switch kind {
	case domain.PluginGitHub:
		return []string{"contents:write", "pull_requests:write", "issues:write", "checks:write", "actions:write", "metadata:read"}
	case domain.PluginGitLab:
		return []string{"read_user", "api"}
	case domain.PluginGitea:
		return []string{"read:user", "write:repository"}
	case domain.PluginJType:
		return []string{"full"}
	default:
		return []string{}
	}
}

type pendingPluginOAuth struct {
	Nonce          string `json:"nonce"`
	Provider       string `json:"provider"`
	InstallationID string `json:"installation_id"`
	ProjectID      string `json:"project_id"`
	UserID         string `json:"user_id"`
}

func (s *Server) pluginOAuthProvider(ctx context.Context, kind domain.ProviderKind) (provider.OAuthProvider, error) {
	if kind != domain.PluginGitLab && kind != domain.PluginGitea {
		return nil, errors.New("provider OAuth installation is not implemented")
	}
	if s.cipher == nil {
		return nil, auth.ErrCipherNotConfigured
	}
	cfg, err := s.st.GetProviderConfig(ctx, kind)
	if err != nil {
		return nil, err
	}
	if !cfg.PluginEnabled || cfg.ClientID == "" || len(cfg.ClientSecretEnc) == 0 || cfg.BaseURL == "" {
		return nil, errors.New("provider plugin OAuth is not configured")
	}
	secret, err := s.cipher.Decrypt(cfg.ClientSecretEnc)
	if err != nil {
		return nil, err
	}
	oauthCfg := provider.OAuthConfig{ClientID: cfg.ClientID, ClientSecret: string(secret), ExternalURL: cfg.BaseURL, InternalURL: cfg.BaseURL}
	if kind == domain.PluginGitLab {
		return provider.NewGitLabOAuth(oauthCfg), nil
	}
	return provider.NewGiteaOAuth(oauthCfg), nil
}

// handleConnectProjectPlugin only records Consent and enters connecting state.
// Browser requests never carry access/refresh tokens: OAuth/App callbacks must
// finalize the installation server-side after independently validating identity.
func (s *Server) handleConnectProjectPlugin(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	provider := domain.ProviderKind(strings.ToLower(strings.TrimSpace(r.PathValue("provider"))))
	if !domain.ValidProviderKind(provider) {
		writeError(w, 400, "bad_request", "provider must be github, gitlab, gitea or jtype")
		return
	}
	var req connectProjectPluginReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if !req.ConsentAccepted || strings.TrimSpace(req.ConsentVersion) != pluginConsentVersion {
		writeError(w, 400, "consent_required", "accept the current project plugin consent before connecting")
		return
	}
	if provider == domain.PluginJType {
		s.startJTypePluginConnect(w, r, projectID, &req)
		return
	}
	if provider != domain.PluginGitLab && provider != domain.PluginGitea {
		writeError(w, http.StatusNotImplemented, "plugin_connect_not_implemented", "this provider's server-side install flow is not wired yet")
		return
	}
	prov, err := s.pluginOAuthProvider(r.Context(), provider)
	if errors.Is(err, auth.ErrCipherNotConfigured) {
		writeError(w, 409, "cipher_not_configured", "set JCLOUD_MASTER_KEY before authorizing a project plugin")
		return
	}
	if errors.Is(err, store.ErrNotFound) || err != nil {
		writeError(w, 409, "provider_not_configured", "configure this provider's OAuth client on the Cluster page first")
		return
	}
	cfg, cfgErr := s.st.GetProviderConfig(r.Context(), provider)
	if cfgErr != nil || !cfg.PluginEnabled {
		writeError(w, http.StatusConflict, "provider_not_configured", "configure and enable this Provider Plugin first")
		return
	}
	now := time.Now().UTC()
	in, getErr := s.st.GetPluginInstallationForProject(r.Context(), projectID, provider)
	isReconnect := getErr == nil
	if errors.Is(getErr, store.ErrNotFound) {
		in = &domain.PluginInstallation{ID: domain.NewID(), ProjectID: projectID, Provider: provider, CreatedAt: now}
	} else if getErr != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project plugin")
		return
	}
	// Persist the reconnect boundary before redirecting to OAuth. A failed or
	// abandoned flow must not leave the former grant usable as if it belonged to
	// the new provider configuration/identity.
	if isReconnect {
		in.AccessTokenEnc, in.RefreshTokenEnc, in.TokenExpiresAt = nil, nil, nil
	}
	in.Status = domain.PluginStatusConnecting
	in.Scopes = canonicalPluginConsentScopes(provider)
	in.ConsentVersion = strings.TrimSpace(req.ConsentVersion)
	in.ConsentedBy, in.ConsentedAt = principalFrom(r.Context()).userID(), now
	in.ConfigRevision, in.LastHealthError = cfg.ConfigRevision, ""
	var saveErr error
	if isReconnect {
		saveErr = s.st.UpdatePluginInstallation(r.Context(), in)
	} else {
		saveErr = s.st.CreatePluginInstallation(r.Context(), in)
	}
	if saveErr != nil {
		writeError(w, 500, "internal", "could not create project plugin")
		return
	}
	eventType := "consent_accepted"
	if isReconnect {
		eventType = "reconnect_started"
	}
	_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{ID: domain.NewID(), ProjectID: projectID, InstallationID: in.ID, ActorUserID: in.ConsentedBy, EventType: eventType, Detail: in.ConsentVersion, CreatedAt: now})
	nonce := randToken()
	pending := pendingPluginOAuth{Nonce: nonce, Provider: string(provider), InstallationID: in.ID, ProjectID: projectID, UserID: in.ConsentedBy}
	raw, _ := json.Marshal(pending)
	sealed, err := s.cipher.Encrypt(raw)
	if err != nil {
		writeError(w, 500, "internal", "could not protect plugin OAuth state")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: pluginOAuthCookieName, Value: base64.RawURLEncoding.EncodeToString(sealed), Path: "/auth", HttpOnly: true, Secure: requestScheme(r) == "https", SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: nonce, Path: "/auth", HttpOnly: true, Secure: requestScheme(r) == "https", SameSite: http.SameSiteLaxMode, MaxAge: 600})
	state := s.signState(oauthState{Nonce: nonce, Provider: string(provider), Mode: oauthModePlugin, UserID: in.ConsentedBy})
	writeJSON(w, http.StatusAccepted, map[string]any{"plugin": pluginInstallationViewOf(in), "authorize_url": prov.AuthorizeURL(state, s.callbackRedirectURI(r, string(provider)))})
}

func (s *Server) startJTypePluginConnect(w http.ResponseWriter, r *http.Request, projectID string, req *connectProjectPluginReq) {
	if s.cipher == nil {
		writeError(w, http.StatusConflict, "cipher_not_configured", "set JCLOUD_MASTER_KEY before authorizing a JType plugin")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), domain.PluginJType)
	if err != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" ||
		strings.TrimSpace(cfg.ClientID) == "" || len(cfg.ClientSecretEnc) == 0 {
		writeError(w, http.StatusConflict, "provider_not_configured", "configure the JType URL and OAuth client on the Cluster page first")
		return
	}
	secret, err := s.cipher.DecryptString(cfg.ClientSecretEnc)
	if err != nil || secret == "" {
		writeError(w, http.StatusConflict, "provider_not_configured", "the JType OAuth client secret is unavailable")
		return
	}
	now := time.Now().UTC()
	in, getErr := s.st.GetPluginInstallationForProject(r.Context(), projectID, domain.PluginJType)
	isReconnect := getErr == nil
	if errors.Is(getErr, store.ErrNotFound) {
		in = &domain.PluginInstallation{ID: domain.NewID(), ProjectID: projectID, Provider: domain.PluginJType, CreatedAt: now}
	} else if getErr != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load JType project plugin")
		return
	}
	// The workspace is an authorization boundary. Clear it with the old grant
	// before starting a new device flow so a refresh cannot make stale settings
	// look connected to the new authorization.
	if isReconnect {
		in.AccessTokenEnc, in.RefreshTokenEnc, in.TokenExpiresAt = nil, nil, nil
		in.WorkspaceID = ""
	}
	in.Status = domain.PluginStatusConnecting
	in.Scopes = canonicalPluginConsentScopes(domain.PluginJType)
	in.ConsentVersion = strings.TrimSpace(req.ConsentVersion)
	in.ConsentedBy, in.ConsentedAt = principalFrom(r.Context()).userID(), now
	in.ConfigRevision, in.LastHealthError = cfg.ConfigRevision, ""
	var saveErr error
	if isReconnect {
		saveErr = s.st.UpdatePluginInstallation(r.Context(), in)
	} else {
		saveErr = s.st.CreatePluginInstallation(r.Context(), in)
	}
	if saveErr != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create JType project plugin")
		return
	}
	_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{
		ID: domain.NewID(), ProjectID: projectID, InstallationID: in.ID,
		ActorUserID: in.ConsentedBy, EventType: func() string {
			if isReconnect {
				return "reconnect_started"
			}
			return "consent_accepted"
		}(),
		Detail: in.ConsentVersion, CreatedAt: now,
	})
	client := jtypeoauth.NewFullClient(cfg.BaseURL, cfg.ClientID, secret, nil)
	rec := s.startConnectFlowWithClient(w, r, connectSurface{
		kind: surfacePlugin, projectID: projectID, installationID: in.ID,
	}, strings.TrimRight(cfg.BaseURL, "/"), client)
	if rec == nil {
		in.Status = domain.PluginStatusError
		in.LastHealthError = "JType device authorization could not be started"
		_ = s.st.UpdatePluginInstallation(r.Context(), in)
	}
}

func (s *Server) handlePollJTypePluginConnect(w http.ResponseWriter, r *http.Request) {
	projectID, installationID := r.PathValue("id"), r.PathValue("installation")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	rec := s.connects.get(r.PathValue("connectID"))
	if rec == nil || rec.surface.kind != surfacePlugin || rec.surface.projectID != projectID ||
		rec.surface.installationID != installationID ||
		rec.principal != principalIdentity(principalFrom(r.Context())) {
		writeError(w, http.StatusNotFound, "connect_expired", "Connection expired — click Connect again.")
		return
	}
	status := s.advanceConnect(r.Context(), rec)
	response := map[string]any{"status": status.Status, "token_set": status.TokenSet}
	if status.TokenExpiresAt != "" {
		response["token_expires_at"] = status.TokenExpiresAt
	}
	if status.Status == connectComplete {
		if in, err := s.st.GetPluginInstallation(r.Context(), installationID); err == nil {
			response["plugin"] = pluginInstallationViewOf(in)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) readPendingPluginOAuth(r *http.Request) (*pendingPluginOAuth, error) {
	cookie, err := r.Cookie(pluginOAuthCookieName)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return nil, err
	}
	raw, err := s.cipher.Decrypt(sealed)
	if err != nil {
		return nil, err
	}
	var pending pendingPluginOAuth
	if err := json.Unmarshal(raw, &pending); err != nil {
		return nil, err
	}
	return &pending, nil
}

// completePluginOAuth is reached only from the signed callback after a server
// side token exchange. The browser never observes the token ciphertext or its
// plaintext. A plugin becomes enabled only after the authenticated provider
// identity has been recorded successfully.
func (s *Server) completePluginOAuth(w http.ResponseWriter, r *http.Request, pending *pendingPluginOAuth, token *provider.OAuthToken, user *provider.OAuthUser) {
	installation, err := s.st.GetPluginInstallation(r.Context(), pending.InstallationID)
	if err != nil || installation.ProjectID != pending.ProjectID || installation.Provider != domain.ProviderKind(pending.Provider) || installation.Status != domain.PluginStatusConnecting {
		s.redirectConsole(w, r, map[string]string{"plugin_error": "invalid_installation"})
		return
	}
	projectUser, err := s.st.GetUser(r.Context(), pending.UserID)
	if err != nil {
		s.redirectConsole(w, r, map[string]string{"plugin_error": "forbidden"})
		return
	}
	role, access, err := s.effectiveRole(r.Context(), &principal{user: projectUser}, pending.ProjectID)
	if err != nil || !access || !role.AtLeast(domain.RoleOwner) {
		s.redirectConsole(w, r, map[string]string{"plugin_error": "forbidden"})
		return
	}
	if token == nil || token.AccessToken == "" {
		s.redirectConsole(w, r, map[string]string{"plugin_error": "missing_token"})
		return
	}
	// OAuth providers may omit refresh_token and expiry on a subsequent grant.
	// Clear every nullable credential field before applying the new response;
	// retaining one from a previous identity would create a mixed grant.
	installation.AccessTokenEnc, installation.RefreshTokenEnc, installation.TokenExpiresAt = nil, nil, nil
	installation.AccessTokenEnc, err = s.cipher.EncryptString(token.AccessToken)
	if err != nil {
		s.redirectConsole(w, r, map[string]string{"plugin_error": "server_error"})
		return
	}
	if token.RefreshToken != "" {
		installation.RefreshTokenEnc, err = s.cipher.EncryptString(token.RefreshToken)
		if err != nil {
			s.redirectConsole(w, r, map[string]string{"plugin_error": "server_error"})
			return
		}
	}
	if !token.Expiry.IsZero() {
		expiry := token.Expiry.UTC()
		installation.TokenExpiresAt = &expiry
	}
	installation.ExternalAccountID, installation.ExternalAccount = user.ProviderUID, user.Username
	installation.Status, installation.LastHealthError = domain.PluginStatusEnabled, ""
	if err := s.st.UpdatePluginInstallation(r.Context(), installation); err != nil {
		s.redirectConsole(w, r, map[string]string{"plugin_error": "persist_failed"})
		return
	}
	_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{ID: domain.NewID(), ProjectID: installation.ProjectID, InstallationID: installation.ID, ActorUserID: pending.UserID, EventType: "connected", Detail: string(installation.Provider), CreatedAt: time.Now().UTC()})
	s.redirectConsole(w, r, map[string]string{"plugin_connected": installation.ID})
}

func (s *Server) pluginOwner(w http.ResponseWriter, r *http.Request, installationID string) (*domain.PluginInstallation, bool) {
	in, err := s.st.GetPluginInstallation(r.Context(), installationID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "project plugin not found")
		return nil, false
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load project plugin")
		return nil, false
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), in.ProjectID, domain.RoleOwner) {
		return nil, false
	}
	return in, true
}

func (s *Server) handlePluginImpact(w http.ResponseWriter, r *http.Request) {
	projectID, installationID := r.PathValue("id"), r.PathValue("installation")
	in, ok := s.pluginOwner(w, r, installationID)
	if !ok {
		return
	}
	if in.ProjectID != projectID {
		writeError(w, 404, "not_found", "project plugin not found")
		return
	}
	services, automations, err := s.st.CountPluginInstallationImpact(r.Context(), installationID)
	if err != nil {
		writeError(w, 500, "internal", "could not calculate plugin impact")
		return
	}
	writeJSON(w, 200, map[string]any{"services": services, "automations": automations})
}

func (s *Server) handlePluginAudit(w http.ResponseWriter, r *http.Request) {
	projectID, installationID := r.PathValue("id"), r.PathValue("installation")
	in, err := s.st.GetPluginInstallation(r.Context(), installationID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && in.ProjectID != projectID) {
		writeError(w, 404, "not_found", "project plugin not found")
		return
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load project plugin")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleViewer) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	events, err := s.st.ListPluginInstallationAuditEvents(r.Context(), projectID, installationID, limit)
	if err != nil {
		writeError(w, 500, "internal", "could not list plugin audit events")
		return
	}
	writeJSON(w, 200, map[string]any{"audit_events": events})
}

type updatePluginReq struct {
	Status      string `json:"status"`
	WorkspaceID string `json:"workspace_id"`
}

func (s *Server) handleUpdateProjectPlugin(w http.ResponseWriter, r *http.Request) {
	in, ok := s.pluginOwner(w, r, r.PathValue("installation"))
	if !ok {
		return
	}
	var req updatePluginReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID != "" {
		if in.Provider != domain.PluginJType || !in.TokenSet() {
			writeError(w, 400, "bad_request", "workspace_id is only valid for an authorized JType plugin")
			return
		}
		client, clientOK := s.pluginJtypeClient(w, r, in)
		if !clientOK {
			return
		}
		workspaces, err := client.ListWorkspaces(r.Context())
		if err != nil {
			s.writeDiscoveryError(w, "", err)
			return
		}
		found := false
		for i := range workspaces {
			if workspaces[i].ID == workspaceID {
				found = true
				break
			}
		}
		if !found {
			writeError(w, 400, "workspace_not_available", "the selected workspace is not available to this JType grant")
			return
		}
		in.WorkspaceID = workspaceID
		in.Status = domain.PluginStatusEnabled
	}
	if strings.TrimSpace(req.Status) != "" {
		status := domain.PluginStatus(strings.TrimSpace(req.Status))
		if status != domain.PluginStatusEnabled && status != domain.PluginStatusDisabled {
			writeError(w, 400, "bad_request", "status must be enabled or disabled")
			return
		}
		if status == domain.PluginStatusEnabled && in.Provider == domain.PluginJType && in.WorkspaceID == "" {
			writeError(w, 409, "workspace_required", "select the JType workspace before enabling this plugin")
			return
		}
		in.Status = status
	}
	if workspaceID == "" && strings.TrimSpace(req.Status) == "" {
		writeError(w, 400, "bad_request", "status or workspace_id is required")
		return
	}
	if err := s.st.UpdatePluginInstallation(r.Context(), in); err != nil {
		writeError(w, 500, "internal", "could not update project plugin")
		return
	}
	detail := string(in.Status)
	if workspaceID != "" {
		detail = "workspace=" + workspaceID
	}
	_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{ID: domain.NewID(), ProjectID: in.ProjectID, InstallationID: in.ID, ActorUserID: principalFrom(r.Context()).userID(), EventType: "configuration_changed", Detail: detail, CreatedAt: time.Now().UTC()})
	writeJSON(w, 200, pluginInstallationViewOf(in))
}

func (s *Server) handleDeleteProjectPlugin(w http.ResponseWriter, r *http.Request) {
	in, ok := s.pluginOwner(w, r, r.PathValue("installation"))
	if !ok {
		return
	}
	var req struct {
		Confirmation string `json:"confirmation"`
		Force        bool   `json:"force"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Confirmation != "UNINSTALL" {
		writeError(w, 400, "confirmation_required", "type UNINSTALL to confirm destructive Plugin removal")
		return
	}
	services, automations, err := s.st.CountPluginInstallationImpact(r.Context(), in.ID)
	if err != nil {
		writeError(w, 500, "internal", "could not calculate plugin impact")
		return
	}
	in.Status = domain.PluginStatusUninstalling
	in.LastHealthError = ""
	if err := s.st.UpdatePluginInstallation(r.Context(), in); err != nil {
		writeError(w, 500, "internal", "could not begin Plugin uninstall")
		return
	}
	dependentServices, err := s.pluginInstallationServices(r.Context(), in)
	if err != nil {
		writeError(w, 500, "internal", "could not load Plugin dependencies")
		return
	}
	lingeringHook := false
	for i := range dependentServices {
		if err := s.removePluginSCMWebhook(r.Context(), &dependentServices[i]); err != nil {
			lingeringHook = true
			if !req.Force {
				in.LastHealthError = "Provider webhook cleanup failed; retry uninstall or explicitly force local removal"
				_ = s.st.UpdatePluginInstallation(r.Context(), in)
				_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{
					ID: domain.NewID(), ProjectID: in.ProjectID, InstallationID: in.ID,
					ActorUserID: principalFrom(r.Context()).userID(), EventType: "uninstall_cleanup_failed",
					Detail: "provider_webhook", CreatedAt: time.Now().UTC(),
				})
				writeError(w, http.StatusBadGateway, "webhook_cleanup_failed", in.LastHealthError)
				return
			}
		}
	}
	for i := range dependentServices {
		if err := s.cleanupPluginServiceRuntime(r.Context(), &dependentServices[i]); err != nil {
			in.LastHealthError = "Runtime cleanup failed; retry uninstall after the cluster is healthy"
			_ = s.st.UpdatePluginInstallation(r.Context(), in)
			writeError(w, http.StatusServiceUnavailable, "cleanup_failed", in.LastHealthError)
			return
		}
	}
	if err := s.st.UninstallPlugin(r.Context(), in.ID); err != nil {
		writeError(w, 500, "internal", "could not uninstall project plugin")
		return
	}
	eventType := "uninstalled"
	detail := "services=" + strconv.Itoa(services) + ",automations=" + strconv.Itoa(automations)
	if req.Force && lingeringHook {
		eventType, detail = "force_uninstalled", detail+",lingering_provider_webhook=true"
	}
	_ = s.st.CreatePluginAuditEvent(r.Context(), &domain.PluginAuditEvent{ID: domain.NewID(), ProjectID: in.ProjectID, ActorUserID: principalFrom(r.Context()).userID(), EventType: eventType, Detail: detail, CreatedAt: time.Now().UTC()})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pluginInstallationServices(ctx context.Context, installation *domain.PluginInstallation) ([]domain.Service, error) {
	services, err := s.st.ListServices(ctx, installation.ProjectID)
	if err != nil {
		return nil, err
	}
	out := []domain.Service{}
	for i := range services {
		binding, bindErr := s.st.GetServiceRepositoryBinding(ctx, services[i].ID)
		if errors.Is(bindErr, store.ErrNotFound) {
			continue
		}
		if bindErr != nil {
			return nil, bindErr
		}
		if binding.InstallationID == installation.ID {
			out = append(out, services[i])
		}
	}
	return out, nil
}

// cleanupPluginServiceRuntime drains durable runs and external runtime resources
// before the uninstall transaction removes Service rows. Database deletion stays
// last, so every failure is retryable from the installation's uninstalling state.
func (s *Server) cleanupPluginServiceRuntime(ctx context.Context, svc *domain.Service) error {
	if err := s.st.MarkServiceDeleting(ctx, svc.ID, time.Now().UTC()); err != nil {
		return err
	}
	runs, err := s.st.ListRunsByService(ctx, svc.ID, -1)
	if err != nil {
		return err
	}
	jobs := map[string]struct{}{k8s.ArchiveJobName(svc.ID): {}}
	for i := range runs {
		committed := &runs[i]
		if !committed.Status.Terminal() && committed.Status != domain.StatusBlocked {
			committed, err = s.st.CancelRun(ctx, committed.ID, "CanceledByPluginUninstall", time.Now().UTC())
			if errors.Is(err, store.ErrInvalidTransition) {
				committed, err = s.st.GetRun(ctx, runs[i].ID)
			}
			if err != nil {
				return err
			}
			s.emitStatus(ctx, committed)
		}
		if committed.K8sJobName != "" {
			jobs[committed.K8sJobName] = struct{}{}
		}
	}
	if s.launcher == nil {
		for name := range jobs {
			if name != k8s.ArchiveJobName(svc.ID) {
				return errors.New("runtime cleanup is unavailable")
			}
		}
	} else {
		for name := range jobs {
			if err := s.launcher.DeleteJob(ctx, name); err != nil {
				return err
			}
		}
		if err := s.launcher.DeleteWorkspacePVC(ctx, svc.ID); err != nil {
			return err
		}
	}
	if s.archiveCleaner != nil {
		if err := s.archiveCleaner.Delete(ctx, "workspaces/"+svc.ID+".tar.zst"); err != nil {
			return err
		}
	} else if svc.ArchiveKey != "" {
		return errors.New("archive cleanup is unavailable")
	}
	return nil
}
