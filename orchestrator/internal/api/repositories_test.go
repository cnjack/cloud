package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestRepositoryRoutesReplaceServiceRoutes(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()
	project := &domain.Project{ID: "hidden-project", Name: "Personal", CreatedAt: time.Now().UTC()}
	repository := &domain.Service{
		ID: "repository-1", ProjectID: project.ID, Name: "payments",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "acme/payments", DefaultBranch: "main", CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, repository); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodGet, ts.URL+"/api/v1/repositories", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list repositories: status=%d", resp.StatusCode)
	}
	var listed struct {
		Repositories []domain.Service `json:"repositories"`
	}
	decode(t, resp, &listed)
	if len(listed.Repositories) != 1 || listed.Repositories[0].ID != repository.ID {
		t.Fatalf("repositories=%+v", listed.Repositories)
	}

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/repositories/"+repository.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get repository: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	for _, path := range []string{
		"/api/v1/services/" + repository.ID,
		"/api/v1/services/" + repository.ID + "/runs",
		"/api/v1/services/" + repository.ID + "/kanban",
	} {
		resp = do(t, http.MethodGet, ts.URL+path, consoleToken, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("legacy Service route %s: status=%d want 404", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestRepositoryListKeepsAPIKeyInsideItsHiddenContainer(t *testing.T) {
	f := setupAPIKeyFixture(t)
	key := createAPIKey(t, f, "repository-list")

	resp := do(t, http.MethodGet, f.ts.URL+"/api/v1/repositories", key.Key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list repositories with API key: status=%d", resp.StatusCode)
	}
	var listed struct {
		Repositories []domain.Service `json:"repositories"`
	}
	decode(t, resp, &listed)
	if len(listed.Repositories) != 1 || listed.Repositories[0].ID != f.serviceA {
		t.Fatalf("API key repositories=%+v; cross-container Repository leaked", listed.Repositories)
	}
}

func TestRepositoryAgentBoardPersistsExecutionPolicy(t *testing.T) {
	_, st, _ := newServiceKanbanServer(t)
	now := time.Now().UTC()
	owner := mkUser(t, st, "repository-owner")
	project := &domain.Project{
		ID: "agent-board-project", Name: "Personal", OwnerUserID: owner.ID, CreatedAt: now,
	}
	if err := st.CreateProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMember(context.Background(), &domain.ProjectMember{
		ProjectID: project.ID, UserID: owner.ID, Role: domain.RoleOwner, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	repository := &domain.Service{
		ID: "agent-board-repository", ProjectID: project.ID, Name: "payments",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "https://git.example/acme/payments.git",
		DefaultBranch: "main", CreatedAt: now,
	}
	if err := st.CreateService(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginInstallation(context.Background(), &domain.PluginInstallation{
		ID: "jtype-installation", ProjectID: project.ID, Provider: domain.PluginJType,
		Status: domain.PluginStatusEnabled, WorkspaceID: "workspace", AccessTokenEnc: []byte("sealed"),
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: "agent-board-automation", ServiceID: repository.ID,
		InstallationID: "jtype-installation", Name: "Agent Board", TriggerKind: "kanban",
		RunKind: domain.RunKindAgent, PromptTemplate: "Complete the card.",
		ModelID: "model-1", ModelEffort: "high", ExecutionAccountID: owner.ID,
		Enabled: true, CreatedBy: owner.ID, CreatedAt: now,
	}
	trigger := &domain.KanbanTrigger{
		AutomationID: automation.ID, InstallationID: automation.InstallationID,
		BoardRef: "b_board", TriggerColumn: "agent", WorkColumn: "doing",
	}
	if err := st.CreatePluginAutomation(context.Background(), automation, nil, nil, trigger, nil); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetPluginAutomation(context.Background(), automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionAccountID != owner.ID || got.ModelID != automation.ModelID || got.ModelEffort != "high" {
		t.Fatalf("execution policy did not round-trip: %+v", got)
	}
}

func TestAccountModelsExposeDirectGrantsOnly(t *testing.T) {
	ts, st, _ := newTestServer(t)
	now := time.Now().UTC()
	owner := mkUser(t, st, "account-model-owner")
	token := mkSession(t, st, owner.ID)
	provider := &domain.ModelProvider{
		ID: "account-model-provider", Name: "Provider", Kind: "openai",
		BaseURL: "https://models.test/v1", AuthType: domain.ModelProviderAuthNone,
		CatalogMode: domain.ModelProviderCatalogDisabled, CreatedAt: now,
	}
	model := &domain.Model{
		ID: "account-model", ProviderID: provider.ID, Name: "Agent Model",
		BaseURL: provider.BaseURL, ModelName: "openai/agent", ModelID: "agent",
		Capabilities: domain.ModelCapabilities{Reasoning: true, Tools: true},
		Source:       "custom", Enabled: true, CreatedAt: now,
	}
	if err := st.CreateModelProvider(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateModel(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantModelToAccount(context.Background(), model.ID, owner.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/account/models", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("account models status=%d", resp.StatusCode)
	}
	var body struct {
		Models []map[string]any `json:"models"`
	}
	decode(t, resp, &body)
	if len(body.Models) != 1 || body.Models[0]["id"] != model.ID || body.Models[0]["model_name"] != model.ModelName {
		t.Fatalf("account models=%+v", body.Models)
	}
}
