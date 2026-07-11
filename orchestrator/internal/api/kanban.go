package api

import (
	"context"
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
	// TokenExpiresAt is the per-link token's expiry (RFC3339) when it was minted by
	// the "Connect with jtype" device flow (D28); omitted for a hand-pasted token /
	// no token (unknown expiry). Never the token itself.
	TokenExpiresAt string `json:"token_expires_at,omitempty"`
	// CredentialStatus is the DERIVED runtime credential state (P1 — an owner must
	// see a dead link in the console, not in orchestrator logs):
	//   "per_link"         — the link has its own token (token_set).
	//   "cluster_fallback" — no per-link token; the cluster JTYPE_TOKEN covers it.
	//   "missing"          — neither: the poller/writeback skip this link
	//                        fail-visibly until a token is set.
	CredentialStatus string `json:"credential_status"`
	// BoardStatus is the fail-visible board validation state (D30), independent of
	// CredentialStatus (a link can be per_link yet invalid — token fine, board
	// renamed): "ok" | "unvalidated" | "invalid". BoardTitle is the board's friendly
	// name (when resolved) so the console shows it instead of the opaque "b_…"
	// board_ref; omitted until a validation resolves the board.
	BoardStatus string `json:"board_status"`
	BoardTitle  string `json:"board_title,omitempty"`
	CreatedAt   string `json:"created_at"`
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
// because credential_status depends on the EFFECTIVE cluster fallback config
// (D27: the console-managed DB row or the JTYPE_* env, resolved per request —
// never the raw env alone). A resolver error (e.g. a DB token with no cipher)
// resolves clusterTokenSet=false, so a link relying on that now-unusable fallback
// honestly lists as "missing".
func (s *Server) linkView(ctx context.Context, l domain.KanbanLink) kanbanLinkView {
	clusterTokenSet := false
	if eff, err := s.kanban.Effective(ctx); err == nil {
		clusterTokenSet = eff.ClusterTokenSet
	}
	v := kanbanLinkView{
		ID: l.ID, WorkspaceID: l.WorkspaceID, BoardRef: l.BoardRef,
		ProjectID: l.ProjectID, ServiceID: l.ServiceID,
		TriggerColumn: l.TriggerColumn, DoneColumn: l.DoneColumn,
		Enabled: l.Enabled, TokenSet: l.TokenSet(),
		CredentialStatus: credentialStatus(l, clusterTokenSet),
		BoardStatus:      boardStatusOrDefault(l.BoardStatus),
		BoardTitle:       l.BoardTitle,
		CreatedAt:        l.CreatedAt.UTC().Format(time.RFC3339),
	}
	if l.TokenExpiresAt != nil {
		v.TokenExpiresAt = l.TokenExpiresAt.UTC().Format(time.RFC3339)
	}
	return v
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
		out = append(out, s.linkView(r.Context(), l))
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
		out = append(out, s.linkView(r.Context(), l))
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

	// Board validation + canonicalization (D30). The predicate (using the EFFECTIVE
	// jtype config — D27: DB row or JTYPE_* env, resolved per request so a base URL
	// just set in the console validates without a restart):
	//
	//   ok && validationToken != "" -> HARD validate: resolve the board by NAME at
	//     any path, canonicalize board_ref to its config id (a "b_…" — the value the
	//     poller matches on), and validate the trigger/done columns. A bad ref/column
	//     is a typed 400 (board_not_found / board_ambiguous / jtype_unauthorized /
	//     bad_request), a genuine 5xx/transport error stays 503. board_status = "ok".
	//
	//   otherwise (integration off, or on but NO per-link and NO cluster token) ->
	//     SOFT create: store the raw ref, board_status = "unvalidated". This is the
	//     bootstrap path (RC4 deadlock): the link must exist before the "Connect with
	//     jtype" device flow (D28) can attach a per-link token, after which the poller
	//     re-validates + canonicalizes at runtime. Fail-visible: never a silent
	//     success — the console shows "columns not validated" until a live check runs.
	boardRef := req.BoardRef
	boardTitle := ""
	boardStatus := domain.KanbanBoardUnvalidated
	if f, clusterToken, ok := s.kanban.Factory(r.Context()); ok {
		validationToken := token
		if validationToken == "" {
			validationToken = clusterToken
		}
		if validationToken != "" {
			board, err := s.boardValidatorFor(f, validationToken).GetBoard(r.Context(), req.WorkspaceID, req.BoardRef)
			if err != nil {
				s.writeBoardValidationError(w, req.WorkspaceID, req.BoardRef, err)
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
			// Canonicalize: persist the board's config id (what cards' frontmatter
			// carries), not the user-typed name, so the poller matches (RC2). A board
			// with no id (defensive) keeps the submitted ref.
			if board.ID != "" {
				boardRef = board.ID
			}
			boardTitle = board.Title
			boardStatus = domain.KanbanBoardOK
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
		ID: domain.NewID(), WorkspaceID: req.WorkspaceID, BoardRef: boardRef,
		BoardTitle: boardTitle, BoardStatus: boardStatus,
		ProjectID: projectID, ServiceID: req.ServiceID,
		TriggerColumn: req.TriggerColumn, DoneColumn: req.DoneColumn,
		Enabled: true, TokenEnc: tokenEnc, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.st.CreateKanbanLink(r.Context(), link); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "already_exists",
				"a link for workspace/board "+req.WorkspaceID+"/"+boardRef+" already exists")
			return
		}
		s.log.Error("create kanban link", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create kanban link")
		return
	}
	writeJSON(w, http.StatusCreated, s.linkView(r.Context(), *link))
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

	// Manual paste/clear: token_expires_at is set to NULL (unknown expiry — only
	// the device connect flow populates it, D28).
	if err := s.st.SetKanbanLinkToken(r.Context(), linkID, tokenEnc, nil); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "kanban link not found")
			return
		}
		s.log.Error("update kanban link token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not update the jtype token")
		return
	}
	link.TokenEnc = tokenEnc
	link.TokenExpiresAt = nil // manual rotate/clear leaves the expiry unknown (D28)
	writeJSON(w, http.StatusOK, s.linkView(r.Context(), *link))
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

