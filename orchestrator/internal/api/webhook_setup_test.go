package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// webhookSetupProvider is a provider seam that records the exact credential
// path the endpoint used, while retaining the regular Provider methods through
// FakeProvider. It deliberately lives only in the test rig.
type webhookSetupProvider struct {
	*provider.FakeProvider
	calls []webhookSetupCall
	err   error
}

type webhookSetupCall struct {
	owner   string
	repo    string
	hookURL string
	secret  string
}

func (p *webhookSetupProvider) EnsureCommentWebhook(
	_ context.Context,
	owner, repo, hookURL, secret string,
) error {
	p.calls = append(p.calls, webhookSetupCall{
		owner: owner, repo: repo, hookURL: hookURL, secret: secret,
	})
	return p.err
}

type webhookSetupFactoryCall struct {
	provider domain.GitProvider
	token    string
	scheme   string
}

type webhookSetupFactory struct {
	client provider.Provider
	err    error
	calls  []webhookSetupFactoryCall
}

func (f *webhookSetupFactory) PRClient(
	prov domain.GitProvider,
	token, scheme string,
) (provider.Provider, error) {
	f.calls = append(f.calls, webhookSetupFactoryCall{provider: prov, token: token, scheme: scheme})
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
}

type webhookSetupFixtureOptions struct {
	raw               bool
	webhookConfigured bool
	matchingIdentity  bool
	role              domain.Role
}

type webhookSetupFixture struct {
	ts       *httptest.Server
	st       *store.MemStore
	server   *Server
	service  *domain.Service
	token    string
	provider *webhookSetupProvider
	factory  *webhookSetupFactory
}

func newWebhookSetupFixture(t *testing.T, opts webhookSetupFixtureOptions) webhookSetupFixture {
	t.Helper()
	if opts.role == "" {
		opts.role = domain.RoleMember
	}

	st := store.NewMemStore()
	cfg := &config.Config{
		ConsoleToken:  consoleToken,
		GiteaURL:      "http://gitea.test",
		GiteaToken:    "cluster-gitea-pat",
		AuthTokenKey:  validTokenKey(t),
		WebhookURL:    "http://orchestrator.test/webhooks/gitea",
		WebhookSecret: "webhook-secret",
		OAuthProviders: []config.OAuthProviderConfig{{
			ID: "gitea", ClientID: "cid", ClientSecret: "secret",
			ExternalURL: "http://gitea.test", InternalURL: "http://gitea.test",
		}},
	}
	if !opts.webhookConfigured {
		cfg.WebhookURL = ""
		cfg.WebhookSecret = ""
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)

	// The first persisted user becomes a cluster admin. Seed an unrelated account
	// first so the fixture's member/viewer role is tested as written instead of
	// being elevated by bootstrap semantics.
	if _, err := st.CreateUserWithIdentity(context.Background(),
		&domain.User{ID: domain.NewID(), DisplayName: "bootstrap", CreatedAt: time.Now().UTC()},
		&domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: "bootstrap", Username: "bootstrap", AccessTokenEnc: []byte("unused"), CreatedAt: time.Now().UTC()},
	); err != nil {
		t.Fatal(err)
	}

	// A human user needs an identity to authenticate. Use a non-matching GitHub
	// identity for the missing-Gitea-OAuth case so the service principal never
	// accidentally exercises the fallback path.
	identityProvider := domain.ProviderGitea
	identityUsername := "alice"
	if !opts.matchingIdentity {
		identityProvider = domain.ProviderGitHub
		identityUsername = "octocat"
	}
	access, err := srv.Cipher().EncryptString("user-oauth-token")
	if err != nil {
		t.Fatal(err)
	}
	user := &domain.User{ID: domain.NewID(), DisplayName: "Alice", CreatedAt: time.Now().UTC()}
	if _, err := st.CreateUserWithIdentity(context.Background(), user, &domain.UserIdentity{
		ID: domain.NewID(), Provider: identityProvider, ProviderUID: "42",
		Username: identityUsername, AccessTokenEnc: access, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	project := &domain.Project{ID: domain.NewID(), Name: "webhook setup", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMember(context.Background(), &domain.ProjectMember{
		ProjectID: project.ID, UserID: user.ID, Role: opts.role, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Deliberately bind the service to a bot credential as well: a successful
	// webhook setup must still use the member's OAuth token, never this bot token
	// or cfg.GiteaToken.
	botToken, err := srv.Cipher().EncryptString("integration-bot-token")
	if err != nil {
		t.Fatal(err)
	}
	integration := &domain.Integration{
		ID: domain.NewID(), ProjectID: project.ID, Name: "bot", Provider: domain.ProviderGitea,
		Host: "gitea.test", CredType: domain.CredTypePAT, TokenEnc: botToken,
		BotUsername: "jcode-bot", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateIntegration(context.Background(), integration); err != nil {
		t.Fatal(err)
	}

	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "repo", DefaultBranch: "main",
		GitMode: domain.GitModeDraftPR, CreatedAt: time.Now().UTC(),
	}
	if opts.raw {
		service.RepoKind = domain.RepoKindRaw
		service.RawRepoURL = "git://gitea.test/acme/repo.git"
	} else {
		service.RepoKind = domain.RepoKindProvider
		service.Provider = domain.ProviderGitea
		service.RepoOwnerName = "acme/repo"
		service.IntegrationID = &integration.ID
	}
	if err := st.CreateService(context.Background(), service); err != nil {
		t.Fatal(err)
	}

	hooker := &webhookSetupProvider{FakeProvider: provider.NewFakeProvider()}
	factory := &webhookSetupFactory{client: hooker}
	srv.factory = factory
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return webhookSetupFixture{
		ts: ts, st: st, server: srv, service: service, token: mkSession(t, st, user.ID),
		provider: hooker, factory: factory,
	}
}

func webhookSetupErrorCode(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Error.Code
}

func TestEnsureServiceWebhookUsesMemberOAuthOnly(t *testing.T) {
	fixture := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
		matchingIdentity:  true,
		webhookConfigured: true,
	})

	response := do(t, http.MethodPost, fixture.ts.URL+"/api/v1/services/"+fixture.service.ID+"/webhook", fixture.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", response.StatusCode)
	}
	var body struct {
		Provider string `json:"provider"`
		Endpoint string `json:"endpoint"`
		Status   string `json:"status"`
	}
	decode(t, response, &body)
	if body.Provider != "gitea" || body.Endpoint != "http://orchestrator.test/webhooks/gitea" || body.Status != "synced" {
		t.Fatalf("response=%+v", body)
	}
	if len(fixture.factory.calls) != 1 {
		t.Fatalf("factory calls=%d want 1", len(fixture.factory.calls))
	}
	if got := fixture.factory.calls[0]; got.token != "user-oauth-token" || got.scheme != "Bearer" {
		t.Fatalf("credential=%+v want the requesting user's Bearer OAuth token", got)
	}
	if len(fixture.provider.calls) != 1 {
		t.Fatalf("webhook calls=%d want 1", len(fixture.provider.calls))
	}
	if got := fixture.provider.calls[0]; got.owner != "acme" || got.repo != "repo" || got.secret != "webhook-secret" {
		t.Fatalf("hook call=%+v", got)
	}
}

func TestEnsureServiceWebhookFailsVisiblyWhenOAuthIsMissing(t *testing.T) {
	fixture := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
		matchingIdentity:  false,
		webhookConfigured: true,
	})
	response := do(t, http.MethodPost, fixture.ts.URL+"/api/v1/services/"+fixture.service.ID+"/webhook", fixture.token, nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", response.StatusCode)
	}
	if code := webhookSetupErrorCode(t, response); code != "oauth_not_connected" {
		t.Fatalf("error code=%q want oauth_not_connected", code)
	}
	if len(fixture.factory.calls) != 0 || len(fixture.provider.calls) != 0 {
		t.Fatal("must not fall back to a bot credential or cluster PAT")
	}
}

