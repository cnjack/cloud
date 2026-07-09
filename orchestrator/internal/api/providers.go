package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
)

// handleListProviderRepos is GET /api/v1/providers/{provider}/repos?q=&page= —
// the Drone-style onboarding picker. It lists repositories visible to the
// CALLER's credential on that provider: a logged-in user's linked OAuth token
// (so the list mirrors what they can actually access), falling back to the
// global gitea PAT for the service principal / cluster admins with no linked
// identity. The listing is read-only; adding a repo still goes through the
// normal POST /projects/{id}/services authorization.
func (s *Server) handleListProviderRepos(w http.ResponseWriter, r *http.Request) {
	// Project-scoped API key (F12 / D24): forbidden. Repo enumeration is an
	// onboarding / service-creation surface, not a member run action. Critically,
	// a scoped principal has no linked identity (userID=="" below), so it would
	// fall through to the cluster GITEA_TOKEN bot fallback and enumerate every
	// org repository that bot can see — a cross-tenant credential leak. Deny it
	// up front, same isAPIKey()→403 guard as the /system + /users surfaces.
	if principalFrom(r.Context()).isAPIKey() {
		writeError(w, http.StatusForbidden, "forbidden",
			"project-scoped API keys cannot enumerate provider repositories")
		return
	}
	prov := domain.GitProvider(r.PathValue("provider"))
	if !domain.ValidProvider(prov) {
		writeError(w, http.StatusBadRequest, "bad_request", "provider must be gitea, github or gitlab")
		return
	}
	if s.creds == nil || s.factory == nil {
		writeError(w, http.StatusNotFound, "not_found", "no git provider is configured on this server")
		return
	}

	prin := principalFrom(r.Context())
	var userID *string
	if uid := prin.userID(); uid != "" {
		userID = &uid
	}
	tok, err := s.creds.Resolve(r.Context(), prov, userID)
	if errors.Is(err, credentials.ErrNoCredential) {
		// No linked identity and no PAT fallback for this provider: the console
		// shows a "link your account" prompt off this message.
		writeError(w, http.StatusForbidden, "forbidden",
			"no "+string(prov)+" credential available — link your "+string(prov)+" account first")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve provider credential")
		return
	}

	client, err := s.factory.PRClient(prov, tok.Value, tok.Scheme)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", string(prov)+" is not configured on this server")
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
		s.log.Warn("provider repo listing failed", "provider", prov, "source", tok.Source, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "listing repositories from "+string(prov)+" failed")
		return
	}
	if repos == nil {
		repos = []provider.Repo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}
