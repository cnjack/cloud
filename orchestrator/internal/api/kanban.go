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

// kanbanLinkView is the API shape of a kanban_link (never carries a secret).
type kanbanLinkView struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	BoardRef      string `json:"board_ref"`
	ProjectID     string `json:"project_id"`
	ServiceID     string `json:"service_id"`
	TriggerColumn string `json:"trigger_column"`
	DoneColumn    string `json:"done_column,omitempty"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     string `json:"created_at"`
}

func linkView(l domain.KanbanLink) kanbanLinkView {
	v := kanbanLinkView{
		ID: l.ID, WorkspaceID: l.WorkspaceID, BoardRef: l.BoardRef,
		ProjectID: l.ProjectID, ServiceID: l.ServiceID,
		TriggerColumn: l.TriggerColumn, DoneColumn: l.DoneColumn,
		Enabled: l.Enabled, CreatedAt: l.CreatedAt.UTC().Format(time.RFC3339),
	}
	return v
}

// createKanbanLinkReq is the POST /api/v1/system/kanban/links body.
type createKanbanLinkReq struct {
	WorkspaceID   string `json:"workspace_id"`
	BoardRef      string `json:"board_ref"`
	ProjectID     string `json:"project_id"`
	ServiceID     string `json:"service_id"`
	TriggerColumn string `json:"trigger_column"`
	DoneColumn    string `json:"done_column"`
}

// handleListKanbanLinks returns every kanban link. Readable by any logged-in
// principal (so a non-admin console can show the board wiring); writes are
// cluster-admin only.
func (s *Server) handleListKanbanLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.st.ListKanbanLinks(r.Context())
	if err != nil {
		s.log.Error("list kanban links", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list kanban links")
		return
	}
	out := make([]kanbanLinkView, 0, len(links))
	for _, l := range links {
		out = append(out, linkView(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// handleCreateKanbanLink creates a kanban link (cluster-admin only). It validates
// that the project + service exist (and the service belongs to the project), and
// — when the jtype integration is configured — fetches the board to confirm the
// trigger/done columns are real columns on it (fail-visible: a typo'd column
// would otherwise silently never trigger). When jtype is OFF, column validation
// is skipped (the link is stored but cannot dispatch until jtype is configured).
func (s *Server) handleCreateKanbanLink(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	var req createKanbanLinkReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.BoardRef = strings.TrimSpace(req.BoardRef)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.TriggerColumn = strings.TrimSpace(req.TriggerColumn)
	req.DoneColumn = strings.TrimSpace(req.DoneColumn)

	if req.WorkspaceID == "" || req.BoardRef == "" || req.ProjectID == "" ||
		req.ServiceID == "" || req.TriggerColumn == "" {
		writeError(w, http.StatusBadRequest, "bad_request",
			"workspace_id, board_ref, project_id, service_id and trigger_column are required")
		return
	}

	// Project + service existence, and the service must belong to the project.
	svc, err := s.st.GetService(r.Context(), req.ServiceID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "bad_request", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if svc.ProjectID != req.ProjectID {
		writeError(w, http.StatusBadRequest, "bad_request", "service does not belong to this project")
		return
	}
	if _, err := s.st.GetProject(r.Context(), req.ProjectID); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "project not found")
		return
	}

	// Column validation against the live board (only when jtype is configured).
	// A jtype failure is fail-visible (503) rather than a silent accept.
	if s.jtypeBoard != nil {
		board, err := s.jtypeBoard.GetBoard(r.Context(), req.WorkspaceID, req.BoardRef)
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

	link := &domain.KanbanLink{
		ID: domain.NewID(), WorkspaceID: req.WorkspaceID, BoardRef: req.BoardRef,
		ProjectID: req.ProjectID, ServiceID: req.ServiceID,
		TriggerColumn: req.TriggerColumn, DoneColumn: req.DoneColumn,
		Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
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
	writeJSON(w, http.StatusCreated, linkView(*link))
}

// handleDeleteKanbanLink deletes a kanban link (cluster-admin only). Its claims
// cascade (DB FK ON DELETE CASCADE; memory store cascades too).
func (s *Server) handleDeleteKanbanLink(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.st.DeleteKanbanLink(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "kanban link not found")
			return
		}
		s.log.Error("delete kanban link", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete kanban link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
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
