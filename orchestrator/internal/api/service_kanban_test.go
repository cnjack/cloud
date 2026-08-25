package api

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

type serviceKanbanBoardValidator struct {
	board *jtype.Board
	calls *int
}

type serviceKanbanDiscovery struct {
	docs      []jtype.Doc
	documents map[string]*jtype.Document
	calls     *int
}

func (d serviceKanbanDiscovery) ListWorkspaces(context.Context) ([]jtype.Workspace, error) {
	return nil, nil
}

func (d serviceKanbanDiscovery) ListDocuments(context.Context, string) ([]jtype.Doc, error) {
	if d.calls != nil {
		*d.calls++
	}
	return d.docs, nil
}

func (d serviceKanbanDiscovery) GetDocument(_ context.Context, _ string, documentID string) (*jtype.Document, error) {
	if d.calls != nil {
		*d.calls++
	}
	if document, ok := d.documents[documentID]; ok {
		return document, nil
	}
	return nil, jtype.ErrDocNotFound
}

func (d serviceKanbanDiscovery) GetBoard(context.Context, string, string) (*jtype.Board, error) {
	return nil, jtype.ErrDocNotFound
}

func (d serviceKanbanDiscovery) GetBoardByDoc(context.Context, string, string) (*jtype.Board, error) {
	return nil, jtype.ErrDocNotFound
}

func (v serviceKanbanBoardValidator) GetBoard(context.Context, string, string) (*jtype.Board, error) {
	if v.calls != nil {
		*v.calls++
	}
	return v.board, nil
}

type serviceKanbanProxy struct {
	status int
	body   string
	bodies []string
	calls  int
}

func (p *serviceKanbanProxy) ProxyDocumentAPI(context.Context, string, string, io.Reader) (*http.Response, error) {
	p.calls++
	body := p.body
	if len(p.bodies) > 0 {
		body = p.bodies[0]
		p.bodies = p.bodies[1:]
	}
	return &http.Response{
		StatusCode: p.status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func newServiceKanbanServer(t *testing.T) (*httptest.Server, *store.MemStore, *Server) {
	t.Helper()
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t)})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	srv.boardValidatorFor = func(*jtype.Factory, string) boardValidator {
		return serviceKanbanBoardValidator{board: &jtype.Board{
			ID:      "b_board",
			Columns: []jtype.BoardColumn{{Key: "ai"}, {Key: "done"}},
		}}
	}
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	return ts, st, srv
}

