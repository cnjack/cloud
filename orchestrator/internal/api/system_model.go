package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
)

// modelConfigView is the admin-facing model-config snapshot. It NEVER carries
// the plaintext API key — only api_key_set. A non-admin caller gets just the
// Configured field (the rest is zero and omitted).
type modelConfigView struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	ModelName  string `json:"model_name,omitempty"`
	APIKeySet  bool   `json:"api_key_set,omitempty"`
}

// putModelConfigReq is the PUT /api/v1/system/model body. api_key may be empty
// (some OpenAI-compatible endpoints need no key).
type putModelConfigReq struct {
	BaseURL   string `json:"base_url"`
	ModelName string `json:"model_name"`
	APIKey    string `json:"api_key"`
}

// handleGetModelConfig reports the effective model config. Any authenticated
// principal may read whether a model IS configured; only a cluster-admin sees
// the source / base_url / model_name / api_key_set detail. The plaintext key is
// never returned (Feature A / fail-visible).
func (s *Server) handleGetModelConfig(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.models.Resolve(r.Context())
	if err != nil {
		s.log.Error("resolve model config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve model configuration")
		return
	}
	prin := principalFrom(r.Context())
	if !prin.isClusterAdmin() {
		writeJSON(w, http.StatusOK, modelConfigView{Configured: resolved.Configured()})
		return
	}
	writeJSON(w, http.StatusOK, adminModelView(resolved))
}

// handlePutModelConfig sets the cluster model config (cluster-admin only). It
// validates the base URL is http(s) and the model name is "provider/model". The
// API key is stored ENCRYPTED; when a key is supplied but the token cipher is
// unconfigured (AUTH_TOKEN_KEY unset) it returns a typed 409 rather than
// storing a key it cannot protect. A KEYLESS config needs no cipher and saves
// fine without AUTH_TOKEN_KEY.
func (s *Server) handlePutModelConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}

	var req putModelConfigReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.ModelName = strings.TrimSpace(req.ModelName)

	if msg, ok := validateBaseURL(req.BaseURL); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	if msg, ok := validateModelName(req.ModelName); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	// Encrypt the key only when present; an empty key stores NULL (keyless).
	// The cipher gate applies ONLY when there is a key to protect — it must not
	// block keyless endpoints, which never touch the cipher.
	var enc []byte
	if req.APIKey != "" {
		if s.cipher == nil {
			writeError(w, http.StatusConflict, "cipher_not_configured",
				"set AUTH_TOKEN_KEY on the orchestrator before configuring a model API key")
			return
		}
		var err error
		enc, err = s.cipher.EncryptString(req.APIKey)
		if err != nil {
			s.log.Error("encrypt model api key", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not encrypt the API key")
			return
		}
	}

	cfg := &domain.ModelConfig{
		BaseURL:   req.BaseURL,
		ModelName: req.ModelName,
		APIKeyEnc: enc,
		UpdatedBy: principalFrom(r.Context()).userID(),
	}
	if err := s.st.SetModelConfig(r.Context(), cfg); err != nil {
		s.log.Error("set model config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not save the model configuration")
		return
	}
	// Drop the shared cache so the new config is effective immediately (the
	// reconciler resolves through this same Resolver).
	s.models.Invalidate()

	resolved, err := s.models.Resolve(r.Context())
	if err != nil {
		s.log.Error("resolve model config after set", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "saved, but could not read back the model configuration")
		return
	}
	writeJSON(w, http.StatusOK, adminModelView(resolved))
}

// handleDeleteModelConfig clears the DB model config (cluster-admin only); the
// effective config then falls back to env or "not configured". It returns the
// post-clear effective snapshot so the console updates without a second fetch.
func (s *Server) handleDeleteModelConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	if err := s.st.ClearModelConfig(r.Context()); err != nil {
		s.log.Error("clear model config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not clear the model configuration")
		return
	}
	// Drop the shared cache so the fallback (env / none) is effective immediately.
	s.models.Invalidate()
	resolved, err := s.models.Resolve(r.Context())
	if err != nil {
		s.log.Error("resolve model config after clear", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "cleared, but could not read back the model configuration")
		return
	}
	writeJSON(w, http.StatusOK, adminModelView(resolved))
}

// adminModelView projects a resolved config into the admin-facing view.
func adminModelView(resolved modelcfg.Resolved) modelConfigView {
	return modelConfigView{
		Configured: resolved.Configured(),
		Source:     string(resolved.Source),
		BaseURL:    resolved.BaseURL,
		ModelName:  resolved.ModelName,
		APIKeySet:  resolved.APIKeySet,
	}
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