func TestEnsureServiceWebhookFailsVisiblyWhenProviderOAuthIsNotConfigured(t *testing.T) {
	fixture := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
		matchingIdentity:  true,
		webhookConfigured: true,
	})
	fixture.server.oauth = map[domain.GitProvider]provider.OAuthProvider{}

	response := do(t, http.MethodPost, fixture.ts.URL+"/api/v1/services/"+fixture.service.ID+"/webhook", fixture.token, nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", response.StatusCode)
	}
	if code := webhookSetupErrorCode(t, response); code != "oauth_not_configured" {
		t.Fatalf("error code=%q want oauth_not_configured", code)
	}
	if len(fixture.factory.calls) != 0 {
		t.Fatal("an unconfigured provider must not call the provider client")
	}
}

func TestEnsureServiceWebhookFailsVisiblyForUnavailableProviderOrReceiver(t *testing.T) {
	tests := []struct {
		name string
		opts webhookSetupFixtureOptions
		code string
	}{
		{
			name: "raw service",
			opts: webhookSetupFixtureOptions{raw: true, matchingIdentity: true, webhookConfigured: true},
			code: "provider_webhook_unavailable",
		},
		{
			name: "receiver not configured",
			opts: webhookSetupFixtureOptions{matchingIdentity: true, webhookConfigured: false},
			code: "webhook_not_configured",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newWebhookSetupFixture(t, tt.opts)
			response := do(t, http.MethodPost, fixture.ts.URL+"/api/v1/services/"+fixture.service.ID+"/webhook", fixture.token, nil)
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status=%d want 409", response.StatusCode)
			}
			if code := webhookSetupErrorCode(t, response); code != tt.code {
				t.Fatalf("error code=%q want %q", code, tt.code)
			}
			if len(fixture.factory.calls) != 0 {
				t.Fatal("unavailable setup must not call the provider")
			}
		})
	}
}

func TestEnsureServiceWebhookSurfacesProviderFailureAndEnforcesMemberRole(t *testing.T) {
	t.Run("provider failure", func(t *testing.T) {
		fixture := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
			matchingIdentity:  true,
			webhookConfigured: true,
		})
		fixture.provider.err = errors.New("provider rejected webhook creation")
		response := do(t, http.MethodPost, fixture.ts.URL+"/api/v1/services/"+fixture.service.ID+"/webhook", fixture.token, nil)
		if response.StatusCode != http.StatusBadGateway {
			t.Fatalf("status=%d want 502", response.StatusCode)
		}
		if code := webhookSetupErrorCode(t, response); code != "webhook_registration_failed" {
			t.Fatalf("error code=%q want webhook_registration_failed", code)
		}
	})

	t.Run("viewer cannot change webhook setup", func(t *testing.T) {
		fixture := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
			matchingIdentity:  true,
			webhookConfigured: true,
			role:              domain.RoleViewer,
		})
		response := do(t, http.MethodPost, fixture.ts.URL+"/api/v1/services/"+fixture.service.ID+"/webhook", fixture.token, nil)
		if response.StatusCode != http.StatusForbidden {
			response.Body.Close()
			t.Fatalf("status=%d want 403", response.StatusCode)
		}
		response.Body.Close()
		if len(fixture.factory.calls) != 0 {
			t.Fatal("viewer request must not call the provider")
		}
	})
}
