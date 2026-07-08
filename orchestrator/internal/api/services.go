package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// serviceInput is the normalized input for creating a service, shared by the
// POST /projects/{id}/services handler and the POST /projects shim.
type serviceInput struct {
	Name          string
	RepoURL       string // opaque URL; smart-parsed when OwnerName is empty
	Provider      string // explicit provider hint (optional)
	OwnerName     string // explicit "owner/name" (provider form); wins over RepoURL
	GitMode       string
	DefaultBranch string
}

// resolveService validates + normalizes a serviceInput into a domain.Service
// (ID/ProjectID/CreatedAt left unset for the caller to fill). On a validation
// error it returns (nil, code, msg); on success (svc, "", "").
func resolveService(in serviceInput) (*domain.Service, string, string) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "default"
	}
	gitMode := domain.GitMode(strings.TrimSpace(in.GitMode))
	if gitMode == "" {
		gitMode = domain.GitModeReadonly
	}
	if !domain.ValidGitMode(gitMode) {
		return nil, "bad_request", "git_mode must be 'readonly' or 'draft_pr'"
	}
	branch := strings.TrimSpace(in.DefaultBranch)
	if branch == "" {
		branch = "main"
	}

	spec, code, msg := classifyRepo(in.RepoURL, in.Provider, in.OwnerName)
	if code != "" {
		return nil, code, msg
	}

	svc := &domain.Service{
		Name:          name,
		RepoKind:      spec.RepoKind,
		Provider:      spec.Provider,
		RepoOwnerName: spec.RepoOwnerName,
		RawRepoURL:    spec.RawRepoURL,
		DefaultBranch: branch,
		GitMode:       gitMode,
	}
	if code, msg := validateServiceConstraints(svc); code != "" {
		return nil, code, msg
	}
	return svc, "", ""
}

// classifyRepo turns a (repo_url | {provider, owner_name}) input into a RepoSpec.
// An explicit owner_name is authoritative (provider form). Otherwise repo_url is
// smart-parsed (domain.ParseRepoURL) and an explicit provider hint overrides the
// parsed provider when the URL is provider-shaped.
func classifyRepo(repoURL, providerHint, ownerName string) (domain.RepoSpec, string, string) {
	ownerName = strings.TrimSpace(ownerName)
	prov := domain.GitProvider(strings.TrimSpace(providerHint))
	if prov != "" && !domain.ValidProvider(prov) {
		return domain.RepoSpec{}, "bad_request", "provider must be gitea, github or gitlab"
	}
	if ownerName != "" {
		p := prov
		if p == "" {
			p = domain.ProviderGitea
		}
		return domain.RepoSpec{RepoKind: domain.RepoKindProvider, Provider: p, RepoOwnerName: ownerName}, "", ""
	}
	if strings.TrimSpace(repoURL) == "" {
		return domain.RepoSpec{}, "bad_request", "a repo_url or provider owner_name is required"
	}
	spec := domain.ParseRepoURL(repoURL, nil)
	if prov != "" && spec.RepoKind == domain.RepoKindProvider {
		spec.Provider = prov
	}
	return spec, "", ""
}

// validateServiceConstraints enforces the blueprint §1 constraint that draft_pr
// requires a provider repository (raw repos are read-only).
func validateServiceConstraints(svc *domain.Service) (string, string) {
	if svc.GitMode == domain.GitModeDraftPR && svc.RepoKind != domain.RepoKindProvider {
		return "bad_request", "git_mode 'draft_pr' requires a provider repository (owner/name); raw repos are read-only"
	}
	return "", ""
}

