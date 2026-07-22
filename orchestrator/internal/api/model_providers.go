package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

type providerModelAdminView struct {
	ID                string                   `json:"id"`
	ProviderID        string                   `json:"provider_id"`
	Name              string                   `json:"name"`
	ModelID           string                   `json:"model_id"`
	RuntimeModelName  string                   `json:"runtime_model_name"`
	ContextWindow     int                      `json:"context_window"`
	Capabilities      domain.ModelCapabilities `json:"capabilities"`
	Source            string                   `json:"source"`
	GrantedProjectIDs []string                 `json:"granted_project_ids"`
}

type modelProviderAdminView struct {
	ID                    string                          `json:"id"`
	Name                  string                          `json:"name"`
	Kind                  string                          `json:"kind"`
	BaseURL               string                          `json:"base_url"`
	AuthType              domain.ModelProviderAuthType    `json:"auth_type"`
	APIKeySet             bool                            `json:"api_key_set"`
	HeadersSet            bool                            `json:"headers_set"`
	CatalogMode           domain.ModelProviderCatalogMode `json:"catalog_mode"`
	CatalogAvailable      *bool                           `json:"catalog_available"`
	LastVerifiedAt        *time.Time                      `json:"last_verified_at,omitempty"`
	LastVerificationError string                          `json:"last_verification_error,omitempty"`
	Models                []providerModelAdminView        `json:"models"`
	ProjectGrants         int                             `json:"project_grants"`
	CreatedAt             time.Time                       `json:"created_at"`
	UpdatedAt             time.Time                       `json:"updated_at"`
	UpdatedBy             string                          `json:"updated_by"`
}

func providerModelView(m domain.Model, grants []string) providerModelAdminView {
	if grants == nil {
		grants = []string{}
	}
	return providerModelAdminView{
		ID: m.ID, ProviderID: m.ProviderID, Name: m.Name, ModelID: m.ModelID,
		RuntimeModelName: m.ModelName, ContextWindow: m.ContextWindow,
		Capabilities: m.Capabilities, Source: m.Source, GrantedProjectIDs: grants,
	}
}

func (s *Server) modelProviderView(ctx context.Context, p domain.ModelProvider) (modelProviderAdminView, error) {
	models, err := s.st.ListModelsForProvider(ctx, p.ID)
	if err != nil {
		return modelProviderAdminView{}, err
	}
	modelViews := make([]providerModelAdminView, 0, len(models))
	projects := map[string]struct{}{}
	for _, model := range models {
		grants, err := s.st.ListProjectIDsForModel(ctx, model.ID)
		if err != nil {
			return modelProviderAdminView{}, err
		}
		for _, projectID := range grants {
			projects[projectID] = struct{}{}
		}
		modelViews = append(modelViews, providerModelView(model, grants))
	}
	return modelProviderAdminView{
		ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL,
		AuthType: p.AuthType, APIKeySet: p.APIKeySet(), HeadersSet: p.HeadersSet(), CatalogMode: p.CatalogMode,
		CatalogAvailable: p.CatalogAvailable, LastVerifiedAt: p.LastVerifiedAt,
		LastVerificationError: p.LastVerificationError, Models: modelViews,
		ProjectGrants: len(projects), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		UpdatedBy: p.UpdatedBy,
	}, nil
}

func (s *Server) handleListModelProviders(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	providers, err := s.st.ListModelProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list model providers")
		return
	}
	out := make([]modelProviderAdminView, 0, len(providers))
	for _, provider := range providers {
		view, err := s.modelProviderView(r.Context(), provider)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not list provider models")
			return
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

type createModelProviderReq struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	BaseURL     string            `json:"base_url"`
	AuthType    string            `json:"auth_type"`
	APIKey      string            `json:"api_key"`
	CatalogMode string            `json:"catalog_mode"`
	Headers     map[string]string `json:"headers"`
}

