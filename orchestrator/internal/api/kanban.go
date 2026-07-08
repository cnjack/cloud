package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/store"
)

// kanbanLinkView is the API shape of a kanban_link (never carries the jtype PAT
// — only token_set, whether a per-link token is stored; F6 / D25).
type kanbanLinkView struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	BoardRef      string `json:"board_ref"`
	ProjectID     string `json:"project_id"`
	ServiceID     string `json:"service_id"`
	TriggerColumn string `json:"trigger_column"`
	DoneColumn    string `json:"done_column,omitempty"`
	Enabled       bool   `json:"enabled"`
	// TokenSet is true when the link carries its own encrypted jtype PAT; false
	// means it falls back to the cluster JTYPE_TOKEN env. The token is never echoed.
	TokenSet bool `json:"token_set"`
	// CredentialStatus is the DERIVED runtime credential state (P1 — an owner must
	// see a dead link in the console, not in orchestrator logs):
	//   "per_link"         — the link has its own token (token_set).
	//   "cluster_fallback" — no per-link token; the cluster JTYPE_TOKEN covers it.
	//   "missing"          — neither: the poller/writeback skip this link
	//                        fail-visibly until a token is set.
	CredentialStatus string `json:"credential_status"`
	CreatedAt        string `json:"created_at"`
}

// credentialStatus derives the three-state credential label for a link given
// whether the cluster JTYPE_TOKEN fallback is configured. Mirrors
// jtype.ResolveToken's selection order exactly.
func credentialStatus(l domain.KanbanLink, clusterTokenSet bool) string {
	switch {
	case l.TokenSet():
		return "per_link"
	case clusterTokenSet:
		return "cluster_fallback"
	default:
		return "missing"
	}
}