type createServiceReq struct {
	Name          string `json:"name"`
	RepoURL       string `json:"repo_url"`
	Provider      string `json:"provider"`
	OwnerName     string `json:"owner_name"`
	GitMode       string `json:"git_mode"`
	DefaultBranch string `json:"default_branch"`
	// ProviderRepoID is the provider's numeric repo id, sent by the repo picker
	// (GET /providers/{p}/repos). Optional; rename-proof repo identity (0009).
	ProviderRepoID *int64 `json:"provider_repo_id"`
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	// Creating/editing a service is a project-settings action: owner only.
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	var req createServiceReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	svc, code, msg := resolveService(serviceInput{
		Name:          req.Name,
		RepoURL:       req.RepoURL,
		Provider:      req.Provider,
		OwnerName:     req.OwnerName,
		GitMode:       req.GitMode,
		DefaultBranch: req.DefaultBranch,
	})
	if svc == nil {
		writeError(w, http.StatusBadRequest, code, msg)
		return
	}
	// Guardrail: the project's provider_allowlist (when set) restricts which git
	// hosts a service may target. A create with a disallowed provider is a 400
	// (input the caller can fix) — raw repos are addressed by the "raw" sentinel.
	if allowed, err := s.projectAllowsProvider(r.Context(), projectID, svc.Provider); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project guardrails")
		return
	} else if !allowed {
		writeError(w, http.StatusBadRequest, "provider_not_allowed",
			"this project's guardrails do not allow "+providerLabel(svc.Provider)+" repositories")
		return
	}
	// Enforce the (project_id, name) uniqueness up-front for a friendly 409.
	if existing, err := s.st.ListServices(r.Context(), projectID); err == nil {
		for i := range existing {
			if existing[i].Name == svc.Name {
				writeError(w, http.StatusConflict, "conflict", "a service named '"+svc.Name+"' already exists in this project")
				return
			}
		}
	}
	svc.ID = domain.NewID()
	svc.ProjectID = projectID
	svc.CreatedAt = time.Now().UTC()
	if req.ProviderRepoID != nil && svc.RepoKind == domain.RepoKindProvider {
		svc.ProviderRepoID = req.ProviderRepoID
	}
	if err := s.st.CreateService(r.Context(), svc); err != nil {
		s.log.Error("create service", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create service")
		return
	}
	// Drone-style onboarding: best-effort auto-register the @mention comment
	// webhook on the new repo. Never fails the create — a service without the
	// hook still works (manual dispatch), and the console surfaces nothing.
	s.ensureServiceWebhook(r.Context(), svc)
	writeJSON(w, http.StatusCreated, svc)
}