func validProviderKind(kind string) bool {
	if kind == "" {
		return false
	}
	for _, r := range kind {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func parseProviderAuth(raw string) (domain.ModelProviderAuthType, bool) {
	authType := domain.ModelProviderAuthType(raw)
	switch authType {
	case domain.ModelProviderAuthAPIKey, domain.ModelProviderAuthServiceIdentity, domain.ModelProviderAuthNone:
		return authType, true
	default:
		return "", false
	}
}

func parseCatalogMode(raw string) (domain.ModelProviderCatalogMode, bool) {
	mode := domain.ModelProviderCatalogMode(raw)
	switch mode {
	case domain.ModelProviderCatalogAuto, domain.ModelProviderCatalogDisabled:
		return mode, true
	default:
		return "", false
	}
}

func (s *Server) handleCreateModelProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	var req createModelProviderReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if !validProviderKind(kind) {
		writeError(w, http.StatusBadRequest, "bad_request", "kind must be a lowercase provider id")
		return
	}
	if msg, ok := validateBaseURL(baseURL); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	authType, ok := parseProviderAuth(req.AuthType)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "auth_type must be api_key, service_identity, or none")
		return
	}
	catalogMode, ok := parseCatalogMode(req.CatalogMode)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "catalog_mode must be auto or disabled")
		return
	}
	if authType != domain.ModelProviderAuthAPIKey && req.APIKey != "" {
		writeError(w, http.StatusBadRequest, "bad_request", "api_key is only valid with auth_type api_key")
		return
	}
	enc, ok := s.encryptModelKey(w, req.APIKey)
	if !ok {
		return
	}
	if authType != domain.ModelProviderAuthAPIKey {
		enc = nil
	}
	headersEnc, ok := s.encryptModelHeaders(w, req.Headers)
	if !ok {
		return
	}
	now := time.Now().UTC()
	provider := &domain.ModelProvider{
		ID: domain.NewID(), Name: name, Kind: kind, BaseURL: baseURL,
		AuthType: authType, APIKeyEnc: enc, HeadersEnc: headersEnc, CatalogMode: catalogMode,
		CreatedAt: now, UpdatedAt: now, UpdatedBy: principalFrom(r.Context()).userID(),
	}
	if catalogMode == domain.ModelProviderCatalogDisabled {
		available := false
		provider.CatalogAvailable = &available
	}
	if err := s.st.CreateModelProvider(r.Context(), provider); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "a model provider named '"+name+"' already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not create model provider")
		return
	}
	view, err := s.modelProviderView(r.Context(), *provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "provider created, but could not read it back")
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

type updateModelProviderReq struct {
	Name        *string            `json:"name"`
	Kind        *string            `json:"kind"`
	BaseURL     *string            `json:"base_url"`
	AuthType    *string            `json:"auth_type"`
	APIKey      *string            `json:"api_key"`
	CatalogMode *string            `json:"catalog_mode"`
	Headers     *map[string]string `json:"headers"`
}

func (s *Server) handleUpdateModelProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	provider, err := s.st.GetModelProvider(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model provider not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load model provider")
		return
	}
	var req updateModelProviderReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Name != nil {
		provider.Name = strings.TrimSpace(*req.Name)
		if provider.Name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name cannot be empty")
			return
		}
	}
	if req.Kind != nil {
		kind := strings.ToLower(strings.TrimSpace(*req.Kind))
		if !validProviderKind(kind) {
			writeError(w, http.StatusBadRequest, "bad_request", "kind must be a lowercase provider id")
			return
		}
		provider.Kind = kind
	}
	if req.BaseURL != nil {
		baseURL := strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/")
		if msg, ok := validateBaseURL(baseURL); !ok {
			writeError(w, http.StatusBadRequest, "bad_request", msg)
			return
		}
		provider.BaseURL = baseURL
	}
	if req.AuthType != nil {
		authType, ok := parseProviderAuth(*req.AuthType)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "auth_type must be api_key, service_identity, or none")
			return
		}
		provider.AuthType = authType
		if authType != domain.ModelProviderAuthAPIKey {
			provider.APIKeyEnc = nil
		}
	}
	if req.APIKey != nil {
		if provider.AuthType != domain.ModelProviderAuthAPIKey && *req.APIKey != "" {
			writeError(w, http.StatusBadRequest, "bad_request", "api_key is only valid with auth_type api_key")
			return
		}
		enc, ok := s.encryptModelKey(w, *req.APIKey)
		if !ok {
			return
		}
		provider.APIKeyEnc = enc
	}
	if req.CatalogMode != nil {
		mode, ok := parseCatalogMode(*req.CatalogMode)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog_mode must be auto or disabled")
			return
		}
		provider.CatalogMode = mode
	}
	if req.Headers != nil {
		headersEnc, ok := s.encryptModelHeaders(w, *req.Headers)
		if !ok {
			return
		}
		provider.HeadersEnc = headersEnc
	}
	probeChanged := req.Kind != nil || req.BaseURL != nil || req.AuthType != nil || req.APIKey != nil || req.CatalogMode != nil || req.Headers != nil
	if probeChanged {
		provider.CatalogAvailable = nil
		provider.LastVerifiedAt = nil
		provider.LastVerificationError = ""
		if provider.CatalogMode == domain.ModelProviderCatalogDisabled {
			available := false
			provider.CatalogAvailable = &available
		}
	}
	provider.UpdatedBy = principalFrom(r.Context()).userID()
	if err := s.st.UpdateModelProvider(r.Context(), provider); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "a model provider with that name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not update model provider")
		return
	}
	s.models.Invalidate()
	view, err := s.modelProviderView(r.Context(), *provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "provider saved, but could not read it back")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleDeleteModelProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	if err := s.st.DeleteModelProvider(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model provider not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not delete model provider")
		return
	}
	s.models.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