// boardStatusOrDefault returns a link's board_status, defaulting a zero value to
// "ok" (pre-0024 rows loaded through a store that predates the column, or any
// path that left it empty; the pg DEFAULT + MemStore already backfill 'ok').
func boardStatusOrDefault(s string) string {
	if s == "" {
		return domain.KanbanBoardOK
	}
	return s
}

// writeBoardValidationError maps a GetBoard failure to an ACTIONABLE typed status
// (D30 / RC3): a board-not-found or column-ref problem is a 400 the owner can fix,
// NOT a 503 that sends them to debug the network. Only a genuine 5xx/transport
// failure keeps 503 jtype_unreachable. Fail-visible: every message names the
// offending ref/workspace (and, for ambiguous, the candidate paths).
func (s *Server) writeBoardValidationError(w http.ResponseWriter, workspace, boardRef string, err error) {
	s.log.Warn("create kanban link: board validation", "workspace", workspace, "board", boardRef, "err", err)
	var ambig *jtype.ErrBoardAmbiguousError
	switch {
	case errors.Is(err, jtype.ErrDocNotFound):
		writeError(w, http.StatusBadRequest, "board_not_found",
			"no board named '"+boardRef+"' in this workspace — pick one from the board list or pass its full path")
		return
	case errors.As(err, &ambig):
		writeError(w, http.StatusBadRequest, "board_ambiguous",
			"board '"+boardRef+"' is ambiguous ("+strings.Join(ambig.Candidates, ", ")+") — pass the full path")
		return
	}
	var je *jtype.Error
	if errors.As(err, &je) {
		switch je.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			writeError(w, http.StatusBadRequest, "jtype_unauthorized",
				"the jtype token is invalid or lacks access to workspace "+workspace)
			return
		case http.StatusNotFound:
			writeError(w, http.StatusBadRequest, "workspace_not_found",
				"jtype workspace '"+workspace+"' was not found")
			return
		}
	}
	// Genuine network / instance-down: keep 503 so the owner knows it is transient.
	writeError(w, http.StatusServiceUnavailable, "jtype_unreachable",
		"could not fetch board "+boardRef+" from jtype to validate columns: "+err.Error())
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
