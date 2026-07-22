package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
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
	// (GET /providers/{p}/repos or .../integrations/{iid}/repos). Optional;
	// rename-proof repo identity (0009).
	ProviderRepoID *int64 `json:"provider_repo_id"`
	// IntegrationID binds the new service to a project integration (D19 / F5). When
	// present, a MEMBER (not just owner) may create the service, the repo must be
	// reachable by the integration's bot token, and the service's provider is taken
	// from the integration. Empty => the legacy owner-only bare create.
	IntegrationID string `json:"integration_id"`
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
	var req createServiceReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	integrationID := strings.TrimSpace(req.IntegrationID)
	// RBAC (D19): adding a repo off an EXISTING integration is a member action; a
	// bare create (hand-entered repo, no integration) stays owner-only.
	requiredRole := domain.RoleOwner
	if integrationID != "" {
		requiredRole = domain.RoleMember
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, requiredRole) {
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
	// The picker's numeric repo id (rename-proof identity) is populated BEFORE the
	// integration bind (F5 review C3) so the bind's reachability match can key off
	// the id, not just the owner/name string — a renamed repo still matches.
	if req.ProviderRepoID != nil && svc.RepoKind == domain.RepoKindProvider {
		svc.ProviderRepoID = req.ProviderRepoID
	}
	// Integration binding (D19 / F5): validate the integration belongs to this
	// project, its host is still cluster-allowed (defence in depth — the allowlist
	// may have tightened since it was created), and the repo is reachable by the
	// bot token. The integration's provider is authoritative for the service.
	if integrationID != "" {
		if code, msg := s.bindServiceIntegration(r.Context(), projectID, integrationID, svc); code != "" {
			writeError(w, integrationBindStatus(code), code, msg)
			return
		}
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
	if err := s.st.CreateService(r.Context(), svc); err != nil {
		s.log.Error("create service", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create service")
		return
	}
	svc.RepoHTMLURL = s.serviceRepoHTMLURL(r.Context(), svc)
	writeJSON(w, http.StatusCreated, svc)
}

// serviceRepoHTMLURL derives the browser destination from server-owned
// integration/OAuth configuration. It never accepts a client-supplied URL.
func (s *Server) serviceRepoHTMLURL(ctx context.Context, svc *domain.Service) string {
	if svc == nil || svc.RepoKind != domain.RepoKindProvider || !domain.ValidProvider(svc.Provider) {
		return ""
	}
	if _, _, ok := provider.SplitRepo(svc.RepoOwnerName); !ok {
		return ""
	}
	base := ""
	if svc.IntegrationID != nil && *svc.IntegrationID != "" {
		if integration, err := s.st.GetIntegration(ctx, *svc.IntegrationID); err == nil {
			base = integration.Host
		}
	}
	if base == "" {
		for _, configured := range s.cfg.OAuthProviders {
			if domain.GitProvider(configured.ID) == svc.Provider && strings.TrimSpace(configured.ExternalURL) != "" {
				base = configured.ExternalURL
				break
			}
		}
	}
	if base == "" {
		switch svc.Provider {
		case domain.ProviderGitHub:
			base = "https://github.com"
		case domain.ProviderGitLab:
			base = "https://gitlab.com"
		case domain.ProviderGitea:
			base = s.cfg.GiteaURL
		}
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	parts := strings.Split(strings.Trim(svc.RepoOwnerName, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.Join(parts, "/")
	return u.String()
}

// integrationBindStatus maps a bindServiceIntegration error code to an HTTP status.
func integrationBindStatus(code string) int {
	switch code {
	case "not_found":
		return http.StatusNotFound
	case "internal":
		return http.StatusInternalServerError
	case "cipher_not_configured", "cipher_error":
		return http.StatusConflict
	case "provider_error":
		return http.StatusBadGateway
	default: // bad_request, host_not_allowed, repo_not_reachable
		return http.StatusBadRequest
	}
}

// bindServiceIntegration validates that svc may bind to integration integrationID
// and mutates svc accordingly (D19 / F5): the integration must belong to the
// project, its host must still be cluster-allowed (defence in depth), svc must be a
// provider repo, and the repo must be REACHABLE by the integration's bot token (a
// member must not use the bot to reach a repo the picker never surfaced). On
// success svc gets its Provider/RepoOwnerName/ProviderRepoID canonicalised from the
// integration + reachable repo, and IntegrationID set. Returns (code, msg); ""
// code on success. The caller maps the code to a status via integrationBindStatus.
func (s *Server) bindServiceIntegration(ctx context.Context, projectID, integrationID string, svc *domain.Service) (string, string) {
	in, err := s.st.GetIntegration(ctx, integrationID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && in.ProjectID != projectID) {
		return "not_found", "integration not found in this project"
	}
	if err != nil {
		return "internal", "could not load integration"
	}
	if !s.gitHostAllowed(in.Host) {
		return "host_not_allowed",
			"the integration's git host '" + in.Host + "' is no longer in this cluster's allowed hosts"
	}
	if svc.RepoKind != domain.RepoKindProvider || strings.TrimSpace(svc.RepoOwnerName) == "" {
		return "bad_request", "an integration-bound service needs a provider repository (owner/name)"
	}
	// The integration's provider is authoritative for the service.
	svc.Provider = in.Provider
	if code, msg := validateServiceConstraints(svc); code != "" {
		return code, msg
	}
	// Reachability check against the bot token's visible repos.
	client, code, msg := s.integrationClient(in)
	if code != "" {
		return code, msg
	}
	lister, ok := client.(provider.RepoLister)
	if !ok {
		return "internal", "provider client cannot list repositories"
	}
	_, name, _ := provider.SplitRepo(svc.RepoOwnerName)
	repos, err := lister.ListRepos(ctx, name, 1, 50)
	if err != nil {
		return "provider_error",
			"could not verify the repository against " + string(in.Provider) + ": " + summarizeProviderErr(err)
	}
	for i := range repos {
		if strings.EqualFold(repos[i].FullName, svc.RepoOwnerName) ||
			(svc.ProviderRepoID != nil && repos[i].ID == *svc.ProviderRepoID) {
			svc.RepoOwnerName = repos[i].FullName // canonicalise (rename-proof id below)
			id := repos[i].ID
			svc.ProviderRepoID = &id
			iid := in.ID
			svc.IntegrationID = &iid
			return "", ""
		}
	}
	return "repo_not_reachable",
		"the repository '" + svc.RepoOwnerName + "' is not reachable with this integration's credential"
}

// commentWebhookRegistrar is intentionally narrower than provider.Provider:
// only concrete provider clients that can manage repository webhooks implement
// it. The explicit type assertion lets an unsupported deployment fail visibly.
type commentWebhookRegistrar interface {
	EnsureCommentWebhook(ctx context.Context, owner, repo, hookURL, secret string) error
}

type webhookSetupView struct {
	Provider domain.GitProvider `json:"provider"`
	Endpoint string             `json:"endpoint"`
	Status   string             `json:"status"`
}

// handleEnsureServiceWebhook registers (or idempotently re-synchronizes) the
// @jcode PR/MR-comment webhook for one provider-backed service. This is an
// explicit member action: it uses ONLY the requesting user's OAuth grant, never
// a project integration token or the legacy cluster PAT. A service creation must
// remain side-effect free with respect to an external repository so every
// unavailable dependency and permission failure can be shown in the Console.
func (s *Server) handleEnsureServiceWebhook(w http.ResponseWriter, r *http.Request) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleMember) {
		return
	}
	if svc.RepoKind != domain.RepoKindProvider || !domain.ValidProvider(svc.Provider) {
		writeError(w, http.StatusConflict, "provider_webhook_unavailable",
			"This service is not a provider-backed repository, so it cannot receive PR review webhooks.")
		return
	}
	if strings.TrimSpace(s.cfg.WebhookURL) == "" || strings.TrimSpace(s.cfg.WebhookSecret) == "" {
		writeError(w, http.StatusConflict, "webhook_not_configured",
			"This cluster has not configured a webhook receiver. Contact a cluster administrator.")
		return
	}
	if _, configured := s.oauth[svc.Provider]; !configured {
		writeError(w, http.StatusConflict, "oauth_not_configured",
			"OAuth is not configured for this provider. Contact a cluster administrator.")
		return
	}
	userID := principalFrom(r.Context()).userID()
	if userID == "" || s.creds == nil {
		writeError(w, http.StatusConflict, "oauth_not_connected",
			"Connect your provider account with OAuth before enabling this webhook.")
		return
	}
	token, err := s.creds.ResolveUserOAuth(r.Context(), svc.Provider, userID)
	if err != nil {
		if errors.Is(err, credentials.ErrNoCredential) {
			writeError(w, http.StatusConflict, "oauth_not_connected",
				"Connect your provider account with OAuth before enabling this webhook.")
			return
		}
		s.log.Warn("resolve webhook OAuth credential", "service", svc.ID, "provider", svc.Provider, "err", err)
		writeError(w, http.StatusBadGateway, "oauth_unavailable",
			"Could not use your provider OAuth connection. Reconnect it and try again.")
		return
	}
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		writeError(w, http.StatusConflict, "provider_webhook_unavailable",
			"This service does not have a valid provider repository name for webhook setup.")
		return
	}
	hookURL := webhookURLForProvider(s.cfg.WebhookURL, svc.Provider)
	if hookURL == "" || s.factory == nil {
		writeError(w, http.StatusConflict, "provider_webhook_unavailable",
			"This provider webhook cannot be configured in the current cluster.")
		return
	}
	client, err := s.factory.PRClient(svc.Provider, token.Value, token.Scheme)
	if err != nil {
		s.log.Warn("build webhook provider client", "service", svc.ID, "provider", svc.Provider, "err", err)
		writeError(w, http.StatusConflict, "provider_webhook_unavailable",
			"This provider webhook cannot be configured in the current cluster.")
		return
	}
	hooker, ok := client.(commentWebhookRegistrar)
	if !ok {
		writeError(w, http.StatusConflict, "provider_webhook_unavailable",
			"This provider client does not support repository webhook setup.")
		return
	}
	if err := hooker.EnsureCommentWebhook(r.Context(), owner, repo, hookURL, s.cfg.WebhookSecret); err != nil {
		s.log.Warn("service webhook registration failed", "service", svc.ID, "provider", svc.Provider, "repo", svc.RepoOwnerName, "err", err)
		writeError(w, http.StatusBadGateway, "webhook_registration_failed",
			"The provider rejected or could not reach webhook registration. Reconnect OAuth with repository-hook access and confirm you are a repository administrator.")
		return
	}
	s.log.Info("service webhook synchronized", "service", svc.ID, "provider", svc.Provider, "repo", svc.RepoOwnerName, "actor", userID)
	writeJSON(w, http.StatusOK, webhookSetupView{Provider: svc.Provider, Endpoint: hookURL, Status: "synced"})
}

// webhookURLForProvider derives the inbound webhook URL for prov from the single
// configured WEBHOOK_URL (F13). WEBHOOK_URL points at ONE receiver
// (…/webhooks/gitea by deployment convention); the github/gitlab receivers are
// SIBLING paths on the same orchestrator, so a trailing "/webhooks/<known>"
// segment is swapped for "/webhooks/<prov>". A WEBHOOK_URL without a known
// trailing segment is treated as a base and the path is appended. This keeps the
// single-env deploy working for all three providers with no manifest change.
func webhookURLForProvider(base string, prov domain.GitProvider) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	for _, p := range []string{"gitea", "github", "gitlab"} {
		if strings.HasSuffix(base, "/webhooks/"+p) {
			base = strings.TrimSuffix(base, "/webhooks/"+p)
			break
		}
	}
	return base + "/webhooks/" + string(prov)
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
	for i := range services {
		services[i].RepoHTMLURL = s.serviceRepoHTMLURL(r.Context(), &services[i])
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
	// IntegrationID binds/unbinds the service to a project integration (D19 / F5).
	// Presence semantics (pointer): omitted = unchanged; "" = unbind (legacy
	// credential path); an id = bind, validated to belong to this project + a still
	// cluster-allowed host. The integration's provider becomes the service's.
	IntegrationID *string `json:"integration_id"`
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
	// Integration binding (D19 / F5): omitted = unchanged; "" = unbind; an id = the
	// FULL bind validation, symmetric with create (F5 review C3): project scoping,
	// cluster host allowlist, provider repo shape AND the reachability check — a
	// PATCH-bind must not skip the "repo is in the bot's reachable set" gate the
	// create path enforces.
	if req.IntegrationID != nil {
		id := strings.TrimSpace(*req.IntegrationID)
		if id == "" {
			svc.IntegrationID = nil
		} else if code, msg := s.bindServiceIntegration(r.Context(), svc.ProjectID, id, svc); code != "" {
			writeError(w, integrationBindStatus(code), code, msg)
			return
		}
	}
	if err := s.st.UpdateService(r.Context(), svc); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update service")
		return
	}
	svc.RepoHTMLURL = s.serviceRepoHTMLURL(r.Context(), svc)
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
	// Fence new dispatches before touching runtime resources. The marker is
	// durable and idempotent so a failed cleanup can be retried safely.
	if err := s.st.MarkServiceDeleting(r.Context(), id, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "service not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not prepare service deletion")
		return
	}

	runs, err := s.st.ListRunsByService(r.Context(), id, -1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service runs for deletion")
		return
	}

	// Cancel every active run before deleting its Job. CancelRun is a CAS and
	// returns the committed row, so a concurrent queued->scheduling transition
	// cannot hide the Job name from cleanup.
	jobs := map[string]struct{}{}
	for i := range runs {
		run := &runs[i]
		committed := run
		if !run.Status.Terminal() && run.Status != domain.StatusBlocked {
			committed, err = s.st.CancelRun(r.Context(), run.ID, "CanceledByServiceDeletion", time.Now().UTC())
			if err != nil {
				if errors.Is(err, store.ErrInvalidTransition) {
					committed, err = s.st.GetRun(r.Context(), run.ID)
				}
				if err != nil {
					writeError(w, http.StatusServiceUnavailable, "cleanup_failed", "could not stop all service runs; retry deletion")
					return
				}
			}
			s.emitStatus(r.Context(), committed)
		}
		if committed.K8sJobName != "" {
			jobs[committed.K8sJobName] = struct{}{}
		}
	}
	// The archive helper Job is service-scoped rather than run-scoped.
	jobs[k8s.ArchiveJobName(id)] = struct{}{}
	if len(jobs) > 0 && s.launcher == nil {
		// An API-only deployment cannot prove that named cluster Jobs stopped.
		for name := range jobs {
			if name != k8s.ArchiveJobName(id) {
				writeError(w, http.StatusServiceUnavailable, "cleanup_unavailable", "runtime cleanup is unavailable; retry when the launcher is connected")
				return
			}
		}
	}
	if s.launcher != nil {
		for name := range jobs {
			if err := s.launcher.DeleteJob(r.Context(), name); err != nil {
				s.log.Warn("delete service: job cleanup failed", "service", id, "job", name, "err", err)
				writeError(w, http.StatusServiceUnavailable, "cleanup_failed", "could not stop all service jobs; retry deletion")
				return
			}
		}
		if err := s.launcher.DeleteWorkspacePVC(r.Context(), id); err != nil {
			s.log.Warn("delete service: workspace pvc cleanup failed", "service", id, "err", err)
			writeError(w, http.StatusServiceUnavailable, "cleanup_failed", "could not delete the service workspace; retry deletion")
			return
		}
	}

	// A restored workspace leaves the deterministic cold archive behind, so
	// delete by deterministic key even when archive_key has already been cleared.
	if s.archiveCleaner != nil {
		if err := s.archiveCleaner.Delete(r.Context(), "workspaces/"+id+".tar.zst"); err != nil {
			s.log.Warn("delete service: archive cleanup failed", "service", id, "err", err)
			writeError(w, http.StatusServiceUnavailable, "cleanup_failed", "could not delete the archived workspace; retry deletion")
			return
		}
	} else if svc.ArchiveKey != "" {
		writeError(w, http.StatusServiceUnavailable, "cleanup_unavailable", "archived workspace cleanup is unavailable; retry when object storage is connected")
		return
	}

	// Database cleanup is last: runs are deleted first in the store transaction,
	// which cascades their events, artifacts, messages and permissions; service
	// schedules, automations, webhook state and kanban links cascade with service.
	if err := s.st.DeleteService(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "service not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not delete service")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