// ensureServiceWebhook registers the @jcode PR-comment webhook on a freshly
// added gitea repository, when the deployment is configured for it (both
// WEBHOOK_URL and WEBHOOK_SECRET set). It uses the fallback admin PAT — hook
// management needs repo-admin rights the member's OAuth token may lack. Errors
// are logged, never surfaced: webhook wiring is an enhancement, not a gate.
func (s *Server) ensureServiceWebhook(ctx context.Context, svc *domain.Service) {
	if s.cfg.WebhookURL == "" || s.cfg.WebhookSecret == "" {
		return
	}
	if svc.RepoKind != domain.RepoKindProvider || svc.Provider != domain.ProviderGitea {
		return // only gitea has an inbound receiver today (M7)
	}
	if s.creds == nil || s.factory == nil {
		return
	}
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		return
	}
	tok, err := s.creds.Resolve(ctx, domain.ProviderGitea, nil) // admin PAT
	if err != nil {
		s.log.Warn("service webhook: no gitea PAT; skipping registration", "service", svc.ID)
		return
	}
	client, err := s.factory.PRClient(domain.ProviderGitea, tok.Value, tok.Scheme)
	if err != nil {
		return
	}
	hooker, ok := client.(interface {
		EnsureCommentWebhook(ctx context.Context, owner, repo, hookURL, secret string) error
	})
	if !ok {
		return
	}
	if err := hooker.EnsureCommentWebhook(ctx, owner, repo, s.cfg.WebhookURL, s.cfg.WebhookSecret); err != nil {
		s.log.Warn("service webhook: registration failed (service still usable)",
			"service", svc.ID, "repo", svc.RepoOwnerName, "err", err)
		return
	}
	s.log.Info("service webhook: @mention hook ensured", "service", svc.ID, "repo", svc.RepoOwnerName)
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
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
	services, err := s.st.ListServices(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list services")
		return
	}
	if services == nil {
		services = []domain.Service{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": services})
}

// servicePatch carries the optional fields of a service update. An empty string
// means "leave unchanged" (mirrors the project PATCH shim style).
type servicePatch struct {
	Name          string
	RepoURL       string
	Provider      string
	OwnerName     string
	GitMode       string
	DefaultBranch string
}

// applyServicePatch mutates svc in place with any provided fields and re-checks
// the draft_pr⇒provider constraint. Returns (code, msg) on a validation error.
func applyServicePatch(svc *domain.Service, p servicePatch) (string, string) {
	if v := strings.TrimSpace(p.Name); v != "" {
		svc.Name = v
	}
	if v := strings.TrimSpace(p.DefaultBranch); v != "" {
		svc.DefaultBranch = v
	}
	if v := domain.GitMode(strings.TrimSpace(p.GitMode)); v != "" {
		if !domain.ValidGitMode(v) {
			return "bad_request", "git_mode must be 'readonly' or 'draft_pr'"
		}
		svc.GitMode = v
	}
	// Repo retarget: only when a repo field is supplied.
	if strings.TrimSpace(p.RepoURL) != "" || strings.TrimSpace(p.OwnerName) != "" {
		spec, code, msg := classifyRepo(p.RepoURL, p.Provider, p.OwnerName)
		if code != "" {
			return code, msg
		}
		svc.RepoKind = spec.RepoKind
		svc.Provider = spec.Provider
		svc.RepoOwnerName = spec.RepoOwnerName
		svc.RawRepoURL = spec.RawRepoURL
	} else if v := domain.GitProvider(strings.TrimSpace(p.Provider)); v != "" && svc.RepoKind == domain.RepoKindProvider {
		// Provider-only change on an existing provider service.
		if !domain.ValidProvider(v) {
			return "bad_request", "provider must be gitea, github or gitlab"
		}
		svc.Provider = v
	}
	return validateServiceConstraints(svc)
}

type patchServiceReq struct {
	Name          string `json:"name"`
	RepoURL       string `json:"repo_url"`
	Provider      string `json:"provider"`
	OwnerName     string `json:"owner_name"`
	GitMode       string `json:"git_mode"`
	DefaultBranch string `json:"default_branch"`
	// DefaultModelID sets the service's default model (D21). Presence semantics
	// (pointer): omitted/null = unchanged; "" = clear (no default); an id = set,
	// validated to be granted to this service's project. Kept separate from the
	// empty-string="unchanged" fields above because clearing a default is a
	// meaningful action.
	DefaultModelID *string `json:"default_model_id"`
}

func (s *Server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	svc, err := s.st.GetService(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleOwner) {
		return
	}
	var req patchServiceReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if code, msg := applyServicePatch(svc, servicePatch{
		Name:          req.Name,
		RepoURL:       req.RepoURL,
		Provider:      req.Provider,
		OwnerName:     req.OwnerName,
		GitMode:       req.GitMode,
		DefaultBranch: req.DefaultBranch,
	}); code != "" {
		writeError(w, http.StatusBadRequest, code, msg)
		return
	}
	// Default model (D21): pointer presence — omitted = unchanged; "" = clear; an
	// id must be granted to this service's project (else 400 model_not_granted).
	if req.DefaultModelID != nil {
		id := strings.TrimSpace(*req.DefaultModelID)
		if id == "" {
			svc.DefaultModelID = nil
		} else {
			granted, gerr := s.projectGrantsModel(r.Context(), svc.ProjectID, id)
			if gerr != nil {
				writeError(w, http.StatusInternalServerError, "internal", "could not check model grant")
				return
			}
			if !granted {
				writeError(w, http.StatusBadRequest, "model_not_granted",
					"that model is not authorized for this project — a cluster admin must grant it first")
				return
			}
			svc.DefaultModelID = &id
		}
	}
	if err := s.st.UpdateService(r.Context(), svc); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update service")
		return
	}
	writeJSON(w, http.StatusOK, svc)
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	svc, err := s.st.GetService(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleOwner) {
		return
	}
	// A service with runs cannot be deleted (runs.service_id has no cascade —
	// runs are historical). Return a clean 409 rather than a raw FK error.
	if runs, err := s.st.ListRunsByService(r.Context(), id, 1); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not check service runs")
		return
	} else if len(runs) > 0 {
		writeError(w, http.StatusConflict, "conflict", "service has runs and cannot be deleted")
		return
	}
	if err := s.st.DeleteService(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "service not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not delete service")
		return
	}
	// Feature C — best-effort delete the service's persistent workspace PVC so no
	// stale working copy / jcode memory lingers (D05 tenant-erasure guardrail). A
	// failure (or no launcher / no PVC) is only logged, never blocks the delete:
	// the DB row is already gone, and a NotFound is swallowed by the launcher.
	if s.launcher != nil {
		if err := s.launcher.DeleteWorkspacePVC(r.Context(), id); err != nil {
			s.log.Warn("delete service: workspace pvc cleanup failed (non-fatal)", "service", id, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
