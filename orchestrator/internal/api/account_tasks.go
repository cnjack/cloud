package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

const accountExecutionConsentVersion = "account-repository-execution-v1"

type accountRepositoryTarget struct {
	Provider           domain.GitProvider `json:"provider"`
	ProviderRepoID     string             `json:"provider_repo_id"`
	RepositoryID       string             `json:"repository_id,omitempty"`
	FullName           string             `json:"full_name"`
	Description        string             `json:"description,omitempty"`
	DefaultBranch      string             `json:"default_branch"`
	Private            bool               `json:"private"`
	HTMLURL            string             `json:"html_url,omitempty"`
	ExecutionAvailable bool               `json:"execution_available"`
	ExecutionError     string             `json:"execution_error,omitempty"`
}

type accountRepositorySource struct {
	Provider domain.GitProvider `json:"provider"`
	Account  string             `json:"account"`
	Status   string             `json:"status"`
	Message  string             `json:"message,omitempty"`
}

type accountRepositoryCatalog struct {
	Repositories []accountRepositoryTarget `json:"repositories"`
	Sources      []accountRepositorySource `json:"sources"`
}

type accountTaskRequest struct {
	Provider       string `json:"provider"`
	ProviderRepoID string `json:"provider_repo_id"`
	createRunReq
}

type accountTaskResponse struct {
	Run        *domain.Run     `json:"run"`
	Repository *domain.Service `json:"repository"`
}

func (s *Server) accountPrincipal(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid := principalFrom(r.Context()).userID()
	if uid == "" {
		writeError(w, http.StatusForbidden, "account_required", "a signed-in human Account is required")
		return "", false
	}
	return uid, true
}

func (s *Server) accountProviderRepositories(
	ctx context.Context,
	identity *domain.UserIdentity,
	query string,
) ([]provider.Repo, *domain.ProviderConfig, error) {
	if identity == nil || s.creds == nil {
		return nil, nil, credentials.ErrNoCredential
	}
	cfg, err := s.st.GetProviderConfig(ctx, domain.ProviderKind(identity.Provider))
	if err != nil || strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, nil, store.ErrNotFound
	}
	token, err := s.creds.ResolveUserOAuth(ctx, identity.Provider, identity.UserID)
	if err != nil {
		return nil, cfg, err
	}
	client, err := provider.IntegrationClientWithScheme(identity.Provider, cfg.BaseURL, token.Value, token.Scheme)
	if err != nil {
		return nil, cfg, err
	}
	lister, ok := client.(provider.RepoLister)
	if !ok {
		return nil, cfg, errors.New("provider cannot list repositories")
	}
	const pageSize = 50
	items := make([]provider.Repo, 0, pageSize)
	for page := 1; page <= 20; page++ {
		batch, listErr := lister.ListRepos(ctx, query, page, pageSize)
		if listErr != nil {
			return nil, cfg, listErr
		}
		items = append(items, batch...)
		if len(batch) < pageSize {
			break
		}
	}
	return items, cfg, nil
}

func (s *Server) accountOwnedProjectIDs(ctx context.Context, uid string) (map[string]bool, error) {
	projects, err := s.st.ListProjectsForUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	owned := make(map[string]bool, len(projects))
	for i := range projects {
		if projects[i].OwnerUserID == uid {
			owned[projects[i].ID] = true
		}
	}
	return owned, nil
}

