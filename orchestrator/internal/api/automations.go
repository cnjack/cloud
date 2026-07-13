package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

type reviewWebhookRegistrar interface {
	EnsureReviewWebhook(ctx context.Context, owner, repo, hookURL, secret string) error
}

type automationListView struct {
	Automations    []domain.Automation    `json:"automations"`
	WebhookBinding *domain.WebhookBinding `json:"webhook_binding"`
}

type createAutomationReq struct {
	Name          string   `json:"name"`
	Instructions  string   `json:"instructions"`
	TriggerType   string   `json:"trigger_type"`
	ModelID       string   `json:"model_id"`
	Events        []string `json:"events"`
	BaseBranch    string   `json:"base_branch"`
	IncludeDrafts bool     `json:"include_drafts"`
	Enabled       *bool    `json:"enabled"`
}

type patchAutomationReq struct {
	Name          *string   `json:"name"`
	Instructions  *string   `json:"instructions"`
	ModelID       *string   `json:"model_id"`
	Events        *[]string `json:"events"`
	BaseBranch    *string   `json:"base_branch"`
	IncludeDrafts *bool     `json:"include_drafts"`
	Enabled       *bool     `json:"enabled"`
}

type automationHTTPError struct {
	status int
	code   string
	msg    string
}

func (s *Server) handleListServiceAutomations(w http.ResponseWriter, r *http.Request) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleMember) {
		return
	}
	automations, err := s.st.ListAutomationsByService(r.Context(), svc.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list automations")
		return
	}
	if automations == nil {
		automations = []domain.Automation{}
	}
	binding, err := s.st.GetWebhookBinding(r.Context(), svc.ID)
	if errors.Is(err, store.ErrNotFound) {
		binding = nil
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load webhook state")
		return
	}
	writeJSON(w, http.StatusOK, automationListView{Automations: automations, WebhookBinding: binding})
}

