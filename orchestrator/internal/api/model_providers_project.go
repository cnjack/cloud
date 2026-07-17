package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// Project-owned model providers + models (M1). A PROJECT owner manages its own
// providers/models, usable by every service in the project. These handlers mirror
// the cluster catalog handlers (model_providers.go / models.go) but scope every
// read/write to the project (project_id) and gate on the project RBAC: listing is
// member+, all writes are owner. Cluster-global rows (project_id NULL) are NEVER
// reachable here — every {pid}/{mid} handler asserts the row's project_id equals
// the path project (404 otherwise). Credentials stay write-only: api_key and
// headers are echoed only as api_key_set / headers_set, never in plaintext.

// projectProviderModelView is the owner/member view of a project-owned model. Like
// the cluster provider-model view it carries no key material; enabled is the
// per-model on/off toggle.
type projectProviderModelView struct {
	ID               string                   `json:"id"`
	ProviderID       string                   `json:"provider_id"`
	Name             string                   `json:"name"`
	ModelID          string                   `json:"model_id"`
	RuntimeModelName string                   `json:"runtime_model_name"`
	ContextWindow    int                      `json:"context_window"`
	Capabilities     domain.ModelCapabilities `json:"capabilities"`
	Source           string                   `json:"source"`
	Enabled          bool                     `json:"enabled"`
}

func projectProviderModelViewOf(m domain.Model) projectProviderModelView {
	return projectProviderModelView{
		ID: m.ID, ProviderID: m.ProviderID, Name: m.Name, ModelID: m.ModelID,
		RuntimeModelName: m.ModelName, ContextWindow: m.ContextWindow,
		Capabilities: m.Capabilities, Source: m.Source, Enabled: m.Enabled,
	}
}

// projectModelProviderView is the owner/member view of a project-owned provider.
// base_url is visible to owners/members (it is not a secret); the api_key and
// custom headers are NEVER serialised — only api_key_set / headers_set.
type projectModelProviderView struct {
	ID                    string                          `json:"id"`
	ProjectID             string                          `json:"project_id"`
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
	Models                []projectProviderModelView      `json:"models"`
	CreatedAt             time.Time                       `json:"created_at"`
	UpdatedAt             time.Time                       `json:"updated_at"`
	UpdatedBy             string                          `json:"updated_by"`
}

func (s *Server) projectModelProviderView(ctx context.Context, p domain.ModelProvider) (projectModelProviderView, error) {
	models, err := s.st.ListModelsForProvider(ctx, p.ID)
	if err != nil {
		return projectModelProviderView{}, err
	}
	modelViews := make([]projectProviderModelView, 0, len(models))
	for _, model := range models {
		modelViews = append(modelViews, projectProviderModelViewOf(model))
	}
	return projectModelProviderView{
		ID: p.ID, ProjectID: p.ProjectID, Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL,
		AuthType: p.AuthType, APIKeySet: p.APIKeySet(), HeadersSet: p.HeadersSet(),
		CatalogMode: p.CatalogMode, CatalogAvailable: p.CatalogAvailable,
		LastVerifiedAt: p.LastVerifiedAt, LastVerificationError: p.LastVerificationError,
		Models: modelViews, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, UpdatedBy: p.UpdatedBy,
	}, nil
}

