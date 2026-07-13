package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func TestServiceRepoHTMLURLUsesServerOwnedProviderHost(t *testing.T) {
	st := store.NewMemStore()
	srv := New(st, &config.Config{
		OAuthProviders: []config.OAuthProviderConfig{{ID: "gitea", ExternalURL: "https://git.example.test"}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	svc := &domain.Service{RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "acme/repo"}
	if got := srv.serviceRepoHTMLURL(context.Background(), svc); got != "https://git.example.test/acme/repo" {
		t.Fatalf("repo url=%q", got)
	}

	integration := &domain.Integration{ID: "i1", Host: "https://enterprise.example.test/base"}
	if err := st.CreateIntegration(context.Background(), integration); err != nil {
		t.Fatal(err)
	}
	svc.IntegrationID = &integration.ID
	if got := srv.serviceRepoHTMLURL(context.Background(), svc); got != "https://enterprise.example.test/base/acme/repo" {
		t.Fatalf("integration repo url=%q", got)
	}

	svc.RepoKind = domain.RepoKindRaw
	if got := srv.serviceRepoHTMLURL(context.Background(), svc); got != "" {
		t.Fatalf("raw repo must not expose provider action, got %q", got)
	}
}
