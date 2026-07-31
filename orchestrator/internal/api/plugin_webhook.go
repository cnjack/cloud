package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/provenance"
	gitprovider "github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/scmevent"
	"github.com/cnjack/jcloud/internal/store"
)

// handlePluginWebhook is the only SCM ingress used by the Plugin platform. It
// authenticates with the DB-backed Provider config, immediately normalizes the
// payload, and never persists or logs the raw body.
func (s *Server) handlePluginWebhook(w http.ResponseWriter, r *http.Request) {
	provider, eventType, deliveryID, hookID := pluginWebhookHeaders(r)
	if !provider.Valid() {
		writeError(w, http.StatusNotFound, "not_found", "unknown webhook provider")
		return
	}
	cfg, err := s.st.GetProviderConfig(r.Context(), domain.ProviderKind(provider))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider webhook is not configured")
		return
	}
	if err != nil || cfg == nil || !cfg.PluginEnabled {
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider webhook is not configured")
		return
	}
	if s.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider webhook credential is unavailable")
		return
	}

	var (
		rawSecret      []byte
		routeBinding   *domain.WebhookBinding
		repoBinding    *domain.ServiceRepositoryBinding
		boundService   *domain.Service
		boundInstallID string
	)
	if provider == scmevent.ProviderGitHub {
		if len(cfg.WebhookSecretEnc) == 0 {
			writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider webhook secret is unavailable")
			return
		}
		rawSecret, err = s.cipher.Decrypt(cfg.WebhookSecretEnc)
	} else {
		if strings.TrimSpace(hookID) == "" {
			writeError(w, http.StatusNotFound, "not_found", "unknown webhook binding")
			return
		}
		routeBinding, err = s.st.GetWebhookBindingByHookID(r.Context(), hookID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "webhook binding could not be loaded")
			return
		}
		if err != nil || routeBinding.Provider != domain.GitProvider(provider) ||
			(routeBinding.Status != domain.WebhookBindingPending && routeBinding.Status != domain.WebhookBindingActive) ||
			len(routeBinding.SecretEnc) == 0 {
			writeError(w, http.StatusNotFound, "not_found", "unknown webhook binding")
			return
		}
		rawSecret, err = s.cipher.Decrypt(routeBinding.SecretEnc)
	}
	if err != nil {
		s.log.Error("decrypt webhook credential", "provider", provider)
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider webhook credential is unavailable")
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
		if routeBinding != nil {
			_ = s.st.RecordWebhookDelivery(r.Context(), routeBinding.ServiceID, time.Now().UTC(), "rejected", "invalid signature")
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
		return
	}
	if deliveryID == "" {
		writeError(w, http.StatusBadRequest, "missing_delivery_id", "provider delivery id is required")
		return
	}

	now := time.Now().UTC()
	payloadDigest := ""
	if provider == scmevent.ProviderGitHub || provider == scmevent.ProviderGitea {
		// Canonicalize the already-verified body through its per-authentication
		// HMAC before hashing again for storage. For Gitea this scopes identical
		// payloads to their random binding secret, while a replay that only
		// changes the unsigned delivery header still collides.
		mac := hmac.New(sha256.New, rawSecret)
		_, _ = mac.Write(body)
		sum := sha256.Sum256(mac.Sum(nil))
		payloadDigest = hex.EncodeToString(sum[:])
	}
	event, err := scmevent.NormalizeForApp(provider, eventType, deliveryID, body, now, cfg.AppSlug)
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
	if routeBinding != nil {
		repoBinding, err = s.st.GetServiceRepositoryBinding(r.Context(), routeBinding.ServiceID)
		if err == nil {
			boundService, err = s.st.GetService(r.Context(), routeBinding.ServiceID)
		}
		var installation *domain.PluginInstallation
		if err == nil {
			installation, err = s.st.GetPluginInstallation(r.Context(), repoBinding.InstallationID)
		}
		if err != nil || boundService.DeletingAt != nil ||
			boundService.Provider != domain.GitProvider(provider) ||
			installation.Provider != domain.ProviderKind(provider) ||
			installation.ProjectID != boundService.ProjectID ||
			repoBinding.ProviderRepoID != event.Repository.ID {
			_ = s.st.RecordWebhookDelivery(r.Context(), routeBinding.ServiceID, now, "rejected", "repository mismatch")
			writeError(w, http.StatusUnauthorized, "repository_mismatch", "webhook repository does not match its Service binding")
			return
		}
		boundInstallID = repoBinding.InstallationID
	}
	receipt := &domain.WebhookReceipt{
		ID:              domain.NewID(),
		Provider:        domain.ProviderKind(provider),
		DeliveryID:      event.DeliveryID,
		PayloadDigest:   payloadDigest,
		InstallationID:  boundInstallID,
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
		if routeBinding != nil {
			_ = s.st.RecordWebhookDelivery(r.Context(), routeBinding.ServiceID, now, "duplicate", "")
		}
		writeWebhookOK(w, "duplicate")
		return
	}

	var reviewCommand scmevent.ReviewCommand
	manualReview := false
	if event.Family == scmevent.FamilyComment {
		reviewCommand, manualReview = scmevent.ParseReviewCommand(event.Body, cfg.AppSlug)
	}
	var automations []domain.PluginAutomation
	if manualReview {
		automations, err = s.st.ListPluginReviewAutomationsForRepository(
			r.Context(), domain.ProviderKind(provider), event.Repository.ID,
		)
		if len(automations) > 1 {
			automations = automations[:1]
		}
	} else {
		automations, err = s.st.ListPluginAutomationsForEvent(
			r.Context(), domain.ProviderKind(provider), event.Repository.ID,
			string(event.Family), string(event.Action),
		)
	}
	if err != nil {
		s.completePluginReceipt(r, receipt, "error", "", "could not match Automation")
		writeError(w, http.StatusInternalServerError, "match_failed", "could not match webhook Automation")
		return
	}
	dispatched := 0
	failReceipt := func(message string) {
		s.completePluginReceipt(r, receipt, "error", receipt.MatchedAutomationID, message)
		writeError(w, http.StatusInternalServerError, "execution_ledger_failed", message)
	}
	recordDecision := func(
		automation *domain.PluginAutomation,
		service *domain.Service,
		state domain.AutomationExecutionState,
		reasonCode, reasonMessage, repairRole string,
	) bool {
		if err := s.recordPluginSCMDecision(
			r, automation, service, event, state, reasonCode, reasonMessage, repairRole,
		); err != nil {
			failReceipt("could not record Automation execution")
			return false
		}
		return true
	}
	for i := range automations {
		a := &automations[i]
		if routeBinding != nil && a.ServiceID != routeBinding.ServiceID {
			continue
		}
		svc, svcErr := s.st.GetService(r.Context(), a.ServiceID)
		if svcErr != nil {
			failReceipt("could not load Automation Service")
			return
		}
		spec, specErr := s.st.GetPluginAutomationSpec(r.Context(), a.ID)
		if specErr != nil || spec.SCM == nil {
			s.recordPluginAutomationError(r, a, "Automation trigger configuration is unavailable.")
			if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
				"trigger_configuration_unavailable", "Automation trigger configuration is unavailable.", "project_owner") {
				return
			}
			continue
		}
		if svc.DeletingAt != nil {
			s.recordPluginAutomationError(r, a, "Automation Service is unavailable.")
			if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
				"service_unavailable", "Automation Service is being deleted.", "project_owner") {
				return
			}
			continue
		}
		binding, bindErr := s.st.GetServiceRepositoryBinding(r.Context(), svc.ID)
		if bindErr != nil {
			s.recordPluginAutomationError(r, a, "Automation Service repository binding is unavailable.")
			if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
				"repository_unavailable", "Automation Service repository binding is unavailable.", "project_owner") {
				return
			}
			continue
		}
		if a.RunKind == domain.RunKindReview && event.Family == scmevent.FamilyPullRequest &&
			event.Draft && !spec.SCM.IncludeDrafts {
			if !recordDecision(a, svc, domain.AutomationExecutionIgnored,
				"draft_pull_request", "Draft pull requests are excluded by this Automation.", "") {
				return
			}
			continue
		}
		if a.IgnoreJCode && event.GeneratedByJCode {
			if !recordDecision(a, svc, domain.AutomationExecutionIgnored,
				"jcode_generated", "The Automation ignores events generated by jcode.", "") {
				return
			}
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
			if !recordDecision(a, svc, domain.AutomationExecutionIgnored,
				"filter_mismatch", "The event did not match this Automation's filters.", "") {
				return
			}
			continue
		}
		allowed, host, hostErr := s.integrationHostStillAllowed(r.Context(), svc)
		if hostErr != nil {
			s.recordPluginAutomationError(r, a, "The Service repository host policy could not be checked.")
			if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
				"host_policy_unavailable", "The Service repository host policy could not be checked.", "cluster_admin") {
				return
			}
			continue
		}
		if !allowed {
			reason := "The Service repository host is no longer allowed: " + host
			s.recordPluginAutomationError(r, a, reason)
			if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
				"host_not_allowed", reason, "cluster_admin") {
				return
			}
			continue
		}
		if receipt.InstallationID == "" {
			receipt.InstallationID = binding.InstallationID
		}
		var (
			manualClient     gitprovider.Provider
			manualOwner      string
			manualRepo       string
			manualUserID     *string
			manualCommentURL = event.Object.URL
			manualReply      = func(string) {}
		)
		if manualReview {
			manualClient, bindErr = s.pluginProviderClient(r.Context(), binding)
			manualOwner, manualRepo, _ = gitprovider.SplitRepo(svc.RepoOwnerName)
			manualReply = func(message string) {
				if manualClient != nil && manualOwner != "" && manualRepo != "" {
					_ = manualClient.CreateIssueComment(r.Context(), manualOwner, manualRepo, int(event.Object.Number), message)
				}
			}
			if bindErr != nil || manualClient == nil || manualOwner == "" || manualRepo == "" {
				s.recordPluginAutomationError(r, a, "GitHub App credential is unavailable for review acknowledgement.")
				if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
					"provider_credential_unavailable", "The repository Provider credential is unavailable.", "project_owner") {
					return
				}
				continue
			}
			identity, identityErr := s.st.GetIdentity(r.Context(), domain.GitProvider(provider), event.Actor.ID)
			if identityErr != nil {
				manualReply("jcode couldn't find a linked Cloud account for this GitHub user. Sign in to jcode Cloud with GitHub first.")
				if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
					"cloud_account_unlinked", "The requester must link their repository account to jcode Cloud.", "requester") {
					return
				}
				continue
			}
			user, userErr := s.st.GetUser(r.Context(), identity.UserID)
			member, memberErr := s.st.GetMember(r.Context(), svc.ProjectID, identity.UserID)
			if userErr != nil || (!user.IsClusterAdmin && (memberErr != nil || !member.Role.AtLeast(domain.RoleMember))) {
				manualReply("jcode can't start a review for this account. Ask a project owner to add you as a member.")
				if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
					"requester_not_authorized", "The requester is not authorized to run this Project Automation.", "project_owner") {
					return
				}
				continue
			}
			userID := identity.UserID
			manualUserID = &userID
			pr, prErr := manualClient.PRByNumber(r.Context(), manualOwner, manualRepo, int(event.Object.Number))
			if prErr != nil || pr == nil || pr.HeadRef == "" || pr.BaseRef == "" {
				manualReply("jcode couldn't read this pull request. Check that the App still has Pull requests: read and write permission.")
				if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
					"pull_request_unavailable", "The repository Provider could not read this pull request.", "project_owner") {
					return
				}
				continue
			}
			event.Ref, event.BaseRef = pr.HeadRef, pr.BaseRef
			event.Object.URL = pr.URL
		}
		sel, outcome, selectErr := s.models.SelectModel(r.Context(), svc.ProjectID, deref(svc.DefaultModelID), a.ModelID)
		if selectErr != nil || outcome != modelcfg.SelectOK {
			s.recordPluginAutomationError(r, a, pluginAutomationModelError(outcome, selectErr))
			repairRole := "project_owner"
			reasonCode := automationModelReasonCode(outcome, true)
			reasonMessage := pluginAutomationModelError(outcome, selectErr)
			if selectErr != nil {
				repairRole = "cluster_admin"
				reasonCode = "model_resolution_failed"
			} else if outcome == modelcfg.SelectNotConfigured {
				repairRole = "cluster_admin"
			}
			if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
				reasonCode, reasonMessage, repairRole) {
				return
			}
			if manualReview {
				manualReply("jcode couldn't start this review because no usable model is configured. Ask a project owner to check the review settings.")
			}
			continue
		}
		if !sel.SupportsEffort(a.ModelEffort) {
			s.recordPluginAutomationError(r, a, "The selected Automation model no longer supports reasoning effort.")
			if !recordDecision(a, svc, domain.AutomationExecutionBlocked,
				"model_effort_unsupported", "The selected Automation model no longer supports reasoning effort.", "project_owner") {
				return
			}
			if manualReview {
				manualReply("jcode couldn't start this review because the selected model no longer supports its configured reasoning effort.")
			}
			continue
		}
		prompt := renderPluginPrompt(a.PromptTemplate, event)
		if event.Family == scmevent.FamilyComment {
			if manualReview {
				prompt = a.PromptTemplate
				if reviewCommand.Full {
					prompt += "\nReview mode: full base-to-head review."
				}
				if reviewCommand.Focus != "" {
					prompt += "\nReviewer focus: " + reviewCommand.Focus
				}
			} else {
				prompt = event.Body
			}
		}
		var triggeredBy *string
		if manualUserID != nil {
			triggeredBy = manualUserID
		} else if identity, identityErr := s.st.GetIdentity(r.Context(), domain.GitProvider(provider), event.Actor.ID); identityErr == nil {
			id := identity.UserID
			triggeredBy = &id
		}
		run := newQueuedRun(svc.ProjectID, svc.ID, prompt, nil, triggeredBy)
		run.Kind = a.RunKind
		if run.Kind == "" {
			run.Kind = domain.RunKindAgent
		}
		run.Origin = domain.RunOriginAutomation
		run.OriginAutomationID = a.ID
		run.OriginEventKey = pluginEventKey(a, svc.ID, event)
		if manualReview {
			run.Origin = domain.RunOriginWebhook
			run.OriginCommentID = originCommentKey(domain.GitProvider(provider), event.Object.ID)
			run.OriginCommentURL = manualCommentURL
		}
		run.ModelName = sel.ModelName
		run.ModelEffort = a.ModelEffort
		run.PRNumber = int(event.Object.Number)
		// A normalized push ref can be "refs/heads/<name>", while the runner's
		// BASE_BRANCH contract requires the repository branch name accepted by
		// git clone --branch. Persist the resolved baseline independently from
		// PR metadata so push/check Automations do not accidentally enter the
		// PR-head update path.
		run.BaseBranch = automationRunBranch(event)
		if event.Family == scmevent.FamilyPullRequest || manualReview {
			run.PRHeadBranch = automationRunBranch(event)
			run.PRBaseBranch = strings.TrimPrefix(event.BaseRef, "refs/heads/")
		}
		if event.Object.URL != "" {
			run.PRURL = event.Object.URL
		}
		if sel.ModelID != "" {
			modelID := sel.ModelID
			run.ModelID = &modelID
		}
		provenance.Stamp(r.Context(), s.st, run, &provenance.ExternalActor{
			Provider: string(provider), ID: event.Actor.ID,
			Label: event.Actor.Login, Source: "scm_event",
		})
		if coalesceKey := pluginRunCoalesceKey(a.ID, svc.ID, event); coalesceKey != "" && !manualReview {
			run.CoalesceKey = coalesceKey
		}
		execution := s.newPluginSCMExecution(r, a, svc, event, domain.AutomationExecutionQueued)
		execution.RunID = run.ID
		_, created, createErr := s.st.CreateAutomationExecution(r.Context(), execution, run)
		if createErr != nil {
			if existingRun, duplicateErr := s.st.GetRunByOriginEventKey(r.Context(), run.OriginEventKey); duplicateErr == nil {
				execution.RunID = existingRun.ID
				if _, _, recoverErr := s.st.CreateAutomationExecution(r.Context(), execution, nil); recoverErr == nil {
					continue
				}
			}
			s.recordPluginAutomationError(r, a, "Automation Run could not be created.")
			if manualReview {
				manualReply("jcode couldn't queue this review. Try again; if it continues, ask a project owner to check the Automation status.")
			}
			failReceipt("could not create Automation execution")
			return
		}
		if !created {
			continue
		}
		nowTriggered := time.Now().UTC()
		a.LastTriggeredAt = &nowTriggered
		a.LastRunID = run.ID
		a.LastError = ""
		_ = s.st.UpdatePluginAutomation(r.Context(), a)
		s.emitStatus(r.Context(), run)
		if manualReview {
			if reactor, ok := manualClient.(gitprovider.IssueCommentReactor); ok {
				if commentID, parseErr := strconv.ParseInt(event.Object.ID, 10, 64); parseErr == nil {
					_ = reactor.CreateIssueCommentReaction(r.Context(), manualOwner, manualRepo, commentID, "eyes")
				}
			}
		}
		if receipt.MatchedAutomationID == "" {
			receipt.MatchedAutomationID = a.ID
		}
		dispatched++
	}
	if dispatched == 0 {
		if routeBinding != nil {
			_ = s.st.RecordWebhookDelivery(r.Context(), routeBinding.ServiceID, now, "ignored", "")
		}
		s.completePluginReceipt(r, receipt, "ignored", "", "")
		writeWebhookOK(w, "ignored")
		return
	}
	s.completePluginReceipt(r, receipt, "matched", receipt.MatchedAutomationID, "")
	if routeBinding != nil {
		_ = s.st.RecordWebhookDelivery(r.Context(), routeBinding.ServiceID, now, "accepted", "")
	}
	writeWebhookOK(w, "accepted")
}

