package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// modelAdminView is the cluster-admin catalog view of a model (D21). It NEVER
// carries the plaintext API key — only api_key_set. granted_project_ids lets the
// admin UI manage authorization inline.
type modelAdminView struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	BaseURL           string    `json:"base_url"`
	ModelName         string    `json:"model_name"`
	APIKeySet         bool      `json:"api_key_set"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	UpdatedBy         string    `json:"updated_by"`
	GrantedProjectIDs []string  `json:"granted_project_ids"`
}

// modelMemberView is the member-facing projection of a granted model: id/name/
// model_name ONLY — a member NEVER sees the base_url or key (fail-visible red
// line: the endpoint/key are cluster-admin-only detail).
type modelMemberView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ModelName string `json:"model_name"`
}

// adminModel builds the admin view, fetching the model's grants.
func (s *Server) adminModel(r *http.Request, m *domain.Model) modelAdminView {
	grants, err := s.st.ListProjectIDsForModel(r.Context(), m.ID)
	if err != nil {
		s.log.Warn("list model grants", "model", m.ID, "err", err)
	}
	if grants == nil {
		grants = []string{}
	}
	return modelAdminView{
		ID:                m.ID,
		Name:              m.Name,
		BaseURL:           m.BaseURL,
		ModelName:         m.ModelName,
		APIKeySet:         m.APIKeySet(),
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
		UpdatedBy:         m.UpdatedBy,
		GrantedProjectIDs: grants,
	}
}

// handleListModels lists the whole catalog (cluster-admin only).
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	models, err := s.st.ListModels(r.Context())
	if err != nil {
		s.log.Error("list models", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list models")
		return
	}
	out := make([]modelAdminView, 0, len(models))
	for i := range models {
		out = append(out, s.adminModel(r, &models[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// createModelReq is the POST /system/models body. api_key may be empty (keyless).
type createModelReq struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	ModelName string `json:"model_name"`
	APIKey    string `json:"api_key"`
}

// handleCreateModel adds a catalog model (cluster-admin only).
func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	var req createModelReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	base := strings.TrimSpace(req.BaseURL)
	model := strings.TrimSpace(req.ModelName)
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if msg, ok := validateBaseURL(base); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	if msg, ok := validateModelName(model); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	enc, ok := s.encryptModelKey(w, req.APIKey)
	if !ok {
		return
	}
	m := &domain.Model{
		ID:        domain.NewID(),
		Name:      name,
		BaseURL:   base,
		ModelName: model,
		APIKeyEnc: enc,
		CreatedAt: time.Now().UTC(),
		UpdatedBy: principalFrom(r.Context()).userID(),
	}
	if err := s.st.CreateModel(r.Context(), m); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "a model named '"+name+"' already exists")
			return
		}
		s.log.Error("create model", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create model")
		return
	}
	s.models.Invalidate()
	writeJSON(w, http.StatusCreated, s.adminModel(r, m))
}

// patchModelReq is the PATCH /system/models/{id} body. Every field is optional
// (nil = leave unchanged). api_key: a non-nil empty string clears the key
// (keyless); a non-empty value re-encrypts it.
type patchModelReq struct {
	Name      *string `json:"name"`
	BaseURL   *string `json:"base_url"`
	ModelName *string `json:"model_name"`
	APIKey    *string `json:"api_key"`
}

// handleUpdateModel updates a catalog model (cluster-admin only).
func (s *Server) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	m, err := s.st.GetModel(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load model")
		return
	}
	var req patchModelReq
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
		m.Name = name
	}
	if req.BaseURL != nil {
		base := strings.TrimSpace(*req.BaseURL)
		if msg, ok := validateBaseURL(base); !ok {
			writeError(w, http.StatusBadRequest, "bad_request", msg)
			return
		}
		m.BaseURL = base
	}
	if req.ModelName != nil {
		model := strings.TrimSpace(*req.ModelName)
		if msg, ok := validateModelName(model); !ok {
			writeError(w, http.StatusBadRequest, "bad_request", msg)
			return
		}
		m.ModelName = model
		_, m.ModelID, _ = strings.Cut(model, "/")
	}
	if req.APIKey != nil {
		if *req.APIKey == "" {
			m.APIKeyEnc = nil // explicit clear → keyless
		} else {
			enc, ok := s.encryptModelKey(w, *req.APIKey)
			if !ok {
				return
			}
			m.APIKeyEnc = enc
		}
	}
	m.UpdatedBy = principalFrom(r.Context()).userID()
	if err := s.st.UpdateModel(r.Context(), m); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "a model named '"+m.Name+"' already exists")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}
		s.log.Error("update model", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not update model")
		return
	}
	s.models.Invalidate()
	writeJSON(w, http.StatusOK, s.adminModel(r, m))
}

// handleDeleteModel removes a catalog model (cluster-admin only). Its grants
// cascade and any service default / run reference is nulled.
func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	if err := s.st.DeleteModel(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}
		s.log.Error("delete model", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete model")
		return
	}
	s.models.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

// handleGrantModel authorizes a project to use a model (cluster-admin only).
// Idempotent (PUT). A missing model or project is a 404.
func (s *Server) handleGrantModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	modelID := r.PathValue("id")
	projectID := r.PathValue("projectID")
	// Grants are only meaningful for cluster-global models. A project-owned model
	// (project_id != "") is private to its owning project — granting it to another
	// project would leak it cross-project and bypass its enabled toggle. Reject it
	// with a typed error rather than silently creating a cross-project grant.
	target, err := s.st.GetModel(r.Context(), modelID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model or project not found")
		return
	}
	if err != nil {
		s.log.Error("load model for grant", "model", modelID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load model")
		return
	}
	if target.ProjectID != "" {
		writeError(w, http.StatusConflict, "model_not_grantable",
			"this model is owned by a project and cannot be granted; grants apply only to cluster-global models")
		return
	}
	if err := s.st.GrantModel(r.Context(), modelID, projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model or project not found")
			return
		}
		s.log.Error("grant model", "model", modelID, "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not grant model")
		return
	}
	s.models.Invalidate()
	m, err := s.st.GetModel(r.Context(), modelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "granted, but could not read back the model")
		return
	}
	writeJSON(w, http.StatusOK, s.adminModel(r, m))
}

// handleRevokeModel removes a project's grant (cluster-admin only). Idempotent.
func (s *Server) handleRevokeModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	modelID := r.PathValue("id")
	projectID := r.PathValue("projectID")
	if err := s.st.RevokeModel(r.Context(), modelID, projectID); err != nil {
		s.log.Error("revoke model", "model", modelID, "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke model")
		return
	}
	s.models.Invalidate()
	m, err := s.st.GetModel(r.Context(), modelID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "revoked, but could not read back the model")
		return
	}
	writeJSON(w, http.StatusOK, s.adminModel(r, m))
}

// projectModelsView is GET /projects/{id}/models: the models granted to a
// project (member-visible detail only) plus whether the MODEL_* env fallback is
// active (empty catalog). configured = models non-empty OR env_fallback; the
// console's ModelGate keys off that.
type projectModelsView struct {
	Models      []modelMemberView `json:"models"`
	EnvFallback bool              `json:"env_fallback"`
}

// handleListProjectModels lists the models a project is granted (member+). It
// NEVER exposes base_url or the key — only id/name/model_name.
func (s *Server) handleListProjectModels(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleViewer) {
		return
	}
	granted, err := s.st.ListModelsForProject(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list models")
		return
	}
	out := make([]modelMemberView, 0, len(granted))
	for i := range granted {
		out = append(out, modelMemberView{ID: granted[i].ID, Name: granted[i].Name, ModelName: granted[i].ModelName})
	}
	writeJSON(w, http.StatusOK, projectModelsView{Models: out, EnvFallback: s.envFallbackActive(r)})
}

// envFallbackActive reports whether the MODEL_* env fallback is currently usable
// (the catalog is empty AND the env is fully configured). It backs the
// project-scoped "configured" signal for a local rig that never populated the
// catalog.
func (s *Server) envFallbackActive(r *http.Request) bool {
	if s.cfg == nil || s.cfg.ModelBaseURL == "" || s.cfg.ModelName == "" {
		return false
	}
	n, err := s.st.CountModels(r.Context())
	if err != nil {
		s.log.Warn("count models", "err", err)
		return false
	}
	return n == 0
}

// projectGrantsModel reports whether modelID is granted to projectID. Used to
// validate a service default before persisting it.
func (s *Server) projectGrantsModel(ctx context.Context, projectID, modelID string) (bool, error) {
	granted, err := s.st.ListModelsForProject(ctx, projectID)
	if err != nil {
		return false, err
	}
	for i := range granted {
		if granted[i].ID == modelID {
			return true, nil
		}
	}
	return false, nil
}

// encryptModelKey encrypts a plaintext model API key for storage. An empty key
// stores nil (keyless). When a key is present but no cipher is configured
// (AUTH_TOKEN_KEY unset) it writes a typed 409 and returns ok=false rather than
// storing a key it cannot protect. Mirrors the old single-config gate.
func (s *Server) encryptModelKey(w http.ResponseWriter, key string) ([]byte, bool) {
	if key == "" {
		return nil, true
	}
	if s.cipher == nil {
		writeError(w, http.StatusConflict, "cipher_not_configured",
			"set AUTH_TOKEN_KEY on the orchestrator before configuring a model API key")
		return nil, false
	}
	enc, err := s.cipher.EncryptString(key)
	if err != nil {
		s.log.Error("encrypt model api key", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not encrypt the API key")
		return nil, false
	}
	return enc, true
}

// requireClusterAdmin writes a 403 and returns false when the principal is not a
// cluster-admin. It mirrors authorizeProject's write-then-stop convention.
func (s *Server) requireClusterAdmin(w http.ResponseWriter, r *http.Request) bool {
	if principalFrom(r.Context()).isClusterAdmin() {
		return true
	}
	writeError(w, http.StatusForbidden, "forbidden", "this action requires cluster-admin")
	return false
}

// validateBaseURL requires an absolute http(s) URL with a host.
func validateBaseURL(raw string) (string, bool) {
	if raw == "" {
		return "base_url is required", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "base_url must be an http(s) URL", false
	}
	return "", true
}

// validateModelName requires a "provider/model" form (exactly the shape the
// runner writes into the jcode config): a non-empty provider and model split on
// the first "/".
func validateModelName(raw string) (string, bool) {
	provider, model, ok := strings.Cut(raw, "/")
	if !ok || provider == "" || model == "" {
		return "model_name must be in 'provider/model' form", false
	}
	return "", true
}
