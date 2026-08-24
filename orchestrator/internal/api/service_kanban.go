package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"
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
	InstallationID     string  `json:"installation_id"`
	BoardRef           string  `json:"board_ref"`
	TriggerColumn      *string `json:"trigger_column"`
	WorkColumn         *string `json:"work_column"`
	DoneColumn         *string `json:"done_column"`
	ModelID            string  `json:"model_id"`
	ModelEffort        *string `json:"model_effort"`
	ExecutionAccountID string  `json:"execution_account_id"`
	Enabled            *bool   `json:"enabled"`
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
		writeError(w, 404, "not_found", "repository not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load repository")
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
		writeError(w, 500, "internal", "could not load Repository Agent Board")
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
		writeError(w, 404, "not_found", "Agent Board is not enabled for this Repository")
		return
	}
	writeJSON(w, 200, spec)
}

type serviceKanbanPolicyView struct {
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
	Repository     string `json:"repository"`
	Model          struct {
		ID    string `json:"id,omitempty"`
		Label string `json:"label"`
	} `json:"model"`
	Board struct {
		WorkspaceID string `json:"workspace_id"`
		Ref         string `json:"ref"`
	} `json:"board"`
	TriggerColumn struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	} `json:"trigger_column"`
	WorkColumn struct {
		Key   string `json:"key,omitempty"`
		Label string `json:"label,omitempty"`
	} `json:"work_column"`
	DoneColumn struct {
		Key   string `json:"key,omitempty"`
		Label string `json:"label,omitempty"`
	} `json:"done_column"`
	Output string `json:"output"`
	Health struct {
		State      string  `json:"state"`
		Blocker    *string `json:"blocker"`
		RepairRole *string `json:"repair_role"`
	} `json:"health"`
}

