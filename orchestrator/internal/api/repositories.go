package api

import (
	"errors"
	"net/http"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

func (s *Server) handleListRepositories(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	var (
		repositories []domain.Service
		err          error
	)
	switch {
	case principal.isClusterAdmin():
		repositories, err = s.st.ListRepositoriesForUser(r.Context(), "")
	case principal.userID() != "":
		repositories, err = s.st.ListRepositoriesForUser(r.Context(), principal.userID())
	case principal.isAPIKey():
		repositories, err = s.st.ListServices(r.Context(), principal.scopedProjectID)
	default:
		writeError(w, http.StatusForbidden, "account_required", "an Account or Repository-scoped API key is required")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list repositories")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": repositories})
}

func (s *Server) handleGetRepository(w http.ResponseWriter, r *http.Request) {
	repository, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load repository")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), repository.ProjectID, domain.RoleViewer) {
		return
	}
	writeJSON(w, http.StatusOK, repository)
}
