package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/provenance"
	"github.com/cnjack/jcloud/internal/store"
)

type automationExecutionCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type automationOutputView struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Href      string `json:"href,omitempty"`
	Available bool   `json:"available"`
}

type automationRunView struct {
	ID     string           `json:"id"`
	Status domain.RunStatus `json:"status"`
	Href   string           `json:"href"`
}

type automationCardView struct {
	WorkspaceID  string `json:"workspace_id"`
	DocumentID   string `json:"document_id,omitempty"`
	DocumentPath string `json:"document_path"`
	Href         string `json:"href,omitempty"`
	Available    bool   `json:"available"`
}

type automationExecutionView struct {
	ID               string                          `json:"id"`
	AutomationID     string                          `json:"automation_id"`
	AutomationName   string                          `json:"automation_name"`
	TriggerKind      string                          `json:"trigger_kind"`
	State            domain.AutomationExecutionState `json:"state"`
	Outcome          string                          `json:"outcome,omitempty"`
	OutputMode       string                          `json:"output_mode"`
	ReasonCode       string                          `json:"reason_code,omitempty"`
	Reason           string                          `json:"reason,omitempty"`
	RepairRole       string                          `json:"repair_role,omitempty"`
	RequestedActor   *domain.ProvenanceActorRef      `json:"requested_actor"`
	AccountableActor *domain.ProvenanceActorRef      `json:"accountable_actor"`
	Output           automationOutputView            `json:"output"`
	Run              *automationRunView              `json:"run"`
	Card             *automationCardView             `json:"card"`
	ExternalURL      string                          `json:"external_url,omitempty"`
	WritebackState   string                          `json:"writeback_state"`
	Usage            map[string]string               `json:"usage"`
	CreatedAt        time.Time                       `json:"created_at"`
	UpdatedAt        time.Time                       `json:"updated_at"`
	TerminalAt       *time.Time                      `json:"terminal_at,omitempty"`
}

