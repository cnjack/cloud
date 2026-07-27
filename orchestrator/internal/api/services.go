package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	Name           string `json:"name"`
	InstallationID string `json:"installation_id"`
	ProviderRepoID string `json:"provider_repo_id"`
	GitMode        string `json:"git_mode"`
	DefaultModelID string `json:"default_model_id"`
}

// serviceBranchView is deliberately smaller than a provider's branch resource.
// Branch commit metadata is neither useful to the composer nor safe to retain in
// the control plane; the selected name is the only value sent on to a run.
type serviceBranchView struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected,omitempty"`
	Default   bool   `json:"default"`
}

var (
	errServiceBranchPluginUnavailable   = errors.New("service branch plugin unavailable")
	errServiceBranchProviderUnavailable = errors.New("service branch provider unavailable")
)

// handleListServiceBranches lists repository branches for an existing,
// plugin-bound Service. It issues a short-lived plugin credential server-side;
// no persistent or run credential is returned to the browser.
func (s *Server) handleListServiceBranches(w http.ResponseWriter, r *http.Request) {
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
	branches, err := s.listBoundServiceBranches(r.Context(), svc)
	if err != nil {
		s.writeServiceBranchError(w, err)
		return
	}
	defaultBranch := strings.TrimSpace(svc.DefaultBranch)
	seen := make(map[string]bool, len(branches))
	views := make([]serviceBranchView, 0, len(branches))
	for _, branch := range branches {
		name := strings.TrimSpace(branch.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		views = append(views, serviceBranchView{Name: name, Protected: branch.Protected, Default: name == defaultBranch})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Default != views[j].Default {
			return views[i].Default
		}
		return strings.ToLower(views[i].Name) < strings.ToLower(views[j].Name)
	})
	confirmedDefault := ""
	if seen[defaultBranch] {
		confirmedDefault = defaultBranch
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": views, "default_branch": confirmedDefault})
}

// listBoundServiceBranches is the single credentialed branch discovery path.
// Both the picker and POST runs use it so a branch accepted by the UI is
// verified again at dispatch time rather than trusted from browser state.
func (s *Server) listBoundServiceBranches(ctx context.Context, svc *domain.Service) ([]provider.Branch, error) {
	if svc == nil || svc.RepoKind != domain.RepoKindProvider || !domain.ValidProvider(svc.Provider) {
		return nil, fmt.Errorf("%w: a plugin-bound Git service is required", errServiceBranchPluginUnavailable)
	}
	binding, err := s.st.GetServiceRepositoryBinding(ctx, svc.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: service has no repository plugin binding", errServiceBranchPluginUnavailable)
	}
	if err != nil {
		return nil, fmt.Errorf("load service repository binding: %w", err)
	}
	installation, err := s.st.GetPluginInstallation(ctx, binding.InstallationID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (installation.ProjectID != svc.ProjectID || installation.Provider != domain.ProviderKind(svc.Provider))) {
		return nil, fmt.Errorf("%w: service repository plugin is unavailable", errServiceBranchPluginUnavailable)
	}
	if err != nil {
		return nil, fmt.Errorf("load service repository plugin: %w", err)
	}
	if installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" {
		return nil, fmt.Errorf("%w: repository plugin is disabled or needs attention", errServiceBranchPluginUnavailable)
	}
	cfg, err := s.st.GetProviderConfig(ctx, installation.Provider)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (!cfg.PluginEnabled || cfg.LastHealthError != "")) {
		return nil, fmt.Errorf("%w: provider configuration is disabled or needs attention", errServiceBranchPluginUnavailable)
	}
	if err != nil {
		return nil, fmt.Errorf("load provider configuration: %w", err)
	}
	if s.pluginCredentialIssuer == nil {
		return nil, fmt.Errorf("%w: repository credentials are unavailable", errServiceBranchPluginUnavailable)
	}
	credential, err := s.pluginCredentialIssuer.IssueRunPluginCredential(ctx, installation, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: repository credentials are unavailable", errServiceBranchPluginUnavailable)
	}
	client, err := provider.IntegrationClientWithScheme(domain.GitProvider(installation.Provider), cfg.BaseURL, credential.AccessToken, credential.Scheme)
	if err != nil {
		return nil, fmt.Errorf("%w: repository credentials are unavailable", errServiceBranchPluginUnavailable)
	}
	lister, ok := client.(provider.BranchLister)
	if !ok {
		return nil, fmt.Errorf("%w: provider cannot list branches", errServiceBranchPluginUnavailable)
	}
	owner, repo, ok := provider.SplitRepo(binding.RepositoryPath)
	if !ok {
		return nil, fmt.Errorf("%w: bound repository path is invalid", errServiceBranchPluginUnavailable)
	}
	branches := make([]provider.Branch, 0)
	for page := 1; page <= 20; page++ {
		pageBranches, listErr := lister.ListBranches(ctx, owner, repo, page, 100)
		if listErr != nil {
			s.log.Warn("list service repository branches", "service_id", svc.ID, "provider", installation.Provider, "err", listErr)
			return nil, fmt.Errorf("%w: could not list repository branches", errServiceBranchProviderUnavailable)
		}
		branches = append(branches, pageBranches...)
		if len(pageBranches) < 100 {
			break
		}
	}
	return branches, nil
}

