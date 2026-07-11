package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
)

// D31 — embed the real jtype board in the console project page. The console
// injects a board-react `client` whose every listDocuments/getDocument/
// saveDocument call hits the proxy below; jcloud resolves the EFFECTIVE jtype
// token server-side (identical resolution to the poller — per-link token > cluster
// fallback) and forwards to jtype with a Bearer header, so the token NEVER reaches
// the browser (red line #2). Responses are copied through VERBATIM (not
// re-serialized through the typed Doc/Document structs, which drop
// isPublished/versionId/mergeStatus — a silent field-dropping degradation, red
// line #1). See ProxyDocumentAPI in internal/jtype/client.go.
//
// Authz: reads AND writes are member+. Write = member+ matches run-dispatch
// authority exactly (a board move via saveDocument is what the poller turns into a
// run, and POST /runs is member+); read is the same threshold because the board is
// a single read+write component (a viewer cannot meaningfully use it without
// attempting a write). The Kanban button therefore never renders for viewers — the
// member+ board/links endpoint 403s them into an empty list. Owner-only link
// management + discovery (kanban.go / kanban_discovery.go) are untouched.

// boardEmbedLinkView is the REDUCED board-link shape that gates the console's
// Kanban button + feeds the modal's link picker. It carries NO credential fields
// (no token_set / credential_status / token_expires_at) so a member never learns
// the link's credential posture — unlike the owner-only kanbanLinkView.
type boardEmbedLinkView struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	BoardRef      string `json:"board_ref"`
	BoardTitle    string `json:"board_title,omitempty"`
	BoardStatus   string `json:"board_status"`
	ServiceID     string `json:"service_id"`
	TriggerColumn string `json:"trigger_column"`
	DoneColumn    string `json:"done_column,omitempty"`
	Enabled       bool   `json:"enabled"`
}

// jtypeBoardProxy is the slice of *jtype.Client the member+ board embed proxy uses
// to forward document reads/writes to jtype verbatim (D31). A test injects a fake
// so the proxy endpoints are exercised without HTTP.
type jtypeBoardProxy interface {
	ProxyDocumentAPI(ctx context.Context, method, path string, body io.Reader) (*http.Response, error)
}

// maxProxyBody caps both the request body streamed to jtype (a save) and the
// upstream body copied back, so neither side can stream unbounded through the
// proxy. Board documents are small markdown/JSON; 8 MiB is generous headroom.
const maxProxyBody = 8 << 20

// handleListBoardEmbedLinks returns the project's kanban links in the reduced,
// credential-free boardEmbedLinkView (member+). The console gates the Kanban
// button on a non-empty list and drives the modal's link picker from it. Viewers
// (and non-members) get a 403 → the button never renders.
func (s *Server) handleListBoardEmbedLinks(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleMember) {
		return
	}
	links, err := s.st.ListKanbanLinksByProject(r.Context(), projectID)
	if err != nil {
		s.log.Error("list board embed links", "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list kanban links")
		return
	}
	out := make([]boardEmbedLinkView, 0, len(links))
	for _, l := range links {
		if !l.Enabled {
			continue // a disabled link grants no board access (matches resolveBoardProxy)
		}
		out = append(out, boardEmbedLinkView{
			ID: l.ID, WorkspaceID: l.WorkspaceID, BoardRef: l.BoardRef,
			BoardTitle:  l.BoardTitle,
			BoardStatus: boardStatusOrDefault(l.BoardStatus),
			ServiceID:   l.ServiceID, TriggerColumn: l.TriggerColumn,
			DoneColumn: l.DoneColumn, Enabled: l.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// resolveBoardProxy is the shared gate for every documents/* handler. It
// authorizes member+, enforces the confused-deputy workspace-scoping guard, and
// resolves the effective jtype client + token server-side — or writes a typed
// fail-visible error and returns ok=false (the caller must stop). The returned
// client already carries the resolved token; the workspace string is validated.
func (s *Server) resolveBoardProxy(w http.ResponseWriter, r *http.Request) (client jtypeBoardProxy, workspace string, ok bool) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleMember) {
		return nil, "", false
	}

	workspace = strings.TrimSpace(r.URL.Query().Get("workspace"))
	if workspace == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "workspace query parameter is required")
		return nil, "", false
	}

	// ---- CONFUSED-DEPUTY GUARD (the security crux) ----
	// The requested workspace MUST be the workspace_id of one of THIS project's own
	// links. Otherwise the cluster/per-link token — which can read/write every
	// document in ANY workspace it authorizes — would let a project member reach an
	// arbitrary jtype workspace the project was never granted. Rejected here, BEFORE
	// any jtype round-trip.
	links, err := s.st.ListKanbanLinksByProject(r.Context(), projectID)
	if err != nil {
		s.log.Error("board proxy: list links", "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load project links")
		return nil, "", false
	}
	// Only an ENABLED link grants access — disabling a link fully cuts the board
	// embed for that workspace, matching the poller (which scans only enabled links).
	var match *domain.KanbanLink
	for i := range links {
		if links[i].WorkspaceID == workspace && links[i].Enabled {
			match = &links[i]
			break
		}
	}
	if match == nil {
		// 403, not 404: do not confirm whether a foreign workspace exists.
		writeError(w, http.StatusForbidden, "workspace_not_linked",
			"this workspace is not linked to this project")
		return nil, "", false
	}

	// Effective jtype client + token — IDENTICAL resolution to the poller
	// (kanban/poller.go): the tick's Factory + cluster fallback, then ResolveToken
	// with the matched link's per-link token. Never a jtype call with an empty
	// credential; every failure is a typed, visible error.
	f, clusterToken, ok := s.kanban.Factory(r.Context())
	if !ok {
		writeError(w, http.StatusConflict, "kanban_not_configured",
			"the jtype integration is not configured — ask a cluster admin")
		return nil, "", false
	}
	token, _, terr := jtype.ResolveToken(match.TokenEnc, s.JtypeDecrypt(), clusterToken)
	if terr != nil {
		// ErrNoToken / ErrNoCipher / decrypt failure — visible, never a silent skip.
		s.log.Warn("board proxy: no usable jtype credential", "link", match.ID, "err", terr)
		writeError(w, http.StatusServiceUnavailable, "jtype_unreachable",
			"no usable jtype credential for this board's link")
		return nil, "", false
	}
	return s.boardProxyFor(f, token), workspace, true
}

