package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

const accountTaskModelID = "account-task-model"

func newAccountTaskServer(t *testing.T) (*httptest.Server, *store.MemStore, string, string) {
	t.Helper()
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/repos/acme/payments/branches" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "main", "protected": true}})
			return
		}
		if r.URL.Path != "/api/v1/repos/search" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": []map[string]any{{
				"id": 42, "full_name": "acme/payments", "default_branch": "main",
				"private": true, "html_url": accountTaskProviderURL(r),
			}},
		})
	}))
	t.Cleanup(providerServer.Close)

	st := store.NewMemStore()
	key := validTokenKey(t)
	cipher, err := auth.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	access, err := cipher.EncryptString("account-oauth-token")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	user := &domain.User{ID: domain.NewID(), DisplayName: "Alice", CreatedAt: now}
	identity := &domain.UserIdentity{
		ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: "alice-id",
		Username: "alice", AccessTokenEnc: access, CreatedAt: now,
	}
	if _, err := st.CreateUserWithIdentity(context.Background(), user, identity); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{
		Provider: domain.PluginGitea, BaseURL: providerServer.URL,
		LoginEnabled: true, PluginEnabled: true, ClientID: "client", UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	modelProvider := &domain.ModelProvider{
		ID: "account-task-model-provider", Name: "Account provider", Kind: "openai",
		BaseURL: "https://models.example/v1", AuthType: domain.ModelProviderAuthNone,
		CatalogMode: domain.ModelProviderCatalogDisabled, CreatedAt: now,
	}
	if err := st.CreateModelProvider(context.Background(), modelProvider); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateModel(context.Background(), &domain.Model{
		ID: accountTaskModelID, ProviderID: modelProvider.ID, Name: "Account model",
		BaseURL: modelProvider.BaseURL, ModelName: "openai/account-model", ModelID: "account-model",
		Capabilities: domain.ModelCapabilities{Tools: true}, Source: "custom", Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantModelToAccount(context.Background(), accountTaskModelID, user.ID, user.ID); err != nil {
		t.Fatal(err)
	}

	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: key})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, mkSession(t, st, user.ID), user.ID
}

func accountTaskProviderURL(r *http.Request) string {
	return "http://" + r.Host + "/acme/payments"
}