func (s *Server) writeServiceBranchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errServiceBranchPluginUnavailable):
		writeError(w, http.StatusConflict, "plugin_unavailable", "the repository plugin is unavailable; reconnect it or resolve its health check")
	case errors.Is(err, errServiceBranchProviderUnavailable):
		writeError(w, http.StatusBadGateway, "provider_error", "could not list repository branches")
	default:
		s.log.Error("list service repository branches", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load repository branches")
	}
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	project, err := s.st.GetProject(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
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
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleMember) {
		return
	}
	installationID := strings.TrimSpace(req.InstallationID)
	repositoryID := strings.TrimSpace(req.ProviderRepoID)
	if installationID == "" || repositoryID == "" {
		writeError(w, http.StatusBadRequest, "plugin_repository_required", "installation_id and provider_repo_id are required; bare Git URLs are not supported")
		return
	}
	installation, err := s.st.GetPluginInstallation(r.Context(), installationID)
	if errors.Is(err, store.ErrNotFound) || installation.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "not_found", "project plugin not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project plugin")
		return
	}
	if installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" || installation.Provider == domain.PluginJType {
		writeError(w, http.StatusConflict, "plugin_unavailable", "an enabled, healthy Git provider plugin is required")
		return
	}
	repo, cfg, err := s.resolvePluginRepository(r.Context(), installation, repositoryID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "repository_not_found", "the selected repository is not available to this plugin")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "provider_error", "could not verify the selected repository")
		return
	}
	gitMode := domain.GitMode(strings.TrimSpace(req.GitMode))
	if gitMode == "" {
		gitMode = domain.GitModeReadonly
	}
	if !domain.ValidGitMode(gitMode) {
		writeError(w, http.StatusBadRequest, "bad_request", "git_mode must be 'readonly' or 'draft_pr'")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		parts := strings.Split(repo.FullName, "/")
		name = parts[len(parts)-1]
	}
	// Enforce the (project_id, name) uniqueness up-front for a friendly 409.
	if existing, err := s.st.ListServices(r.Context(), projectID); err == nil {
		for i := range existing {
			if existing[i].Name == name {
				writeError(w, http.StatusConflict, "conflict", "a service named '"+name+"' already exists in this project")
				return
			}
		}
	}
	repoID, err := strconv.ParseInt(repositoryID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_repository_id", "provider_repo_id must be a provider numeric id")
		return
	}
	now := time.Now().UTC()
	branch := strings.TrimSpace(repo.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	cloneURL := strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.Trim(repo.FullName, "/") + ".git"
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: projectID, Name: name,
		RepoKind: domain.RepoKindProvider, Provider: domain.GitProvider(installation.Provider),
		RepoOwnerName: repo.FullName, ProviderRepoID: &repoID, DefaultBranch: branch,
		GitMode: gitMode, CreatedAt: now,
	}
	if strings.TrimSpace(req.DefaultModelID) != "" {
		modelID := strings.TrimSpace(req.DefaultModelID)
		models, modelErr := s.st.ListModelsForProject(r.Context(), projectID)
		if modelErr != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not validate service default model")
			return
		}
		granted := false
		for i := range models {
			if models[i].ID == modelID {
				granted = true
				break
			}
		}
		if !granted {
			writeError(w, http.StatusForbidden, "model_not_granted", "the default model is not authorized for this project")
			return
		}
		svc.DefaultModelID = &modelID
	} else if project.DefaultModelID != nil {
		modelID := *project.DefaultModelID
		svc.DefaultModelID = &modelID
	}
	binding := &domain.ServiceRepositoryBinding{
		ServiceID: svc.ID, InstallationID: installation.ID, ProviderRepoID: repositoryID,
		RepositoryPath: repo.FullName, CloneURL: cloneURL, DefaultBranch: branch,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.st.CreatePluginBoundService(r.Context(), svc, binding); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "repository_already_bound", "this plugin repository is already bound to a Service")
			return
		}
		s.log.Error("create service", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create service")
		return
	}
	svc.RepoHTMLURL = repo.HTMLURL
	writeJSON(w, http.StatusCreated, svc)
}