func (s *Server) handleGetServiceKanbanPolicy(w http.ResponseWriter, r *http.Request) {
	svc, spec, ok := s.loadServiceKanban(w, r, domain.RoleViewer)
	if !ok {
		return
	}
	if spec == nil || spec.Kanban == nil {
		writeError(w, http.StatusNotFound, "not_found", "Agent Board is not enabled for this Repository")
		return
	}
	view := serviceKanbanPolicyView{
		RepositoryID: svc.ID, RepositoryName: svc.Name, Repository: svc.RepoOwnerName,
		Output: "comment_and_move_on_success",
	}
	if view.Repository == "" {
		view.Repository = svc.RawRepoURL
	}
	view.Board.Ref = spec.Kanban.BoardRef
	view.TriggerColumn.Key = spec.Kanban.TriggerColumn
	view.TriggerColumn.Label = spec.Kanban.TriggerLabel
	if view.TriggerColumn.Label == "" {
		view.TriggerColumn.Label = spec.Kanban.TriggerColumn
	}
	view.WorkColumn.Key = spec.Kanban.WorkColumn
	view.WorkColumn.Label = spec.Kanban.WorkLabel
	if view.WorkColumn.Label == "" {
		view.WorkColumn.Label = spec.Kanban.WorkColumn
	}
	view.DoneColumn.Key = spec.Kanban.DoneColumn
	view.DoneColumn.Label = spec.Kanban.DoneLabel
	if view.DoneColumn.Label == "" {
		view.DoneColumn.Label = spec.Kanban.DoneColumn
	}
	view.Health.State = "ready"
	if spec.Kanban.DoneColumn == "" {
		view.Output = "comment_only"
	}

	block := func(code, repairRole string) {
		if view.Health.State == "ready" {
			view.Health.State = "blocked"
			value := code
			view.Health.Blocker = &value
			if repairRole != "" {
				role := repairRole
				view.Health.RepairRole = &role
			}
		}
	}
	if !spec.Automation.Enabled {
		block("binding_disabled", "repository_owner")
	}
	if strings.TrimSpace(spec.Kanban.WorkColumn) == "" {
		block("work_column_not_configured", "repository_owner")
	}
	installation, err := s.st.GetPluginInstallation(r.Context(), spec.Kanban.InstallationID)
	if err != nil || installation.Provider != domain.PluginJType ||
		installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" ||
		installation.WorkspaceID == "" || !installation.TokenSet() {
		block("plugin_unavailable", "repository_owner")
	} else {
		view.Board.WorkspaceID = installation.WorkspaceID
		cfg, cfgErr := s.st.GetProviderConfig(r.Context(), domain.PluginJType)
		if cfgErr != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" ||
			cfg.ConfigRevision != installation.ConfigRevision {
			block("plugin_unavailable", "cluster_admin")
		}
	}
	if strings.HasPrefix(spec.Automation.LastError, "event_feed_unavailable:") {
		block("event_feed_unavailable", "repository_owner")
	}
	if strings.HasPrefix(spec.Automation.LastError, "bootstrap_unavailable:") {
		block("bootstrap_unavailable", "repository_owner")
	}
	if strings.HasPrefix(spec.Automation.LastError, "card_index_unavailable:") {
		block("card_index_unavailable", "repository_owner")
	}
	if strings.HasPrefix(spec.Automation.LastError, "board_validation_unavailable:") {
		block("board_validation_unavailable", "repository_owner")
	}
	if strings.HasPrefix(spec.Automation.LastError, "board_drift:") {
		block("board_drift", "repository_owner")
	}
	if blocker, repairRole := s.serviceKanbanRepositoryBlocker(r, svc); blocker != "" {
		block(blocker, repairRole)
	}
	if s.cfg.DisableK8s {
		block("runner_unavailable", "cluster_admin")
	}
	project, ownerErr := s.st.GetProject(r.Context(), svc.ProjectID)
	if ownerErr != nil {
		block("repository_unavailable", "repository_owner")
		view.Model.Label = "Not configured"
	} else if spec.Automation.ExecutionAccountID == "" || project.OwnerUserID != spec.Automation.ExecutionAccountID {
		block("execution_account_not_owner", "repository_owner")
		view.Model.Label = "Not configured"
	} else if model, authorized, selectErr := s.executionAccountModel(
		r, spec.Automation.ExecutionAccountID, spec.Automation.ModelID,
	); selectErr != nil {
		block("model_authorization_unavailable", "repository_owner")
		view.Model.Label = "Not configured"
	} else if !authorized {
		block("model_not_authorized", "repository_owner")
		view.Model.Label = "Not configured"
	} else {
		view.Model.ID = model.ID
		view.Model.Label = model.Name
		effort := strings.TrimSpace(spec.Automation.ModelEffort)
		if (model.Capabilities.Reasoning && (effort == "" || effort == "auto" || !domain.ValidModelEffort(effort))) ||
			(!model.Capabilities.Reasoning && effort != "") {
			block("model_effort_unsupported", "repository_owner")
		}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) serviceKanbanRepositoryBlocker(r *http.Request, svc *domain.Service) (string, string) {
	if svc == nil || svc.DeletingAt != nil {
		return "repository_unavailable", "repository_owner"
	}
	switch svc.RepoKind {
	case domain.RepoKindRaw:
		if strings.TrimSpace(svc.RawRepoURL) == "" {
			return "repository_not_configured", "repository_owner"
		}
		return "", ""
	case domain.RepoKindProvider:
		if !domain.ValidProvider(svc.Provider) || strings.TrimSpace(svc.RepoOwnerName) == "" {
			return "repository_not_configured", "repository_owner"
		}
	default:
		return "repository_not_configured", "repository_owner"
	}
	binding, err := s.st.GetServiceRepositoryBinding(r.Context(), svc.ID)
	if err != nil || binding.InstallationID == "" || strings.TrimSpace(binding.CloneURL) == "" {
		return "repository_unavailable", "repository_owner"
	}
	installation, err := s.st.GetPluginInstallation(r.Context(), binding.InstallationID)
	if err != nil || installation.Provider != domain.ProviderKind(svc.Provider) ||
		installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" {
		return "provider_unavailable", "repository_owner"
	}
	if (installation.Provider == domain.PluginGitHub && installation.GitHubInstallID == "") ||
		(installation.Provider != domain.PluginGitHub && !installation.TokenSet()) {
		return "provider_unavailable", "repository_owner"
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), installation.Provider)
	if err != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" ||
		cfg.ConfigRevision != installation.ConfigRevision {
		return "provider_unavailable", "cluster_admin"
	}
	return "", ""
}