func pluginAutomationModelError(outcome modelcfg.SelectOutcome, err error) string {
	if err != nil {
		return "Automation model could not be resolved because of a temporary internal error."
	}
	switch outcome {
	case modelcfg.SelectNotSelected:
		return "Several project models are available, but this Service has no default model."
	case modelcfg.SelectNotConfigured:
		return "No model is authorized for this Project."
	default:
		return "Automation model is unavailable."
	}
}

func automationRunBranch(event scmevent.NormalizedSCMEvent) string {
	branch := strings.TrimSpace(strings.TrimPrefix(event.Ref, "refs/heads/"))
	if branch == "" {
		branch = strings.TrimSpace(strings.TrimPrefix(event.BaseRef, "refs/heads/"))
	}
	return branch
}

func pluginWebhookHeaders(r *http.Request) (scmevent.ProviderKind, string, string, string) {
	switch r.URL.Path {
	case "/webhooks/github":
		return scmevent.ProviderGitHub, r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery"), ""
	}
	if hookID := webhookBindingPathValue(r, "/webhooks/gitlab/"); hookID != "" {
		delivery := r.Header.Get("X-Gitlab-Event-UUID")
		if delivery == "" {
			delivery = r.Header.Get("X-Gitlab-Webhook-UUID")
		}
		return scmevent.ProviderGitLab, r.Header.Get("X-Gitlab-Event"), delivery, hookID
	}
	if hookID := webhookBindingPathValue(r, "/webhooks/gitea/"); hookID != "" {
		eventType := r.Header.Get("X-Gitea-Event-Type")
		if eventType == "" {
			eventType = r.Header.Get("X-Gitea-Event")
		}
		return scmevent.ProviderGitea, eventType, r.Header.Get("X-Gitea-Delivery"), hookID
	}
	return "", "", "", ""
}