// forwardBoardAPI issues the built request through the token-bound proxy and
// copies the upstream status + body straight back. A >=400 from jtype is a
// verbatim passthrough (board-react renders its own error panel from jtype's
// envelope); only a transport failure to reach jtype is mapped to a typed 503.
func (s *Server) forwardBoardAPI(w http.ResponseWriter, r *http.Request, client jtypeBoardProxy, method, path string, body io.Reader) {
	resp, err := client.ProxyDocumentAPI(r.Context(), method, path, body)
	if err != nil {
		// Genuine network / instance-down. The raw error carries jtype's internal
		// URL/host — log it, but return a GENERIC message so a project member can't
		// probe the cluster's jtype address through the proxy.
		s.log.Warn("board proxy: jtype request", "method", method, "path", path, "err", err)
		writeError(w, http.StatusServiceUnavailable, "jtype_unreachable",
			"could not reach jtype")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxProxyBody))
}

// handleBoardListDocuments proxies board-react's listDocuments(ws) (member+).
func (s *Server) handleBoardListDocuments(w http.ResponseWriter, r *http.Request) {
	client, ws, ok := s.resolveBoardProxy(w, r)
	if !ok {
		return
	}
	// The upstream path is built server-side from the validated workspace only; no
	// client-controlled segment is forwarded.
	path := "/api/v1/workspaces/" + url.PathEscape(ws) + "/documents"
	s.forwardBoardAPI(w, r, client, http.MethodGet, path, nil)
}

// handleBoardGetDocument proxies board-react's getDocument(ws, docId) (member+).
func (s *Server) handleBoardGetDocument(w http.ResponseWriter, r *http.Request) {
	client, ws, ok := s.resolveBoardProxy(w, r)
	if !ok {
		return
	}
	docID := strings.TrimSpace(r.PathValue("docID"))
	if docID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "document id is required")
		return
	}
	path := "/api/v1/workspaces/" + url.PathEscape(ws) + "/documents/" + url.PathEscape(docID)
	s.forwardBoardAPI(w, r, client, http.MethodGet, path, nil)
}

// handleBoardSaveDocument proxies board-react's saveDocument(ws, req) — a card
// create/edit/move (member+). The (bounded) body is buffered so the target path
// can be validated: the member+ embed may only write CARD documents (`.md`),
// never the `.board` config JSON or a traversal/other-type path — so a member
// can't overwrite an arbitrary note in the linked workspace via a crafted save
// (the board itself only ever saves card `.md` files). jtype's SaveDocument
// response (mergeStatus/contentHash) is copied back verbatim.
func (s *Server) handleBoardSaveDocument(w http.ResponseWriter, r *http.Request) {
	client, ws, ok := s.resolveBoardProxy(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}
	var req struct {
		RelativePath string `json:"relativePath"`
	}
	rp := ""
	if json.Unmarshal(raw, &req) == nil {
		rp = strings.TrimSpace(req.RelativePath)
	}
	if rp == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "relativePath is required")
		return
	}
	if !strings.HasSuffix(strings.ToLower(rp), ".md") || strings.Contains(rp, "..") {
		writeError(w, http.StatusForbidden, "forbidden",
			"the board embed may only write card (.md) documents")
		return
	}
	path := "/api/v1/workspaces/" + url.PathEscape(ws) + "/documents/save"
	s.forwardBoardAPI(w, r, client, http.MethodPost, path, bytes.NewReader(raw))
}
