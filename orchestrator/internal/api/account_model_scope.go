package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

func accountProviderRoute(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/v1/account/model-providers")
}

// The route selects a scope, never a different principal or elevated role.
func (s *Server) requireProviderManager(w http.ResponseWriter, r *http.Request) bool {
	if !accountProviderRoute(r) {
		return s.requireClusterAdmin(w, r)
	}
	if principalFrom(r.Context()).userID() == "" {
		writeError(w, 403, "account_required", "sign in with an account to manage personal providers")
		return false
	}
	return true
}

func (s *Server) canManageProvider(w http.ResponseWriter, r *http.Request, provider *domain.ModelProvider) bool {
	allowed := provider.OwnerUserID == ""
	if accountProviderRoute(r) {
		allowed = provider.OwnerUserID != "" && provider.OwnerUserID == principalFrom(r.Context()).userID()
	}
	if !allowed {
		writeError(w, 404, "not_found", "model provider not found")
		return false
	}
	return true
}

func (s *Server) loadManagedProvider(w http.ResponseWriter, r *http.Request) (*domain.ModelProvider, bool) {
	if !s.requireProviderManager(w, r) {
		return nil, false
	}
	provider, err := s.st.GetModelProvider(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "model provider not found")
		return nil, false
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load model provider")
		return nil, false
	}
	return provider, s.canManageProvider(w, r, provider)
}

// Personal models are never editable or grantable through cluster endpoints.
func (s *Server) requireGlobalModel(w http.ResponseWriter, r *http.Request) bool {
	model, err := s.st.GetModel(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "model not found")
		return false
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load model")
		return false
	}
	provider, err := s.st.GetModelProvider(r.Context(), model.ProviderID)
	if err != nil {
		writeError(w, 500, "internal", "could not load model provider")
		return false
	}
	if provider.OwnerUserID != "" {
		writeError(w, 404, "not_found", "model not found")
		return false
	}
	return true
}

func (s *Server) providerForModelMutation(w http.ResponseWriter, r *http.Request) (*domain.ModelProvider, bool) {
	if accountProviderRoute(r) {
		return s.loadManagedProvider(w, r)
	}
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return nil, false
	}
	return s.projectOwnedProvider(w, r, projectID)
}
