package api

// This file is the clean-cut Automation API for the Project Plugin platform.
// The older service automation resource deliberately has no route registration:
// migration 0043 discarded its data and its model/PR-review-only contract.

import (
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/schedule"
	"github.com/cnjack/jcloud/internal/scmevent"
	"github.com/cnjack/jcloud/internal/store"
)

func (s *Server) handleProviderCapabilities(w http.ResponseWriter, r *http.Request) {
	provider := scmevent.ProviderKind(strings.TrimSpace(r.PathValue("provider")))
	if !provider.Valid() {
		writeError(w, http.StatusBadRequest, "bad_provider", "unsupported provider")
		return
	}
	capabilities := scmevent.Capabilities(provider)
	instanceURL := ""
	if cfg, err := s.st.GetProviderConfig(r.Context(), domain.ProviderKind(provider)); err == nil {
		instanceURL = cfg.BaseURL
		capabilities = providerCapabilitiesForConfig(provider, cfg)
	}
	if provider == scmevent.ProviderGitHub && instanceURL == "" {
		instanceURL = "https://github.com"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":        capabilities.Provider,
		"minimum_version": capabilities.MinimumVersion,
		"capabilities":    capabilities.Capabilities,
		"instance_url":    instanceURL,
		"oauth_scopes":    canonicalPluginConsentScopes(domain.ProviderKind(provider)),
	})
}

func providerCapabilitiesForConfig(provider scmevent.ProviderKind, cfg *domain.ProviderConfig) scmevent.ProviderCapabilities {
	capabilities := scmevent.Capabilities(provider)
	if cfg == nil || !cfg.PluginEnabled || cfg.LastHealthError != "" {
		capabilities.Capabilities = []scmevent.Capability{}
		return capabilities
	}
	if cfg.LastCapabilityCheck != nil {
		return scmevent.CapabilitiesForVersion(provider, cfg.CapabilityVersion)
	}
	if provider == scmevent.ProviderGitLab || provider == scmevent.ProviderGitea {
		// Self-hosted event support is version-dependent. Until a real
		// instance/grant probe succeeds, showing the optimistic catalog would
		// allow an Automation that can never fire.
		capabilities.Capabilities = []scmevent.Capability{}
	}
	return capabilities
}

type pluginAutomationReq struct {
	ServiceID      string           `json:"service_id"`
	Name           string           `json:"name"`
	PromptTemplate string           `json:"prompt_template"`
	Enabled        *bool            `json:"enabled"`
	IgnoreJCode    *bool            `json:"ignore_jcode"`
	SCM            *pluginSCMReq    `json:"scm"`
	Kanban         *pluginKanbanReq `json:"kanban"`
	Cron           *pluginCronReq   `json:"cron"`
}
type pluginSCMReq struct {
	Branch      string               `json:"branch"`
	PathPattern string               `json:"path_pattern"`
	Conclusion  string               `json:"conclusion"`
	Actions     []pluginSCMActionReq `json:"actions"`
}
type pluginSCMActionReq struct {
	EventFamily string `json:"event_family"`
	Action      string `json:"action"`
}
type pluginKanbanReq struct {
	InstallationID string `json:"installation_id"`
	BoardRef       string `json:"board_ref"`
	TriggerColumn  string `json:"trigger_column"`
	DoneColumn     string `json:"done_column"`
}
type pluginCronReq struct {
	CronExpr string `json:"cron_expr"`
}

func (s *Server) handleListPluginAutomations(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleViewer) {
		return
	}
	items, err := s.st.ListPluginAutomationsByProject(r.Context(), projectID)
	if err != nil {
		writeError(w, 500, "internal", "could not list Automations")
		return
	}
	out := make([]domain.PluginAutomationSpec, 0, len(items))
	for _, a := range items {
		if a.TriggerKind == "kanban" {
			continue // Kanban is surfaced as a Service capability, not an Automation.
		}
		spec, err := s.st.GetPluginAutomationSpec(r.Context(), a.ID)
		if err != nil {
			writeError(w, 500, "internal", "could not load Automation trigger")
			return
		}
		out = append(out, *spec)
	}
	writeJSON(w, 200, map[string]any{"automations": out})
}

