package api

import (
	"errors"
	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
	"net/http"
	"strings"
)

// Resolve the caller's provider identity before reusing the bounded repository
// upload path. The browser cannot choose a project, owner, or object-store key.
func (s *Server) handleCreateAccountAttachmentIntent(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.accountPrincipal(w, r)
	if !ok {
		return
	}
	if s.attachmentStore == nil {
		writeError(w, 409, "attachments_unavailable", "object storage is not configured for attachments")
		return
	}
	providerName := domain.GitProvider(strings.TrimSpace(r.PathValue("provider")))
	repoID := strings.TrimSpace(r.PathValue("repo"))
	if !domain.ValidProvider(providerName) || repoID == "" {
		writeError(w, 400, "bad_request", "provider and repository are required")
		return
	}
	repo, identity, cfg, err := s.resolveAccountRepository(r.Context(), uid, providerName, repoID)
	switch {
	case errors.Is(err, credentials.ErrNoCredential):
		writeError(w, 409, "provider_account_required", "link this provider account before uploading attachments")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "repository_not_found", "the selected repository is not available to this Account")
		return
	case err != nil:
		writeError(w, 502, "provider_error", "could not verify the selected repository")
		return
	case !cfg.PluginEnabled:
		writeError(w, 409, "provider_execution_unavailable", "Cloud execution is disabled for this provider")
		return
	}
	repository, err := s.ensureAccountRepository(r.Context(), uid, repo, identity, cfg)
	if errors.Is(err, credentials.ErrPluginCredentialUnavailable) {
		writeError(w, 409, "provider_execution_unavailable", "the linked provider account cannot be used for Cloud execution")
		return
	}
	if err != nil {
		s.log.Error("prepare account attachment", "err", err)
		writeError(w, 500, "internal", "could not prepare the Repository upload")
		return
	}
	r.SetPathValue("id", repository.ID)
	s.handleCreateAttachmentIntent(w, r)
}