func (s *Server) handleGetServiceKanbanCardExecutions(w http.ResponseWriter, r *http.Request) {
	svc, spec, ok := s.loadServiceKanban(w, r, domain.RoleViewer)
	if !ok {
		return
	}
	if spec == nil || spec.Kanban == nil {
		writeError(w, http.StatusNotFound, "not_found", "Agent Board is not enabled for this Repository")
		return
	}
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	documentPath := strings.TrimSpace(r.URL.Query().Get("document_path"))
	if workspaceID == "" || documentPath == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "workspace_id and document_path are required")
		return
	}
	installation, err := s.st.GetPluginInstallation(r.Context(), spec.Kanban.InstallationID)
	if err != nil || installation.WorkspaceID != workspaceID {
		writeError(w, http.StatusNotFound, "not_found", "Card is not part of this Repository Agent Board")
		return
	}
	usage, err := s.st.GetUsageSummary(r.Context(), domain.UsageSummaryQuery{
		SubjectKind: domain.UsageSubjectRun,
		ProjectID:   svc.ProjectID, ServiceID: svc.ID,
		CardWorkspace: workspaceID, CardPath: documentPath,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Card usage")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > 50 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 50")
			return
		}
		limit = value
	}
	before, cursorErr := decodeKanbanOccurrenceCursor(strings.TrimSpace(r.URL.Query().Get("before")))
	if cursorErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "before is not a valid Card execution cursor")
		return
	}
	claim, err := s.st.GetPluginKanbanClaimByPath(
		r.Context(), spec.Automation.ID, workspaceID, documentPath,
	)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, serviceKanbanExecutionsView{
			Items: []serviceKanbanExecutionItem{}, UsageSummary: usage,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Card execution claim")
		return
	}
	occurrences, err := s.st.ListPluginKanbanCardExecutions(
		r.Context(), spec.Automation.ID, svc.ID, workspaceID, documentPath, before, limit+1,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Card executions")
		return
	}
	var nextCursor *string
	if len(occurrences) > limit {
		occurrences = occurrences[:limit]
		value := encodeKanbanOccurrenceCursor(occurrences[len(occurrences)-1])
		nextCursor = &value
	}
	items := make([]serviceKanbanExecutionItem, 0, len(occurrences))
	for i := range occurrences {
		occurrence := &occurrences[i]
		var run *domain.Run
		if occurrence.RunID == "" {
			items = append(items, serviceKanbanExecutionView(*occurrence, nil))
			continue
		}
		run, err = s.st.GetRun(r.Context(), occurrence.RunID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "internal", "could not load Card execution Run")
			return
		}
		item := serviceKanbanExecutionView(*occurrence, run)
		if run != nil {
			runUsage, usageErr := s.st.GetUsageSummary(r.Context(), domain.UsageSummaryQuery{
				SubjectKind: domain.UsageSubjectRun,
				RunID:       run.ID,
			})
			if usageErr != nil {
				writeError(w, http.StatusInternalServerError, "internal", "could not load Card execution usage")
				return
			}
			item.UsageSummary = &runUsage
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, serviceKanbanExecutionsView{
		Claim: &serviceKanbanClaimView{
			DocumentPath: claim.DocumentPath, ExternalRefAvailable: claim.ExternalRefAvailable,
		},
		Items: items, NextCursor: nextCursor, UsageSummary: usage,
	})
}

type createServiceKanbanOccurrenceReq struct {
	WorkspaceID    string `json:"workspace_id"`
	DocumentPath   string `json:"document_path"`
	IdempotencyKey string `json:"idempotency_key"`
}