type catalogModelView struct {
	ID            string                   `json:"id"`
	Name          string                   `json:"name,omitempty"`
	Capabilities  domain.ModelCapabilities `json:"capabilities"`
	ContextWindow int                      `json:"context_window"`
}

type openAIModelsEnvelope struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Models []struct {
		ID string `json:"id"`
	} `json:"models"`
}

func (s *Server) providerCredential(p *domain.ModelProvider) (string, error) {
	if p.AuthType != domain.ModelProviderAuthAPIKey {
		return "", nil
	}
	if len(p.APIKeyEnc) == 0 {
		return "", fmt.Errorf("credential is not configured")
	}
	if s.cipher == nil {
		return "", fmt.Errorf("AUTH_TOKEN_KEY is not configured")
	}
	return s.cipher.DecryptString(p.APIKeyEnc)
}

// providerHeaders decrypts the provider's optional custom-header map so the
// verify/catalog probe reaches header-gated gateways, mirroring providerCredential.
// A sealed-but-uncipherable set (no cipher / wrong key) is a fail-visible error,
// never silently dropped. Returns nil when the provider has no custom headers.
func (s *Server) providerHeaders(p *domain.ModelProvider) (map[string]string, error) {
	if len(p.HeadersEnc) == 0 {
		return nil, nil
	}
	if s.cipher == nil {
		return nil, fmt.Errorf("AUTH_TOKEN_KEY is not configured")
	}
	raw, err := s.cipher.DecryptString(p.HeadersEnc)
	if err != nil {
		return nil, err
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, err
	}
	return headers, nil
}

func (s *Server) requestProviderModels(ctx context.Context, p *domain.ModelProvider) (*http.Response, error) {
	credential, err := s.providerCredential(p)
	if err != nil {
		return nil, err
	}
	headers, err := s.providerHeaders(p)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	// Custom headers first; the managed Authorization (below) then wins for a
	// keyed provider, while a keyless provider's custom Authorization survives.
	for k, v := range headers {
		if skipCustomHeader(k) {
			continue
		}
		request.Header.Set(k, v)
	}
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	return s.modelProviderHTTP.Do(request)
}

func decodeProviderCatalog(resp *http.Response) ([]catalogModelView, error) {
	defer resp.Body.Close()
	var envelope openAIModelsEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return nil, err
	}
	items := envelope.Data
	if len(items) == 0 {
		items = envelope.Models
	}
	models := make([]catalogModelView, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, catalogModelView{ID: id})
	}
	return models, nil
}

func (s *Server) recordProviderVerification(ctx context.Context, p *domain.ModelProvider, available *bool, message string) {
	now := time.Now().UTC()
	p.CatalogAvailable = available
	p.LastVerifiedAt = &now
	p.LastVerificationError = message
	p.UpdatedBy = principalFrom(ctx).userID()
	if err := s.st.UpdateModelProvider(ctx, p); err != nil {
		s.log.Warn("record model provider verification", "provider", p.ID, "err", err)
	}
}

func writeProviderResponseError(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		writeError(w, http.StatusConflict, "provider_auth_failed", "the provider rejected its configured credential")
	default:
		if resp.StatusCode >= 500 {
			writeError(w, http.StatusBadGateway, "provider_unavailable", "the provider returned "+resp.Status)
		} else {
			writeError(w, http.StatusConflict, "provider_rejected", "the provider returned "+resp.Status)
		}
	}
}

