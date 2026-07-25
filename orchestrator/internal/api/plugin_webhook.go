package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/scmevent"
	"github.com/cnjack/jcloud/internal/store"
)

// handlePluginWebhook is the only SCM ingress used by the Plugin platform. It
// authenticates with the DB-backed Provider config, immediately normalizes the
// payload, and never persists or logs the raw body.
func (s *Server) handlePluginWebhook(w http.ResponseWriter, r *http.Request) {
	provider, eventType, deliveryID := pluginWebhookHeaders(r)
	if !provider.Valid() {
		writeError(w, http.StatusNotFound, "not_found", "unknown webhook provider")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), domain.ProviderKind(provider))
	if errors.Is(err, store.ErrNotFound) || !cfg.PluginEnabled {
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider webhook is not configured")
		return
	}
	if err != nil || s.cipher == nil || len(cfg.WebhookSecretEnc) == 0 {
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider webhook secret is unavailable")
		return
	}
	rawSecret, err := s.cipher.Decrypt(cfg.WebhookSecretEnc)
	if err != nil {
		s.log.Error("decrypt provider webhook secret", "provider", provider)
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider webhook secret is unavailable")
		return
	}
	body, ok := readWebhookBody(w, r)
	if !ok {
		return
	}
	secret := string(rawSecret)
	authenticated := false
	switch provider {
	case scmevent.ProviderGitHub:
		authenticated = validGitHubSignature(secret, body, r.Header.Get("X-Hub-Signature-256"))
	case scmevent.ProviderGitea:
		authenticated = validGiteaSignature(secret, body, r.Header.Get("X-Gitea-Signature"))
	case scmevent.ProviderGitLab:
		authenticated = validGitLabToken(secret, r.Header.Get("X-Gitlab-Token"))
	}
	if !authenticated {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
		return
	}
	if deliveryID == "" {
		writeError(w, http.StatusBadRequest, "missing_delivery_id", "provider delivery id is required")
		return
	}

	now := time.Now().UTC()
	event, err := scmevent.Normalize(provider, eventType, deliveryID, body, now)
	// Do not retain the provider payload beyond normalization/authentication.
	body = nil
	if errors.Is(err, scmevent.ErrIgnored) || errors.Is(err, scmevent.ErrUnsupported) {
		writeWebhookOK(w, "ignored")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_event", "webhook event could not be normalized")
		return
	}
	receipt := &domain.WebhookReceipt{
		ID:              domain.NewID(),
		Provider:        domain.ProviderKind(provider),
		DeliveryID:      event.DeliveryID,
		EventFamily:     string(event.Family),
		Action:          string(event.Action),
		ExternalActorID: event.Actor.ID,
		ExternalActor:   event.Actor.Login,
		ObjectRef:       normalizedObjectRef(event),
		Status:          "received",
		ReceivedAt:      now,
	}
	claimed, err := s.st.ClaimWebhookReceipt(r.Context(), receipt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "receipt_failed", "could not record webhook delivery")
		return
	}
	if !claimed {
		writeWebhookOK(w, "duplicate")
		return
	}

	automations, err := s.st.ListPluginAutomationsForEvent(
		r.Context(), domain.ProviderKind(provider), event.Repository.ID,
		string(event.Family), string(event.Action),
	)
	if err != nil {
		s.completePluginReceipt(r, receipt, "error", "", "could not match Automation")
		writeError(w, http.StatusInternalServerError, "match_failed", "could not match webhook Automation")
		return
	}
	dispatched := 0
	for i := range automations {
		a := &automations[i]
		spec, specErr := s.st.GetPluginAutomationSpec(r.Context(), a.ID)
		if specErr != nil || spec.SCM == nil {
			s.recordPluginAutomationError(r, a, "Automation trigger configuration is unavailable.")
			continue
		}
		if a.IgnoreJCode && event.GeneratedByJCode {
			continue
		}
		filter := scmevent.Filter{
			Branch:      spec.SCM.Branch,
			Conclusions: splitFilterValues(spec.SCM.Conclusion),
		}
		if spec.SCM.PathPattern != "" {
			filter.IncludePaths = splitFilterValues(spec.SCM.PathPattern)
		}
		if !filter.Matches(event, event.ChangedPaths) {
			continue
		}
		svc, svcErr := s.st.GetService(r.Context(), a.ServiceID)
		if svcErr != nil || svc.DeletingAt != nil {
			s.recordPluginAutomationError(r, a, "Automation Service is unavailable.")
			continue
		}
		binding, bindErr := s.st.GetServiceRepositoryBinding(r.Context(), svc.ID)
		if bindErr != nil {
			s.recordPluginAutomationError(r, a, "Automation Service repository binding is unavailable.")
			continue
		}
		if receipt.InstallationID == "" {
			receipt.InstallationID = binding.InstallationID
		}
		sel, outcome, selectErr := s.models.SelectModel(r.Context(), svc.ProjectID, deref(svc.DefaultModelID), "")
		if selectErr != nil || outcome != modelcfg.SelectOK {
			s.recordPluginAutomationError(r, a, "Automation model is unavailable.")
			continue
		}
		prompt := renderPluginPrompt(a.PromptTemplate, event)
		if event.Family == scmevent.FamilyComment {
			prompt = event.Body
		}
		var triggeredBy *string
		if identity, identityErr := s.st.GetIdentity(r.Context(), domain.GitProvider(provider), event.Actor.ID); identityErr == nil {
			id := identity.UserID
			triggeredBy = &id
		}
		run := newQueuedRun(svc.ProjectID, svc.ID, prompt, nil, triggeredBy)
		run.Origin = domain.RunOriginAutomation
		run.OriginAutomationID = a.ID
		run.OriginEventKey = pluginEventKey(a.ID, svc.ID, event)
		run.ModelName = sel.ModelName
		run.PRNumber = int(event.Object.Number)
		run.PRHeadBranch = event.Ref
		run.PRBaseBranch = event.BaseRef
		if event.Object.URL != "" {
			run.PRURL = event.Object.URL
		}
		if sel.ModelID != "" {
			modelID := sel.ModelID
			run.ModelID = &modelID
		}
		s.supersedeQueuedPluginRun(r, svc.ID, a.ID, event)
		if createErr := s.st.CreateRun(r.Context(), run); createErr != nil {
			if _, duplicateErr := s.st.GetRunByOriginEventKey(r.Context(), run.OriginEventKey); duplicateErr == nil {
				continue
			}
			s.recordPluginAutomationError(r, a, "Automation Run could not be created.")
			continue
		}
		nowTriggered := time.Now().UTC()
		a.LastTriggeredAt = &nowTriggered
		a.LastRunID = run.ID
		a.LastError = ""
		_ = s.st.UpdatePluginAutomation(r.Context(), a)
		s.emitStatus(r.Context(), run)
		if receipt.MatchedAutomationID == "" {
			receipt.MatchedAutomationID = a.ID
		}
		dispatched++
	}
	if dispatched == 0 {
		s.completePluginReceipt(r, receipt, "ignored", "", "")
		writeWebhookOK(w, "ignored")
		return
	}
	s.completePluginReceipt(r, receipt, "matched", receipt.MatchedAutomationID, "")
	writeWebhookOK(w, "accepted")
}