func (s *Server) handleCreateServiceAutomation(w http.ResponseWriter, r *http.Request) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleOwner) {
		return
	}
	var req createAutomationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	a, aerr := automationFromCreate(req, svc)
	if aerr != nil {
		writeError(w, aerr.status, aerr.code, aerr.msg)
		return
	}
	if merr := s.validateAutomationModel(r.Context(), svc, a.ModelID); merr != nil {
		writeError(w, merr.status, merr.code, merr.msg)
		return
	}
	if herr := s.synchronizeReviewWebhook(r.Context(), svc, principalFrom(r.Context()).userID()); herr != nil {
		writeError(w, herr.status, herr.code, herr.msg)
		return
	}
	now := time.Now().UTC()
	a.ID = domain.NewID()
	a.ServiceID = svc.ID
	a.CreatedAt = now
	a.UpdatedAt = now
	if userID := principalFrom(r.Context()).userID(); userID != "" {
		a.CreatedBy = &userID
	}
	if err := s.st.CreateAutomation(r.Context(), a); err != nil {
		s.log.Error("create automation", "service", svc.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create automation")
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func automationFromCreate(req createAutomationReq, svc *domain.Service) (*domain.Automation, *automationHTTPError) {
	name := strings.TrimSpace(req.Name)
	instructions := strings.TrimSpace(req.Instructions)
	trigger := domain.AutomationTrigger(strings.TrimSpace(req.TriggerType))
	modelID := strings.TrimSpace(req.ModelID)
	baseBranch := strings.TrimSpace(req.BaseBranch)
	if baseBranch == "" && svc != nil {
		baseBranch = svc.DefaultBranch
	}
	if name == "" || instructions == "" || modelID == "" || baseBranch == "" {
		return nil, &automationHTTPError{http.StatusBadRequest, "bad_request", "name, instructions, model_id and base_branch are required"}
	}
	if !domain.ValidAutomationTrigger(trigger) {
		return nil, &automationHTTPError{http.StatusBadRequest, "bad_request", "trigger_type must be 'pr_review'"}
	}
	events, err := normalizeAutomationEvents(req.Events)
	if err != nil {
		return nil, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return &domain.Automation{
		Name: name, Instructions: instructions, TriggerType: trigger, ModelID: modelID,
		Events: events, BaseBranch: baseBranch, IncludeDrafts: req.IncludeDrafts, Enabled: enabled,
	}, nil
}

func normalizeAutomationEvents(raw []string) ([]domain.AutomationEvent, *automationHTTPError) {
	if len(raw) == 0 {
		return nil, &automationHTTPError{http.StatusBadRequest, "bad_request", "at least one PR event is required"}
	}
	seen := map[domain.AutomationEvent]bool{}
	out := make([]domain.AutomationEvent, 0, len(raw))
	for _, value := range raw {
		event := domain.AutomationEvent(strings.ToLower(strings.TrimSpace(value)))
		if !domain.ValidAutomationEvent(event) {
			return nil, &automationHTTPError{http.StatusBadRequest, "bad_request", "events may contain opened, ready, synchronize or reopened"}
		}
		if !seen[event] {
			seen[event] = true
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *Server) validateAutomationModel(ctx context.Context, svc *domain.Service, modelID string) *automationHTTPError {
	sel, outcome, err := s.models.SelectModel(ctx, svc.ProjectID, deref(svc.DefaultModelID), modelID)
	if err != nil {
		return &automationHTTPError{http.StatusInternalServerError, "internal", "could not validate the automation model"}
	}
	if outcome == modelcfg.SelectOK && sel.ModelID == modelID {
		return nil
	}
	switch outcome {
	case modelcfg.SelectNotGranted:
		return &automationHTTPError{http.StatusBadRequest, "model_not_granted", "that model is not authorized for this project"}
	case modelcfg.SelectNotConfigured:
		return &automationHTTPError{http.StatusConflict, "model_not_configured", modelcfg.NotConfiguredMessage(s.cfg.ConsoleURL)}
	default:
		return &automationHTTPError{http.StatusConflict, "model_not_selected", modelcfg.NotSelectedMessage()}
	}
}

func (s *Server) synchronizeReviewWebhook(ctx context.Context, svc *domain.Service, userID string) *automationHTTPError {
	if svc.RepoKind != domain.RepoKindProvider || svc.Provider != domain.ProviderGitea {
		return &automationHTTPError{http.StatusConflict, "automatic_review_unsupported", "Automatic PR event review is currently available for Gitea provider repositories only."}
	}
	if strings.TrimSpace(s.cfg.WebhookURL) == "" || strings.TrimSpace(s.cfg.WebhookSecret) == "" {
		return &automationHTTPError{http.StatusConflict, "webhook_not_configured", "This cluster has not configured a webhook receiver. Contact a cluster administrator."}
	}
	if _, configured := s.oauth[svc.Provider]; !configured {
		return &automationHTTPError{http.StatusConflict, "oauth_not_configured", "OAuth is not configured for Gitea. Contact a cluster administrator."}
	}
	if userID == "" || s.creds == nil || s.factory == nil {
		return &automationHTTPError{http.StatusConflict, "oauth_not_connected", "Connect your Gitea account with OAuth before creating this Automation."}
	}
	token, err := s.creds.ResolveUserOAuth(ctx, svc.Provider, userID)
	if err != nil {
		if errors.Is(err, credentials.ErrNoCredential) {
			return &automationHTTPError{http.StatusConflict, "oauth_not_connected", "Connect your Gitea account with OAuth before creating this Automation."}
		}
		return &automationHTTPError{http.StatusBadGateway, "oauth_unavailable", "Could not use your Gitea OAuth connection. Reconnect it and try again."}
	}
	owner, repo, ok := provider.SplitRepo(svc.RepoOwnerName)
	if !ok {
		return &automationHTTPError{http.StatusConflict, "provider_webhook_unavailable", "This service has no valid provider repository name."}
	}
	hookURL := webhookURLForProvider(s.cfg.WebhookURL, svc.Provider)
	client, err := s.factory.PRClient(svc.Provider, token.Value, token.Scheme)
	if err != nil {
		return &automationHTTPError{http.StatusConflict, "provider_webhook_unavailable", "This provider webhook cannot be configured in the current cluster."}
	}
	hooker, ok := client.(reviewWebhookRegistrar)
	if !ok {
		return &automationHTTPError{http.StatusConflict, "automatic_review_unsupported", "This Gitea client cannot subscribe to PR lifecycle events."}
	}
	now := time.Now().UTC()
	if err := hooker.EnsureReviewWebhook(ctx, owner, repo, hookURL, s.cfg.WebhookSecret); err != nil {
		_ = s.st.UpsertWebhookBinding(ctx, &domain.WebhookBinding{
			ServiceID: svc.ID, Provider: svc.Provider, Endpoint: hookURL,
			Status: domain.WebhookBindingError, LastError: "Gitea rejected webhook synchronization.", UpdatedAt: now,
		})
		return &automationHTTPError{http.StatusBadGateway, "webhook_registration_failed", "Gitea rejected webhook synchronization. Reconnect OAuth with repository-hook access and confirm you are a repository administrator."}
	}
	if err := s.st.UpsertWebhookBinding(ctx, &domain.WebhookBinding{
		ServiceID: svc.ID, Provider: svc.Provider, Endpoint: hookURL,
		Status: domain.WebhookBindingActive, LastSyncedAt: &now, UpdatedAt: now,
	}); err != nil {
		return &automationHTTPError{http.StatusInternalServerError, "internal", "Webhook was synchronized, but its state could not be saved."}
	}
	return nil
}

func (s *Server) handleUpdateAutomation(w http.ResponseWriter, r *http.Request) {
	a, err := s.st.GetAutomation(r.Context(), r.PathValue("aid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "automation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load automation")
		return
	}
	svc, err := s.st.GetService(r.Context(), a.ServiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load automation service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleOwner) {
		return
	}
	var req patchAutomationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Name != nil {
		a.Name = strings.TrimSpace(*req.Name)
	}
	if req.Instructions != nil {
		a.Instructions = strings.TrimSpace(*req.Instructions)
	}
	if req.ModelID != nil {
		a.ModelID = strings.TrimSpace(*req.ModelID)
	}
	if req.Events != nil {
		events, eventErr := normalizeAutomationEvents(*req.Events)
		if eventErr != nil {
			writeError(w, eventErr.status, eventErr.code, eventErr.msg)
			return
		}
		a.Events = events
	}
	if req.BaseBranch != nil {
		a.BaseBranch = strings.TrimSpace(*req.BaseBranch)
	}
	if req.IncludeDrafts != nil {
		a.IncludeDrafts = *req.IncludeDrafts
	}
	if req.Enabled != nil {
		a.Enabled = *req.Enabled
	}
	if a.Name == "" || a.Instructions == "" || a.ModelID == "" || a.BaseBranch == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name, instructions, model_id and base_branch are required")
		return
	}
	validateRuntime := a.Enabled || req.ModelID != nil || req.Events != nil || req.BaseBranch != nil || req.IncludeDrafts != nil
	if validateRuntime {
		if merr := s.validateAutomationModel(r.Context(), svc, a.ModelID); merr != nil {
			writeError(w, merr.status, merr.code, merr.msg)
			return
		}
	}
	if a.Enabled {
		if herr := s.synchronizeReviewWebhook(r.Context(), svc, principalFrom(r.Context()).userID()); herr != nil {
			writeError(w, herr.status, herr.code, herr.msg)
			return
		}
	}
	if err := s.st.UpdateAutomation(r.Context(), a); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update automation")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleDeleteAutomation(w http.ResponseWriter, r *http.Request) {
	a, err := s.st.GetAutomation(r.Context(), r.PathValue("aid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "automation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load automation")
		return
	}
	svc, err := s.st.GetService(r.Context(), a.ServiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load automation service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleOwner) {
		return
	}
	if err := s.st.DeleteAutomation(r.Context(), a.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not delete automation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