func (s *Server) handleListAccountRepositories(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.accountPrincipal(w, r)
	if !ok {
		return
	}
	identities, err := s.st.ListIdentities(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load linked provider accounts")
		return
	}
	materialized, err := s.st.ListRepositoriesForUser(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Repository details")
		return
	}
	ownedProjects, err := s.accountOwnedProjectIDs(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Account workspaces")
		return
	}
	byProviderRepo := make(map[string]string, len(materialized))
	for i := range materialized {
		if ownedProjects[materialized[i].ProjectID] && materialized[i].ProviderRepoID != nil {
			key := string(materialized[i].Provider) + ":" + strconv.FormatInt(*materialized[i].ProviderRepoID, 10)
			byProviderRepo[key] = materialized[i].ID
		}
	}

	out := accountRepositoryCatalog{Repositories: []accountRepositoryTarget{}, Sources: []accountRepositorySource{}}
	seenProviders := map[domain.GitProvider]bool{}
	for i := range identities {
		identity := &identities[i]
		if seenProviders[identity.Provider] || !domain.ValidProvider(identity.Provider) {
			continue
		}
		seenProviders[identity.Provider] = true
		repos, cfg, listErr := s.accountProviderRepositories(r.Context(), identity, r.URL.Query().Get("q"))
		source := accountRepositorySource{Provider: identity.Provider, Account: identity.Username, Status: "ready"}
		if listErr != nil {
			source.Status = "unavailable"
			source.Message = "Repository access is unavailable; reconnect this provider account"
			out.Sources = append(out.Sources, source)
			continue
		}
		executionAvailable := cfg.PluginEnabled
		executionError := ""
		if !executionAvailable {
			executionError = "Cloud execution is disabled for this provider"
		}
		for _, repo := range repos {
			repoID := strconv.FormatInt(repo.ID, 10)
			out.Repositories = append(out.Repositories, accountRepositoryTarget{
				Provider: identity.Provider, ProviderRepoID: repoID,
				RepositoryID: byProviderRepo[string(identity.Provider)+":"+repoID],
				FullName:     repo.FullName, Description: repo.Description,
				DefaultBranch: repo.DefaultBranch, Private: repo.Private, HTMLURL: repo.HTMLURL,
				ExecutionAvailable: executionAvailable, ExecutionError: executionError,
			})
		}
		out.Sources = append(out.Sources, source)
	}
	sort.Slice(out.Repositories, func(i, j int) bool {
		if out.Repositories[i].Provider != out.Repositories[j].Provider {
			return out.Repositories[i].Provider < out.Repositories[j].Provider
		}
		return strings.ToLower(out.Repositories[i].FullName) < strings.ToLower(out.Repositories[j].FullName)
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) resolveAccountRepository(
	ctx context.Context,
	uid string,
	providerName domain.GitProvider,
	repositoryID string,
) (*provider.Repo, *domain.UserIdentity, *domain.ProviderConfig, error) {
	identity, err := s.st.GetIdentityForUser(ctx, uid, providerName)
	if err != nil {
		return nil, nil, nil, credentials.ErrNoCredential
	}
	repos, cfg, err := s.accountProviderRepositories(ctx, identity, "")
	if err != nil {
		return nil, identity, cfg, err
	}
	for i := range repos {
		if strconv.FormatInt(repos[i].ID, 10) == repositoryID {
			return &repos[i], identity, cfg, nil
		}
	}
	return nil, identity, cfg, store.ErrNotFound
}

func (s *Server) ensurePersonalProject(ctx context.Context, uid string) (*domain.Project, error) {
	projects, err := s.st.ListProjectsForUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].OwnerUserID == uid && projects[i].Name == "Personal workspace" {
			return &projects[i], nil
		}
	}
	for i := range projects {
		if projects[i].OwnerUserID == uid {
			return &projects[i], nil
		}
	}
	now := time.Now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "Personal workspace", OwnerUserID: uid, CreatedAt: now}
	if err := s.st.CreateProject(ctx, project); err != nil {
		return nil, err
	}
	if err := s.st.UpsertMember(ctx, &domain.ProjectMember{
		ProjectID: project.ID, UserID: uid, Role: domain.RoleOwner, CreatedAt: now,
	}); err != nil {
		_ = s.st.DeleteProject(ctx, project.ID)
		return nil, err
	}
	return project, nil
}

func (s *Server) syncAccountExecutionPlugin(
	ctx context.Context,
	projectID, uid string,
	identity *domain.UserIdentity,
	cfg *domain.ProviderConfig,
) (*domain.PluginInstallation, error) {
	if identity == nil || cfg == nil || !cfg.PluginEnabled || len(identity.AccessTokenEnc) == 0 {
		return nil, credentials.ErrPluginCredentialUnavailable
	}
	now := time.Now().UTC()
	installation, err := s.st.GetPluginInstallationForProject(ctx, projectID, domain.ProviderKind(identity.Provider))
	create := errors.Is(err, store.ErrNotFound)
	if create {
		installation = &domain.PluginInstallation{
			ID: domain.NewID(), ProjectID: projectID, Provider: domain.ProviderKind(identity.Provider), CreatedAt: now,
		}
	} else if err != nil {
		return nil, err
	}
	changed := create || installation.ExternalAccountID != identity.ProviderUID ||
		installation.ExternalAccount != identity.Username || installation.Status != domain.PluginStatusEnabled ||
		installation.ConfigRevision != cfg.ConfigRevision || installation.GitHubInstallID != "" ||
		!bytes.Equal(installation.AccessTokenEnc, identity.AccessTokenEnc) ||
		!bytes.Equal(installation.RefreshTokenEnc, identity.RefreshTokenEnc)
	installation.Status = domain.PluginStatusEnabled
	installation.ExternalAccountID, installation.ExternalAccount = identity.ProviderUID, identity.Username
	installation.GitHubInstallID = ""
	installation.Scopes = []string{"account_oauth"}
	installation.AccessTokenEnc = append([]byte(nil), identity.AccessTokenEnc...)
	installation.RefreshTokenEnc = append([]byte(nil), identity.RefreshTokenEnc...)
	installation.TokenExpiresAt = identity.TokenExpiresAt
	installation.ConsentVersion = accountExecutionConsentVersion
	installation.ConsentedBy, installation.ConsentedAt = uid, now
	installation.ConfigRevision = cfg.ConfigRevision
	installation.LastHealthError, installation.LastHealthyAt = "", &now
	if !changed {
		return installation, nil
	}
	if create {
		err = s.st.CreatePluginInstallation(ctx, installation)
	} else {
		err = s.st.UpdatePluginInstallation(ctx, installation)
	}
	return installation, err
}