func TestServiceKanbanPolicyAndCardExecutions(t *testing.T) {
	st := store.NewMemStore()
	srv := New(st, &config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t)},
		slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	ctx := context.Background()
	owner := mkUser(t, st, "receipt-owner")
	project := &domain.Project{ID: "receipt-project", Name: "Receipt project", OwnerUserID: owner.ID}
	service := &domain.Service{
		ID: "receipt-service", ProjectID: project.ID, Name: "payments-api",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "acme/payments", DefaultBranch: "main",
	}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	repositoryProvider := &domain.ProviderConfig{
		Provider: domain.PluginGitea, BaseURL: "https://gitea.test", PluginEnabled: true,
	}
	if err := st.UpsertProviderConfig(ctx, repositoryProvider); err != nil {
		t.Fatal(err)
	}
	repositoryInstallation := &domain.PluginInstallation{
		ID: "receipt-gitea", ProjectID: project.ID, Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, AccessTokenEnc: []byte("sealed-repository-token"),
		ConfigRevision: repositoryProvider.ConfigRevision,
	}
	if err := st.CreatePluginInstallation(ctx, repositoryInstallation); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertServiceRepositoryBinding(ctx, &domain.ServiceRepositoryBinding{
		ServiceID: service.ID, InstallationID: repositoryInstallation.ID,
		ProviderRepoID: "42", RepositoryPath: service.RepoOwnerName,
		CloneURL: "https://gitea.test/acme/payments.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	provider := &domain.ProviderConfig{
		Provider: domain.PluginJType, BaseURL: "https://jtype.test", PluginEnabled: true,
	}
	if err := st.UpsertProviderConfig(ctx, provider); err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID: "receipt-jtype", ProjectID: project.ID, Provider: domain.PluginJType,
		Status: domain.PluginStatusEnabled, WorkspaceID: "workspace",
		AccessTokenEnc: []byte("sealed"), ConfigRevision: provider.ConfigRevision,
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: "receipt-automation", ServiceID: service.ID, InstallationID: installation.ID,
		Name: "Kanban", TriggerKind: "kanban", RunKind: domain.RunKindAgent, Enabled: true,
		ModelID: "not-granted", ExecutionAccountID: owner.ID,
	}
	trigger := &domain.KanbanTrigger{
		AutomationID: automation.ID, InstallationID: installation.ID,
		BoardRef: "delivery", TriggerColumn: "agent", WorkColumn: "doing", DoneColumn: "done",
	}
	if err := st.CreatePluginAutomation(ctx, automation, nil, nil, trigger, nil); err != nil {
		t.Fatal(err)
	}
	observed, err := st.ObservePluginKanbanCard(ctx, store.PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: service.ID, InstallationID: installation.ID,
		WorkspaceID: "workspace", DocumentID: "card", DocumentPath: "cards/payment.md",
		TriggerColumn: "agent", DoneColumn: "done", ObservedColumn: "agent",
		EventKey: "event:1", EventSequence: int64PtrAPI(1), ActorDisplay: "jtype editor",
		ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPluginKanbanOccurrenceBlocked(
		ctx, observed.Occurrence.ID, "model_not_configured",
		"Choose an allowed model for this Service.", "repository_owner",
	); err != nil {
		t.Fatal(err)
	}
	firstRun := &domain.Run{
		ID: "receipt-run", ProjectID: project.ID, ServiceID: service.ID,
		Status: domain.StatusQueued, Origin: domain.RunOriginKanban,
		OriginAutomationID: automation.ID, OriginEventKey: observed.Occurrence.ID,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
	if attached, err := st.CreatePluginKanbanOccurrenceRun(ctx, observed.Occurrence.ID, firstRun); err != nil || !attached {
		t.Fatalf("attach first execution=%v err=%v", attached, err)
	}
	if _, err := st.ScheduleRun(ctx, firstRun.ID, "job", "token", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, firstRun.ID, "Running", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, firstRun.ID, "Succeeded", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	cardInput, cardOutput := int64(120), int64(30)
	if _, err := st.RecordUsageEvent(ctx, &domain.UsageEvent{
		ID: domain.NewID(), RequestID: domain.NewID(),
		SubjectKind: domain.UsageSubjectRun, SubjectID: firstRun.ID, RunID: firstRun.ID,
		ProjectID: project.ID, ProjectName: project.Name,
		ServiceID: service.ID, ServiceName: service.Name,
		CardWorkspace: "workspace", CardDocumentID: "card", CardPath: "cards/payment.md",
		InputTokens: &cardInput, OutputTokens: &cardOutput,
		CaptureStatus: domain.UsageCaptureReported,
		OccurredAt:    time.Now().UTC(), CreatedAt: time.Now().UTC(), Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if wrote, err := st.MarkPluginKanbanWriteback(
		ctx, automation.ID, "card", observed.Occurrence.ID,
		domain.StatusSucceeded, nil, time.Now().UTC(),
	); err != nil || !wrote {
		t.Fatalf("writeback=%v err=%v", wrote, err)
	}
	now := time.Now().UTC()
	if _, err := st.ObservePluginKanbanCard(ctx, store.PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: service.ID, InstallationID: installation.ID,
		WorkspaceID: "workspace", DocumentID: "card", DocumentPath: "cards/payment.md",
		TriggerColumn: "agent", DoneColumn: "done", ObservedColumn: "todo",
		EventKey: "event:2", EventSequence: int64PtrAPI(2), ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := st.ObservePluginKanbanCard(ctx, store.PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: service.ID, InstallationID: installation.ID,
		WorkspaceID: "workspace", DocumentID: "card", DocumentPath: "cards/payment.md",
		TriggerColumn: "agent", DoneColumn: "done", ObservedColumn: "agent",
		EventKey: "event:3", EventSequence: int64PtrAPI(3), ActorDisplay: "jtype editor",
		ObservedAt: now.Add(time.Second),
	})
	if err != nil || second.Occurrence == nil {
		t.Fatalf("second execution=%+v err=%v", second, err)
	}
	if _, err := st.SetPluginKanbanOccurrenceBlocked(
		ctx, second.Occurrence.ID, "model_not_configured",
		"Choose an allowed model for this Service.", "repository_owner",
	); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodGet, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board/policy", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("policy status=%d", resp.StatusCode)
	}
	var policy map[string]any
	decode(t, resp, &policy)
	if policy["repository_name"] != "payments-api" || policy["repository"] != "acme/payments" ||
		policy["trigger_column"].(map[string]any)["key"] != "agent" {
		t.Fatalf("policy=%+v", policy)
	}
	health := policy["health"].(map[string]any)
	if health["state"] != "blocked" || health["blocker"] != "model_not_authorized" {
		t.Fatalf("health=%+v", health)
	}
	if health["repair_role"] != "repository_owner" {
		t.Fatalf("health repair role=%+v", health)
	}

	spec, err := st.GetPluginAutomationSpec(ctx, automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	spec.Automation.LastError = "board_drift: the configured board or column no longer exists"
	if err := st.UpdatePluginAutomation(ctx, &spec.Automation); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board/policy", consoleToken, nil)
	var driftPolicy map[string]any
	decode(t, resp, &driftPolicy)
	driftHealth := driftPolicy["health"].(map[string]any)
	if driftHealth["state"] != "blocked" || driftHealth["blocker"] != "board_drift" {
		t.Fatalf("drift health=%+v", driftHealth)
	}

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/repositories/"+service.ID+
		"/agent-board/card-executions?workspace_id=workspace&document_path=cards%2Fpayment.md&limit=1",
		consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("executions status=%d", resp.StatusCode)
	}
	var executions struct {
		Claim struct {
			DocumentPath         string `json:"document_path"`
			ExternalRefAvailable bool   `json:"external_ref_available"`
		} `json:"claim"`
		Items      []map[string]any    `json:"items"`
		NextCursor *string             `json:"next_cursor"`
		Usage      domain.UsageSummary `json:"usage_summary"`
	}
	decode(t, resp, &executions)
	if executions.Claim.DocumentPath != "cards/payment.md" || !executions.Claim.ExternalRefAvailable {
		t.Fatalf("claim=%+v", executions.Claim)
	}
	if len(executions.Items) != 1 || executions.NextCursor == nil ||
		executions.Items[0]["status"] != "blocked" ||
		executions.Items[0]["reason_code"] != "model_not_configured" ||
		executions.Items[0]["requested_actor"].(map[string]any)["label"] != "jtype editor" {
		t.Fatalf("executions=%+v", executions.Items)
	}
	if executions.Usage.Requests != 1 || executions.Usage.Tokens.Input == nil ||
		*executions.Usage.Tokens.Input != 120 || executions.Usage.Tokens.Output == nil ||
		*executions.Usage.Tokens.Output != 30 {
		t.Fatalf("Card usage=%+v", executions.Usage)
	}
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/repositories/"+service.ID+
		"/agent-board/card-executions?workspace_id=workspace&document_path=cards%2Fpayment.md&limit=1&before="+
		*executions.NextCursor, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second executions page status=%d", resp.StatusCode)
	}
	var earlier struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	decode(t, resp, &earlier)
	if len(earlier.Items) != 1 || earlier.Items[0]["status"] != "terminal" ||
		earlier.Items[0]["outcome"] != "succeeded" || earlier.NextCursor != nil {
		t.Fatalf("earlier executions=%+v cursor=%v", earlier.Items, earlier.NextCursor)
	}

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/repositories/"+service.ID+
		"/agent-board/card-executions?workspace_id=workspace&document_path=cards%2Fpayment.md&before=not-a-cursor",
		consoleToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/repositories/"+service.ID+
		"/agent-board/card-executions?workspace_id=other&document_path=cards%2Fpayment.md",
		consoleToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func int64PtrAPI(value int64) *int64 { return &value }

func TestServiceKanbanExecutionMissingRunIsVisible(t *testing.T) {
	view := serviceKanbanExecutionView(domain.PluginKanbanOccurrence{
		ID: "occurrence", RunID: "deleted-run",
		State: domain.KanbanOccurrenceQueued,
	}, nil)
	if view.Status != domain.KanbanOccurrenceBlocked ||
		view.ReasonCode != "run_unavailable" ||
		view.Reason == nil ||
		view.RepairRole == nil || *view.RepairRole != "repository_owner" {
		t.Fatalf("missing Run view=%+v", view)
	}
}

func TestServiceKanbanUsesDefaultTriggerAndStaysOutOfAutomations(t *testing.T) {
	ts, st, srv := newServiceKanbanServer(t)
	validatorCalls := 0
	srv.boardValidatorFor = func(*jtype.Factory, string) boardValidator {
		return serviceKanbanBoardValidator{
			board: &jtype.Board{
				ID: "b_board",
				Columns: []jtype.BoardColumn{
					{Key: "ai", Name: "Agent queue"},
					{Key: "doing", Name: "Doing"},
					{Key: "review", Name: "Human review"},
					{Key: "done", Name: "Done"},
				},
			},
			calls: &validatorCalls,
		}
	}
	ctx := context.Background()
	now := time.Now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "service-kanban", CreatedAt: now}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := &domain.Service{ID: domain.NewID(), ProjectID: project.ID, Name: "repo", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitHub, RepoOwnerName: "acme/repo", DefaultBranch: "main", CreatedAt: now}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	member := mkUser(t, st, "service-kanban-member")
	contributor := mkUser(t, st, "service-kanban-contributor")
	viewer := mkUser(t, st, "service-kanban-viewer")
	project.OwnerUserID = member.ID
	if err := st.UpdateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	for _, membership := range []*domain.ProjectMember{
		{ProjectID: project.ID, UserID: member.ID, Role: domain.RoleOwner, CreatedAt: now},
		{ProjectID: project.ID, UserID: contributor.ID, Role: domain.RoleMember, CreatedAt: now},
		{ProjectID: project.ID, UserID: viewer.ID, Role: domain.RoleViewer, CreatedAt: now},
	} {
		if err := st.UpsertMember(ctx, membership); err != nil {
			t.Fatal(err)
		}
	}
	memberToken := mkSession(t, st, member.ID)
	contributorToken := mkSession(t, st, contributor.ID)
	viewerToken := mkSession(t, st, viewer.ID)
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginJType, BaseURL: "https://jtype.test", PluginEnabled: true, ConfigRevision: 1}); err != nil {
		t.Fatal(err)
	}
	sealed, err := srv.Cipher().EncryptString("jtype-secret-that-must-not-leak")
	if err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: project.ID, Provider: domain.PluginJType, Status: domain.PluginStatusEnabled, WorkspaceID: "workspace-1", AccessTokenEnc: sealed, ConfigRevision: 1, ConsentedAt: now, CreatedAt: now}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	modelProvider := &domain.ModelProvider{
		ID: domain.NewID(), Name: "agent-board-provider", Kind: "openai",
		BaseURL: "https://models.test/v1", AuthType: domain.ModelProviderAuthNone,
		CatalogMode: domain.ModelProviderCatalogDisabled, CreatedAt: now,
	}
	if err := st.CreateModelProvider(ctx, modelProvider); err != nil {
		t.Fatal(err)
	}
	workflowModel := &domain.Model{
		ID: domain.NewID(), ProviderID: modelProvider.ID, Name: "Agent model",
		BaseURL: modelProvider.BaseURL, ModelName: "openai/agent-model", ModelID: "agent-model",
		Capabilities: domain.ModelCapabilities{Reasoning: true, Tools: true},
		Source:       "custom", Enabled: true, CreatedAt: now,
	}
	if err := st.CreateModel(ctx, workflowModel); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantModelToAccount(ctx, workflowModel.ID, member.ID, member.ID); err != nil {
		t.Fatal(err)
	}

	// Workspace browsing is granted by the healthy Project Plugin before any
	// Service trigger exists.
	proxy := &serviceKanbanProxy{status: http.StatusOK, body: `[]`}
	srv.boardProxyFor = func(*jtype.Factory, string) jtypeBoardProxy { return proxy }
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents?workspace=workspace-1", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("browse before enable status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", contributorToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "delivery.board",
		"work_column":     "doing",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member Agent Board write status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "delivery.board",
		"work_column":     "doing",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusBadRequest || errorCode(t, resp) != "model_required" {
		t.Fatalf("missing workflow model status=%d", resp.StatusCode)
	}

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id":      installation.ID,
		"board_ref":            "delivery.board",
		"work_column":          "doing",
		"model_id":             "not-granted",
		"execution_account_id": member.ID,
		"enabled":              true,
	})
	if resp.StatusCode != http.StatusConflict || errorCode(t, resp) != "model_not_authorized" {
		t.Fatalf("unauthorized workflow model status=%d", resp.StatusCode)
	}

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id":      installation.ID,
		"board_ref":            "delivery.board",
		"work_column":          "doing",
		"model_id":             workflowModel.ID,
		"execution_account_id": member.ID,
		"enabled":              true,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reasoning model without effort status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id":      installation.ID,
		"board_ref":            "delivery.board",
		"work_column":          "doing",
		"model_id":             workflowModel.ID,
		"model_effort":         "high",
		"execution_account_id": member.ID,
		"enabled":              true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status=%d", resp.StatusCode)
	}
	var created domain.PluginAutomationSpec
	decode(t, resp, &created)
	if created.Automation.TriggerKind != "kanban" || created.Kanban == nil ||
		created.Automation.ModelID != workflowModel.ID || created.Automation.ModelEffort != "high" ||
		created.Automation.ExecutionAccountID != member.ID ||
		created.Kanban.BoardRef != "b_board" || created.Kanban.TriggerColumn != "ai" ||
		created.Kanban.TriggerLabel != "Agent queue" ||
		created.Kanban.WorkColumn != "doing" || created.Kanban.WorkLabel != "Doing" ||
		created.Kanban.DoneColumn != "done" || created.Kanban.DoneLabel != "Done" {
		t.Fatalf("binding=%+v", created)
	}
	if validatorCalls != 1 {
		t.Fatalf("initial path validation calls=%d want 1", validatorCalls)
	}
	resp = do(t, http.MethodDelete, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", contributorToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member Agent Board delete status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// GET returns the canonical board id. Round-tripping that current id must
	// work without scanning JType board documents again.
	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "b_board",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("canonical round-trip status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	if validatorCalls != 1 {
		t.Fatalf("canonical round-trip triggered live scan: calls=%d", validatorCalls)
	}

	plainModel := &domain.Model{
		ID: domain.NewID(), ProviderID: modelProvider.ID, Name: "Plain model",
		BaseURL: modelProvider.BaseURL, ModelName: "openai/plain-model", ModelID: "plain-model",
		Capabilities: domain.ModelCapabilities{Tools: true}, Source: "custom", Enabled: true, CreatedAt: now,
	}
	if err := st.CreateModel(ctx, plainModel); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantModelToAccount(ctx, plainModel.ID, member.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "b_board",
		"model_id":        plainModel.ID,
		"model_effort":    "",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plain model without effort status=%d", resp.StatusCode)
	}
	var plainUpdated domain.PluginAutomationSpec
	decode(t, resp, &plainUpdated)
	if plainUpdated.Automation.ModelID != plainModel.ID || plainUpdated.Automation.ModelEffort != "" {
		t.Fatalf("plain model policy=%+v", plainUpdated.Automation)
	}
	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "b_board",
		"model_id":        workflowModel.ID,
		"model_effort":    "high",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore reasoning model status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "delivery.board",
		"trigger_column":  "review",
		"done_column":     "done",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("column update status=%d", resp.StatusCode)
	}
	var updated domain.PluginAutomationSpec
	decode(t, resp, &updated)
	if updated.Kanban == nil || updated.Kanban.TriggerColumn != "review" ||
		updated.Kanban.TriggerLabel != "Human review" || updated.Kanban.DoneColumn != "done" {
		t.Fatalf("updated columns=%+v", updated.Kanban)
	}
	if validatorCalls != 1 {
		t.Fatalf("column update did not reuse cached board metadata: calls=%d", validatorCalls)
	}

	// A canonical board id may only round-trip unchanged. Column edits need the
	// board path so the server can fetch and validate the board schema.
	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "b_board",
		"trigger_column":  "ai",
		"done_column":     "done",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unvalidated canonical column update status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
	if validatorCalls != 1 {
		t.Fatalf("canonical column update triggered live scan: calls=%d", validatorCalls)
	}

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "delivery.board",
		"trigger_column":  "missing",
		"done_column":     "done",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("invalid trigger column status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
	if validatorCalls != 1 {
		t.Fatalf("invalid column check did not reuse cached board metadata: calls=%d", validatorCalls)
	}

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "b_untrusted",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("untrusted canonical id status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
	if validatorCalls != 1 {
		t.Fatalf("untrusted canonical id triggered live scan: calls=%d", validatorCalls)
	}

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/automations", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("automations status=%d", resp.StatusCode)
	}
	var list struct {
		Automations []domain.PluginAutomationSpec `json:"automations"`
	}
	decode(t, resp, &list)
	if len(list.Automations) != 0 {
		t.Fatalf("implicit Kanban leaked into Automations: %+v", list.Automations)
	}
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/automations", consoleToken, map[string]any{
		"service_id": service.ID, "name": "manual", "prompt_template": "task",
		"kanban": map[string]any{"installation_id": installation.ID, "board_ref": "b_board", "trigger_column": "ai"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("public Kanban Automation status=%d, want 400", resp.StatusCode)
	}

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/links", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("board links status=%d", resp.StatusCode)
	}
	var links struct {
		Links []boardEmbedLinkView `json:"links"`
	}
	decode(t, resp, &links)
	if len(links.Links) != 1 || links.Links[0].ServiceID != service.ID || links.Links[0].WorkspaceID != "workspace-1" || links.Links[0].BoardRef != "b_board" {
		t.Fatalf("board links=%+v", links.Links)
	}

	// Viewer execution-output links can discover and read the linked board, but
	// writes remain member+ and must stop before any upstream request.
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/links", viewerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer board links status=%d", resp.StatusCode)
	}
	var viewerLinks struct {
		Links []boardEmbedLinkView `json:"links"`
	}
	decode(t, resp, &viewerLinks)
	if len(viewerLinks.Links) != 1 || viewerLinks.Links[0].BoardRef != "b_board" {
		t.Fatalf("viewer board links=%+v", viewerLinks.Links)
	}
	before := proxy.calls
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents?workspace=workspace-1", viewerToken, nil)
	if resp.StatusCode != http.StatusOK || proxy.calls != before+1 {
		t.Fatalf("viewer board read status=%d upstream_calls=%d want=%d", resp.StatusCode, proxy.calls, before+1)
	}
	resp.Body.Close()
	before = proxy.calls
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents/save?workspace=workspace-1", viewerToken, map[string]any{
		"relativePath": "cards/viewer.md",
		"content":      "---\nboard: b_board\nstatus: ai\n---\nread only",
	})
	if resp.StatusCode != http.StatusForbidden || proxy.calls != before {
		t.Fatalf("viewer board write status=%d upstream_calls=%d want=%d", resp.StatusCode, proxy.calls, before)
	}
	resp.Body.Close()

	// A compromised upstream cannot reflect the server-held token to a member.
	proxy.body = `{"echo":"jtype-secret-that-must-not-leak"}`
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents?workspace=workspace-1", consoleToken, nil)
	reflectedBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || strings.Contains(string(reflectedBody), "jtype-secret-that-must-not-leak") {
		t.Fatalf("reflected credential response status=%d body=%s", resp.StatusCode, reflectedBody)
	}
	proxy.body = `{"echo":"` + base64.StdEncoding.EncodeToString([]byte("jtype-secret-that-must-not-leak")) + `"}`
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents?workspace=workspace-1", consoleToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("encoded credential response status=%d", resp.StatusCode)
	}

	// Even with a valid card body, the board proxy cannot overwrite an arbitrary
	// Markdown note outside the card namespace.
	proxy.body = `{}`
	before = proxy.calls
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents/save?workspace=workspace-1", consoleToken, map[string]any{
		"relativePath": "notes/private.md",
		"content":      "---\nboard: b_board\nstatus: ai\n---\nsecret",
	})
	if resp.StatusCode != http.StatusForbidden || proxy.calls != before {
		t.Fatalf("arbitrary note write status=%d upstream_calls=%d want=%d", resp.StatusCode, proxy.calls, before)
	}
	resp.Body.Close()

	// A member cannot overwrite a card from another board by changing its
	// frontmatter to the board enabled for this Service.
	proxy.bodies = []string{
		`[{"id":"other-card","relativePath":"cards/Other.md"}]`,
		`{"content":"---\nboard: b_other\nstatus: ai\n---\nother","contentHash":"other-hash"}`,
	}
	before = proxy.calls
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents/save?workspace=workspace-1", consoleToken, map[string]any{
		"relativePath":    "cards/other.md",
		"content":         "---\nboard: b_board\nstatus: ai\n---\nhijack",
		"baseContentHash": "other-hash",
	})
	if resp.StatusCode != http.StatusForbidden || proxy.calls != before+2 {
		t.Fatalf("cross-board overwrite status=%d upstream_calls=%d want=%d", resp.StatusCode, proxy.calls, before+2)
	}
	resp.Body.Close()

	// Cloud Automation Cards use a separate deterministic namespace. Members
	// may update one only after the proxy proves that exact Card already exists.
	managedPath := "jcode-automation/automation-1/execution-1.md"
	proxy.bodies = []string{
		`[{"id":"managed-card","relativePath":"` + managedPath + `"}]`,
		`{"content":"---\nboard: b_board\nstatus: ai\n---\nmanaged","contentHash":"managed-hash"}`,
		`{"relativePath":"` + managedPath + `","contentHash":"next-hash","mergeStatus":"accepted"}`,
	}
	before = proxy.calls
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents/save?workspace=workspace-1", memberToken, map[string]any{
		"relativePath":    managedPath,
		"content":         "---\nboard: b_board\nstatus: done\n---\nmanaged",
		"baseContentHash": "managed-hash",
	})
	if resp.StatusCode != http.StatusOK || proxy.calls != before+3 {
		t.Fatalf("managed Card update status=%d upstream_calls=%d want=%d", resp.StatusCode, proxy.calls, before+3)
	}
	resp.Body.Close()

	// A crafted request cannot create a new document in Cloud's managed
	// namespace; only the materializer owns that operation.
	proxy.bodies = []string{`[]`}
	before = proxy.calls
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents/save?workspace=workspace-1", memberToken, map[string]any{
		"relativePath": "jcode-automation/automation-1/forged.md",
		"content":      "---\nboard: b_board\nstatus: ai\n---\nforged",
	})
	if resp.StatusCode != http.StatusForbidden || proxy.calls != before+1 {
		t.Fatalf("managed Card create status=%d upstream_calls=%d want=%d", resp.StatusCode, proxy.calls, before+1)
	}
	resp.Body.Close()

	// A Card action creates the same durable occurrence as a trigger-column
	// entry. The client key makes transport retries idempotent, while a distinct
	// click is rejected until the active occurrence finishes.
	discoveryCalls := 0
	srv.jtypeDiscoveryFor = func(*jtype.Factory, string) jtypeDiscovery {
		return serviceKanbanDiscovery{
			docs: []jtype.Doc{{ID: "card-payment", Path: "cards/payment.md"}},
			documents: map[string]*jtype.Document{
				"card-payment": {
					Path:    "cards/payment.md",
					Content: "---\nboard: b_board\nstatus: backlog\n---\nImplement payments",
				},
			},
			calls: &discoveryCalls,
		}
	}
	manualURL := ts.URL + "/api/v1/repositories/" + service.ID + "/agent-board/occurrences"
	resp = do(t, http.MethodPost, manualURL, viewerToken, map[string]any{
		"workspace_id": "workspace-1", "document_path": "cards/payment.md",
		"idempotency_key": "click-1",
	})
	if resp.StatusCode != http.StatusForbidden || discoveryCalls != 0 {
		t.Fatalf("viewer manual trigger status=%d discovery_calls=%d", resp.StatusCode, discoveryCalls)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, manualURL, memberToken, map[string]any{
		"workspace_id": "workspace-1", "document_path": "cards/payment.md",
		"idempotency_key": "click-1",
	})
	if resp.StatusCode != http.StatusAccepted {
		code := errorCode(t, resp)
		history, _ := st.ListPluginKanbanOccurrences(ctx, created.Automation.ID, "card-payment", 10)
		t.Fatalf("manual trigger status=%d code=%s history=%+v", resp.StatusCode, code, history)
	}
	var firstManual struct {
		Occurrence serviceKanbanExecutionItem `json:"occurrence"`
		Replayed   bool                       `json:"replayed"`
	}
	decode(t, resp, &firstManual)
	if firstManual.Replayed || firstManual.Occurrence.ID == "" || firstManual.Occurrence.Status != domain.KanbanOccurrenceReceived {
		t.Fatalf("manual trigger response=%+v", firstManual)
	}
	resp = do(t, http.MethodPost, manualURL, memberToken, map[string]any{
		"workspace_id": "workspace-1", "document_path": "cards/payment.md",
		"idempotency_key": "click-1",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("manual replay status=%d", resp.StatusCode)
	}
	var replayedManual struct {
		Occurrence serviceKanbanExecutionItem `json:"occurrence"`
		Replayed   bool                       `json:"replayed"`
	}
	decode(t, resp, &replayedManual)
	if !replayedManual.Replayed || replayedManual.Occurrence.ID != firstManual.Occurrence.ID {
		t.Fatalf("manual replay response=%+v first=%+v", replayedManual, firstManual)
	}
	resp = do(t, http.MethodPost, manualURL, memberToken, map[string]any{
		"workspace_id": "workspace-1", "document_path": "cards/payment.md",
		"idempotency_key": "click-2",
	})
	if resp.StatusCode != http.StatusConflict || errorCode(t, resp) != "occurrence_active" {
		t.Fatalf("active manual trigger status=%d", resp.StatusCode)
	}

	resp = do(t, http.MethodDelete, ts.URL+"/api/v1/repositories/"+service.ID+"/agent-board", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict || errorCode(t, resp) != "active_occurrences" {
		t.Fatalf("disable with active occurrence status=%d", resp.StatusCode)
	}
}