func (s *Server) handleListAutomationExecutions(w http.ResponseWriter, r *http.Request) {
	spec, _, ok := s.loadPluginAutomationForMember(w, r, domain.RoleViewer)
	if !ok {
		return
	}
	if spec.Automation.TriggerKind == "kanban" {
		writeError(w, http.StatusNotFound, "not_found", "Automation not found")
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state != "" && !domain.ValidAutomationExecutionState(domain.AutomationExecutionState(state)) {
		writeError(w, http.StatusBadRequest, "bad_request", "state is not a valid Automation execution state")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 50")
			return
		}
		limit = value
	}
	before, err := decodeAutomationExecutionCursor(strings.TrimSpace(r.URL.Query().Get("before")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "before is not a valid Automation execution cursor")
		return
	}
	var beforeAt *time.Time
	beforeID := ""
	if before != nil {
		beforeAt, beforeID = &before.CreatedAt, before.ID
	}
	values, err := s.st.ListAutomationExecutions(r.Context(), spec.Automation.ID, state, beforeAt, beforeID, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list Automation executions")
		return
	}
	var nextCursor *string
	if len(values) > limit {
		values = values[:limit]
		cursor := encodeAutomationExecutionCursor(values[len(values)-1])
		nextCursor = &cursor
	}
	items := make([]automationExecutionView, 0, len(values))
	for i := range values {
		view, err := s.automationExecutionView(r, &values[i])
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not project Automation execution")
			return
		}
		items = append(items, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (s *Server) handleGetAutomationExecution(w http.ResponseWriter, r *http.Request) {
	spec, _, ok := s.loadPluginAutomationForMember(w, r, domain.RoleViewer)
	if !ok {
		return
	}
	if spec.Automation.TriggerKind == "kanban" {
		writeError(w, http.StatusNotFound, "not_found", "Automation not found")
		return
	}
	value, err := s.st.GetAutomationExecution(r.Context(), spec.Automation.ID, r.PathValue("eid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Automation execution not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load Automation execution")
		return
	}
	view, err := s.automationExecutionView(r, value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not project Automation execution")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type runAutomationNowRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) handleRunAutomationNow(w http.ResponseWriter, r *http.Request) {
	spec, svc, ok := s.loadPluginAutomationForMember(w, r, domain.RoleMember)
	if !ok {
		return
	}
	if spec.Automation.TriggerKind == "kanban" {
		writeError(w, http.StatusNotFound, "not_found", "Automation not found")
		return
	}
	var req runAutomationNowRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if len(key) < 8 || len(key) > 128 || strings.ContainsAny(key, " \t\r\n/\\") {
		writeError(w, http.StatusBadRequest, "bad_request", "idempotency_key must be 8-128 non-space characters")
		return
	}
	eventKey := "manual:" + key
	if existing, err := s.st.GetAutomationExecutionByEventKey(r.Context(), spec.Automation.ID, eventKey); err == nil {
		view, projectErr := s.automationExecutionView(r, existing)
		if projectErr != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not project Automation execution")
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "could not check Automation execution idempotency")
		return
	}

	now := time.Now().UTC()
	value := &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: spec.Automation.ID,
		AutomationName: spec.Automation.Name, PromptSnapshot: spec.Automation.PromptTemplate,
		ProjectID: svc.ProjectID, ServiceID: svc.ID, TriggerKind: "manual",
		EventKey: eventKey, State: domain.AutomationExecutionAccepted,
		OutputMode:       domain.AutomationOutputRunOnly,
		RequestedActor:   manualExecutionActor(principalFrom(r.Context())),
		AccountableActor: s.automationOwnerActor(r, spec.Automation.CreatedBy),
		CreatedAt:        now, UpdatedAt: now,
	}
	if spec.Cron != nil && spec.Cron.OutputMode != "" {
		value.OutputMode = spec.Cron.OutputMode
	}
	if value.OutputMode == domain.AutomationOutputCreateCard {
		value.CardPath = automationExecutionCardPath(spec.Automation.ID, value.ID)
		value.CardState = "planned"
		value.WritebackState = "pending"
		s.createManualAutomationExecution(w, r, value, nil)
		return
	}
	if spec.Automation.RunKind == domain.RunKindReview {
		value.State = domain.AutomationExecutionBlocked
		value.ReasonCode = "manual_review_requires_pull_request"
		value.ReasonMessage = "Run now cannot create a native review without a pull request."
		value.RepairRole = "project_owner"
		s.createManualAutomationExecution(w, r, value, nil)
		return
	}
	allowed, host, err := s.integrationHostStillAllowed(r.Context(), svc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not check the integration host policy")
		return
	}
	if !allowed {
		value.State = domain.AutomationExecutionBlocked
		value.ReasonCode = "host_not_allowed"
		value.ReasonMessage = "The Service repository host is no longer allowed: " + host
		value.RepairRole = "cluster_admin"
		s.createManualAutomationExecution(w, r, value, nil)
		return
	}
	sel, outcome, err := s.models.SelectModel(r.Context(), svc.ProjectID, deref(svc.DefaultModelID), spec.Automation.ModelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve Automation model")
		return
	}
	if outcome != modelcfg.SelectOK || !sel.SupportsEffort(spec.Automation.ModelEffort) {
		value.State = domain.AutomationExecutionBlocked
		value.ReasonCode = automationModelReasonCode(outcome, sel.SupportsEffort(spec.Automation.ModelEffort))
		if outcome == modelcfg.SelectOK {
			value.ReasonMessage = "The selected model no longer supports this Automation's reasoning effort."
		} else {
			value.ReasonMessage = pluginAutomationModelError(outcome, nil)
		}
		value.RepairRole = "project_owner"
		if outcome == modelcfg.SelectNotConfigured {
			value.RepairRole = "cluster_admin"
		}
		s.createManualAutomationExecution(w, r, value, nil)
		return
	}
	run := newQueuedRun(svc.ProjectID, svc.ID, spec.Automation.PromptTemplate, nil, principalFrom(r.Context()).userIDPtr())
	run.Origin = domain.RunOriginAPI
	run.OriginAutomationID = spec.Automation.ID
	run.OriginEventKey = eventKey
	run.ModelName = sel.ModelName
	run.ModelEffort = spec.Automation.ModelEffort
	if sel.ModelID != "" {
		modelID := sel.ModelID
		run.ModelID = &modelID
	}
	provenance.Stamp(r.Context(), s.st, run, nil)
	value.State = domain.AutomationExecutionQueued
	value.RunID = run.ID
	s.createManualAutomationExecution(w, r, value, run)
}

func (s *Server) createManualAutomationExecution(w http.ResponseWriter, r *http.Request, value *domain.AutomationExecution, run *domain.Run) {
	saved, created, err := s.st.CreateAutomationExecution(r.Context(), value, run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create Automation execution")
		return
	}
	if run != nil && created {
		s.emitStatus(r.Context(), run)
	}
	view, err := s.automationExecutionView(r, saved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not project Automation execution")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, view)
}

func (s *Server) automationExecutionView(r *http.Request, value *domain.AutomationExecution) (automationExecutionView, error) {
	view := automationExecutionView{
		ID: value.ID, AutomationID: value.AutomationID, AutomationName: value.AutomationName,
		TriggerKind: value.TriggerKind, State: value.State, Outcome: value.Outcome,
		OutputMode: value.OutputMode, ReasonCode: value.ReasonCode, Reason: value.ReasonMessage,
		RepairRole: value.RepairRole, ExternalURL: value.ExternalURL,
		WritebackState: value.WritebackState, CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt, TerminalAt: value.TerminalAt,
		Usage: map[string]string{"state": "unavailable"},
	}
	if value.RequestedActor.Kind != "" {
		actor := value.RequestedActor
		view.RequestedActor = &actor
	}
	if value.AccountableActor.Kind != "" {
		actor := value.AccountableActor
		view.AccountableActor = &actor
	}
	var run *domain.Run
	if value.RunID != "" {
		loaded, err := s.st.GetRun(r.Context(), value.RunID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return view, err
		}
		run = loaded
	}
	cardAvailable := value.CardState == "bound"
	if value.CardPath != "" {
		view.Card = &automationCardView{
			WorkspaceID: value.CardWorkspaceID, DocumentID: value.CardDocumentID,
			DocumentPath: value.CardPath, Available: cardAvailable,
			Href: "/projects/" + value.ProjectID + "?service=" + value.ServiceID +
				"&tab=automations&kanban=1&card=" + url.QueryEscape(value.CardPath),
		}
		if value.CardAutomationID != "" && value.CardWorkspaceID != "" {
			claim, err := s.st.GetPluginKanbanClaimByPath(
				r.Context(), value.CardAutomationID, value.CardWorkspaceID, value.CardPath,
			)
			if err == nil {
				view.Card.Available = claim.ExternalRefAvailable
				if !claim.ExternalRefAvailable {
					view.ReasonCode = "card_unavailable"
					view.Reason = "The jtype Card is no longer available."
					view.RepairRole = "project_owner"
					if run == nil {
						view.State = domain.AutomationExecutionBlocked
					}
				}
				if claim.LatestOccurrenceID != "" {
					occurrences, listErr := s.st.ListPluginKanbanOccurrences(r.Context(), value.CardAutomationID, claim.DocumentID, 1)
					if listErr != nil {
						return view, listErr
					}
					if len(occurrences) > 0 {
						occurrence := occurrences[0]
						view.WritebackState = occurrence.WritebackState
						if run == nil && occurrence.RunID != "" {
							loaded, loadErr := s.st.GetRun(r.Context(), occurrence.RunID)
							if loadErr != nil && !errors.Is(loadErr, store.ErrNotFound) {
								return view, loadErr
							}
							run = loaded
						}
					}
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				return view, err
			}
		}
		view.Output = automationOutputView{
			Kind: "card", Label: value.CardPath, Href: view.Card.Href, Available: view.Card.Available,
		}
	}
	if run != nil {
		projectAutomationRunState(&view, run)
		view.Run = &automationRunView{ID: run.ID, Status: run.Status, Href: "/runs/" + run.ID}
		view.Output = automationOutputView{
			Kind: "run", Label: "Run " + run.ID, Href: "/runs/" + run.ID, Available: true,
		}
		if value.OutputMode == domain.AutomationOutputCreateCard && view.Card != nil {
			view.Output = automationOutputView{
				Kind: "card", Label: value.CardPath, Href: view.Card.Href, Available: view.Card.Available,
			}
		}
	} else if value.RunID != "" {
		view.Output = automationOutputView{Kind: "run", Label: "Run unavailable", Available: false}
	}
	if view.Output.Kind == "" {
		view.Output = automationOutputView{Kind: "none", Label: "No output", Available: false}
	}
	return view, nil
}

func projectAutomationRunState(view *automationExecutionView, run *domain.Run) {
	switch {
	case run.Status == domain.StatusCanceled && run.Phase == "Superseded":
		view.State = domain.AutomationExecutionSuperseded
		view.Outcome = "canceled"
		view.TerminalAt = run.FinishedAt
	case run.Status.Terminal():
		view.State = domain.AutomationExecutionTerminal
		view.Outcome = string(run.Status)
		view.TerminalAt = run.FinishedAt
	case run.Status == domain.StatusRunning || run.Status == domain.StatusAwaitingInput || run.Status == domain.StatusScheduling:
		view.State = domain.AutomationExecutionRunning
	default:
		view.State = domain.AutomationExecutionQueued
	}
	view.UpdatedAt = run.CreatedAt
	if run.StartedAt != nil {
		view.UpdatedAt = *run.StartedAt
	}
	if run.FinishedAt != nil {
		view.UpdatedAt = *run.FinishedAt
	}
}

func encodeAutomationExecutionCursor(value domain.AutomationExecution) string {
	payload, _ := json.Marshal(automationExecutionCursor{CreatedAt: value.CreatedAt.UTC(), ID: value.ID})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeAutomationExecutionCursor(raw string) (*automationExecutionCursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var value automationExecutionCursor
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	if value.CreatedAt.IsZero() || value.ID == "" {
		return nil, errors.New("cursor fields are required")
	}
	return &value, nil
}

func manualExecutionActor(principal *principal) domain.ProvenanceActorRef {
	if principal != nil && principal.user != nil {
		label := principal.user.DisplayName
		if label == "" {
			label = principal.user.ID
		}
		return domain.ProvenanceActorRef{Kind: "cloud_user", ID: principal.user.ID, Label: label}
	}
	if principal != nil && principal.isAPIKey() {
		return domain.ProvenanceActorRef{Kind: "service_principal", Label: "Project API key"}
	}
	return domain.ProvenanceActorRef{Kind: "service_principal", Label: "Cloud service principal"}
}

func (s *Server) automationOwnerActor(r *http.Request, userID string) domain.ProvenanceActorRef {
	if userID == "" {
		return domain.ProvenanceActorRef{Kind: "automation", Label: "Automation rule"}
	}
	user, err := s.st.GetUser(r.Context(), userID)
	if err != nil {
		return domain.ProvenanceActorRef{Kind: "cloud_user", ID: userID, Label: "Former member"}
	}
	label := user.DisplayName
	if label == "" {
		label = user.ID
	}
	return domain.ProvenanceActorRef{Kind: "cloud_user", ID: user.ID, Label: label}
}

func automationExecutionCardPath(automationID, executionID string) string {
	return "jcode-automation/" + automationID + "/" + executionID + ".md"
}

func automationModelReasonCode(outcome modelcfg.SelectOutcome, effortSupported bool) string {
	if !effortSupported {
		return "model_effort_unsupported"
	}
	switch outcome {
	case modelcfg.SelectNotConfigured:
		return "model_not_configured"
	case modelcfg.SelectNotSelected:
		return "model_not_selected"
	case modelcfg.SelectNotGranted:
		return "model_not_granted"
	default:
		return "model_unavailable"
	}
}