func (s *Server) handleCreatePluginAutomation(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleMember) {
		return
	}
	var req pluginAutomationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Kanban != nil {
		writeError(w, 400, "bad_request", "Kanban is enabled from the Service header, not created as an Automation")
		return
	}
	a, scm, actions, kanban, cron, err := pluginAutomationFromReq(req, "")
	if err != "" {
		writeError(w, 400, "bad_request", err)
		return
	}
	svc, loadErr := s.st.GetService(r.Context(), a.ServiceID)
	if errors.Is(loadErr, store.ErrNotFound) || svc.ProjectID != projectID {
		writeError(w, 404, "not_found", "service not found")
		return
	}
	if loadErr != nil {
		writeError(w, 500, "internal", "could not load service")
		return
	}
	installationID, msg := s.validatePluginAutomationTarget(r, svc, scm, actions, kanban)
	if msg != "" {
		writeError(w, 409, "plugin_unavailable", msg)
		return
	}
	a.InstallationID = installationID
	now := time.Now().UTC()
	a.ID = domain.NewID()
	a.CreatedAt = now
	a.CreatedBy = principalFrom(r.Context()).userID()
	if scm != nil {
		scm.AutomationID = a.ID
		for i := range actions {
			actions[i].AutomationID = a.ID
			actions[i].ServiceID = a.ServiceID
		}
	}
	if kanban != nil {
		kanban.AutomationID = a.ID
	}
	if cron != nil {
		cron.AutomationID = a.ID
	}
	if err := s.st.CreatePluginAutomation(r.Context(), a, scm, actions, kanban, cron); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, 409, "automation_overlap", "another Automation already owns one of the selected SCM actions")
			return
		}
		writeError(w, 500, "internal", "could not create Automation")
		return
	}
	if a.TriggerKind == "scm" && a.Enabled {
		if err := s.reconcilePluginSCMWebhook(r.Context(), svc); err != nil {
			writeError(w, http.StatusBadGateway, "webhook_reconcile_failed", pluginWebhookLifecycleError)
			return
		}
	}
	spec, getErr := s.st.GetPluginAutomationSpec(r.Context(), a.ID)
	if getErr != nil {
		writeError(w, 500, "internal", "could not load created Automation")
		return
	}
	writeJSON(w, 201, spec)
}

func (s *Server) handleGetPluginAutomation(w http.ResponseWriter, r *http.Request) {
	spec, svc, ok := s.loadPluginAutomationForMember(w, r, domain.RoleViewer)
	if !ok {
		return
	}
	if spec.Automation.TriggerKind == "kanban" {
		writeError(w, 404, "not_found", "Automation not found")
		return
	}
	_ = svc
	writeJSON(w, 200, spec)
}

func (s *Server) handleUpdatePluginAutomation(w http.ResponseWriter, r *http.Request) {
	spec, svc, ok := s.loadPluginAutomationForMember(w, r, domain.RoleMember)
	if !ok {
		return
	}
	if spec.Automation.TriggerKind == "kanban" {
		writeError(w, 404, "not_found", "Automation not found")
		return
	}
	var req pluginAutomationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.ServiceID != "" && req.ServiceID != svc.ID {
		writeError(w, 400, "bad_request", "service_id cannot be changed")
		return
	}
	// PATCH uses presence of a typed trigger section to replace it; otherwise it
	// retains the existing validated trigger. Aggregate fields are patch-like.
	if req.Name == "" {
		req.Name = spec.Automation.Name
	}
	if req.PromptTemplate == "" {
		req.PromptTemplate = spec.Automation.PromptTemplate
	}
	if req.Enabled == nil {
		v := spec.Automation.Enabled
		req.Enabled = &v
	}
	if req.IgnoreJCode == nil {
		v := spec.Automation.IgnoreJCode
		req.IgnoreJCode = &v
	}
	req.ServiceID = svc.ID
	if req.SCM == nil && req.Kanban == nil && req.Cron == nil {
		req.SCM = specToSCM(spec)
		req.Kanban = specToKanban(spec)
		req.Cron = specToCron(spec)
	}
	a, scm, actions, kanban, cron, msg := pluginAutomationFromReq(req, spec.Automation.ID)
	if msg != "" {
		writeError(w, 400, "bad_request", msg)
		return
	}
	a.CreatedAt = spec.Automation.CreatedAt
	a.CreatedBy = spec.Automation.CreatedBy
	a.LastError = spec.Automation.LastError
	installationID, msg := s.validatePluginAutomationTarget(r, svc, scm, actions, kanban)
	if msg != "" {
		writeError(w, 409, "plugin_unavailable", msg)
		return
	}
	a.InstallationID = installationID
	if scm != nil {
		scm.AutomationID = a.ID
		for i := range actions {
			actions[i].AutomationID = a.ID
			actions[i].ServiceID = svc.ID
		}
	}
	if kanban != nil {
		kanban.AutomationID = a.ID
	}
	if cron != nil {
		cron.AutomationID = a.ID
	}
	if err := s.st.ReplacePluginAutomationSpec(r.Context(), a, scm, actions, kanban, cron); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, 409, "automation_overlap", "another Automation already owns one of the selected SCM actions")
			return
		}
		writeError(w, 500, "internal", "could not update Automation")
		return
	}
	if spec.Automation.TriggerKind == "scm" || a.TriggerKind == "scm" {
		if err := s.reconcilePluginSCMWebhook(r.Context(), svc); err != nil {
			writeError(w, http.StatusBadGateway, "webhook_reconcile_failed", pluginWebhookLifecycleError)
			return
		}
	}
	updated, err := s.st.GetPluginAutomationSpec(r.Context(), a.ID)
	if err != nil {
		writeError(w, 500, "internal", "could not load updated Automation")
		return
	}
	writeJSON(w, 200, updated)
}