func TestAccountRepositoryCatalogAndDirectTask(t *testing.T) {
	ts, st, token, accountID := newAccountTaskServer(t)
	ctx := context.Background()
	other := &domain.User{ID: domain.NewID(), DisplayName: "Other", CreatedAt: time.Now().UTC()}
	otherIdentity := &domain.UserIdentity{
		ID: domain.NewID(), Provider: domain.ProviderGitHub, ProviderUID: "other-id",
		Username: "other", CreatedAt: time.Now().UTC(),
	}
	if _, err := st.CreateUserWithIdentity(ctx, other, otherIdentity); err != nil {
		t.Fatal(err)
	}
	sharedProject := &domain.Project{ID: domain.NewID(), Name: "Shared", OwnerUserID: other.ID, CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, sharedProject); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMember(ctx, &domain.ProjectMember{
		ProjectID: sharedProject.ID, UserID: accountID, Role: domain.RoleViewer, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	sharedRepoID := int64(42)
	sharedRepository := &domain.Service{
		ID: domain.NewID(), ProjectID: sharedProject.ID, Name: "shared-payments",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "acme/payments", ProviderRepoID: &sharedRepoID,
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateService(ctx, sharedRepository); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodGet, ts.URL+"/api/v1/account/repositories", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list account repositories: status=%d", resp.StatusCode)
	}
	var catalog accountRepositoryCatalog
	decode(t, resp, &catalog)
	if len(catalog.Repositories) != 1 || catalog.Repositories[0].ProviderRepoID != "42" ||
		catalog.Repositories[0].FullName != "acme/payments" || catalog.Repositories[0].RepositoryID != "" {
		t.Fatalf("catalog=%+v", catalog)
	}

	branchesResp := do(t, http.MethodGet, ts.URL+"/api/v1/account/repositories/gitea/42/branches", token, nil)
	if branchesResp.StatusCode != http.StatusOK {
		t.Fatalf("list Account Repository branches: status=%d", branchesResp.StatusCode)
	}
	var branchBody struct {
		Branches      []serviceBranchView `json:"branches"`
		DefaultBranch string              `json:"default_branch"`
	}
	decode(t, branchesResp, &branchBody)
	if branchBody.DefaultBranch != "main" || len(branchBody.Branches) != 1 || branchBody.Branches[0].Name != "main" || !branchBody.Branches[0].Default {
		t.Fatalf("branches=%+v default=%q", branchBody.Branches, branchBody.DefaultBranch)
	}

	invalidBranch := do(t, http.MethodPost, ts.URL+"/api/v1/account/tasks", token, map[string]any{
		"provider": "gitea", "provider_repo_id": "42", "prompt": "run on a missing branch",
		"base_branch": "missing", "model_id": accountTaskModelID, "session": true,
	})
	invalidBody, readErr := io.ReadAll(invalidBranch.Body)
	invalidBranch.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if invalidBranch.StatusCode != http.StatusBadRequest || !strings.Contains(string(invalidBody), "Repository") || strings.Contains(string(invalidBody), "Service") {
		t.Fatalf("invalid branch response: status=%d body=%s", invalidBranch.StatusCode, invalidBody)
	}

	start := func(prompt string) accountTaskResponse {
		resp := do(t, http.MethodPost, ts.URL+"/api/v1/account/tasks", token, map[string]any{
			"provider": "gitea", "provider_repo_id": "42", "prompt": prompt,
			"base_branch": "main", "model_id": accountTaskModelID, "session": true,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("start direct task: status=%d", resp.StatusCode)
		}
		var out accountTaskResponse
		decode(t, resp, &out)
		return out
	}

	first := start("fix the checkout flow")
	if first.Run.ID == "" || first.Run.ServiceID == "" || first.Repository.ID != first.Run.ServiceID {
		t.Fatalf("direct task response=%+v", first)
	}
	if first.Repository.RepoOwnerName != "acme/payments" || first.Repository.ProjectID == "" {
		t.Fatalf("materialized repository=%+v", first.Repository)
	}
	if first.Run.ModelID == nil || *first.Run.ModelID != accountTaskModelID {
		t.Fatalf("account-granted model was not selected: run=%+v", first.Run)
	}
	if first.Repository.ID == sharedRepository.ID {
		t.Fatal("Account composer reused a Repository owned by another Account")
	}
	binding, err := st.GetServiceRepositoryBinding(context.Background(), first.Repository.ID)
	if err != nil || binding.ProviderRepoID != "42" || binding.RepositoryPath != "acme/payments" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	installation, err := st.GetPluginInstallation(context.Background(), binding.InstallationID)
	if err != nil || installation.ConsentedBy == "" || !installation.TokenSet() {
		t.Fatalf("account execution installation=%+v err=%v", installation, err)
	}

	second := start("add a regression test")
	if second.Repository.ID != first.Repository.ID {
		t.Fatalf("same account repository created twice: first=%s second=%s", first.Repository.ID, second.Repository.ID)
	}
	repositories, err := st.ListRepositoriesForUser(context.Background(), installation.ConsentedBy)
	if err != nil {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	ownedCopies := 0
	for i := range repositories {
		if repositories[i].ID == first.Repository.ID {
			ownedCopies++
		}
	}
	if ownedCopies != 1 {
		t.Fatalf("materialized Account Repository count=%d repositories=%+v", ownedCopies, repositories)
	}
}

func TestAccountTaskRequiresHumanAccountAndKnownRepository(t *testing.T) {
	ts, _, token, _ := newAccountTaskServer(t)

	resp := do(t, http.MethodPost, ts.URL+"/api/v1/account/tasks", consoleToken, map[string]any{
		"provider": "gitea", "provider_repo_id": "42", "prompt": "run",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("service principal: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPost, ts.URL+"/api/v1/account/tasks", token, map[string]any{
		"provider": "gitea", "provider_repo_id": "404", "prompt": "run",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown repository: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