func (s *Server) resolvePluginRepository(ctx context.Context, installation *domain.PluginInstallation, repositoryID string) (*provider.Repo, *domain.ProviderConfig, error) {
	cfg, err := s.st.GetProviderConfig(ctx, installation.Provider)
	if err != nil {
		return nil, nil, err
	}
	if s.pluginCredentialIssuer == nil {
		return nil, nil, errors.New("plugin credential issuer unavailable")
	}
	credential, err := s.pluginCredentialIssuer.IssueRunPluginCredential(ctx, installation, cfg)
	if err != nil {
		return nil, nil, errors.New("plugin credential unavailable")
	}
	client, err := provider.IntegrationClientWithScheme(
		domain.GitProvider(installation.Provider), cfg.BaseURL,
		credential.AccessToken, credential.Scheme,
	)
	if err != nil {
		return nil, nil, err
	}
	for page := 1; page <= 20; page++ {
		var repos []provider.Repo
		var listErr error
		if installation.Provider == domain.PluginGitHub {
			lister, ok := client.(provider.InstallationRepoLister)
			if !ok {
				return nil, nil, errors.New("GitHub installation repository listing unavailable")
			}
			repos, listErr = lister.ListInstallationRepos(ctx, "", page, 50)
		} else {
			lister, ok := client.(provider.RepoLister)
			if !ok {
				return nil, nil, errors.New("provider repository listing unavailable")
			}
			repos, listErr = lister.ListRepos(ctx, "", page, 50)
		}
		if listErr != nil {
			return nil, nil, listErr
		}
		for i := range repos {
			if strconv.FormatInt(repos[i].ID, 10) == repositoryID {
				return &repos[i], cfg, nil
			}
		}
		if len(repos) < 50 {
			break
		}
	}
	return nil, nil, store.ErrNotFound
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
	if strings.TrimSpace(req.RepoURL) != "" || strings.TrimSpace(req.Provider) != "" ||
		strings.TrimSpace(req.OwnerName) != "" || req.IntegrationID != nil {
		writeError(w, http.StatusBadRequest, "repository_binding_immutable",
			"repository bindings cannot be changed through Service PATCH; create a new Service from the Project Plugin repository picker")
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
