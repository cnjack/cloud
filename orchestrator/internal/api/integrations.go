package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// integrationView is the API shape of an integration (D19 / F5). It NEVER carries
// the token — only token_set. host/provider/bot_username are member-visible (they
// are not secrets; the token is).
type integrationView struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Host        string `json:"host"`
	CredType    string `json:"cred_type"`
	BotUsername string `json:"bot_username"`
	TokenSet    bool   `json:"token_set"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func integrationViewOf(in *domain.Integration) integrationView {
	return integrationView{
		ID:          in.ID,
		ProjectID:   in.ProjectID,
		Name:        in.Name,
		Provider:    string(in.Provider),
		Host:        in.Host,
		CredType:    string(in.CredType),
		BotUsername: in.BotUsername,
		TokenSet:    in.TokenSet(),
		CreatedAt:   in.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   in.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// gitHostAllowed reports whether host is permitted by the cluster git-host
// allowlist (D20). An empty allowlist imposes NO restriction (suitable only for
// closed deployments); otherwise the host is compared in the canonical
// "hostname[:port]" form (domain.NormalizeGitHost) — the comparison is
// PORT-SENSITIVE (SSRF review C1②), so an entry for gitea.svc:3000 does not
// open gitea.svc:9999.
func (s *Server) gitHostAllowed(host string) bool {
	if len(s.cfg.AllowedGitHosts) == 0 {
		return true
	}
	h := domain.NormalizeGitHost(host)
	if h == "" {
		return false
	}
	for _, a := range s.cfg.AllowedGitHosts {
		if domain.NormalizeGitHost(a) == h {
			return true
		}
	}
	return false
}

// integrationHostMatchesWiring enforces the CURRENT deployment constraint (F5
// review P2): the actual git operations — clone URL derivation
// (domain.ServiceCloneURL) and the PR clients (provider.Factory) — run against
// the CLUSTER-configured hosts: gitea via GITEA_URL, github/gitlab via their
// public hosts. An integration host that differed would VERIFY the token against
// one host but push/PR against another, a silent mismatch. Reject it up front
// with a typed 400 host_mismatch. Wiring per-integration base URLs through the
// clone/PR paths (true multi-host) is a recorded follow-up (11-api.md §2.5c).
// Returns (msg, ok); msg explains the mismatch when !ok.
func (s *Server) integrationHostMatchesWiring(prov domain.GitProvider, host string) (string, bool) {
	h := domain.NormalizeGitHost(host)
	switch prov {
	case domain.ProviderGitea:
		g := domain.NormalizeGitHost(s.cfg.GiteaURL)
		if g == "" {
			return "GITEA_URL is not configured on this cluster, so a gitea integration cannot operate yet — ask a cluster admin", false
		}
		if h != g {
			return "this deployment performs gitea git operations against '" + s.cfg.GiteaURL +
				"' — the integration host must match it (multi-host support is a planned follow-up)", false
		}
	case domain.ProviderGitHub:
		if h != "github.com" {
			return "github integrations currently support github.com only — git operations run against the public host (multi-host support is a planned follow-up)", false
		}
	case domain.ProviderGitLab:
		if h != "gitlab.com" {
			return "gitlab integrations currently support gitlab.com only — git operations run against the public host (multi-host support is a planned follow-up)", false
		}
	}
	return "", true
}

// integrationHostStillAllowed reports whether a service's bound integration (if
// any) still targets a cluster-allowed git host. This is the DISPATCH-time
// defence (D20, F5 review adjudication A): the allowlist may have been tightened
// AFTER the integration was created — existing integrations must be stopped
// immediately, not only at the next create/rotate. A service with no
// integration imposes no gate; a dangling reference (integration deleted
// mid-flight) is left for the credential resolver's fail-visible error.
func (s *Server) integrationHostStillAllowed(ctx context.Context, svc *domain.Service) (allowed bool, host string, err error) {
	if svc.IntegrationID == nil || *svc.IntegrationID == "" {
		return true, "", nil
	}
	in, err := s.st.GetIntegration(ctx, *svc.IntegrationID)
	if errors.Is(err, store.ErrNotFound) {
		return true, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return s.gitHostAllowed(in.Host), in.Host, nil
}

// integrationDispatchAllowed is the HTTP wrapper for the dispatch-time host gate
// (run create / retry / resume / review): a 403 host_not_allowed (a POLICY denial
// on existing state, mirroring the old allowlist dispatch gates) when the bound
// integration's host is no longer cluster-allowed; a store error is a 500.
// Returns false after writing the response; the caller must stop.
func (s *Server) integrationDispatchAllowed(w http.ResponseWriter, r *http.Request, svc *domain.Service) bool {
	allowed, host, err := s.integrationHostStillAllowed(r.Context(), svc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not check the integration host policy")
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "host_not_allowed",
			"this service's integration targets git host '"+host+"', which is no longer in the cluster's allowed hosts")
		return false
	}
	return true
}

// handleListIntegrations lists a project's integrations (member+). Never carries
// the token — only token_set/bot_username/host/provider.
func (s *Server) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleMember) {
		return
	}
	ins, err := s.st.ListIntegrationsByProject(r.Context(), projectID)
	if err != nil {
		s.log.Error("list integrations", "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list integrations")
		return
	}
	out := make([]integrationView, 0, len(ins))
	for i := range ins {
		out = append(out, integrationViewOf(&ins[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"integrations": out})
}

// createIntegrationReq is the POST body. token is write-only (plaintext in, never
// out). cred_type defaults to "pat" (the only kind wired this cycle).
type createIntegrationReq struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Host     string `json:"host"`
	CredType string `json:"cred_type"`
	Token    string `json:"token"`
}

// handleCreateIntegration creates a project integration (owner+). It validates the
// host against the cluster allowlist (D20), verifies token connectivity against the
// provider (discovering bot_username; fail-visible 400 on failure), then seals the
// token (AES-256-GCM). Every unconfigured/rejected state is a typed error — no
// silent success (CLAUDE.md red line #1).
func (s *Server) handleCreateIntegration(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}

	var req createIntegrationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "default"
	}
	prov := domain.GitProvider(strings.ToLower(strings.TrimSpace(req.Provider)))
	if !domain.ValidProvider(prov) {
		writeError(w, http.StatusBadRequest, "bad_request", "provider must be gitea, github or gitlab")
		return
	}
	credType := domain.CredType(strings.ToLower(strings.TrimSpace(req.CredType)))
	if credType == "" {
		credType = domain.CredTypePAT
	}
	if credType != domain.CredTypePAT {
		// github_app is an accepted schema slot but not wired this cycle — reject
		// fail-visibly rather than store a credential we cannot use.
		writeError(w, http.StatusBadRequest, "bad_request",
			"cred_type '"+string(credType)+"' is not supported yet — only 'pat' is available")
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "host is required")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "token is required")
		return
	}
	// Cluster git-host allowlist (D20): reject a disallowed host BEFORE any network
	// round-trip so the policy denial is the visible error.
	if !s.gitHostAllowed(host) {
		writeError(w, http.StatusBadRequest, "host_not_allowed",
			"the git host '"+host+"' is not in this cluster's allowed hosts — ask a cluster admin to add it")
		return
	}
	// Deployment wiring constraint (F5 review P2): the host must match where this
	// cluster actually performs git operations, or verification and push would
	// silently target different hosts. Also pre-network.
	if msg, ok := s.integrationHostMatchesWiring(prov, host); !ok {
		writeError(w, http.StatusBadRequest, "host_mismatch", msg)
		return
	}
	// Cipher precondition: a token we cannot seal is a typed 409 before any network
	// round-trip (fail-visible; never store a secret in the clear).
	if s.cipher == nil {
		writeError(w, http.StatusConflict, "cipher_not_configured",
			"set AUTH_TOKEN_KEY on the orchestrator before storing an integration token")
		return
	}
	// Connectivity verification (fail-visible): the token must actually authenticate
	// against the host, and its account becomes bot_username.
	botUsername, code, msg := s.verifyIntegration(r.Context(), prov, host, token)
	if code != "" {
		writeError(w, http.StatusBadRequest, code, msg)
		return
	}
	enc, err := s.cipher.EncryptString(token)
	if err != nil {
		s.log.Error("encrypt integration token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not encrypt the integration token")
		return
	}
	now := time.Now().UTC()
	in := &domain.Integration{
		ID:          domain.NewID(),
		ProjectID:   projectID,
		Name:        name,
		Provider:    prov,
		Host:        host,
		CredType:    credType,
		TokenEnc:    enc,
		BotUsername: botUsername,
		CreatedBy:   principalFrom(r.Context()).userID(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.st.CreateIntegration(r.Context(), in); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict",
				"an integration named '"+name+"' already exists in this project")
			return
		}
		s.log.Error("create integration", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create integration")
		return
	}
	writeJSON(w, http.StatusCreated, integrationViewOf(in))
}

// updateIntegrationReq is the PATCH body (D19 / F5). Both fields are pointers so
// "absent" is unchanged. token, when present, must be non-empty (rotation): an
// integration token is required and cannot be cleared. A rotation re-verifies
// connectivity and refreshes bot_username.
type updateIntegrationReq struct {
	Name  *string `json:"name"`
	Token *string `json:"token"`
}

// handleUpdateIntegration rotates the token and/or renames an integration (owner+).
// host/provider are immutable (delete + recreate to change a host).
func (s *Server) handleUpdateIntegration(w http.ResponseWriter, r *http.Request) {
	in, err := s.st.GetIntegration(r.Context(), r.PathValue("iid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load integration")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), in.ProjectID, domain.RoleOwner) {
		return
	}
	var req updateIntegrationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name cannot be empty")
			return
		}
		in.Name = name
	}
	if req.Token != nil {
		token := strings.TrimSpace(*req.Token)
		if token == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				"token cannot be empty — an integration always needs a credential (delete the integration to remove it)")
			return
		}
		// A rotation re-runs the host gates too (P2 / D20): a pre-existing row whose
		// host has since fallen out of policy — or that never matched the cluster
		// wiring — is caught here rather than silently re-armed with a fresh token.
		if !s.gitHostAllowed(in.Host) {
			writeError(w, http.StatusBadRequest, "host_not_allowed",
				"the git host '"+in.Host+"' is no longer in this cluster's allowed hosts")
			return
		}
		if msg, ok := s.integrationHostMatchesWiring(in.Provider, in.Host); !ok {
			writeError(w, http.StatusBadRequest, "host_mismatch", msg)
			return
		}
		if s.cipher == nil {
			writeError(w, http.StatusConflict, "cipher_not_configured",
				"set AUTH_TOKEN_KEY on the orchestrator before rotating an integration token")
			return
		}
		// Re-verify the rotated token and refresh bot_username (fail-visible).
		botUsername, code, msg := s.verifyIntegration(r.Context(), in.Provider, in.Host, token)
		if code != "" {
			writeError(w, http.StatusBadRequest, code, msg)
			return
		}
		enc, err := s.cipher.EncryptString(token)
		if err != nil {
			s.log.Error("encrypt integration token", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not encrypt the integration token")
			return
		}
		in.TokenEnc = enc
		in.BotUsername = botUsername
	}
	if err := s.st.UpdateIntegration(r.Context(), in); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict",
				"an integration named '"+in.Name+"' already exists in this project")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "integration not found")
			return
		}
		s.log.Error("update integration", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not update integration")
		return
	}
	writeJSON(w, http.StatusOK, integrationViewOf(in))
}

// handleDeleteIntegration removes an integration (owner+). Services bound to it
// fall back to the legacy credential path (their integration_id is nulled).
func (s *Server) handleDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	iid := r.PathValue("iid")
	in, err := s.st.GetIntegration(r.Context(), iid)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load integration")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), in.ProjectID, domain.RoleOwner) {
		return
	}
	unbound, _ := s.st.CountServicesUsingIntegration(r.Context(), iid)
	if err := s.st.DeleteIntegration(r.Context(), iid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "integration not found")
			return
		}
		s.log.Error("delete integration", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete integration")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": iid, "services_unbound": unbound})
}

// handleListIntegrationRepos lists repositories the integration's bot token can see
// (member+ — a member may add a repo off a project's existing integration, D19). It
// decrypts the token and lists with it; a missing cipher / decrypt failure is
// fail-visible.
func (s *Server) handleListIntegrationRepos(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	iid := r.PathValue("iid")
	in, err := s.st.GetIntegration(r.Context(), iid)
	if errors.Is(err, store.ErrNotFound) || (err == nil && in.ProjectID != projectID) {
		writeError(w, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load integration")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleMember) {
		return
	}
	client, code, msg := s.integrationClient(in)
	if code != "" {
		writeError(w, http.StatusConflict, code, msg)
		return
	}
	lister, ok := client.(provider.RepoLister)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "provider client cannot list repositories")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	repos, err := lister.ListRepos(r.Context(), r.URL.Query().Get("q"), page, 50)
	if err != nil {
		s.log.Warn("integration repo listing failed", "integration", in.ID, "provider", in.Provider, "err", err)
		writeError(w, http.StatusBadGateway, "provider_error",
			"listing repositories from "+string(in.Provider)+" failed: "+summarizeProviderErr(err))
		return
	}
	if repos == nil {
		repos = []provider.Repo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// verifyIntegration builds a client for (prov, host, token) and calls CurrentUser
// to confirm the token authenticates against the host, returning the discovered
// bot username. On failure it returns a typed (code, msg) so the caller writes a
// fail-visible 400 (never stores an unusable credential). code=="" on success.
func (s *Server) verifyIntegration(ctx context.Context, prov domain.GitProvider, host, token string) (botUsername, code, msg string) {
	client, err := provider.IntegrationClient(prov, host, token)
	if err != nil {
		return "", "bad_request", "could not build a client for host '" + host + "'"
	}
	who, ok := client.(provider.CurrentUser)
	if !ok {
		return "", "bad_request", "connectivity check is not supported for this provider"
	}
	username, err := who.CurrentUser(ctx)
	if err != nil {
		return "", "integration_unreachable",
			"the token could not authenticate against " + string(prov) + " at '" + host + "': " + summarizeProviderErr(err)
	}
	return username, "", ""
}

// integrationClient decrypts an integration's token and builds a REST client for
// its host. A missing cipher or a decrypt failure is a fail-visible typed
// (code, msg); code=="" on success.
func (s *Server) integrationClient(in *domain.Integration) (provider.Provider, string, string) {
	if s.cipher == nil {
		return nil, "cipher_not_configured",
			"AUTH_TOKEN_KEY is not configured, so this integration's token cannot be decrypted"
	}
	token, err := s.cipher.DecryptString(in.TokenEnc)
	if err != nil {
		s.log.Error("decrypt integration token", "integration", in.ID, "err", err)
		return nil, "cipher_error", "the integration token could not be decrypted"
	}
	client, err := provider.IntegrationClient(in.Provider, in.Host, token)
	if err != nil {
		return nil, "bad_request", "could not build a client for host '" + in.Host + "'"
	}
	return client, "", ""
}

// summarizeProviderErr truncates a provider error for a fail-visible client message
// (never leaks a token — the provider clients redact by never echoing auth).
func summarizeProviderErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const max = 300
	if len(msg) > max {
		msg = msg[:max] + "…"
	}
	return msg
}