func (s *Server) handleModelProviderCatalog(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	provider, err := s.st.GetModelProvider(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model provider not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load model provider")
		return
	}
	if provider.CatalogMode == domain.ModelProviderCatalogDisabled {
		writeError(w, http.StatusConflict, "catalog_unavailable", "this provider does not expose a model catalog; add a custom model")
		return
	}
	resp, err := s.requestProviderModels(r.Context(), provider)
	if err != nil {
		code := "provider_unreachable"
		message := "could not reach the model provider"
		if provider.AuthType == domain.ModelProviderAuthAPIKey && !provider.APIKeySet() {
			code = "provider_credential_missing"
			message = "configure the provider API key before browsing its catalog"
		}
		writeError(w, http.StatusConflict, code, message)
		return
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		available := false
		s.recordProviderVerification(r.Context(), provider, &available, "model catalog unavailable")
		resp.Body.Close()
		writeError(w, http.StatusConflict, "catalog_unavailable", "this endpoint does not expose /models; add a custom model")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		available := false
		s.recordProviderVerification(r.Context(), provider, &available, "provider returned "+resp.Status)
		writeProviderResponseError(w, resp)
		return
	}
	models, err := decodeProviderCatalog(resp)
	if err != nil {
		available := false
		s.recordProviderVerification(r.Context(), provider, &available, "invalid model catalog")
		writeError(w, http.StatusBadGateway, "catalog_invalid", "the provider returned an invalid model catalog")
		return
	}
	available := true
	s.recordProviderVerification(r.Context(), provider, &available, "")
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) handleVerifyModelProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	provider, err := s.st.GetModelProvider(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model provider not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load model provider")
		return
	}
	started := time.Now()
	resp, err := s.requestProviderModels(r.Context(), provider)
	if err != nil {
		message := "could not reach the model provider"
		if provider.AuthType == domain.ModelProviderAuthAPIKey && !provider.APIKeySet() {
			writeError(w, http.StatusConflict, "provider_credential_missing", "configure the provider API key before testing it")
			return
		}
		available := false
		s.recordProviderVerification(r.Context(), provider, &available, message)
		writeError(w, http.StatusBadGateway, "provider_unreachable", message)
		return
	}
	latency := time.Since(started).Milliseconds()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		resp.Body.Close()
		available := false
		s.recordProviderVerification(r.Context(), provider, &available, "model catalog unavailable")
		writeJSON(w, http.StatusOK, map[string]any{
			"reachable": true, "catalog_available": false, "latency_ms": latency,
		})
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		available := false
		s.recordProviderVerification(r.Context(), provider, &available, "provider returned "+resp.Status)
		writeProviderResponseError(w, resp)
		return
	}
	resp.Body.Close()
	available := true
	s.recordProviderVerification(r.Context(), provider, &available, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"reachable": true, "catalog_available": true, "latency_ms": latency,
	})
}

type createProviderModelReq struct {
	Name          string                   `json:"name"`
	ModelID       string                   `json:"model_id"`
	ContextWindow int                      `json:"context_window"`
	Capabilities  domain.ModelCapabilities `json:"capabilities"`
	Source        string                   `json:"source"`
}

func (s *Server) handleCreateProviderModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	provider, err := s.st.GetModelProvider(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model provider not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load model provider")
		return
	}
	var req createProviderModelReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	modelID := strings.TrimSpace(req.ModelID)
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if modelID == "" || strings.ContainsAny(modelID, "\r\n\t ") {
		writeError(w, http.StatusBadRequest, "bad_request", "model_id is required and cannot contain whitespace")
		return
	}
	if req.ContextWindow < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "context_window cannot be negative")
		return
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "custom"
	}
	if source != "custom" && source != "catalog" {
		writeError(w, http.StatusBadRequest, "bad_request", "source must be custom or catalog")
		return
	}
	now := time.Now().UTC()
	model := &domain.Model{
		ID: domain.NewID(), ProviderID: provider.ID, Name: name,
		BaseURL: provider.BaseURL, ModelName: provider.Kind + "/" + modelID,
		ModelID: modelID, APIKeyEnc: append([]byte(nil), provider.APIKeyEnc...),
		HeadersEnc:    append([]byte(nil), provider.HeadersEnc...),
		ContextWindow: req.ContextWindow, Capabilities: req.Capabilities, Source: source,
		CreatedAt: now, UpdatedAt: now, UpdatedBy: principalFrom(r.Context()).userID(),
	}
	if err := s.st.CreateModel(r.Context(), model); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "that model is already configured")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not create provider model")
		return
	}
	s.models.Invalidate()
	writeJSON(w, http.StatusCreated, providerModelView(*model, []string{}))
}
