package api

// Provider-side SCM webhook lifecycle for Project Plugins. GitHub App delivery
// is configured once at cluster scope; GitLab/Gitea need one hook per bound
// repository while it has at least one enabled SCM Automation.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

const pluginWebhookLifecycleError = "SCM webhook could not be reconciled; verify the Plugin credential and provider hook permissions, then save the Automation again"

// reconcilePluginSCMWebhook converges the provider state after a committed
// Automation mutation. Keeping the Automation committed is intentional: a
// provider failure is made visible in its last_error and in webhook_bindings
// rather than silently dropping a user-requested Automation.
func (s *Server) reconcilePluginSCMWebhook(ctx context.Context, svc *domain.Service) error {
	if svc == nil {
		return errors.New("service is required")
	}
	active, err := s.serviceHasEnabledSCMAutomation(ctx, svc.ProjectID, svc.ID)
	if err != nil {
		return err
	}
	if !active {
		return s.removePluginSCMWebhook(ctx, svc)
	}
	return s.ensurePluginSCMWebhook(ctx, svc)
}

func (s *Server) serviceHasEnabledSCMAutomation(ctx context.Context, projectID, serviceID string) (bool, error) {
	automations, err := s.st.ListPluginAutomationsByProject(ctx, projectID)
	if err != nil {
		return false, fmt.Errorf("list project Automations: %w", err)
	}
	for _, a := range automations {
		if a.ServiceID == serviceID && a.TriggerKind == "scm" && a.Enabled {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) ensurePluginSCMWebhook(ctx context.Context, svc *domain.Service) error {
	manager, installation, err := s.pluginSCMWebhookManager(ctx, svc)
	if err != nil {
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	owner, repo, err := webhookRepositoryPath(svc)
	if err != nil {
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	if manager == nil {
		// GitHub App hooks are cluster-level. There is no mutable repository hook
		// to create, and therefore no project-level state to claim as healthy.
		return nil
	}
	endpoint, binding, err := s.preparePluginSCMWebhookEndpoint(ctx, svc, installation, manager, owner, repo)
	if err != nil {
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	if err := manager.EnsureSCMWebhook(ctx, owner, repo, endpoint.URL, endpoint.Secret); err != nil {
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	now := time.Now().UTC()
	binding.Status = domain.WebhookBindingActive
	binding.LastSyncedAt = &now
	binding.LastError = ""
	binding.UpdatedAt = now
	if err := s.st.UpsertWebhookBinding(ctx, binding); err != nil {
		return fmt.Errorf("record active webhook binding: %w", err)
	}
	s.clearPluginSCMWebhookFailure(ctx, svc)
	return nil
}

func (s *Server) removePluginSCMWebhook(ctx context.Context, svc *domain.Service) error {
	manager, _, err := s.pluginSCMWebhookManager(ctx, svc)
	if err != nil {
		// No SCM Automation remains, but preserve a visible error instead of
		// pretending the provider hook was removed.
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	if manager == nil { // GitHub App's shared hook is never removed per Service.
		return nil
	}
	binding, err := s.st.GetWebhookBinding(ctx, svc.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load webhook binding: %w", err)
	}
	owner, repo, err := webhookRepositoryPath(svc)
	if err != nil {
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	if strings.TrimSpace(binding.Endpoint) != "" {
		if err := manager.DeleteSCMWebhook(ctx, owner, repo, binding.Endpoint); err != nil {
			s.recordPluginSCMWebhookFailure(ctx, svc, err)
			return err
		}
	}
	if err := s.st.DeleteWebhookBinding(ctx, svc.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("clear webhook binding: %w", err)
	}
	s.clearPluginSCMWebhookFailure(ctx, svc)
	return nil
}

type pluginWebhookEndpoint struct {
	URL    string
	Secret string
}

// pluginSCMWebhookManager resolves the current Project Installation credential.
// Webhook authentication secrets are deliberately not Provider config: each
// GitLab/Gitea Service binding owns a random encrypted secret.
func (s *Server) pluginSCMWebhookManager(ctx context.Context, svc *domain.Service) (provider.SCMWebhookManager, *domain.PluginInstallation, error) {
	binding, err := s.st.GetServiceRepositoryBinding(ctx, svc.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load Service repository binding: %w", err)
	}
	installation, err := s.st.GetPluginInstallation(ctx, binding.InstallationID)
	if err != nil {
		return nil, nil, fmt.Errorf("load Service Plugin: %w", err)
	}
	if installation.Provider == domain.PluginGitHub {
		return nil, installation, nil
	}
	if installation.Provider != domain.PluginGitLab && installation.Provider != domain.PluginGitea {
		return nil, nil, fmt.Errorf("Plugin %s cannot manage SCM webhooks", installation.Provider)
	}
	cfg, err := s.st.GetProviderConfig(ctx, installation.Provider)
	if err != nil || !cfg.PluginEnabled {
		return nil, nil, errors.New("Provider Plugin configuration is unavailable")
	}
	if s.pluginCredentialIssuer == nil {
		return nil, nil, errors.New("Project Plugin credential issuer is unavailable")
	}
	credential, err := s.pluginCredentialIssuer.IssueRunPluginCredential(ctx, installation, cfg)
	if err != nil || strings.TrimSpace(credential.AccessToken) == "" {
		return nil, nil, errors.New("Project Plugin credential is unavailable")
	}
	// Project Installations are OAuth grants. Gitea accepts a personal token as
	// "token" but OAuth access tokens must use Bearer; do not reuse the legacy
	// IntegrationClient (whose Gitea path deliberately targets PATs).
	var client provider.Provider
	if installation.Provider == domain.PluginGitea {
		client, err = provider.NewGiteaClientWithScheme(cfg.BaseURL, credential.AccessToken, credential.Scheme)
	} else {
		client, err = provider.IntegrationClient(domain.GitProvider(installation.Provider), cfg.BaseURL, credential.AccessToken)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("create Plugin provider client: %w", err)
	}
	manager, ok := client.(provider.SCMWebhookManager)
	if !ok {
		return nil, nil, fmt.Errorf("Provider %s cannot manage SCM webhooks", installation.Provider)
	}
	return manager, installation, nil
}

func (s *Server) preparePluginSCMWebhookEndpoint(
	ctx context.Context,
	svc *domain.Service,
	installation *domain.PluginInstallation,
	manager provider.SCMWebhookManager,
	owner, repo string,
) (pluginWebhookEndpoint, *domain.WebhookBinding, error) {
	if s.cipher == nil {
		return pluginWebhookEndpoint{}, nil, errors.New("webhook credential encryption is unavailable")
	}
	existing, err := s.st.GetWebhookBinding(ctx, svc.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return pluginWebhookEndpoint{}, nil, fmt.Errorf("load webhook binding: %w", err)
	}
	if err == nil && existing.Provider == domain.GitProvider(installation.Provider) &&
		strings.TrimSpace(existing.HookID) != "" && len(existing.SecretEnc) > 0 &&
		strings.HasSuffix(strings.TrimRight(existing.Endpoint, "/"), "/"+existing.HookID) {
		secret, decryptErr := s.cipher.DecryptString(existing.SecretEnc)
		if decryptErr == nil && strings.TrimSpace(secret) != "" {
			return pluginWebhookEndpoint{URL: existing.Endpoint, Secret: secret}, existing, nil
		}
	}

	// A pre-upgrade binding used the shared Provider endpoint and secret. Remove
	// the known old hook before replacing it; never reactivate a row with an
	// empty per-binding secret.
	if err == nil && strings.TrimSpace(existing.Endpoint) != "" {
		if deleteErr := manager.DeleteSCMWebhook(ctx, owner, repo, existing.Endpoint); deleteErr != nil {
			return pluginWebhookEndpoint{}, nil, fmt.Errorf("remove legacy webhook: %w", deleteErr)
		}
	}
	settings, settingsErr := s.st.GetClusterSettings(ctx)
	if settingsErr != nil || !settings.SetupComplete || strings.TrimSpace(settings.PublicURL) == "" {
		return pluginWebhookEndpoint{}, nil, errors.New("Cluster public URL is unavailable")
	}
	hookID := domain.NewID()
	secret := randToken()
	secretEnc, encryptErr := s.cipher.EncryptString(secret)
	if encryptErr != nil {
		return pluginWebhookEndpoint{}, nil, errors.New("encrypt webhook secret")
	}
	now := time.Now().UTC()
	binding := &domain.WebhookBinding{
		ServiceID: svc.ID,
		Provider:  domain.GitProvider(installation.Provider),
		Endpoint: strings.TrimRight(settings.PublicURL, "/") + "/webhooks/" +
			string(installation.Provider) + "/" + hookID,
		HookID:    hookID,
		SecretEnc: secretEnc,
		Status:    domain.WebhookBindingPending,
		UpdatedAt: now,
	}
	// Persist before the Provider call so an immediate validation ping can be
	// authenticated. Pending bindings are accepted by ingress; error bindings
	// are not.
	if upsertErr := s.st.UpsertWebhookBinding(ctx, binding); upsertErr != nil {
		return pluginWebhookEndpoint{}, nil, fmt.Errorf("record pending webhook binding: %w", upsertErr)
	}
	return pluginWebhookEndpoint{URL: binding.Endpoint, Secret: secret}, binding, nil
}

func webhookRepositoryPath(svc *domain.Service) (string, string, error) {
	path := strings.Trim(svc.RepoOwnerName, "/")
	owner, repo, ok := strings.Cut(path, "/")
	if !ok || owner == "" || repo == "" {
		return "", "", errors.New("Service repository path is unavailable")
	}
	return owner, repo, nil
}

func (s *Server) recordPluginSCMWebhookFailure(ctx context.Context, svc *domain.Service, _ error) {
	if svc == nil {
		return
	}
	// Never persist raw provider errors: these can include upstream response
	// fragments. The user gets an actionable stable message, and credentials
	// remain absent from both logs and durable state.
	binding, err := s.st.GetWebhookBinding(ctx, svc.ID)
	if errors.Is(err, store.ErrNotFound) {
		binding = &domain.WebhookBinding{ServiceID: svc.ID, Provider: svc.Provider}
	} else if err != nil {
		binding = nil
	}
	if binding != nil {
		binding.Status = domain.WebhookBindingError
		binding.LastError = pluginWebhookLifecycleError
		binding.UpdatedAt = time.Now().UTC()
		_ = s.st.UpsertWebhookBinding(ctx, binding)
	}
	automations, err := s.st.ListPluginAutomationsByProject(ctx, svc.ProjectID)
	if err != nil {
		return
	}
	for i := range automations {
		if automations[i].ServiceID != svc.ID || automations[i].TriggerKind != "scm" {
			continue
		}
		automations[i].LastError = pluginWebhookLifecycleError
		_ = s.st.UpdatePluginAutomation(ctx, &automations[i])
	}
}

func (s *Server) clearPluginSCMWebhookFailure(ctx context.Context, svc *domain.Service) {
	if svc == nil {
		return
	}
	automations, err := s.st.ListPluginAutomationsByProject(ctx, svc.ProjectID)
	if err != nil {
		return
	}
	for i := range automations {
		if automations[i].ServiceID != svc.ID || automations[i].TriggerKind != "scm" ||
			automations[i].LastError == "" {
			continue
		}
		automations[i].LastError = ""
		_ = s.st.UpdatePluginAutomation(ctx, &automations[i])
	}
}
