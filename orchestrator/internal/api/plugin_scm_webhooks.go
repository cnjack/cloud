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
	manager, installation, endpoint, err := s.pluginSCMWebhookManager(ctx, svc)
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
	if err := manager.EnsureSCMWebhook(ctx, owner, repo, endpoint.URL, endpoint.Secret); err != nil {
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	now := time.Now().UTC()
	if err := s.st.UpsertWebhookBinding(ctx, &domain.WebhookBinding{
		ServiceID: svc.ID, Provider: domain.GitProvider(installation.Provider), Endpoint: endpoint.URL,
		Status: domain.WebhookBindingActive, LastSyncedAt: &now, UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("record active webhook binding: %w", err)
	}
	s.clearPluginSCMWebhookFailure(ctx, svc)
	return nil
}

func (s *Server) removePluginSCMWebhook(ctx context.Context, svc *domain.Service) error {
	manager, _, endpoint, err := s.pluginSCMWebhookManager(ctx, svc)
	if err != nil {
		// No SCM Automation remains, but preserve a visible error instead of
		// pretending the provider hook was removed.
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	if manager == nil { // GitHub App's shared hook is never removed per Service.
		return nil
	}
	owner, repo, err := webhookRepositoryPath(svc)
	if err != nil {
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
	}
	if err := manager.DeleteSCMWebhook(ctx, owner, repo, endpoint.URL); err != nil {
		s.recordPluginSCMWebhookFailure(ctx, svc, err)
		return err
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

// pluginSCMWebhookManager resolves every secret from the current Project
// Installation / Provider Config. It never consults legacy environment tokens
// or a human OAuth identity.
func (s *Server) pluginSCMWebhookManager(ctx context.Context, svc *domain.Service) (provider.SCMWebhookManager, *domain.PluginInstallation, pluginWebhookEndpoint, error) {
	binding, err := s.st.GetServiceRepositoryBinding(ctx, svc.ID)
	if err != nil {
		return nil, nil, pluginWebhookEndpoint{}, fmt.Errorf("load Service repository binding: %w", err)
	}
	installation, err := s.st.GetPluginInstallation(ctx, binding.InstallationID)
	if err != nil {
		return nil, nil, pluginWebhookEndpoint{}, fmt.Errorf("load Service Plugin: %w", err)
	}
	if installation.Provider == domain.PluginGitHub {
		return nil, installation, pluginWebhookEndpoint{}, nil
	}
	if installation.Provider != domain.PluginGitLab && installation.Provider != domain.PluginGitea {
		return nil, nil, pluginWebhookEndpoint{}, fmt.Errorf("Plugin %s cannot manage SCM webhooks", installation.Provider)
	}
	cfg, err := s.st.GetProviderConfig(ctx, installation.Provider)
	if err != nil || !cfg.PluginEnabled {
		return nil, nil, pluginWebhookEndpoint{}, errors.New("Provider Plugin configuration is unavailable")
	}
	if s.cipher == nil || len(cfg.WebhookSecretEnc) == 0 {
		return nil, nil, pluginWebhookEndpoint{}, errors.New("Provider webhook secret is unavailable")
	}
	secret, err := s.cipher.DecryptString(cfg.WebhookSecretEnc)
	if err != nil || strings.TrimSpace(secret) == "" {
		return nil, nil, pluginWebhookEndpoint{}, errors.New("Provider webhook secret is unavailable")
	}
	if s.pluginCredentialIssuer == nil {
		return nil, nil, pluginWebhookEndpoint{}, errors.New("Project Plugin credential issuer is unavailable")
	}
	credential, err := s.pluginCredentialIssuer.IssueRunPluginCredential(ctx, installation, cfg)
	if err != nil || strings.TrimSpace(credential.AccessToken) == "" {
		return nil, nil, pluginWebhookEndpoint{}, errors.New("Project Plugin credential is unavailable")
	}
	settings, err := s.st.GetClusterSettings(ctx)
	if err != nil || !settings.SetupComplete || strings.TrimSpace(settings.PublicURL) == "" {
		return nil, nil, pluginWebhookEndpoint{}, errors.New("Cluster public URL is unavailable")
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
		return nil, nil, pluginWebhookEndpoint{}, fmt.Errorf("create Plugin provider client: %w", err)
	}
	manager, ok := client.(provider.SCMWebhookManager)
	if !ok {
		return nil, nil, pluginWebhookEndpoint{}, fmt.Errorf("Provider %s cannot manage SCM webhooks", installation.Provider)
	}
	return manager, installation, pluginWebhookEndpoint{
		URL: strings.TrimRight(settings.PublicURL, "/") + "/webhooks/" + string(installation.Provider), Secret: secret,
	}, nil
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
	_ = s.st.UpsertWebhookBinding(ctx, &domain.WebhookBinding{
		ServiceID: svc.ID, Provider: svc.Provider, Status: domain.WebhookBindingError,
		LastError: pluginWebhookLifecycleError, UpdatedAt: time.Now().UTC(),
	})
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