// linkView renders a link for the API. A Server method (not a free function)
// because credential_status depends on the cluster fallback config.
func (s *Server) linkView(l domain.KanbanLink) kanbanLinkView {
	return kanbanLinkView{
		ID: l.ID, WorkspaceID: l.WorkspaceID, BoardRef: l.BoardRef,
		ProjectID: l.ProjectID, ServiceID: l.ServiceID,
		TriggerColumn: l.TriggerColumn, DoneColumn: l.DoneColumn,
		Enabled: l.Enabled, TokenSet: l.TokenSet(),
		CredentialStatus: credentialStatus(l, s.cfg.JtypeToken != ""),
		CreatedAt:        l.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// createKanbanLinkReq is the POST /api/v1/projects/{id}/kanban/links body.
// project_id comes from the path (not the body). token is the optional per-link
// jtype PAT — write-only (plaintext in, never out); omit it to fall back to the
// cluster JTYPE_TOKEN env.
type createKanbanLinkReq struct {
	WorkspaceID   string `json:"workspace_id"`
	BoardRef      string `json:"board_ref"`
	ServiceID     string `json:"service_id"`
	TriggerColumn string `json:"trigger_column"`
	DoneColumn    string `json:"done_column"`
	Token         string `json:"token"`
}

// handleListKanbanLinks returns every kanban link across all projects. It is a
// cluster-admin READ-ONLY overview (F6 / D25 downshifted management to owners via
// the project-scoped routes; this stays for the Cluster page's global view). Each
// view carries project_id so the admin sees which project owns a link.
func (s *Server) handleListKanbanLinks(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	links, err := s.st.ListKanbanLinks(r.Context())
	if err != nil {
		s.log.Error("list kanban links", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list kanban links")
		return
	}
	out := make([]kanbanLinkView, 0, len(links))
	for _, l := range links {
		out = append(out, s.linkView(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// handleListProjectKanbanLinks lists one project's kanban links (owner only,
// F6 / D25). Returns token_set per link, never the token.
func (s *Server) handleListProjectKanbanLinks(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	links, err := s.st.ListKanbanLinksByProject(r.Context(), projectID)
	if err != nil {
		s.log.Error("list project kanban links", "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list kanban links")
		return
	}
	out := make([]kanbanLinkView, 0, len(links))
	for _, l := range links {
		out = append(out, s.linkView(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// handleCreateProjectKanbanLink creates a kanban link on a project (owner only,
// F6 / D25). It validates that the service belongs to this project, seals the
// optional per-link jtype PAT (AES-256-GCM; a token without AUTH_TOKEN_KEY is a
// typed 409, never stored in the clear), and — when the jtype integration is on —
// fetches the board with the link's token (or the cluster fallback) to confirm
// the trigger/done columns exist (fail-visible: a typo'd column would otherwise
// silently never trigger).
func (s *Server) handleCreateProjectKanbanLink(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	if _, err := s.st.GetProject(r.Context(), projectID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	var req createKanbanLinkReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.BoardRef = strings.TrimSpace(req.BoardRef)
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.TriggerColumn = strings.TrimSpace(req.TriggerColumn)
	req.DoneColumn = strings.TrimSpace(req.DoneColumn)
	token := strings.TrimSpace(req.Token)

	if req.WorkspaceID == "" || req.BoardRef == "" || req.ServiceID == "" || req.TriggerColumn == "" {
		writeError(w, http.StatusBadRequest, "bad_request",
			"workspace_id, board_ref, service_id and trigger_column are required")
		return
	}

	// The service must exist and belong to THIS project.
	svc, err := s.st.GetService(r.Context(), req.ServiceID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "bad_request", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if svc.ProjectID != projectID {
		writeError(w, http.StatusBadRequest, "bad_request", "service does not belong to this project")
		return
	}

	// Cipher precondition FIRST (P3): a per-link token we cannot encrypt is a
	// typed 409 before any jtype network round-trip — the config error should not
	// hide behind (or be masked by) a slow/failed board fetch.
	if token != "" && s.cipher == nil {
		writeError(w, http.StatusConflict, "cipher_not_configured",
			"set AUTH_TOKEN_KEY on the orchestrator before storing a per-link jtype token")
		return
	}

	// Column validation against the live board (only when jtype is configured).
	// The token used is the link's own (when supplied) else the cluster fallback;
	// with neither, there is nothing to authenticate with and the link would be
	// useless — reject fail-visibly rather than storing a dead link.
	if s.jtypeBoardFor != nil {
		validationToken := token
		if validationToken == "" {
			validationToken = s.cfg.JtypeToken
		}
		if validationToken == "" {
			writeError(w, http.StatusBadRequest, "token_required",
				"a jtype token is required for this link: no cluster JTYPE_TOKEN fallback is configured")
			return
		}
		board, err := s.jtypeBoardFor(validationToken).GetBoard(r.Context(), req.WorkspaceID, req.BoardRef)
		if err != nil {
			s.log.Warn("create kanban link: board validation", "workspace", req.WorkspaceID, "board", req.BoardRef, "err", err)
			writeError(w, http.StatusServiceUnavailable, "jtype_unreachable",
				"could not fetch board "+req.BoardRef+" from jtype to validate columns: "+err.Error())
			return
		}
		if !boardHasColumn(board, req.TriggerColumn) {
			writeError(w, http.StatusBadRequest, "bad_request",
				"trigger_column '"+req.TriggerColumn+"' is not a column on board "+req.BoardRef)
			return
		}
		if req.DoneColumn != "" && !boardHasColumn(board, req.DoneColumn) {
			writeError(w, http.StatusBadRequest, "bad_request",
				"done_column '"+req.DoneColumn+"' is not a column on board "+req.BoardRef)
			return
		}
	}

	// Seal the per-link token. Empty => nil (cluster fallback). The cipher was
	// verified above (P3), so encryption here only fails on entropy errors.
	var tokenEnc []byte
	if token != "" {
		enc, err := s.cipher.EncryptString(token)
		if err != nil {
			s.log.Error("encrypt kanban link token", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not encrypt the jtype token")
			return
		}
		tokenEnc = enc
	}

	now := time.Now().UTC()
	link := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: req.WorkspaceID, BoardRef: req.BoardRef,
		ProjectID: projectID, ServiceID: req.ServiceID,
		TriggerColumn: req.TriggerColumn, DoneColumn: req.DoneColumn,
		Enabled: true, TokenEnc: tokenEnc, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.st.CreateKanbanLink(r.Context(), link); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "already_exists",
				"a link for workspace/board "+req.WorkspaceID+"/"+req.BoardRef+" already exists")
			return
		}
		s.log.Error("create kanban link", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create kanban link")
		return
	}
	writeJSON(w, http.StatusCreated, s.linkView(*link))
}

// updateKanbanLinkReq is the PATCH /api/v1/projects/{id}/kanban/links/{linkID}
// body (P2 — token rotation). Token is a pointer so "field absent" is a typed
// 400 rather than an accidental clear: "" (explicit empty) CLEARS the per-link
// token (back to the cluster fallback), any other value ROTATES it. Write-only,
// as on create.
type updateKanbanLinkReq struct {
	Token *string `json:"token"`
}

// handleUpdateProjectKanbanLink rotates or clears a link's per-link jtype PAT
// (owner only, P2). ONLY token_enc changes — the link's binding and its claims
// are retained, so a rotation never re-dispatches already-claimed cards. The
// link must belong to the path project (404 otherwise, as on delete).
func (s *Server) handleUpdateProjectKanbanLink(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	linkID := r.PathValue("linkID")
	link, err := s.st.GetKanbanLink(r.Context(), linkID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && link.ProjectID != projectID) {
		writeError(w, http.StatusNotFound, "not_found", "kanban link not found")
		return
	}
	if err != nil {
		s.log.Error("load kanban link", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load kanban link")
		return
	}

	var req updateKanbanLinkReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Token == nil {
		writeError(w, http.StatusBadRequest, "bad_request",
			`the "token" field is required: "" clears the per-link token (cluster fallback), any other value rotates it`)
		return
	}
	token := strings.TrimSpace(*req.Token)

	// Same cipher precondition as create (and same ordering discipline, P3):
	// checked before any store write.
	var tokenEnc []byte
	if token != "" {
		if s.cipher == nil {
			writeError(w, http.StatusConflict, "cipher_not_configured",
				"set AUTH_TOKEN_KEY on the orchestrator before storing a per-link jtype token")
			return
		}
		enc, err := s.cipher.EncryptString(token)
		if err != nil {
			s.log.Error("encrypt kanban link token", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not encrypt the jtype token")
			return
		}
		tokenEnc = enc
	}

	if err := s.st.SetKanbanLinkToken(r.Context(), linkID, tokenEnc); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "kanban link not found")
			return
		}
		s.log.Error("update kanban link token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not update the jtype token")
		return
	}
	link.TokenEnc = tokenEnc
	writeJSON(w, http.StatusOK, s.linkView(*link))
}

// handleDeleteProjectKanbanLink deletes a project's kanban link (owner only,
// F6 / D25). The link must belong to the path project (a link from another
// project is a 404, not a cross-project delete). Its claims cascade.
func (s *Server) handleDeleteProjectKanbanLink(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return
	}
	linkID := r.PathValue("linkID")
	link, err := s.st.GetKanbanLink(r.Context(), linkID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && link.ProjectID != projectID) {
		writeError(w, http.StatusNotFound, "not_found", "kanban link not found")
		return
	}
	if err != nil {
		s.log.Error("load kanban link", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load kanban link")
		return
	}
	if err := s.st.DeleteKanbanLink(r.Context(), linkID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "kanban link not found")
			return
		}
		s.log.Error("delete kanban link", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete kanban link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": linkID})
}

// boardHasColumn reports whether the board has a column with the given key.
func boardHasColumn(b *jtype.Board, key string) bool {
	if b == nil {
		return false
	}
	for _, c := range b.Columns {
		if c.Key == key {
			return true
		}
	}
	return false
}