// projectOwnedProvider loads the {pid} provider and asserts it is OWNED by the
// path project. A cluster-global provider (project_id "") or one owned by another
// project is a 404 — a project owner can never reach outside its own scope. On
// failure it writes the response and returns ok=false; the caller must stop.
func (s *Server) projectOwnedProvider(w http.ResponseWriter, r *http.Request, projectID string) (*domain.ModelProvider, bool) {
	provider, err := s.st.GetModelProvider(r.Context(), r.PathValue("pid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && provider.ProjectID != projectID) {
		writeError(w, http.StatusNotFound, "not_found", "model provider not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load model provider")
		return nil, false
	}
	return provider, true
}

// projectOwnedModel loads the {mid} model and asserts it belongs to the given
// (already ownership-checked) provider. Any other id is a 404.
func (s *Server) projectOwnedModel(w http.ResponseWriter, r *http.Request, provider *domain.ModelProvider) (*domain.Model, bool) {
	model, err := s.st.GetModel(r.Context(), r.PathValue("mid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && model.ProviderID != provider.ID) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load model")
		return nil, false
	}
	return model, true
}

// handleListProjectModelProviders lists a project's own providers with their
// nested models (member+). Never carries the api_key or custom headers — only
// api_key_set / headers_set.
func (s *Server) handleListProjectModelProviders(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleMember) {
		return
	}
	providers, err := s.st.ListModelProvidersForProject(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list model providers")
		return
	}
	out := make([]projectModelProviderView, 0, len(providers))
	for _, provider := range providers {
		view, err := s.projectModelProviderView(r.Context(), provider)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not list provider models")
			return
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// createProjectModelProviderReq mirrors the cluster create body plus optional
// custom headers (write-only; stored encrypted, echoed only as headers_set).
type createProjectModelProviderReq struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	BaseURL     string            `json:"base_url"`
	AuthType    string            `json:"auth_type"`
	APIKey      string            `json:"api_key"`
	Headers     map[string]string `json:"headers"`
	CatalogMode string            `json:"catalog_mode"`
}

func (s *Server) handleCreateProjectModelProvider(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	var req createProjectModelProviderReq
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
	if msg, ok := validateProviderHeaders(req.Headers); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
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
		ID: domain.NewID(), ProjectID: projectID, Name: name, Kind: kind, BaseURL: baseURL,
		AuthType: authType, APIKeyEnc: enc, HeadersEnc: headersEnc, CatalogMode: catalogMode,
		CreatedAt: now, UpdatedAt: now, UpdatedBy: principalFrom(r.Context()).userID(),
	}
	if catalogMode == domain.ModelProviderCatalogDisabled {
		available := false
		provider.CatalogAvailable = &available
	}
	if err := s.st.CreateModelProvider(r.Context(), provider); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "a model provider named '"+name+"' already exists in this project")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not create model provider")
		return
	}
	s.models.Invalidate()
	view, err := s.projectModelProviderView(r.Context(), *provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "provider created, but could not read it back")
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// updateProjectModelProviderReq is the PATCH body; every field is optional
// (nil = unchanged). headers, when present, replaces the stored set (empty object
// clears them).
type updateProjectModelProviderReq struct {
	Name        *string            `json:"name"`
	Kind        *string            `json:"kind"`
	BaseURL     *string            `json:"base_url"`
	AuthType    *string            `json:"auth_type"`
	APIKey      *string            `json:"api_key"`
	Headers     *map[string]string `json:"headers"`
	CatalogMode *string            `json:"catalog_mode"`
}

