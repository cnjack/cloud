package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// Project-scoped API keys (F12 / D24) — a revocable automation credential that
// replaces borrowing the cluster-wide CONSOLE_TOKEN for external/CI use. A key
// resolves (api/principal.go resolvePrincipal) to a principal capped at
// RoleMember on EXACTLY the project it was minted for (effectiveRole); see
// docs/11-api.md § "Project-scoped API keys" for the full permission matrix.
// Management here (list/create/revoke) is OWNER only — a scoped principal
// itself can never call these (RoleMember never satisfies the RoleOwner gate
// authorizeProject enforces below), which is the "no self-renewal privilege
// escalation" red line.

// apiKeyView is the safe (never-secret) representation of an API key: the
// plaintext and key_hash are NEVER part of this type. Used for both the list
// response and — embedded in createAPIKeyResponse — the one-time create
// response.
type apiKeyView struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func apiKeyViewOf(k domain.APIKey) apiKeyView {
	return apiKeyView{
		ID:         k.ID,
		ProjectID:  k.ProjectID,
		Name:       k.Name,
		Prefix:     k.Prefix,
		CreatedAt:  k.CreatedAt,
		LastUsedAt: k.LastUsedAt,
		RevokedAt:  k.RevokedAt,
	}
}

// createAPIKeyResponse is apiKeyView plus the ONE-TIME plaintext Key. This
// struct is the ONLY place the plaintext is ever serialized — there is no
// read-back path afterward (CLAUDE.md fail-visible credential discipline).
type createAPIKeyResponse struct {
	apiKeyView
	Key string `json:"key"`
}

type createAPIKeyReq struct {
	Name string `json:"name"`
}

// handleListAPIKeys lists a project's API keys (owner only, F12). Revoked keys
// are included so the owner can see history/status; key_hash/plaintext never
// appear.
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.projectExists(w, r, projectID) {
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	keys, err := s.st.ListAPIKeysByProject(r.Context(), projectID)
	if err != nil {
		s.log.Error("list api keys", "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list api keys")
		return
	}
	out := make([]apiKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, apiKeyViewOf(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": out})
}

// handleCreateAPIKey mints a new project-scoped API key (owner only, F12). The
// plaintext (auth.GenerateAPIKey) is generated here, hashed for storage
// (auth.HashToken — one-way, same discipline as Session/Run tokens), and
// returned EXACTLY ONCE in this response. A missing/blank name is rejected
// (400) so every key stays identifiable in the list.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.projectExists(w, r, projectID) {
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	var req createAPIKeyReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	plaintext, err := auth.GenerateAPIKey()
	if err != nil {
		s.log.Error("generate api key", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not generate the api key")
		return
	}
	k := &domain.APIKey{
		ID:        domain.NewID(),
		ProjectID: projectID,
		Name:      name,
		KeyHash:   auth.HashToken(plaintext),
		Prefix:    auth.APIKeyDisplayPrefix(plaintext),
		CreatedBy: principalFrom(r.Context()).userIDPtr(),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateAPIKey(r.Context(), k); err != nil {
		s.log.Error("create api key", "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create the api key")
		return
	}
	writeJSON(w, http.StatusCreated, createAPIKeyResponse{apiKeyView: apiKeyViewOf(*k), Key: plaintext})
}

// handleRevokeAPIKey revokes an API key (owner only, F12). Effective
// immediately: the very next request bearing this key's plaintext resolves no
// principal (401) — store.GetAPIKeyByHash excludes revoked rows, so there is
// no cache to invalidate. Revoking an already-revoked key is a no-op 200
// (idempotent DELETE); revoking a key that belongs to a DIFFERENT project 404s
// (does not leak whether the id exists elsewhere).
func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	keyID := r.PathValue("keyID")
	if !s.projectExists(w, r, projectID) {
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	k, err := s.st.GetAPIKey(r.Context(), keyID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}
	if err != nil {
		s.log.Error("load api key", "key", keyID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the api key")
		return
	}
	if k.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}
	if err := s.st.RevokeAPIKey(r.Context(), keyID); err != nil {
		s.log.Error("revoke api key", "key", keyID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke the api key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": keyID})
}
