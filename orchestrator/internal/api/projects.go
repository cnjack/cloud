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

// createProjectReq carries the pure-API field (name) plus the compatibility-shim
// repo fields. When any repo field is present a 'default' service is created
// alongside the project (multitenant blueprint §4).
type createProjectReq struct {
	Name string `json:"name"`
	// Compat shim: repo config for the auto-created default service.
	RepoURL       string `json:"repo_url"`
	DefaultBranch string `json:"default_branch"`
	GitMode       string `json:"git_mode"`
	Provider      string `json:"provider"`
	ProviderURL   string `json:"provider_url"` // accepted for compat; the base URL is config-derived in M1
	ProviderRepo  string `json:"provider_repo"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}

	// If any repo field is present, validate the default service up-front (before
	// creating the project) so a bad repo is a clean 400 with no orphan project.
	var svc *domain.Service
	if strings.TrimSpace(req.RepoURL) != "" || strings.TrimSpace(req.ProviderRepo) != "" {
		var code, msg string
		svc, code, msg = resolveService(serviceInput{
			Name:          "default",
			RepoURL:       req.RepoURL,
			Provider:      req.Provider,
			OwnerName:     req.ProviderRepo, // legacy field name for the default service
			GitMode:       req.GitMode,
			DefaultBranch: req.DefaultBranch,
		})
		if svc == nil {
			writeError(w, http.StatusBadRequest, code, msg)
			return
		}
	}

	p := &domain.Project{
		ID:        domain.NewID(),
		Name:      req.Name,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateProject(r.Context(), p); err != nil {
		s.log.Error("create project", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create project")
		return
	}
	if svc != nil {
		svc.ID = domain.NewID()
		svc.ProjectID = p.ID
		svc.CreatedAt = time.Now().UTC()
		if err := s.st.CreateService(r.Context(), svc); err != nil {
			// Roll back the project so a failed default-service create does not leave
			// a repo-less project behind (the shim contract is project+default svc).
			_ = s.st.DeleteProject(r.Context(), p.ID)
			s.log.Error("create default service", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not create default service")
			return
		}
	}
	pv, err := s.projectViewOf(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	writeJSON(w, http.StatusCreated, pv)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.st.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list projects")
		return
	}
	views := make([]projectView, 0, len(ps))
	for i := range ps {
		pv, err := s.projectViewOf(r.Context(), &ps[i])
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not load project")
			return
		}
		views = append(views, *pv)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": views})
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
	pv, err := s.projectViewOf(r.Context(), p)
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
	var req createProjectReq
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
	// Compat shim: repo/git_mode/branch changes retarget the project's default
	// service in place (e.g. e2e J2-S5 PATCHes repo_url to fix a bad repo before
	// retrying). The retry keeps the same service_id, so it picks up the fix.
	if s.patchDefaultServiceFromProjectReq(r.Context(), w, existing.ID, req) {
		return // an error response was already written
	}
	pv, err := s.projectViewOf(r.Context(), existing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

// patchDefaultServiceFromProjectReq applies any repo-shim fields from a project
// PATCH to the project's default service. Returns true if it wrote an error
// response (caller must stop). A project with no default service and no repo
// fields in the request is a no-op.
func (s *Server) patchDefaultServiceFromProjectReq(ctx context.Context, w http.ResponseWriter, projectID string, req createProjectReq) bool {
	touchesRepo := strings.TrimSpace(req.RepoURL) != "" ||
		strings.TrimSpace(req.ProviderRepo) != "" ||
		strings.TrimSpace(req.GitMode) != "" ||
		strings.TrimSpace(req.DefaultBranch) != ""
	if !touchesRepo {
		return false
	}
	svc, err := s.resolveDefaultService(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		// No default service to retarget (a pure-API project). The old fields are
		// silently ignored rather than 400, matching the shim's best-effort intent.
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load default service")
		return true
	}
	if code, msg := applyServicePatch(svc, servicePatch{
		RepoURL:       req.RepoURL,
		Provider:      req.Provider,
		OwnerName:     req.ProviderRepo,
		GitMode:       req.GitMode,
		DefaultBranch: req.DefaultBranch,
	}); code != "" {
		writeError(w, http.StatusBadRequest, code, msg)
		return true
	}
	if err := s.st.UpdateService(ctx, svc); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update default service")
		return true
	}
	return false
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

// --- project view (compat shim) ---------------------------------------------

// projectView is the wire shape for a project: the project's own fields plus, as
// a backward-compatibility shim, the default service's repo config flattened
// onto the project and a full services array (multitenant blueprint §4).
type projectView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`

	MaxConcurrentRuns *int              `json:"max_concurrent_runs,omitempty"`
	RunTimeoutSecs    *int64            `json:"run_timeout_secs,omitempty"`
	ProviderAllowlist []string          `json:"provider_allowlist,omitempty"`
	InjectedEnv       map[string]string `json:"injected_env,omitempty"`

	// Flattened default-service fields (compat). Present only when the project
	// has a 'default' (or sole) service.
	RepoURL       string             `json:"repo_url,omitempty"`
	DefaultBranch string             `json:"default_branch,omitempty"`
	GitMode       domain.GitMode     `json:"git_mode,omitempty"`
	Provider      domain.GitProvider `json:"provider,omitempty"`
	ProviderURL   string             `json:"provider_url,omitempty"`
	ProviderRepo  string             `json:"provider_repo,omitempty"`

	Services []domain.Service `json:"services"`
}

func (s *Server) projectViewOf(ctx context.Context, p *domain.Project) (*projectView, error) {
	services, err := s.st.ListServices(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if services == nil {
		services = []domain.Service{}
	}
	pv := &projectView{
		ID:                p.ID,
		Name:              p.Name,
		CreatedAt:         p.CreatedAt,
		MaxConcurrentRuns: p.MaxConcurrentRuns,
		RunTimeoutSecs:    p.RunTimeoutSecs,
		ProviderAllowlist: p.ProviderAllowlist,
		InjectedEnv:       p.InjectedEnv,
		Services:          services,
	}
	if def := defaultOrSoleService(services); def != nil {
		pv.RepoURL = domain.ServiceCloneURL(*def, s.cfg.GiteaURL)
		pv.DefaultBranch = def.DefaultBranch
		pv.GitMode = def.GitMode
		pv.Provider = def.Provider
		pv.ProviderURL = domain.ProviderBaseURL(def.Provider, s.cfg.GiteaURL)
		pv.ProviderRepo = def.RepoOwnerName
	}
	return pv, nil
}

// defaultOrSoleService returns the 'default'-named service, or the sole service
// if there is exactly one, or nil. This is what the flatten shim keys off.
func defaultOrSoleService(services []domain.Service) *domain.Service {
	for i := range services {
		if services[i].Name == "default" {
			return &services[i]
		}
	}
	if len(services) == 1 {
		return &services[0]
	}
	return nil
}

// resolveDefaultService returns the project's default service (name='default'),
// falling back to the sole service if the project has exactly one. ErrNotFound
// when neither applies.
func (s *Server) resolveDefaultService(ctx context.Context, projectID string) (*domain.Service, error) {
	svc, err := s.st.GetDefaultService(ctx, projectID)
	if err == nil {
		return svc, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	services, err := s.st.ListServices(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(services) == 1 {
		return &services[0], nil
	}
	return nil, store.ErrNotFound
}