func (s *Server) handleUpdateProjectModelProvider(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	provider, ok := s.projectOwnedProvider(w, r, projectID)
	if !ok {
		return
	}
	var req updateProjectModelProviderReq
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
	if req.Headers != nil {
		if msg, ok := validateProviderHeaders(*req.Headers); !ok {
			writeError(w, http.StatusBadRequest, "bad_request", msg)
			return
		}
		headersEnc, ok := s.encryptModelHeaders(w, *req.Headers)
		if !ok {
			return
		}
		provider.HeadersEnc = headersEnc
	}
	if req.CatalogMode != nil {
		mode, ok := parseCatalogMode(*req.CatalogMode)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog_mode must be auto or disabled")
			return
		}
		provider.CatalogMode = mode
	}
	// A probe-affecting change resets verification state (same as the cluster path).
	probeChanged := req.Kind != nil || req.BaseURL != nil || req.AuthType != nil ||
		req.APIKey != nil || req.Headers != nil || req.CatalogMode != nil
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
			writeError(w, http.StatusConflict, "conflict", "a model provider with that name already exists in this project")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not update model provider")
		return
	}
	s.models.Invalidate()
	view, err := s.projectModelProviderView(r.Context(), *provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "provider saved, but could not read it back")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleDeleteProjectModelProvider(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	provider, ok := s.projectOwnedProvider(w, r, projectID)
	if !ok {
		return
	}
	if err := s.st.DeleteModelProvider(r.Context(), provider.ID); err != nil {
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

func (s *Server) handleVerifyProjectModelProvider(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	provider, ok := s.projectOwnedProvider(w, r, projectID)
	if !ok {
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

func (s *Server) handleProjectModelProviderCatalog(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	provider, ok := s.projectOwnedProvider(w, r, projectID)
	if !ok {
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

func (s *Server) handleCreateProjectProviderModel(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	provider, ok := s.projectOwnedProvider(w, r, projectID)
	if !ok {
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
		ID: domain.NewID(), ProviderID: provider.ID, ProjectID: provider.ProjectID, Name: name,
		BaseURL: provider.BaseURL, ModelName: provider.Kind + "/" + modelID,
		ModelID: modelID, APIKeyEnc: append([]byte(nil), provider.APIKeyEnc...),
		HeadersEnc:    append([]byte(nil), provider.HeadersEnc...),
		ContextWindow: req.ContextWindow, Capabilities: req.Capabilities, Source: source,
		Enabled: true, CreatedAt: now, UpdatedAt: now, UpdatedBy: principalFrom(r.Context()).userID(),
	}
	if err := s.st.CreateModel(r.Context(), model); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "that model is already configured in this project")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not create provider model")
		return
	}
	s.models.Invalidate()
	writeJSON(w, http.StatusCreated, projectProviderModelViewOf(*model))
}

// updateProjectProviderModelReq is the PATCH model body. Every field is optional
// (nil = unchanged). Only metadata + the enabled toggle are editable here (the
// endpoint identity/credential live on the provider).
type updateProjectProviderModelReq struct {
	Name          *string                   `json:"name"`
	ContextWindow *int                      `json:"context_window"`
	Capabilities  *domain.ModelCapabilities `json:"capabilities"`
	Enabled       *bool                     `json:"enabled"`
}

func (s *Server) handleUpdateProjectProviderModel(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	provider, ok := s.projectOwnedProvider(w, r, projectID)
	if !ok {
		return
	}
	model, ok := s.projectOwnedModel(w, r, provider)
	if !ok {
		return
	}
	var req updateProjectProviderModelReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name cannot be empty")
			return
		}
		model.Name = name
	}
	if req.ContextWindow != nil {
		if *req.ContextWindow < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "context_window cannot be negative")
			return
		}
		model.ContextWindow = *req.ContextWindow
	}
	if req.Capabilities != nil {
		model.Capabilities = *req.Capabilities
	}
	if req.Enabled != nil {
		model.Enabled = *req.Enabled
	}
	model.UpdatedBy = principalFrom(r.Context()).userID()
	if err := s.st.UpdateModel(r.Context(), model); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "a model named '"+model.Name+"' already exists in this project")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not update provider model")
		return
	}
	s.models.Invalidate()
	writeJSON(w, http.StatusOK, projectProviderModelViewOf(*model))
}

func (s *Server) handleDeleteProjectProviderModel(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	provider, ok := s.projectOwnedProvider(w, r, projectID)
	if !ok {
		return
	}
	model, ok := s.projectOwnedModel(w, r, provider)
	if !ok {
		return
	}
	if err := s.st.DeleteModel(r.Context(), model.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not delete provider model")
		return
	}
	s.models.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

// validateProviderHeaders rejects a headers map with blank keys or values so a
// bad custom-header set is a fail-visible 400 rather than silently stored.
func validateProviderHeaders(headers map[string]string) (string, bool) {
	for k, v := range headers {
		if strings.TrimSpace(k) == "" {
			return "header names cannot be blank", false
		}
		if strings.TrimSpace(v) == "" {
			return "header '" + k + "' cannot have a blank value", false
		}
	}
	return "", true
}

// encryptModelHeaders marshals a custom-headers map to JSON and seals it
// (AES-256-GCM). An empty map stores nil. A missing cipher is a typed 409 (never
// store a secret in the clear), mirroring encryptModelKey.
func (s *Server) encryptModelHeaders(w http.ResponseWriter, headers map[string]string) ([]byte, bool) {
	if len(headers) == 0 {
		return nil, true
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "headers must be a JSON object of strings")
		return nil, false
	}
	if s.cipher == nil {
		writeError(w, http.StatusConflict, "cipher_not_configured",
			"set AUTH_TOKEN_KEY on the orchestrator before configuring provider headers")
		return nil, false
	}
	enc, err := s.cipher.EncryptString(string(raw))
	if err != nil {
		s.log.Error("encrypt provider headers", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not encrypt the provider headers")
		return nil, false
	}
	return enc, true
}