func webhookBindingPathValue(r *http.Request, prefix string) string {
	hookID := strings.TrimSpace(r.PathValue("binding"))
	if hookID == "" {
		hookID = strings.TrimSpace(strings.TrimPrefix(r.URL.Path, prefix))
	}
	if hookID == "" || strings.Contains(hookID, "/") || r.URL.Path != prefix+hookID {
		return ""
	}
	return hookID
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

func pluginEventKey(automation *domain.PluginAutomation, serviceID string, event scmevent.NormalizedSCMEvent) string {
	automationID := automation.ID
	if automation.RunKind == domain.RunKindReview && event.Family == scmevent.FamilyPullRequest &&
		event.Object.Number > 0 && strings.TrimSpace(event.HeadSHA) != "" {
		return fmt.Sprintf("plugin-review:%s:%s:%d:%s", automationID, event.Repository.ID, event.Object.Number, event.HeadSHA)
	}
	if key := scmevent.CoalesceKey(serviceID, event); key != "" {
		return fmt.Sprintf("plugin-coalesce:%s:%s:%s", automationID, key, event.DeliveryID)
	}
	return fmt.Sprintf("plugin:%s:%s:%s", automationID, event.Provider, event.DeliveryID)
}

func (s *Server) pluginProviderClient(ctx context.Context, binding *domain.ServiceRepositoryBinding) (gitprovider.Provider, error) {
	if binding == nil || s.pluginCredentialIssuer == nil {
		return nil, errors.New("Plugin credential issuer is unavailable")
	}
	installation, err := s.st.GetPluginInstallation(ctx, binding.InstallationID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.st.GetProviderConfig(ctx, installation.Provider)
	if err != nil {
		return nil, err
	}
	credential, err := s.pluginCredentialIssuer.IssueRunPluginCredential(ctx, installation, cfg)
	if err != nil {
		return nil, err
	}
	return gitprovider.IntegrationClientWithScheme(
		domain.GitProvider(installation.Provider), cfg.BaseURL, credential.AccessToken, credential.Scheme,
	)
}

func pluginRunCoalesceKey(automationID, serviceID string, event scmevent.NormalizedSCMEvent) string {
	key := scmevent.CoalesceKey(serviceID, event)
	if key == "" {
		return ""
	}
	return "plugin:" + automationID + ":" + key
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

func (s *Server) recordPluginAutomationError(r *http.Request, a *domain.PluginAutomation, message string) {
	a.LastError = message
	_ = s.st.UpdatePluginAutomation(r.Context(), a)
}

func (s *Server) newPluginSCMExecution(
	r *http.Request,
	automation *domain.PluginAutomation,
	service *domain.Service,
	event scmevent.NormalizedSCMEvent,
	state domain.AutomationExecutionState,
) *domain.AutomationExecution {
	now := time.Now().UTC()
	return &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: automation.ID, AutomationName: automation.Name,
		PromptSnapshot: automation.PromptTemplate, ProjectID: service.ProjectID, ServiceID: service.ID,
		TriggerKind: "scm", EventKey: pluginEventKey(automation, service.ID, event),
		State: state, OutputMode: domain.AutomationOutputRunOnly,
		RequestedActor:   s.pluginSCMRequestedActor(r, event),
		AccountableActor: s.automationOwnerActor(r, automation.CreatedBy),
		ExternalURL:      event.Object.URL, CreatedAt: now, UpdatedAt: now,
	}
}

func (s *Server) pluginSCMRequestedActor(r *http.Request, event scmevent.NormalizedSCMEvent) domain.ProvenanceActorRef {
	external := domain.ProvenanceActorRef{
		Kind: "external_actor", ID: event.Actor.ID, Label: event.Actor.Login,
		Provider: string(event.Provider),
	}
	identity, err := s.st.GetIdentity(r.Context(), domain.GitProvider(event.Provider), event.Actor.ID)
	if err != nil {
		return external
	}
	actor := domain.ProvenanceActorRef{
		Kind: "cloud_user", ID: identity.UserID, Label: "Former member",
		Provider: string(event.Provider), ExternalID: event.Actor.ID, ExternalLabel: event.Actor.Login,
	}
	if user, userErr := s.st.GetUser(r.Context(), identity.UserID); userErr == nil {
		actor.Label = strings.TrimSpace(user.DisplayName)
		if actor.Label == "" {
			actor.Label = identity.UserID
		}
	}
	return actor
}

func (s *Server) recordPluginSCMDecision(
	r *http.Request,
	automation *domain.PluginAutomation,
	service *domain.Service,
	event scmevent.NormalizedSCMEvent,
	state domain.AutomationExecutionState,
	reasonCode, reasonMessage, repairRole string,
) error {
	execution := s.newPluginSCMExecution(r, automation, service, event, state)
	execution.ReasonCode = reasonCode
	execution.ReasonMessage = reasonMessage
	execution.RepairRole = repairRole
	_, _, err := s.st.CreateAutomationExecution(r.Context(), execution, nil)
	return err
}

func (s *Server) completePluginReceipt(r *http.Request, receipt *domain.WebhookReceipt, status, automationID, message string) {
	receipt.Status = status
	receipt.MatchedAutomationID = automationID
	receipt.Error = message
	_ = s.st.CompleteWebhookReceipt(r.Context(), receipt)
}
