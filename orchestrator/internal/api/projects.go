package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// projectReq is the create/update payload. A project is a pure container (name
// + membership + guardrails); repositories are attached afterwards as services
// (POST /projects/{id}/services). The former repo-field compat shim that
// auto-created a 'default' service was removed with the console's two-step flow.
type projectReq struct {
	Name string `json:"name"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req projectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}

	// Any logged-in principal may create a project and becomes its owner. A
	// service principal (CONSOLE_TOKEN) has no user, so owner_user_id stays NULL
	// and no member row is written — cluster-admins see every project regardless.
	prin := principalFrom(r.Context())
	p := &domain.Project{
		ID:          domain.NewID(),
		Name:        req.Name,
		CreatedAt:   time.Now().UTC(),
		OwnerUserID: prin.userID(),
	}
	if err := s.st.CreateProject(r.Context(), p); err != nil {
		s.log.Error("create project", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create project")
		return
	}
	if uid := prin.userID(); uid != "" {
		if err := s.st.UpsertMember(r.Context(), &domain.ProjectMember{
			ProjectID: p.ID, UserID: uid, Role: domain.RoleOwner, CreatedAt: time.Now().UTC(),
		}); err != nil {
			// Rollback so we never leave a project the creator cannot see.
			_ = s.st.DeleteProject(r.Context(), p.ID)
			s.log.Error("create owner membership", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not create project membership")
			return
		}
	}
	pv, err := s.projectViewOf(r.Context(), p, domain.RoleOwner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	writeJSON(w, http.StatusCreated, pv)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	prin := principalFrom(r.Context())
	// Cluster-admins (and the service principal) see every project; a regular user
	// sees only the projects they are a member of (blueprint §2 RBAC matrix).
	var ps []domain.Project
	var err error
	if prin.isClusterAdmin() {
		ps, err = s.st.ListProjects(r.Context())
	} else {
		ps, err = s.st.ListProjectsForUser(r.Context(), prin.userID())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list projects")
		return
	}
	views := make([]projectView, 0, len(ps))
	for i := range ps {
		role, _, err := s.effectiveRole(r.Context(), prin, ps[i].ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not resolve project access")
			return
		}
		pv, err := s.projectViewOf(r.Context(), &ps[i], role)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not load project")
			return
		}
		views = append(views, *pv)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": views})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	p, err := s.st.GetProject(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get project")
		return
	}
	prin := principalFrom(r.Context())
	role, hasAccess, err := s.effectiveRole(r.Context(), prin, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve project access")
		return
	}
	if !hasAccess {
		writeError(w, http.StatusForbidden, "forbidden", "you are not a member of this project")
		return
	}
	pv, err := s.projectViewOf(r.Context(), p, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	writeJSON(w, http.StatusOK, pv)
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
	// Project settings changes require owner (or cluster-admin).
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), id, domain.RoleOwner) {
		return
	}
	var req projectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if v := strings.TrimSpace(req.Name); v != "" {
		existing.Name = v
	}
	if err := s.st.UpdateProject(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update project")
		return
	}
	role, _, err := s.effectiveRole(r.Context(), principalFrom(r.Context()), existing.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve project access")
		return
	}
	pv, err := s.projectViewOf(r.Context(), existing, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.projectExists(w, r, projectID) {
		return
	}
	// Deleting a project requires owner (or cluster-admin).
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	err := s.st.DeleteProject(r.Context(), projectID)
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

// --- project view -------------------------------------------------------------

// projectView is the wire shape for a project: the project's own fields plus the
// full services array. Repo config lives ONLY on services (multitenant blueprint
// §1); the old flattened default-service compat fields were removed with the
// simple-mode shim.
type projectView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`

	// Role is the requesting principal's role on this project (M2). A
	// cluster-admin or the CONSOLE_TOKEN service principal reports "owner" — the
	// strongest role — since they have full authority everywhere; a real member
	// reports their stored owner/member/viewer role.
	Role string `json:"role,omitempty"`
	// OwnerUserID is the project's owner (empty for a service-principal-created
	// project).
	OwnerUserID string `json:"owner_user_id,omitempty"`

	MaxConcurrentRuns *int              `json:"max_concurrent_runs,omitempty"`
	RunTimeoutSecs    *int64            `json:"run_timeout_secs,omitempty"`
	ProviderAllowlist []string          `json:"provider_allowlist,omitempty"`
	InjectedEnv       map[string]string `json:"injected_env,omitempty"`

	Services []domain.Service `json:"services"`
}

func (s *Server) projectViewOf(ctx context.Context, p *domain.Project, role domain.Role) (*projectView, error) {
	services, err := s.st.ListServices(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if services == nil {
		services = []domain.Service{}
	}
	return &projectView{
		ID:                p.ID,
		Name:              p.Name,
		CreatedAt:         p.CreatedAt,
		Role:              string(role),
		OwnerUserID:       p.OwnerUserID,
		MaxConcurrentRuns: p.MaxConcurrentRuns,
		RunTimeoutSecs:    p.RunTimeoutSecs,
		ProviderAllowlist: p.ProviderAllowlist,
		InjectedEnv:       p.InjectedEnv,
		Services:          services,
	}, nil
}