func (s *Server) handleDeletePluginAutomation(w http.ResponseWriter, r *http.Request) {
	spec, svc, ok := s.loadPluginAutomationForMember(w, r, domain.RoleMember)
	if !ok {
		return
	}
	if spec.Automation.TriggerKind == "kanban" {
		writeError(w, 404, "not_found", "Automation not found")
		return
	}
	if err := s.st.DeletePluginAutomation(r.Context(), r.PathValue("aid")); err != nil {
		writeError(w, 500, "internal", "could not delete Automation")
		return
	}
	if spec.Automation.TriggerKind == "scm" && spec.Automation.Enabled {
		if err := s.reconcilePluginSCMWebhook(r.Context(), svc); err != nil {
			writeError(w, http.StatusBadGateway, "webhook_reconcile_failed", pluginWebhookLifecycleError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) loadPluginAutomationForMember(w http.ResponseWriter, r *http.Request, role domain.Role) (*domain.PluginAutomationSpec, *domain.Service, bool) {
	spec, err := s.st.GetPluginAutomationSpec(r.Context(), r.PathValue("aid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "Automation not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, 500, "internal", "could not load Automation")
		return nil, nil, false
	}
	svc, err := s.st.GetService(r.Context(), spec.Automation.ServiceID)
	if err != nil {
		writeError(w, 500, "internal", "could not load Automation service")
		return nil, nil, false
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, role) {
		return nil, nil, false
	}
	return spec, svc, true
}

func pluginAutomationFromReq(req pluginAutomationReq, id string) (*domain.PluginAutomation, *domain.SCMTrigger, []domain.SCMAction, *domain.KanbanTrigger, *domain.CronTrigger, string) {
	name := strings.TrimSpace(req.Name)
	prompt := strings.TrimSpace(req.PromptTemplate)
	if strings.TrimSpace(req.ServiceID) == "" || name == "" || prompt == "" {
		return nil, nil, nil, nil, nil, "service_id, name and prompt_template are required"
	}
	count := 0
	if req.SCM != nil {
		count++
	}
	if req.Kanban != nil {
		count++
	}
	if req.Cron != nil {
		count++
	}
	if count != 1 {
		return nil, nil, nil, nil, nil, "exactly one of scm, kanban or cron is required"
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ignore := true
	if req.IgnoreJCode != nil {
		ignore = *req.IgnoreJCode
	}
	a := &domain.PluginAutomation{ID: id, ServiceID: strings.TrimSpace(req.ServiceID), Name: name, PromptTemplate: prompt, Enabled: enabled, IgnoreJCode: ignore}
	if req.SCM != nil {
		a.TriggerKind = "scm"
		x := req.SCM
		scm := &domain.SCMTrigger{Branch: strings.TrimSpace(x.Branch), PathPattern: strings.TrimSpace(x.PathPattern), Conclusion: strings.TrimSpace(x.Conclusion)}
		if len(x.Actions) == 0 {
			return nil, nil, nil, nil, nil, "scm.actions is required"
		}
		actions := make([]domain.SCMAction, 0, len(x.Actions))
		seen := map[string]bool{}
		for _, in := range x.Actions {
			family := scmevent.Family(strings.TrimSpace(in.EventFamily))
			action := scmevent.Action(strings.TrimSpace(in.Action))
			if !scmevent.ValidAction(family, action) {
				return nil, nil, nil, nil, nil, "unsupported scm event_family/action"
			}
			key := string(family) + "|" + string(action)
			if seen[key] {
				return nil, nil, nil, nil, nil, "scm.actions contains a duplicate action"
			}
			seen[key] = true
			actions = append(actions, domain.SCMAction{EventFamily: string(family), Action: string(action)})
		}
		if scm.PathPattern != "" {
			for _, action := range actions {
				if action.EventFamily != string(scmevent.FamilyPush) || action.Action != string(scmevent.ActionUpdated) {
					return nil, nil, nil, nil, nil, "scm.path_pattern is supported only for push.updated actions"
				}
			}
		}
		if scm.Conclusion != "" {
			for _, action := range actions {
				if action.EventFamily != string(scmevent.FamilyCheck) || action.Action != string(scmevent.ActionCompleted) {
					return nil, nil, nil, nil, nil, "scm.conclusion is supported only for check.completed actions"
				}
			}
		}
		if scm.Branch != "" {
			if _, err := path.Match(scm.Branch, ""); err != nil {
				return nil, nil, nil, nil, nil, "scm.branch contains an invalid glob pattern"
			}
			for _, action := range actions {
				switch action.EventFamily {
				case string(scmevent.FamilyPush), string(scmevent.FamilyPullRequest), string(scmevent.FamilyCheck):
				// These event families carry a branch or base branch.
				default:
					return nil, nil, nil, nil, nil, "scm.branch is supported only for push, pull_request, and check actions"
				}
			}
		}
		return a, scm, actions, nil, nil, ""
	}
	if req.Kanban != nil {
		a.TriggerKind = "kanban"
		x := req.Kanban
		k := &domain.KanbanTrigger{InstallationID: strings.TrimSpace(x.InstallationID), BoardRef: strings.TrimSpace(x.BoardRef), TriggerColumn: strings.TrimSpace(x.TriggerColumn), DoneColumn: strings.TrimSpace(x.DoneColumn)}
		if k.InstallationID == "" || k.BoardRef == "" || k.TriggerColumn == "" {
			return nil, nil, nil, nil, nil, "kanban installation_id, board_ref and trigger_column are required"
		}
		return a, nil, nil, k, nil, ""
	}
	a.TriggerKind = "cron"
	expr := strings.TrimSpace(req.Cron.CronExpr)
	if expr == "" {
		return nil, nil, nil, nil, nil, "cron.cron_expr is required"
	}
	if _, err := schedule.ParseCron(expr); err != nil {
		return nil, nil, nil, nil, nil, "cron.cron_expr is invalid: " + err.Error()
	}
	return a, nil, nil, nil, &domain.CronTrigger{CronExpr: expr}, ""
}

func (s *Server) validatePluginAutomationTarget(r *http.Request, svc *domain.Service, scm *domain.SCMTrigger, actions []domain.SCMAction, kanban *domain.KanbanTrigger) (string, string) {
	if scm != nil {
		b, err := s.st.GetServiceRepositoryBinding(r.Context(), svc.ID)
		if err != nil {
			return "", "this Service is not bound to an enabled SCM Plugin"
		}
		in, err := s.st.GetPluginInstallation(r.Context(), b.InstallationID)
		if err != nil || in.Status != domain.PluginStatusEnabled || scmevent.ProviderKind(in.Provider) != scmevent.ProviderKind(svc.Provider) {
			return "", "the SCM Plugin must be enabled before this Automation can run"
		}
		cfg, err := s.st.GetProviderConfig(r.Context(), in.Provider)
		if err != nil || cfg.ConfigRevision != in.ConfigRevision {
			return "", "the SCM Plugin must be reconnected to the current cluster Provider configuration"
		}
		capabilities := providerCapabilitiesForConfig(scmevent.ProviderKind(in.Provider), cfg)
		for _, action := range actions {
			family := scmevent.Family(action.EventFamily)
			providerAction := scmevent.Action(action.Action)
			if !capabilities.Supports(family, providerAction) {
				return "", "the selected SCM action is not supported by this Provider and its observed version"
			}
		}
		return in.ID, ""
	}
	if kanban != nil {
		in, err := s.st.GetPluginInstallation(r.Context(), kanban.InstallationID)
		if err != nil || in.ProjectID != svc.ProjectID || in.Provider != domain.PluginJType || in.Status != domain.PluginStatusEnabled {
			return "", "the selected JType Plugin must be enabled for this Project"
		}
		return in.ID, ""
	}
	return "", ""
}
func specToSCM(s *domain.PluginAutomationSpec) *pluginSCMReq {
	if s.SCM == nil {
		return nil
	}
	v := &pluginSCMReq{Branch: s.SCM.Branch, PathPattern: s.SCM.PathPattern, Conclusion: s.SCM.Conclusion}
	for _, x := range s.Actions {
		v.Actions = append(v.Actions, pluginSCMActionReq{EventFamily: x.EventFamily, Action: x.Action})
	}
	return v
}
func specToKanban(s *domain.PluginAutomationSpec) *pluginKanbanReq {
	if s.Kanban == nil {
		return nil
	}
	return &pluginKanbanReq{InstallationID: s.Kanban.InstallationID, BoardRef: s.Kanban.BoardRef, TriggerColumn: s.Kanban.TriggerColumn, DoneColumn: s.Kanban.DoneColumn}
}
func specToCron(s *domain.PluginAutomationSpec) *pluginCronReq {
	if s.Cron == nil {
		return nil
	}
	return &pluginCronReq{CronExpr: s.Cron.CronExpr}
}
