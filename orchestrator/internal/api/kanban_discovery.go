package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
)

// D30 — jtype discovery endpoints backing the console's cascading pickers
// (workspace -> board -> trigger/done column), replacing the un-findable
// hand-typed workspace UUID + board ref + column keys (RC5).
//
// Both are OWNER-ONLY (mirror handleListProjectKanbanLinks) and resolve the
// EFFECTIVE cluster jtype factory + token the create path uses (D27). The token
// authorises the jtype reads but is NEVER serialized into a response (P0 privacy):
// the shapes below carry only ids/names/columns.

// jtypeWorkspaceView is one workspace option for the picker. No token field.
type jtypeWorkspaceView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// jtypeBoardView is one board option. `ref` (the relativePath) is what the create
// request submits (A8 — the exact-match arm of GetBoard resolves it); `id` is the
// canonical config id the server will store as board_ref after canonicalization;
// `columns` seed the trigger/done column selects. No token field.
type jtypeBoardView struct {
	ID      string                 `json:"id"`
	Ref     string                 `json:"ref"`
	Title   string                 `json:"title"`
	Columns []jtypeBoardColumnView `json:"columns"`
}

type jtypeBoardColumnView struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// discoveryClient resolves the effective jtype factory + token and builds the
// owner-authorized discovery client, or writes a typed fail-visible error and
// returns ok=false. Shared by both discovery handlers so the "integration off"
// and authz gates stay identical.
func (s *Server) discoveryClient(w http.ResponseWriter, r *http.Request) (jtypeDiscovery, bool) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleOwner) {
		return nil, false
	}
	f, clusterToken, ok := s.kanban.Factory(r.Context())
	if !ok {
		// Integration off / config errored — fail-visible, not an empty 200.
		writeError(w, http.StatusConflict, "kanban_not_configured",
			"the jtype integration is not configured — set it on the Cluster page (ask a cluster admin)")
		return nil, false
	}
	// The discovery reads use the effective cluster fallback token. A fresh cluster
	// with no fallback token can't enumerate — surface it (the console then falls
	// back to manual entry). Never a silent empty list.
	if clusterToken == "" {
		writeError(w, http.StatusServiceUnavailable, "jtype_unreachable",
			"no jtype token is configured to list workspaces/boards — connect one or enter the ids manually")
		return nil, false
	}
	return s.jtypeDiscoveryFor(f, clusterToken), true
}

// handleListJtypeWorkspaces returns the caller's jtype workspaces for the picker
// (owner only). Token never serialized.
func (s *Server) handleListJtypeWorkspaces(w http.ResponseWriter, r *http.Request) {
	client, ok := s.discoveryClient(w, r)
	if !ok {
		return
	}
	wss, err := client.ListWorkspaces(r.Context())
	if err != nil {
		s.writeDiscoveryError(w, "", err)
		return
	}
	out := make([]jtypeWorkspaceView, 0, len(wss))
	for _, ws := range wss {
		out = append(out, jtypeWorkspaceView{ID: ws.ID, Name: ws.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": out})
}

// maxDiscoveryBoards caps the per-board document fetches so a pathological
// workspace can't fan out unbounded (each board is one GetDocument round-trip).
const maxDiscoveryBoards = 100

// handleListJtypeBoards lists the `.board` documents of one workspace with their
// id/title/columns for the board + column pickers (owner only). Requires
// ?workspace=<wsid>. It lists documents, filters to `.board`, and parses each for
// its config id + columns. Token never serialized.
func (s *Server) handleListJtypeBoards(w http.ResponseWriter, r *http.Request) {
	// Authorize (owner-only) + resolve the jtype client BEFORE validating input, so
	// a non-owner probing this endpoint gets 403, never a 400 that leaks the shape.
	client, ok := s.discoveryClient(w, r)
	if !ok {
		return
	}
	workspace := strings.TrimSpace(r.URL.Query().Get("workspace"))
	if workspace == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "workspace query parameter is required")
		return
	}
	docs, err := client.ListDocuments(r.Context(), workspace)
	if err != nil {
		s.writeDiscoveryError(w, workspace, err)
		return
	}
	out := make([]jtypeBoardView, 0)
	fetched := 0
	for _, d := range docs {
		if !strings.HasSuffix(strings.ToLower(d.Path), ".board") {
			continue
		}
		if fetched >= maxDiscoveryBoards {
			break
		}
		fetched++
		// Fetch by the known doc id (no re-listing) to get id/title/columns.
		board, berr := client.GetBoardByDoc(r.Context(), workspace, d.ID)
		if berr != nil {
			// One unparseable/removed board must not sink the whole picker; skip it.
			s.log.Warn("kanban discovery: parse board", "workspace", workspace, "path", d.Path, "err", berr)
			continue
		}
		view := jtypeBoardView{ID: board.ID, Ref: d.Path, Title: board.Title}
		for _, c := range board.Columns {
			view.Columns = append(view.Columns, jtypeBoardColumnView{Key: c.Key, Name: c.Name})
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"boards": out})
}

// writeDiscoveryError maps a jtype read failure to the same actionable typed
// codes as create-link validation (D30 / A2): an auth error is a 400
// jtype_unauthorized, a workspace 404 is a 400 workspace_not_found, and only a
// genuine 5xx/transport failure is a 503 jtype_unreachable. Fail-visible, never a
// blank 200.
func (s *Server) writeDiscoveryError(w http.ResponseWriter, workspace string, err error) {
	s.log.Warn("kanban discovery: jtype read", "workspace", workspace, "err", err)
	var je *jtype.Error
	if errors.As(err, &je) {
		switch je.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			writeError(w, http.StatusBadRequest, "jtype_unauthorized",
				"the jtype token is invalid or lacks access")
			return
		case http.StatusNotFound:
			writeError(w, http.StatusBadRequest, "workspace_not_found",
				"jtype workspace '"+workspace+"' was not found")
			return
		}
	}
	writeError(w, http.StatusServiceUnavailable, "jtype_unreachable",
		"could not reach jtype: "+err.Error())
}
