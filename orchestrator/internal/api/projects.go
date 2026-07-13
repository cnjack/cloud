package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// projectReq is the create payload. A project is created empty (name only);
// repositories are attached afterwards as services (POST /projects/{id}/services)
// and guardrails are set via PATCH. The former repo-field compat shim that
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
	// Cluster-admins (and the service principal) see every project; a
	// project-scoped API key (F12 / D24) sees ONLY the one project it is bound
	// to — never the cluster-wide list, never another project (the same
	// boundary GET /projects/{id} enforces via effectiveRole); a regular user
	// sees only the projects they are a member of (blueprint §2 RBAC matrix).
	var ps []domain.Project
	var err error
	switch {
	case prin.isClusterAdmin():
		ps, err = s.st.ListProjects(r.Context())
	case prin.isAPIKey():
		var p *domain.Project
		p, err = s.st.GetProject(r.Context(), prin.scopedProjectID)
		switch {
		case err == nil:
			ps = []domain.Project{*p}
		case errors.Is(err, store.ErrNotFound):
			// The bound project is gone (should not happen: the FK cascade
			// deletes the key with its project) — fail visibly empty, not a 500.
			ps, err = nil, nil
		}
	default:
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
	if code, msg := applyProjectPatch(r, existing); code != "" {
		// All guardrail validation failures are 400. A reserved injected_env key is
		// a first-class typed code (reserved_env_key) so the console can point at the
		// offending key (fail-visible; CLAUDE.md red line #1).
		writeError(w, http.StatusBadRequest, code, msg)
		return
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

// patchProjectKeys is the closed set of fields a project PATCH may carry. Any
// other key (e.g. a legacy repo_url) is a loud 400 — repo config lives on
// services, not the project.
var patchProjectKeys = map[string]bool{
	"name":                      true,
	"max_concurrent_runs":       true,
	"run_timeout_secs":          true,
	"provider_allowlist":        true,
	"injected_env":              true,
	"max_live_sessions":         true,
	"session_idle_timeout_secs": true,
	"session_ttl_secs":          true,
}

// applyProjectPatch mutates p in place from the PATCH body. It uses PRESENCE
// semantics: an OMITTED field is left unchanged; a field explicitly present
// (including JSON null) is applied — so a rename-only PATCH (`{"name":...}`)
// never wipes the guardrails, and the console can clear a guardrail to "inherit"
// by sending null. Returns (code, msg) on a validation error (empty code = ok).
func applyProjectPatch(r *http.Request, p *domain.Project) (string, string) {
	var body map[string]json.RawMessage
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&body); err != nil {
		return "bad_request", "invalid JSON: " + err.Error()
	}
	// Match field names case-INSENSITIVELY (mirrors the stdlib struct decoder the
	// old handler used, so a legacy {"Name":...} still renames), while still
	// rejecting genuinely unknown fields. Canonicalize to lowercase keys; a later
	// duplicate case-variant wins (same as encoding/json).
	raw := make(map[string]json.RawMessage, len(body))
	for k, v := range body {
		lk := strings.ToLower(k)
		if !patchProjectKeys[lk] {
			return "bad_request", "unknown field: " + k
		}
		raw[lk] = v
	}

	if v, ok := raw["name"]; ok {
		var name string
		if err := json.Unmarshal(v, &name); err != nil {
			return "bad_request", "name must be a string"
		}
		if t := strings.TrimSpace(name); t != "" {
			p.Name = t // an empty/whitespace name is ignored (a project must keep a name)
		}
	}

	if v, ok := raw["max_concurrent_runs"]; ok {
		n, err := parseNullableInt(v)
		if err != nil {
			return "bad_request", "max_concurrent_runs must be a whole number or null"
		}
		// NULL or ≤0 means "inherit the cluster default" — stored as NULL so the
		// project view omits it and the console shows the "cluster default"
		// placeholder.
		if n == nil || *n <= 0 {
			p.MaxConcurrentRuns = nil
		} else {
			p.MaxConcurrentRuns = n
		}
	}

	if v, ok := raw["run_timeout_secs"]; ok {
		n, err := parseNullableInt64(v)
		if err != nil {
			return "bad_request", "run_timeout_secs must be a whole number of seconds or null"
		}
		if n == nil || *n <= 0 {
			p.RunTimeoutSecs = nil
		} else {
			p.RunTimeoutSecs = n
		}
	}

	if _, ok := raw["provider_allowlist"]; ok {
		// Deprecated (D20 / F5, partial reversal of D15): an owner-set provider
		// allowlist could not constrain the owner themselves, so git-host policy moved
		// to a CLUSTER-level allowlist (ALLOWED_GIT_HOSTS) checked at integration
		// create, plus per-project integrations. The column is retained for historical
		// data but is no longer editable — fail-visible rather than silently ignore it.
		return "deprecated_key",
			"provider_allowlist is deprecated: git-host policy is now a cluster-level allowlist (ask a cluster admin) enforced when creating a project integration; set up an integration under Project Settings instead"
	}

	if v, ok := raw["injected_env"]; ok {
		env, code, msg := parseInjectedEnv(v)
		if code != "" {
			return code, msg
		}
		p.InjectedEnv = env
	}

	// Session guardrails (D22): same presence + "null/≤0 = inherit cluster
	// default" semantics as the other numeric guardrails.
	if v, ok := raw["max_live_sessions"]; ok {
		n, err := parseNullableInt(v)
		if err != nil {
			return "bad_request", "max_live_sessions must be a whole number or null"
		}
		if n == nil || *n <= 0 {
			p.MaxLiveSessions = nil
		} else {
			p.MaxLiveSessions = n
		}
	}
	if v, ok := raw["session_idle_timeout_secs"]; ok {
		n, err := parseNullableInt64(v)
		if err != nil {
			return "bad_request", "session_idle_timeout_secs must be a whole number of seconds or null"
		}
		if n == nil || *n <= 0 {
			p.SessionIdleTimeoutSecs = nil
		} else {
			p.SessionIdleTimeoutSecs = n
		}
	}
	if v, ok := raw["session_ttl_secs"]; ok {
		n, err := parseNullableInt64(v)
		if err != nil {
			return "bad_request", "session_ttl_secs must be a whole number of seconds or null"
		}
		if n == nil || *n <= 0 {
			p.SessionTTLSecs = nil
		} else {
			p.SessionTTLSecs = n
		}
	}
	return "", ""
}

// parseNullableInt unmarshals a JSON number or null into *int.
func parseNullableInt(v json.RawMessage) (*int, error) {
	if isJSONNull(v) {
		return nil, nil
	}
	var n int
	if err := json.Unmarshal(v, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// parseNullableInt64 unmarshals a JSON number or null into *int64.
func parseNullableInt64(v json.RawMessage) (*int64, error) {
	if isJSONNull(v) {
		return nil, nil
	}
	var n int64
	if err := json.Unmarshal(v, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func isJSONNull(v json.RawMessage) bool {
	return strings.TrimSpace(string(v)) == "null"
}

// parseInjectedEnv validates the injected_env map: every key must be a valid env
// name AND must NOT be a reserved orchestrator↔runner variable (a first-class
// typed 400 naming the key, so the fix is obvious — CLAUDE.md fail-visible). null
// returns an empty map (no injection).
func parseInjectedEnv(v json.RawMessage) (map[string]string, string, string) {
	if isJSONNull(v) {
		return map[string]string{}, "", ""
	}
	var in map[string]string
	if err := json.Unmarshal(v, &in); err != nil {
		return nil, "bad_request", "injected_env must be an object of string values"
	}
	for k := range in {
		if !domain.ValidEnvKey(k) {
			return nil, "bad_request", fmt.Sprintf("injected_env key %q is not a valid environment variable name", k)
		}
		if domain.IsReservedEnvKey(k) {
			return nil, "reserved_env_key", fmt.Sprintf("injected_env key %q is reserved by the orchestrator and cannot be set", k)
		}
	}
	if in == nil {
		in = map[string]string{}
	}
	return in, "", ""
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

	MaxConcurrentRuns *int     `json:"max_concurrent_runs,omitempty"`
	RunTimeoutSecs    *int64   `json:"run_timeout_secs,omitempty"`
	ProviderAllowlist []string `json:"provider_allowlist,omitempty"`
	// Session guardrails (D22). Absent => the project inherits the cluster default
	// (the console shows a "cluster default" placeholder).
	MaxLiveSessions        *int   `json:"max_live_sessions,omitempty"`
	SessionIdleTimeoutSecs *int64 `json:"session_idle_timeout_secs,omitempty"`
	SessionTTLSecs         *int64 `json:"session_ttl_secs,omitempty"`
	// InjectedEnv values can hold secrets (tokens, proxy creds). They are returned
	// ONLY to an owner/cluster-admin — the same role that may edit them. For a
	// member/viewer this is omitted entirely (not just masked): they never need the
	// values, and leaking them via GET /projects would defeat the owner-only edit
	// gate (fail-visible; CLAUDE.md red line #1).
	InjectedEnv map[string]string `json:"injected_env,omitempty"`

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
	for i := range services {
		services[i].RepoHTMLURL = s.serviceRepoHTMLURL(ctx, &services[i])
	}
	pv := &projectView{
		ID:                     p.ID,
		Name:                   p.Name,
		CreatedAt:              p.CreatedAt,
		Role:                   string(role),
		OwnerUserID:            p.OwnerUserID,
		MaxConcurrentRuns:      p.MaxConcurrentRuns,
		RunTimeoutSecs:         p.RunTimeoutSecs,
		ProviderAllowlist:      p.ProviderAllowlist,
		MaxLiveSessions:        p.MaxLiveSessions,
		SessionIdleTimeoutSecs: p.SessionIdleTimeoutSecs,
		SessionTTLSecs:         p.SessionTTLSecs,
		Services:               services,
	}
	// Only an owner (cluster-admin / service principal report "owner") sees the
	// injected_env values — they may contain secrets.
	if role == domain.RoleOwner {
		pv.InjectedEnv = p.InjectedEnv
	}
	return pv, nil
}
