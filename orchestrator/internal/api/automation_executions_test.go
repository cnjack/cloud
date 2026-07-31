package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestAutomationExecutionRBACAndManualIdempotency(t *testing.T) {
	f := setupRBAC(t)
	now := time.Now().UTC()
	automation := &domain.PluginAutomation{
		ID: domain.NewID(), ServiceID: f.serviceID, Name: "Run now",
		TriggerKind: "cron", PromptTemplate: "inspect the repository",
		Enabled: true, CreatedAt: now,
	}
	if err := f.st.CreatePluginAutomation(
		t.Context(), automation, nil, nil, nil,
		&domain.CronTrigger{
			AutomationID: automation.ID, CronExpr: "0 9 * * *",
			OutputMode: domain.AutomationOutputRunOnly,
		},
	); err != nil {
		t.Fatal(err)
	}
	listURL := f.ts.URL + "/api/v1/automations/" + automation.ID + "/executions"
	resp := do(t, http.MethodGet, listURL, f.tokens["viewer"], nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer list=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPost, listURL, f.tokens["viewer"], map[string]any{"idempotency_key": "same-browser-key"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer run now=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPost, listURL, f.tokens["member"], map[string]any{"idempotency_key": "same-browser-key"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first run now=%d want 201", resp.StatusCode)
	}
	var first automationExecutionView
	decode(t, resp, &first)
	if first.State != domain.AutomationExecutionQueued || first.Run == nil ||
		first.RequestedActor == nil || first.RequestedActor.Label != "member" {
		t.Fatalf("first=%+v", first)
	}

	resp = do(t, http.MethodPost, listURL, f.tokens["member"], map[string]any{"idempotency_key": "same-browser-key"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay=%d want 200", resp.StatusCode)
	}
	var replay automationExecutionView
	decode(t, resp, &replay)
	if replay.ID != first.ID || replay.Run == nil || replay.Run.ID != first.Run.ID {
		t.Fatalf("first=%+v replay=%+v", first, replay)
	}
	runs, err := f.st.ListRunsByService(t.Context(), f.serviceID, -1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}

	resp = do(t, http.MethodGet, listURL, f.tokens["viewer"], nil)
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	body, _ := json.Marshal(raw)
	if string(body) == "" || containsAny(string(body), "event_key", "prompt_snapshot", "inspect the repository") {
		t.Fatalf("public ledger leaked internal fields: %s", body)
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if len(candidate) > 0 && len(value) >= len(candidate) {
			for i := 0; i+len(candidate) <= len(value); i++ {
				if value[i:i+len(candidate)] == candidate {
					return true
				}
			}
		}
	}
	return false
}

func TestAutomationExecutionRejectsInvalidCursorAndState(t *testing.T) {
	f := setupRBAC(t)
	automation := &domain.PluginAutomation{
		ID: domain.NewID(), ServiceID: f.serviceID, Name: "History",
		TriggerKind: "cron", PromptTemplate: "work", Enabled: true, CreatedAt: time.Now().UTC(),
	}
	if err := f.st.CreatePluginAutomation(t.Context(), automation, nil, nil, nil, &domain.CronTrigger{
		AutomationID: automation.ID, CronExpr: "0 9 * * *", OutputMode: domain.AutomationOutputRunOnly,
	}); err != nil {
		t.Fatal(err)
	}
	base := f.ts.URL + "/api/v1/automations/" + automation.ID + "/executions"
	for _, suffix := range []string{"?before=bad", "?state=succeeded"} {
		resp := do(t, http.MethodGet, base+suffix, f.tokens["viewer"], nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s status=%d want 400", suffix, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestKanbanAutomationExecutionRoutesFailVisibly(t *testing.T) {
	f := setupRBAC(t)
	now := time.Now().UTC()
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: f.projectID, Provider: domain.PluginJType,
		Status: domain.PluginStatusEnabled, ConsentVersion: "v1",
		ConsentedAt: now, CreatedAt: now,
	}
	if err := f.st.CreatePluginInstallation(t.Context(), installation); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: domain.NewID(), ServiceID: f.serviceID, InstallationID: installation.ID,
		Name: "Service Kanban", TriggerKind: "kanban", PromptTemplate: "{{card}}",
		Enabled: true, CreatedAt: now,
	}
	if err := f.st.CreatePluginAutomation(t.Context(), automation, nil, nil, &domain.KanbanTrigger{
		AutomationID: automation.ID, InstallationID: installation.ID,
		BoardRef: "board", TriggerColumn: "agent",
	}, nil); err != nil {
		t.Fatal(err)
	}
	base := f.ts.URL + "/api/v1/automations/" + automation.ID + "/executions"
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var body any
		if method == http.MethodPost {
			body = map[string]any{"idempotency_key": "kanban-route-key"}
		}
		resp := do(t, method, base, f.tokens["member"], body)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s status=%d want 404", method, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := do(t, http.MethodGet, base+"/missing", f.tokens["viewer"], nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("detail status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAutomationExecutionCardDeepLinkUsesWorkspaceContract(t *testing.T) {
	f := setupRBAC(t)
	now := time.Now().UTC()
	automation := &domain.PluginAutomation{
		ID: domain.NewID(), ServiceID: f.serviceID, Name: "Create triage Card",
		TriggerKind: "cron", PromptTemplate: "triage", Enabled: true, CreatedAt: now,
	}
	if err := f.st.CreatePluginAutomation(t.Context(), automation, nil, nil, nil, &domain.CronTrigger{
		AutomationID: automation.ID, CronExpr: "0 9 * * *",
		OutputMode: domain.AutomationOutputCreateCard,
	}); err != nil {
		t.Fatal(err)
	}
	execution := &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: automation.ID, AutomationName: automation.Name,
		ProjectID: f.projectID, ServiceID: f.serviceID, TriggerKind: "cron",
		EventKey: "cron:deep-link", State: domain.AutomationExecutionAccepted,
		OutputMode: domain.AutomationOutputCreateCard,
		CardPath:   "jcode-automation/" + automation.ID + "/payment + review.md",
		CardState:  "bound", CreatedAt: now, UpdatedAt: now,
	}
	if _, created, err := f.st.CreateAutomationExecution(t.Context(), execution, nil); err != nil || !created {
		t.Fatalf("create execution created=%v err=%v", created, err)
	}

	resp := do(t, http.MethodGet,
		f.ts.URL+"/api/v1/automations/"+automation.ID+"/executions/"+execution.ID,
		f.tokens["viewer"], nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("execution detail status=%d", resp.StatusCode)
	}
	var view automationExecutionView
	decode(t, resp, &view)
	if view.Card == nil || !view.Card.Available {
		t.Fatalf("card=%+v", view.Card)
	}
	href, err := url.Parse(view.Card.Href)
	if err != nil {
		t.Fatal(err)
	}
	if href.Path != "/projects/"+f.projectID ||
		href.Query().Get("service") != f.serviceID ||
		href.Query().Get("tab") != "automations" ||
		href.Query().Get("kanban") != "1" ||
		href.Query().Get("card") != execution.CardPath {
		t.Fatalf("Card href=%q query=%v", view.Card.Href, href.Query())
	}
}
