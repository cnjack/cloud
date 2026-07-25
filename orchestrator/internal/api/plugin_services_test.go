package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
)

func TestPluginBoundServiceCreation(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/search" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer project-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": []map[string]any{{
				"id": 42, "full_name": "acme/platform", "default_branch": "trunk",
				"private": true, "html_url": providerServerURL(r) + "/acme/platform",
			}},
		})
	}))
	defer providerServer.Close()

	ts, st, cfg := newCipherServer(t, nil, providerServer.URL)
	projectID := newProject(t, ts, "plugin-service")
	cipher, err := auth.NewCipher(cfg.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	accessToken, err := cipher.EncryptString("project-token")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: projectID, Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, ExternalAccountID: "7", ExternalAccount: "acme",
		Scopes: []string{"repository:write"}, AccessTokenEnc: accessToken,
		ConsentVersion: pluginConsentVersion, ConsentedAt: now, CreatedAt: now,
	}
	if err := st.CreatePluginInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{
		Provider: domain.PluginGitea, BaseURL: providerServer.URL, PluginEnabled: true,
		ConfigRevision: 1, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	bare := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/services", consoleToken, map[string]any{
		"name": "forbidden", "repo_url": providerServer.URL + "/acme/platform.git",
	})
	if bare.StatusCode != http.StatusBadRequest {
		t.Fatalf("bare Git service status=%d want 400", bare.StatusCode)
	}
	bare.Body.Close()

	created := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/services", consoleToken, map[string]any{
		"installation_id": installation.ID, "provider_repo_id": "42", "git_mode": "draft_pr",
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create Plugin Service status=%d want 201", created.StatusCode)
	}
	var service domain.Service
	decode(t, created, &service)
	if service.Name != "platform" || service.Provider != domain.ProviderGitea ||
		service.RepoOwnerName != "acme/platform" || service.DefaultBranch != "trunk" ||
		service.GitMode != domain.GitModeDraftPR {
		t.Fatalf("created Service=%+v", service)
	}
	binding, err := st.GetServiceRepositoryBinding(context.Background(), service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.InstallationID != installation.ID || binding.ProviderRepoID != "42" ||
		binding.CloneURL != providerServer.URL+"/acme/platform.git" {
		t.Fatalf("binding=%+v", binding)
	}
	retarget := do(t, http.MethodPatch, ts.URL+"/api/v1/services/"+service.ID, consoleToken, map[string]any{
		"provider": "gitlab", "owner_name": "other/private",
	})
	if retarget.StatusCode != http.StatusBadRequest {
		t.Fatalf("repository retarget status=%d want 400", retarget.StatusCode)
	}
	retarget.Body.Close()
	unchanged, err := st.GetServiceRepositoryBinding(context.Background(), service.ID)
	if err != nil || unchanged.InstallationID != installation.ID || unchanged.RepositoryPath != "acme/platform" {
		t.Fatalf("binding changed through Service PATCH: %+v err=%v", unchanged, err)
	}

	duplicate := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/services", consoleToken, map[string]any{
		"name": "second", "installation_id": installation.ID, "provider_repo_id": "42",
	})
	if duplicate.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate repository status=%d want 409", duplicate.StatusCode)
	}
	duplicate.Body.Close()
}

// providerServerURL exists only to keep the fixture payload deterministic; the
// production Service binding derives clone URLs from the DB-backed Provider URL.
func providerServerURL(r *http.Request) string {
	return "http://" + r.Host
}
