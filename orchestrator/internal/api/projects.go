package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

type createProjectReq struct {
	Name          string `json:"name"`
	RepoURL       string `json:"repo_url"`
	DefaultBranch string `json:"default_branch"`
	// Git integration (ST-1). All optional; omit for readonly (diff-only).
	GitMode      string `json:"git_mode"`
	Provider     string `json:"provider"`
	ProviderURL  string `json:"provider_url"`
	ProviderRepo string `json:"provider_repo"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.RepoURL = strings.TrimSpace(req.RepoURL)
	if req.Name == "" || req.RepoURL == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name and repo_url are required")
		return
	}
	if req.DefaultBranch == "" {
		req.DefaultBranch = "main"
	}
	gitMode := domain.GitMode(strings.TrimSpace(req.GitMode))
	if gitMode == "" {
		gitMode = domain.GitModeReadonly
	}
	if !domain.ValidGitMode(gitMode) {
		writeError(w, http.StatusBadRequest, "bad_request", "git_mode must be 'readonly' or 'draft_pr'")
		return
	}
	prov := domain.GitProvider(strings.TrimSpace(req.Provider))
	// draft_pr requires a provider + owner/name repo to have anywhere to push.
	if gitMode == domain.GitModeDraftPR {
		if prov == "" {
			prov = domain.ProviderGitea // gitea is the only MVP provider (D09)
		}
		if prov != domain.ProviderGitea {
			writeError(w, http.StatusBadRequest, "bad_request", "provider must be 'gitea' for draft_pr")
			return
		}
		if strings.TrimSpace(req.ProviderRepo) == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "provider_repo (owner/name) is required for draft_pr")
			return
		}
	}
	p := &domain.Project{
		ID:            domain.NewID(),
		Name:          req.Name,
		RepoURL:       req.RepoURL,
		DefaultBranch: req.DefaultBranch,
		CreatedAt:     time.Now().UTC(),
		GitMode:       gitMode,
		Provider:      prov,
		ProviderURL:   strings.TrimSpace(req.ProviderURL),
		ProviderRepo:  strings.TrimSpace(req.ProviderRepo),
	}
	if err := s.st.CreateProject(r.Context(), p); err != nil {
		s.log.Error("create project", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create project")
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.st.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list projects")
		return
	}
	if ps == nil {
		ps = []domain.Project{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": ps})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.GetProject(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get project")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.st.GetProject(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get project")
		return
	}
	var req createProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if v := strings.TrimSpace(req.Name); v != "" {
		existing.Name = v
	}
	if v := strings.TrimSpace(req.RepoURL); v != "" {
		existing.RepoURL = v
	}
	if v := strings.TrimSpace(req.DefaultBranch); v != "" {
		existing.DefaultBranch = v
	}
	// Git integration fields (ST-1): apply only the ones provided.
	if v := domain.GitMode(strings.TrimSpace(req.GitMode)); v != "" {
		if !domain.ValidGitMode(v) {
			writeError(w, http.StatusBadRequest, "bad_request", "git_mode must be 'readonly' or 'draft_pr'")
			return
		}
		existing.GitMode = v
	}
	if v := domain.GitProvider(strings.TrimSpace(req.Provider)); v != "" {
		existing.Provider = v
	}
	if v := strings.TrimSpace(req.ProviderURL); v != "" {
		existing.ProviderURL = v
	}
	if v := strings.TrimSpace(req.ProviderRepo); v != "" {
		existing.ProviderRepo = v
	}
	if err := s.st.UpdateProject(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update project")
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	err := s.st.DeleteProject(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not delete project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
