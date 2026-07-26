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
	branchFailure := false
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer project-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/repos/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"data": []map[string]any{{
					"id": 42, "full_name": "acme/platform", "default_branch": "trunk",
					"private": true, "html_url": providerServerURL(r) + "/acme/platform",
				}},
			})
		case "/api/v1/repos/acme/platform/branches":
			if branchFailure {
				http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
				return
			}
			if got := r.URL.Query().Get("limit"); got != "100" {
				t.Fatalf("branch list limit=%q want 100", got)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "feature/next"},
				{"name": "trunk", "protected": true},
			})
		default:
			http.NotFound(w, r)
		}
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

	branches := do(t, http.MethodGet, ts.URL+"/api/v1/services/"+service.ID+"/branches", consoleToken, nil)
	if branches.StatusCode != http.StatusOK {
		t.Fatalf("list branches status=%d want 200", branches.StatusCode)
	}
	var branchBody struct {
		Branches []serviceBranchView `json:"branches"`
		Default  string              `json:"default_branch"`
	}
	decode(t, branches, &branchBody)
	if branchBody.Default != "trunk" || len(branchBody.Branches) != 2 ||
		branchBody.Branches[0].Name != "trunk" || !branchBody.Branches[0].Default || !branchBody.Branches[0].Protected ||
		branchBody.Branches[1].Name != "feature/next" {
		t.Fatalf("branch response=%+v", branchBody)
	}

	validBranch := do(t, http.MethodPost, ts.URL+"/api/v1/services/"+service.ID+"/runs", consoleToken, map[string]any{
		"prompt": "ship it", "base_branch": "trunk",
	})
	if validBranch.StatusCode != http.StatusCreated {
		t.Fatalf("known base branch status=%d want 201", validBranch.StatusCode)
	}
	var run domain.Run
	decode(t, validBranch, &run)
	if run.BaseBranch != "trunk" {
		t.Fatalf("run base branch=%q want trunk", run.BaseBranch)
	}

	missingBranch := do(t, http.MethodPost, ts.URL+"/api/v1/services/"+service.ID+"/runs", consoleToken, map[string]any{
		"prompt": "ship it", "base_branch": "deleted-branch",
	})
	if missingBranch.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing base branch status=%d want 400", missingBranch.StatusCode)
	}
	var missingBranchBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, missingBranch, &missingBranchBody)
	if missingBranchBody.Error.Code != "invalid_base_branch" {
		t.Fatalf("missing branch error=%+v", missingBranchBody)
	}

	branchFailure = true
	providerFailure := do(t, http.MethodPost, ts.URL+"/api/v1/services/"+service.ID+"/runs", consoleToken, map[string]any{
		"prompt": "ship it", "base_branch": "trunk",
	})
	if providerFailure.StatusCode != http.StatusBadGateway {
		t.Fatalf("provider base branch failure status=%d want 502", providerFailure.StatusCode)
	}
	var providerFailureBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, providerFailure, &providerFailureBody)
	if providerFailureBody.Error.Code != "provider_error" {
		t.Fatalf("provider failure error=%+v", providerFailureBody)
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