type createServiceKanbanOccurrenceView struct {
	Occurrence serviceKanbanExecutionItem `json:"occurrence"`
	Replayed   bool                       `json:"replayed"`
}

func (s *Server) handleCreateServiceKanbanOccurrence(w http.ResponseWriter, r *http.Request) {
	svc, spec, ok := s.loadServiceKanban(w, r, domain.RoleMember)
	if !ok {
		return
	}
	if spec == nil || spec.Kanban == nil || !spec.Automation.Enabled {
		writeError(w, http.StatusConflict, "agent_board_disabled", "Agent Board is not enabled for this Repository")
		return
	}
	var req createServiceKanbanOccurrenceReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.DocumentPath = strings.TrimSpace(req.DocumentPath)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	normalized := strings.ReplaceAll(req.DocumentPath, "\\", "/")
	cleaned := path.Clean(normalized)
	if req.WorkspaceID == "" || req.IdempotencyKey == "" || len(req.IdempotencyKey) > 256 ||
		cleaned != normalized || strings.HasPrefix(cleaned, "/") ||
		(!strings.HasPrefix(cleaned, "cards/") && !strings.HasPrefix(cleaned, "jcode-automation/")) ||
		!strings.HasSuffix(strings.ToLower(cleaned), ".md") {
		writeError(w, http.StatusBadRequest, "bad_request", "workspace_id, a valid Card document_path, and idempotency_key are required")
		return
	}
	installation, err := s.st.GetPluginInstallation(r.Context(), spec.Kanban.InstallationID)
	if err != nil || installation.ProjectID != svc.ProjectID || installation.Provider != domain.PluginJType ||
		installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" ||
		installation.WorkspaceID != req.WorkspaceID {
		writeError(w, http.StatusConflict, "plugin_unavailable", "reconnect the Repository JType Plugin before running this Card")
		return
	}
	client, ok := s.pluginJtypeClient(w, r, installation)
	if !ok {
		return
	}
	documents, err := client.ListDocuments(r.Context(), req.WorkspaceID)
	if err != nil {
		s.writeDiscoveryError(w, req.WorkspaceID, err)
		return
	}
	var documentID string
	for i := range documents {
		if documents[i].Path == cleaned {
			documentID = documents[i].ID
			break
		}
	}
	if documentID == "" {
		writeError(w, http.StatusNotFound, "card_not_found", "the Card is no longer available in JType")
		return
	}
	document, err := client.GetDocument(r.Context(), req.WorkspaceID, documentID)
	if err != nil {
		s.writeDiscoveryError(w, req.WorkspaceID, err)
		return
	}
	card := jtype.ParseCard(document.Content)
	if document.Path != cleaned || card.Board != spec.Kanban.BoardRef || strings.TrimSpace(card.Status) == "" {
		writeError(w, http.StatusConflict, "card_not_on_board", "the document is not a Card on this Repository Agent Board")
		return
	}
	principal := principalFrom(r.Context())
	actor := "Cloud user"
	if principal != nil && principal.user != nil && strings.TrimSpace(principal.user.DisplayName) != "" {
		actor = strings.TrimSpace(principal.user.DisplayName)
	}
	key := sha256.Sum256([]byte(documentID + "\x00" + req.IdempotencyKey))
	result, err := s.st.ObservePluginKanbanCard(r.Context(), store.PluginKanbanObservation{
		AutomationID: spec.Automation.ID, ServiceID: svc.ID,
		InstallationID: installation.ID, WorkspaceID: req.WorkspaceID,
		DocumentID: documentID, DocumentPath: cleaned,
		TriggerColumn: spec.Kanban.TriggerColumn, DoneColumn: spec.Kanban.DoneColumn,
		ObservedColumn: card.Status, EventKey: "manual:" + base64.RawURLEncoding.EncodeToString(key[:]),
		ActorDisplay: actor, Manual: true, ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create the Card occurrence")
		return
	}
	if result.Occurrence == nil {
		code := "occurrence_active"
		message := "this Card already has an active Agent run"
		if result.SuppressedReason == store.PluginKanbanWritebackPending {
			code = "writeback_pending"
			message = "the previous Agent run is still writing its result back to this Card"
		} else if result.SuppressedReason == "" {
			writeError(w, http.StatusInternalServerError, "occurrence_not_created", "the Card occurrence was not created")
			return
		}
		writeError(w, http.StatusConflict, code, message)
		return
	}
	if result.SuppressedReason != "" {
		writeError(w, http.StatusConflict, "occurrence_active", "this Card already has an active Agent run")
		return
	}
	writeJSON(w, http.StatusAccepted, createServiceKanbanOccurrenceView{
		Occurrence: serviceKanbanExecutionView(*result.Occurrence, nil),
		Replayed:   !result.Created,
	})
}

type kanbanOccurrenceCursorEnvelope struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func encodeKanbanOccurrenceCursor(occurrence domain.PluginKanbanOccurrence) string {
	payload, _ := json.Marshal(kanbanOccurrenceCursorEnvelope{
		CreatedAt: occurrence.CreatedAt.UTC(), ID: occurrence.ID,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeKanbanOccurrenceCursor(raw string) (*store.PluginKanbanOccurrenceCursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var envelope kanbanOccurrenceCursorEnvelope
	if err = json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	if envelope.CreatedAt.IsZero() || strings.TrimSpace(envelope.ID) == "" {
		return nil, errors.New("cursor fields are required")
	}
	return &store.PluginKanbanOccurrenceCursor{
		CreatedAt: envelope.CreatedAt, ID: envelope.ID,
	}, nil
}

type serviceKanbanClaimView struct {
	DocumentPath         string `json:"document_path"`
	ExternalRefAvailable bool   `json:"external_ref_available"`
}

type serviceKanbanRequestedActorView struct {
	Label     string `json:"label"`
	Precision string `json:"precision"`
}

type serviceKanbanExecutionRunView struct {
	ID     string           `json:"id"`
	Status domain.RunStatus `json:"status"`
	Href   string           `json:"href"`
}

type serviceKanbanExecutionReceiptView struct {
	External  string `json:"external"`
	Writeback string `json:"writeback"`
}

type serviceKanbanExecutionItem struct {
	ID             string                            `json:"id"`
	Status         domain.KanbanOccurrenceState      `json:"status"`
	Outcome        string                            `json:"outcome,omitempty"`
	Summary        string                            `json:"summary"`
	Reason         *string                           `json:"reason"`
	ReasonCode     string                            `json:"reason_code,omitempty"`
	RepairRole     *string                           `json:"repair_role"`
	RequestedActor *serviceKanbanRequestedActorView  `json:"requested_actor"`
	Run            *serviceKanbanExecutionRunView    `json:"run"`
	Receipt        serviceKanbanExecutionReceiptView `json:"receipt"`
	CreatedAt      time.Time                         `json:"created_at"`
	UpdatedAt      time.Time                         `json:"updated_at"`
	TerminalAt     *time.Time                        `json:"terminal_at,omitempty"`
	UsageSummary   *domain.UsageSummary              `json:"usage_summary,omitempty"`
}

type serviceKanbanExecutionsView struct {
	Claim        *serviceKanbanClaimView      `json:"claim"`
	Items        []serviceKanbanExecutionItem `json:"items"`
	NextCursor   *string                      `json:"next_cursor"`
	UsageSummary domain.UsageSummary          `json:"usage_summary"`
}

func serviceKanbanExecutionView(occurrence domain.PluginKanbanOccurrence, run *domain.Run) serviceKanbanExecutionItem {
	status := occurrence.State
	outcome := occurrence.Outcome
	terminalAt := occurrence.TerminalAt
	reasonCode := occurrence.ReasonCode
	reasonMessage := occurrence.ReasonMessage
	repairRoleCode := occurrence.RepairRole
	var runView *serviceKanbanExecutionRunView
	if run != nil {
		switch {
		case run.Status.Terminal():
			status = domain.KanbanOccurrenceTerminal
			outcome = string(run.Status)
			terminalAt = run.FinishedAt
		case run.Status == domain.StatusRunning || run.Status == domain.StatusAwaitingInput:
			status = domain.KanbanOccurrenceRunning
		default:
			status = domain.KanbanOccurrenceQueued
		}
		runView = &serviceKanbanExecutionRunView{
			ID: run.ID, Status: run.Status, Href: "/runs/" + run.ID,
		}
	} else if occurrence.RunID != "" && status != domain.KanbanOccurrenceTerminal {
		// Runs are the execution truth while they exist. If a retained Card
		// occurrence points at a Run that is no longer readable, do not fall
		// back to a stale queued/received state that looks healthy.
		status = domain.KanbanOccurrenceBlocked
		reasonCode = "run_unavailable"
		reasonMessage = "The linked Cloud Run is no longer available."
		repairRoleCode = "repository_owner"
	}
	summary := "Card entry received"
	switch status {
	case domain.KanbanOccurrenceBlocked:
		summary = "Execution is blocked"
	case domain.KanbanOccurrenceQueued:
		summary = "Run is queued"
	case domain.KanbanOccurrenceRunning:
		summary = "Run is applying the requested change"
	case domain.KanbanOccurrenceTerminal:
		switch outcome {
		case string(domain.StatusSucceeded):
			summary = "Run completed successfully"
		case string(domain.StatusCanceled):
			summary = "Run was canceled"
		default:
			summary = "Run failed"
		}
	}
	var reason *string
	if reasonMessage != "" {
		value := reasonMessage
		reason = &value
	}
	var repairRole *string
	if repairRoleCode != "" {
		value := repairRoleCode
		repairRole = &value
	}
	var actor *serviceKanbanRequestedActorView
	if occurrence.ActorDisplay != "" {
		actor = &serviceKanbanRequestedActorView{
			Label: occurrence.ActorDisplay, Precision: "display_only",
		}
	}
	externalReceipt := "not_required"
	if occurrence.WritebackState == "unavailable" {
		externalReceipt = "unavailable"
	} else if occurrence.ReceiptPhase != "" {
		externalReceipt = "pending"
		if occurrence.ReceiptWrittenAt != nil {
			externalReceipt = "written"
		}
	}
	return serviceKanbanExecutionItem{
		ID: occurrence.ID, Status: status, Outcome: outcome, Summary: summary,
		Reason: reason, ReasonCode: reasonCode, RepairRole: repairRole,
		RequestedActor: actor, Run: runView,
		Receipt: serviceKanbanExecutionReceiptView{
			External: externalReceipt, Writeback: occurrence.WritebackState,
		},
		CreatedAt: occurrence.CreatedAt, UpdatedAt: occurrence.UpdatedAt,
		TerminalAt: terminalAt,
	}
}

func (s *Server) handlePutServiceKanban(w http.ResponseWriter, r *http.Request) {
	svc, current, ok := s.loadServiceKanban(w, r, domain.RoleOwner)
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
	modelID := strings.TrimSpace(req.ModelID)
	modelEffort := ""
	executionAccountID := strings.TrimSpace(req.ExecutionAccountID)
	if current != nil {
		if modelID == "" {
			modelID = current.Automation.ModelID
		}
		modelEffort = current.Automation.ModelEffort
		if executionAccountID == "" {
			executionAccountID = current.Automation.ExecutionAccountID
		}
	}
	if req.ModelEffort != nil {
		modelEffort = strings.TrimSpace(*req.ModelEffort)
	}
	project, projectErr := s.st.GetProject(r.Context(), svc.ProjectID)
	if projectErr != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load repository owner")
		return
	}
	if executionAccountID == "" {
		executionAccountID = project.OwnerUserID
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if enabled {
		if modelID == "" {
			writeError(w, http.StatusBadRequest, "model_required", "model_id is required when Agent Board is enabled")
			return
		}
		if executionAccountID == "" {
			writeError(w, http.StatusBadRequest, "execution_account_required", "execution_account_id is required when Agent Board is enabled")
			return
		}
		if project.OwnerUserID == "" || executionAccountID != project.OwnerUserID {
			writeError(w, http.StatusConflict, "execution_account_not_owner", "the execution account must own this repository")
			return
		}
		model, authorized, modelErr := s.executionAccountModel(r, executionAccountID, modelID)
		if modelErr != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not validate execution model")
			return
		}
		if !authorized {
			writeError(w, http.StatusConflict, "model_not_authorized", "the selected model is not authorized for the execution account")
			return
		}
		if model.Capabilities.Reasoning && (modelEffort == "" || modelEffort == "auto" || !domain.ValidModelEffort(modelEffort)) {
			writeError(w, http.StatusBadRequest, "bad_request", "model_effort must be one of low, medium, or high for a reasoning model")
			return
		}
		if modelEffort != "" && !model.Capabilities.Reasoning {
			writeError(w, http.StatusConflict, "model_effort_unsupported", "the selected model does not support reasoning effort")
			return
		}
	}
	triggerColumn := defaultKanbanTriggerColumn
	workColumn := ""
	doneColumn := defaultKanbanDoneColumn
	if current != nil && current.Kanban != nil {
		triggerColumn = current.Kanban.TriggerColumn
		workColumn = current.Kanban.WorkColumn
		doneColumn = current.Kanban.DoneColumn
	}
	if req.TriggerColumn != nil {
		triggerColumn = strings.TrimSpace(*req.TriggerColumn)
	}
	if req.WorkColumn != nil {
		workColumn = strings.TrimSpace(*req.WorkColumn)
	}
	if req.DoneColumn != nil {
		doneColumn = strings.TrimSpace(*req.DoneColumn)
	}
	if triggerColumn == "" || workColumn == "" {
		writeError(w, 400, "bad_request", "trigger_column and work_column are required")
		return
	}
	if workColumn == triggerColumn || (doneColumn != "" && (doneColumn == triggerColumn || doneColumn == workColumn)) {
		writeError(w, 400, "bad_request", "trigger_column, work_column, and configured done_column must be distinct")
		return
	}
	in, err := s.st.GetPluginInstallation(r.Context(), req.InstallationID)
	if err != nil || in.ProjectID != svc.ProjectID || in.Provider != domain.PluginJType || in.Status != domain.PluginStatusEnabled || in.LastHealthError != "" || in.WorkspaceID == "" {
		writeError(w, 409, "plugin_unavailable", "connect and configure the Repository JType Provider first")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), domain.PluginJType)
	if err != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" || cfg.ConfigRevision != in.ConfigRevision || !in.TokenSet() {
		writeError(w, 409, "plugin_unavailable", "reconnect the Repository JType Provider before enabling Agent Board")
		return
	}
	token, tokenOK := s.pluginAccessToken(in)
	if !tokenOK {
		writeError(w, 409, "plugin_unavailable", "reconnect the Repository JType Provider before enabling Agent Board")
		return
	}
	canonicalBoardRef := ""
	triggerLabel := triggerColumn
	workLabel := workColumn
	doneLabel := doneColumn
	roundTripCurrent := current != nil && current.Kanban != nil &&
		current.Kanban.InstallationID == req.InstallationID &&
		current.Kanban.BoardRef == req.BoardRef &&
		current.Kanban.TriggerColumn == triggerColumn &&
		current.Kanban.WorkColumn == workColumn &&
		current.Kanban.DoneColumn == doneColumn
	if roundTripCurrent {
		// GET returns the persisted canonical board id. An enabled-only PUT may
		// round-trip that value without performing an unbounded board-document
		// scan; the poller remains responsible for detecting later board drift.
		canonicalBoardRef = current.Kanban.BoardRef
		if current.Kanban.TriggerLabel != "" {
			triggerLabel = current.Kanban.TriggerLabel
		}
		if current.Kanban.WorkLabel != "" {
			workLabel = current.Kanban.WorkLabel
		}
		if current.Kanban.DoneLabel != "" {
			doneLabel = current.Kanban.DoneLabel
		}
	} else {
		// New or changed bindings use a document path/name. A canonical-looking
		// b_* value is only accepted when it exactly matches the current binding.
		if strings.HasPrefix(req.BoardRef, "b_") && !strings.HasSuffix(strings.ToLower(req.BoardRef), ".board") {
			writeError(w, 400, "board_ref_requires_path", "select the board by its .board document path")
			return
		}
		factory := jtype.NewFactory(cfg.BaseURL, 20*time.Second)
		cacheKey := strings.Join([]string{
			in.ID, strconv.FormatInt(in.ConfigRevision, 10), in.WorkspaceID, req.BoardRef,
		}, "|")
		board, boardErr := s.agentBoardCache.get(r.Context(), cacheKey, func(ctx context.Context) (*jtype.Board, error) {
			return s.boardValidatorFor(factory, token).GetBoard(ctx, in.WorkspaceID, req.BoardRef)
		})
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
		if workColumn != "" && !boardHasColumn(board, workColumn) {
			writeError(w, 409, "column_not_found", "work_column '"+workColumn+"' is not a column on board "+req.BoardRef)
			return
		}
		if doneColumn != "" && !boardHasColumn(board, doneColumn) {
			writeError(w, 409, "column_not_found", "done_column '"+doneColumn+"' is not a column on board "+req.BoardRef)
			return
		}
		triggerLabel = boardColumnLabel(board, triggerColumn)
		workLabel = boardColumnLabel(board, workColumn)
		doneLabel = boardColumnLabel(board, doneColumn)
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
			writeError(w, 409, "board_already_bound", "this board is already connected to another Repository")
			return
		}
	}
	now := time.Now().UTC()
	a := &domain.PluginAutomation{ID: domain.NewID(), ServiceID: svc.ID, InstallationID: req.InstallationID, Name: "Agent Board", TriggerKind: "kanban", RunKind: domain.RunKindAgent, PromptTemplate: "Complete the task described by the JType card.", ModelID: modelID, ModelEffort: modelEffort, ExecutionAccountID: executionAccountID, Enabled: enabled, IgnoreJCode: true, CreatedBy: principalFrom(r.Context()).userID(), CreatedAt: now}
	trigger := &domain.KanbanTrigger{
		AutomationID: a.ID, InstallationID: req.InstallationID, BoardRef: canonicalBoardRef,
		TriggerColumn: triggerColumn, TriggerLabel: triggerLabel,
		WorkColumn: workColumn, WorkLabel: workLabel,
		DoneColumn: doneColumn, DoneLabel: doneLabel,
	}
	if current == nil {
		if err := s.st.CreatePluginAutomation(r.Context(), a, nil, nil, trigger, nil); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				writeError(w, 409, "board_already_bound", "this Repository or board already has an Agent Board connection")
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
				writeError(w, 409, "board_already_bound", "this board is already connected to another Repository")
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

func (s *Server) executionAccountModel(r *http.Request, accountID, modelID string) (domain.Model, bool, error) {
	models, err := s.st.ListModelsForAccount(r.Context(), strings.TrimSpace(accountID))
	if err != nil {
		return domain.Model{}, false, err
	}
	for i := range models {
		if models[i].ID == modelID {
			return models[i], true, nil
		}
	}
	return domain.Model{}, false, nil
}

func (s *Server) handleDeleteServiceKanban(w http.ResponseWriter, r *http.Request) {
	_, spec, ok := s.loadServiceKanban(w, r, domain.RoleOwner)
	if !ok {
		return
	}
	if spec == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	active, err := s.st.HasActivePluginKanbanOccurrences(r.Context(), spec.Automation.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not check Agent Board occurrences")
		return
	}
	if active {
		writeError(w, http.StatusConflict, "active_occurrences", "finish or resolve active Card executions before disconnecting this Agent Board")
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

func boardColumnLabel(board *jtype.Board, key string) string {
	if board == nil || key == "" {
		return ""
	}
	for _, column := range board.Columns {
		if column.Key == key {
			if strings.TrimSpace(column.Name) != "" {
				return column.Name
			}
			return column.Key
		}
	}
	return key
}
