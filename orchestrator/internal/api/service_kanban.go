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

const (
	defaultKanbanTriggerColumn = "ai"
	defaultKanbanDoneColumn    = "done"
)

type serviceKanbanReq struct {
	InstallationID string  `json:"installation_id"`
	BoardRef       string  `json:"board_ref"`
	TriggerColumn  *string `json:"trigger_column"`
	DoneColumn     *string `json:"done_column"`
	Enabled        *bool   `json:"enabled"`
}

func (s *Server) serviceKanbanSpec(ctxReq *http.Request, svc *domain.Service) (*domain.PluginAutomationSpec, error) {
	items, err := s.st.ListPluginAutomationsByProject(ctxReq.Context(), svc.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ServiceID == svc.ID && item.TriggerKind == "kanban" {
			return s.st.GetPluginAutomationSpec(ctxReq.Context(), item.ID)
		}
	}
	return nil, store.ErrNotFound
}

func (s *Server) loadServiceKanban(w http.ResponseWriter, r *http.Request, role domain.Role) (*domain.Service, *domain.PluginAutomationSpec, bool) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "service not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load service")
		return nil, nil, false
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, role) {
		return nil, nil, false
	}
	spec, err := s.serviceKanbanSpec(r, svc)
	if errors.Is(err, store.ErrNotFound) {
		return svc, nil, true
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load Service Kanban")
		return nil, nil, false
	}
	return svc, spec, true
}

func (s *Server) handleGetServiceKanban(w http.ResponseWriter, r *http.Request) {
	_, spec, ok := s.loadServiceKanban(w, r, domain.RoleViewer)
	if !ok {
		return
	}
	if spec == nil {
		writeError(w, 404, "not_found", "Kanban is not enabled for this Service")
		return
	}
	writeJSON(w, 200, spec)
}