func (s *Server) ensureAccountRepository(
	ctx context.Context,
	uid string,
	repo *provider.Repo,
	identity *domain.UserIdentity,
	cfg *domain.ProviderConfig,
) (*domain.Service, error) {
	visible, err := s.st.ListRepositoriesForUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	ownedProjects, err := s.accountOwnedProjectIDs(ctx, uid)
	if err != nil {
		return nil, err
	}
	for i := range visible {
		if ownedProjects[visible[i].ProjectID] && visible[i].Provider == identity.Provider && visible[i].ProviderRepoID != nil &&
			strconv.FormatInt(*visible[i].ProviderRepoID, 10) == strconv.FormatInt(repo.ID, 10) {
			if _, syncErr := s.syncAccountExecutionPlugin(ctx, visible[i].ProjectID, uid, identity, cfg); syncErr != nil {
				return nil, syncErr
			}
			return &visible[i], nil
		}
	}
	project, err := s.ensurePersonalProject(ctx, uid)
	if err != nil {
		return nil, err
	}
	installation, err := s.syncAccountExecutionPlugin(ctx, project.ID, uid, identity, cfg)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	branch := strings.TrimSpace(repo.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	repoID := repo.ID
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: repo.FullName,
		RepoKind: domain.RepoKindProvider, Provider: identity.Provider,
		RepoOwnerName: repo.FullName, ProviderRepoID: &repoID, DefaultBranch: branch,
		GitMode: domain.GitModeDraftPR, PRReadyPolicy: domain.PRReadyPolicyLifecycleAware,
		RunnerProfile: "default", RepoHTMLURL: repo.HTMLURL, CreatedAt: now,
	}
	binding := &domain.ServiceRepositoryBinding{
		ServiceID: service.ID, InstallationID: installation.ID,
		ProviderRepoID: strconv.FormatInt(repo.ID, 10), RepositoryPath: repo.FullName,
		CloneURL:      strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.Trim(repo.FullName, "/") + ".git",
		DefaultBranch: branch, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.st.CreatePluginBoundService(ctx, service, binding); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			visible, listErr := s.st.ListRepositoriesForUser(ctx, uid)
			if listErr == nil {
				ownedProjects, ownedErr := s.accountOwnedProjectIDs(ctx, uid)
				if ownedErr != nil {
					return nil, ownedErr
				}
				for i := range visible {
					if ownedProjects[visible[i].ProjectID] && visible[i].Provider == identity.Provider &&
						visible[i].ProviderRepoID != nil && *visible[i].ProviderRepoID == repo.ID {
						return &visible[i], nil
					}
				}
			}
		}
		return nil, err
	}
	return service, nil
}

func (s *Server) handleCreateAccountTask(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.accountPrincipal(w, r)
	if !ok {
		return
	}
	var req accountTaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	providerName := domain.GitProvider(strings.TrimSpace(req.Provider))
	repositoryID := strings.TrimSpace(req.ProviderRepoID)
	if !domain.ValidProvider(providerName) || repositoryID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "provider and provider_repo_id are required")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt is required")
		return
	}
	repo, identity, cfg, err := s.resolveAccountRepository(r.Context(), uid, providerName, repositoryID)
	switch {
	case errors.Is(err, credentials.ErrNoCredential):
		writeError(w, http.StatusConflict, "provider_account_required", "link this provider account before starting a task")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "repository_not_found", "the selected repository is not available to this Account")
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, "provider_error", "could not verify the selected repository")
		return
	case !cfg.PluginEnabled:
		writeError(w, http.StatusConflict, "provider_execution_unavailable", "Cloud execution is disabled for this provider")
		return
	}
	repository, err := s.ensureAccountRepository(r.Context(), uid, repo, identity, cfg)
	if errors.Is(err, credentials.ErrPluginCredentialUnavailable) {
		writeError(w, http.StatusConflict, "provider_execution_unavailable", "the linked provider account cannot be used for Cloud execution")
		return
	}
	if err != nil {
		s.log.Error("prepare account repository task", "provider", providerName, "repository_id", repositoryID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not prepare the Repository task")
		return
	}
	s.createRunForServiceRequest(w, r, repository, req.createRunReq, func(run *domain.Run) any {
		return accountTaskResponse{Run: run, Repository: repository}
	})
}
