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
	p := &domain.Project{
		ID:            domain.NewID(),
		Name:          req.Name,
		RepoURL:       req.RepoURL,
		DefaultBranch: req.DefaultBranch,
		CreatedAt:     time.Now().UTC(),
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
