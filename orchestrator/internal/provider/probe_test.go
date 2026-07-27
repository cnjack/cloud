package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestProbeConfigurationReadsOfficialInstanceVersionEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		provider domain.ProviderKind
		path     string
		body     string
		version  string
	}{
		{name: "gitlab", provider: domain.PluginGitLab, path: "/api/v4/version", body: `{"version":"17.11.2","revision":"abc"}`, version: "17.11.2"},
		{name: "gitea", provider: domain.PluginGitea, path: "/api/v1/version", body: `{"version":"1.25.1"}`, version: "1.25.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Fatalf("path=%s want %s", r.URL.Path, tt.path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			got, err := ProbeConfiguration(context.Background(), ConfigProbeInput{Provider: tt.provider, BaseURL: srv.URL})
			if err != nil || got.Version != tt.version {
				t.Fatalf("probe=%+v err=%v", got, err)
			}
		})
	}
}

func TestProbeConfigurationTreatsProtectedGitLabVersionAsPartialNotBroken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/version" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	got, err := ProbeConfiguration(context.Background(), ConfigProbeInput{Provider: domain.PluginGitLab, BaseURL: srv.URL})
	if err != nil || got.Version != "" {
		t.Fatalf("protected GitLab version probe=%+v err=%v", got, err)
	}
}

func TestProbeAuthenticatedVersionUsesProjectGrantForProtectedInstances(t *testing.T) {
	tests := []struct {
		name     string
		provider domain.ProviderKind
		path     string
		scheme   string
	}{
		{name: "gitlab", provider: domain.PluginGitLab, path: "/api/v4/version", scheme: "Bearer"},
		{name: "gitea", provider: domain.PluginGitea, path: "/api/v1/version", scheme: "Bearer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Fatalf("path=%s want %s", r.URL.Path, tt.path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer project-grant" {
					t.Fatalf("authorization=%q", got)
				}
				_, _ = w.Write([]byte(`{"version":"17.11.2"}`))
			}))
			defer srv.Close()
			version, err := ProbeAuthenticatedVersion(context.Background(), tt.provider, srv.URL, "project-grant", tt.scheme)
			if err != nil || version != "17.11.2" {
				t.Fatalf("version=%q err=%v", version, err)
			}
		})
	}
}

func TestProbeConfigurationChecksJTypeHealthWithoutCreatingDeviceGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected JType probe path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	if _, err := ProbeConfiguration(context.Background(), ConfigProbeInput{Provider: domain.PluginJType, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubAppIssuerVerifyChecksAppIdentity(t *testing.T) {
	issuer, err := NewGitHubAppIssuer("1234", testGitHubAppKey(t))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/app" || r.Header.Get("Authorization") == "" {
			t.Fatalf("unexpected GitHub App verification request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1234,"slug":"jcode-cloud-app"}`))
	}))
	defer srv.Close()
	issuer.apiBase, issuer.http = srv.URL, srv.Client()
	if err := issuer.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	metadata, err := issuer.VerifyMetadata(context.Background())
	if err != nil || metadata.Slug != "jcode-cloud-app" {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
}