func pluginWebhookHeaders(r *http.Request) (scmevent.ProviderKind, string, string) {
	switch r.URL.Path {
	case "/webhooks/github":
		return scmevent.ProviderGitHub, r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	case "/webhooks/gitlab":
		delivery := r.Header.Get("X-Gitlab-Event-UUID")
		if delivery == "" {
			delivery = r.Header.Get("X-Gitlab-Webhook-UUID")
		}
		return scmevent.ProviderGitLab, r.Header.Get("X-Gitlab-Event"), delivery
	case "/webhooks/gitea":
		eventType := r.Header.Get("X-Gitea-Event-Type")
		if eventType == "" {
			eventType = r.Header.Get("X-Gitea-Event")
		}
		return scmevent.ProviderGitea, eventType, r.Header.Get("X-Gitea-Delivery")
	default:
		return "", "", ""
	}
}

func normalizedObjectRef(event scmevent.NormalizedSCMEvent) string {
	if event.Object.ID != "" {
		return event.Repository.FullName + ":" + event.Object.ID
	}
	if event.Ref != "" {
		return event.Repository.FullName + ":" + event.Ref
	}
	return event.Repository.FullName
}

func pluginEventKey(automationID, serviceID string, event scmevent.NormalizedSCMEvent) string {
	if key := scmevent.CoalesceKey(serviceID, event); key != "" {
		return fmt.Sprintf("plugin-coalesce:%s:%s:%s", automationID, key, event.DeliveryID)
	}
	return fmt.Sprintf("plugin:%s:%s:%s", automationID, event.Provider, event.DeliveryID)
}

func splitFilterValues(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func renderPluginPrompt(template string, event scmevent.NormalizedSCMEvent) string {
	return strings.NewReplacer(
		"{{provider}}", string(event.Provider),
		"{{event}}", string(event.Family)+"."+string(event.Action),
		"{{repository}}", event.Repository.FullName,
		"{{ref}}", event.Ref,
		"{{base_ref}}", event.BaseRef,
		"{{head_sha}}", event.HeadSHA,
		"{{object_url}}", event.Object.URL,
		"{{actor}}", event.Actor.Login,
	).Replace(template)
}

func (s *Server) supersedeQueuedPluginRun(r *http.Request, serviceID, automationID string, event scmevent.NormalizedSCMEvent) {
	key := scmevent.CoalesceKey(serviceID, event)
	if key == "" {
		return
	}
	runs, err := s.st.ListRunsByService(r.Context(), serviceID, 100)
	if err != nil {
		return
	}
	prefix := "plugin-coalesce:" + automationID + ":" + key + ":"
	for i := range runs {
		if runs[i].Status == domain.StatusQueued && strings.HasPrefix(runs[i].OriginEventKey, prefix) {
			_, _ = s.st.CancelRun(r.Context(), runs[i].ID, "Superseded", time.Now().UTC())
		}
	}
	// The new run still has a delivery-unique suffix for exactly-once de-dupe.
	// Change its key at the caller by encoding the coalescing prefix there.
}

func (s *Server) recordPluginAutomationError(r *http.Request, a *domain.PluginAutomation, message string) {
	a.LastError = message
	_ = s.st.UpdatePluginAutomation(r.Context(), a)
}

func (s *Server) completePluginReceipt(r *http.Request, receipt *domain.WebhookReceipt, status, automationID, message string) {
	receipt.Status = status
	receipt.MatchedAutomationID = automationID
	receipt.Error = message
	_ = s.st.CompleteWebhookReceipt(r.Context(), receipt)
}