func (s *Server) handlePutServiceKanban(w http.ResponseWriter, r *http.Request) {
	svc, current, ok := s.loadServiceKanban(w, r, domain.RoleMember)
	if !ok {
		return
	}
	var req serviceKanbanReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.InstallationID = strings.TrimSpace(req.InstallationID)
	req.BoardRef = strings.TrimSpace(req.BoardRef)
	if current != nil {
		if req.InstallationID == "" {
			req.InstallationID = current.Automation.InstallationID
		}
		if req.BoardRef == "" && current.Kanban != nil {
			req.BoardRef = current.Kanban.BoardRef
		}
	}
	if req.InstallationID == "" || req.BoardRef == "" {
		writeError(w, 400, "bad_request", "installation_id and board_ref are required")
		return
	}
	triggerColumn := defaultKanbanTriggerColumn
	doneColumn := defaultKanbanDoneColumn
	if current != nil && current.Kanban != nil {
		triggerColumn = current.Kanban.TriggerColumn
		doneColumn = current.Kanban.DoneColumn
	}
	if req.TriggerColumn != nil {
		triggerColumn = strings.TrimSpace(*req.TriggerColumn)
	}
	if req.DoneColumn != nil {
		doneColumn = strings.TrimSpace(*req.DoneColumn)
	}
	if triggerColumn == "" {
		writeError(w, 400, "bad_request", "trigger_column is required")
		return
	}
	in, err := s.st.GetPluginInstallation(r.Context(), req.InstallationID)
	if err != nil || in.ProjectID != svc.ProjectID || in.Provider != domain.PluginJType || in.Status != domain.PluginStatusEnabled || in.LastHealthError != "" || in.WorkspaceID == "" {
		writeError(w, 409, "plugin_unavailable", "enable and configure the Project JType Plugin first")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), domain.PluginJType)
	if err != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" || cfg.ConfigRevision != in.ConfigRevision || !in.TokenSet() {
		writeError(w, 409, "plugin_unavailable", "reconnect the Project JType Plugin before enabling Kanban")
		return
	}
	token, tokenOK := s.pluginAccessToken(in)
	if !tokenOK {
		writeError(w, 409, "plugin_unavailable", "reconnect the Project JType Plugin before enabling Kanban")
		return
	}
	canonicalBoardRef := ""
	roundTripCurrent := current != nil && current.Kanban != nil &&
		current.Kanban.InstallationID == req.InstallationID &&
		current.Kanban.BoardRef == req.BoardRef &&
		current.Kanban.TriggerColumn == triggerColumn &&
		current.Kanban.DoneColumn == doneColumn
	if roundTripCurrent {
		// GET returns the persisted canonical board id. An enabled-only PUT may
		// round-trip that value without performing an unbounded board-document
		// scan; the poller remains responsible for detecting later board drift.
		canonicalBoardRef = current.Kanban.BoardRef
	} else {
		// New or changed bindings use a document path/name. A canonical-looking
		// b_* value is only accepted when it exactly matches the current binding.
		if strings.HasPrefix(req.BoardRef, "b_") && !strings.HasSuffix(strings.ToLower(req.BoardRef), ".board") {
			writeError(w, 400, "board_ref_requires_path", "select the board by its .board document path")
			return
		}
		factory := jtype.NewFactory(cfg.BaseURL, 20*time.Second)
		board, boardErr := s.boardValidatorFor(factory, token).GetBoard(r.Context(), in.WorkspaceID, req.BoardRef)
		if boardErr != nil {
			s.writeDiscoveryError(w, in.WorkspaceID, boardErr)
			return
		}
		canonicalBoardRef = strings.TrimSpace(board.ID)
		if canonicalBoardRef == "" {
			writeError(w, 400, "board_not_available", "the selected board is not available to this JType Plugin")
			return
		}
		if !boardHasColumn(board, triggerColumn) {
			writeError(w, 409, "column_not_found", "trigger_column '"+triggerColumn+"' is not a column on board "+req.BoardRef)
			return
		}
		if doneColumn != "" && !boardHasColumn(board, doneColumn) {
			writeError(w, 409, "column_not_found", "done_column '"+doneColumn+"' is not a column on board "+req.BoardRef)
			return
		}
	}
	items, err := s.st.ListPluginAutomationsByProject(r.Context(), svc.ProjectID)
	if err != nil {
		writeError(w, 500, "internal", "could not validate Kanban binding")
		return
	}
	for _, item := range items {
		if item.TriggerKind != "kanban" || item.ServiceID == svc.ID {
			continue
		}
		other, getErr := s.st.GetPluginAutomationSpec(r.Context(), item.ID)
		if getErr == nil && other.Kanban != nil && other.Kanban.InstallationID == req.InstallationID && other.Kanban.BoardRef == canonicalBoardRef {
			writeError(w, 409, "board_already_bound", "this board is already enabled for another Service")
			return
		}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	now := time.Now().UTC()
	a := &domain.PluginAutomation{ID: domain.NewID(), ServiceID: svc.ID, InstallationID: req.InstallationID, Name: "Kanban", TriggerKind: "kanban", PromptTemplate: "Complete the task described by the JType card.", Enabled: enabled, IgnoreJCode: true, CreatedBy: principalFrom(r.Context()).userID(), CreatedAt: now}
	trigger := &domain.KanbanTrigger{AutomationID: a.ID, InstallationID: req.InstallationID, BoardRef: canonicalBoardRef, TriggerColumn: triggerColumn, DoneColumn: doneColumn}
	if current == nil {
		if err := s.st.CreatePluginAutomation(r.Context(), a, nil, nil, trigger, nil); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				writeError(w, 409, "board_already_bound", "this Service or board already has a Kanban binding")
				return
			}
			writeError(w, 500, "internal", "could not enable Kanban")
			return
		}
	} else {
		a.ID = current.Automation.ID
		a.CreatedAt = current.Automation.CreatedAt
		a.CreatedBy = current.Automation.CreatedBy
		trigger.AutomationID = a.ID
		if err := s.st.ReplacePluginAutomationSpec(r.Context(), a, nil, nil, trigger, nil); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				writeError(w, 409, "board_already_bound", "this board is already enabled for another Service")
				return
			}
			writeError(w, 500, "internal", "could not update Kanban")
			return
		}
	}
	spec, err := s.st.GetPluginAutomationSpec(r.Context(), a.ID)
	if err != nil {
		writeError(w, 500, "internal", "could not load Kanban")
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

func (s *Server) handleDeleteServiceKanban(w http.ResponseWriter, r *http.Request) {
	_, spec, ok := s.loadServiceKanban(w, r, domain.RoleMember)
	if !ok {
		return
	}
	if spec == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Disable new polling without deleting claims for already-started runs.
	// Their immutable Plugin snapshot + frozen claim target must remain available
	// until result writeback completes.
	spec.Automation.Enabled = false
	if err := s.st.UpdatePluginAutomation(r.Context(), &spec.Automation); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, "internal", "could not disable Kanban")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
