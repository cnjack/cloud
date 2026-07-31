package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestAutomationExecutionManualReplayCreatesOneRun(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Now().UTC().Truncate(time.Second)
	project := &domain.Project{ID: domain.NewID(), Name: "ledger", CreatedAt: now}
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "svc",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: now,
	}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	const callers = 24
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			executionID, runID := domain.NewID(), domain.NewID()
			value := &domain.AutomationExecution{
				ID: executionID, AutomationID: "automation", AutomationName: "Nightly",
				ProjectID: project.ID, ServiceID: service.ID, TriggerKind: "manual",
				EventKey: "manual:same-key", State: domain.AutomationExecutionQueued,
				OutputMode: domain.AutomationOutputRunOnly, RunID: runID,
				CreatedAt: now, UpdatedAt: now,
			}
			run := &domain.Run{
				ID: runID, ProjectID: project.ID, ServiceID: service.ID,
				Prompt: "work", Status: domain.StatusQueued, Phase: "Queued",
				OriginEventKey: value.EventKey, Attempt: 1, CreatedAt: now,
			}
			_, won, err := st.CreateAutomationExecution(ctx, value, run)
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			if won {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := created.Load(); got != 1 {
		t.Fatalf("created=%d want 1", got)
	}
	values, err := st.ListAutomationExecutions(ctx, "automation", "", nil, "", 20)
	if err != nil || len(values) != 1 {
		t.Fatalf("executions=%+v err=%v", values, err)
	}
	runs, err := st.ListRunsByService(ctx, service.ID, -1)
	if err != nil || len(runs) != 1 || runs[0].ID != values[0].RunID {
		t.Fatalf("runs=%+v execution=%+v err=%v", runs, values[0], err)
	}
}

func TestAutomationExecutionPaginationAndStateStayScoped(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Now().UTC().Truncate(time.Second)
	for _, value := range []domain.AutomationExecution{
		{ID: "e3", AutomationID: "a1", AutomationName: "A", ProjectID: "p", ServiceID: "s", TriggerKind: "scm", EventKey: "3", State: domain.AutomationExecutionBlocked, OutputMode: domain.AutomationOutputRunOnly, CreatedAt: now, UpdatedAt: now},
		{ID: "e2", AutomationID: "a1", AutomationName: "A", ProjectID: "p", ServiceID: "s", TriggerKind: "scm", EventKey: "2", State: domain.AutomationExecutionQueued, OutputMode: domain.AutomationOutputRunOnly, CreatedAt: now, UpdatedAt: now},
		{ID: "e1", AutomationID: "a2", AutomationName: "B", ProjectID: "p", ServiceID: "s", TriggerKind: "scm", EventKey: "1", State: domain.AutomationExecutionBlocked, OutputMode: domain.AutomationOutputRunOnly, CreatedAt: now, UpdatedAt: now},
	} {
		copyValue := value
		if _, _, err := st.CreateAutomationExecution(ctx, &copyValue, nil); err != nil {
			t.Fatal(err)
		}
	}
	first, err := st.ListAutomationExecutions(ctx, "a1", "", nil, "", 1)
	if err != nil || len(first) != 1 || first[0].ID != "e3" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := st.ListAutomationExecutions(ctx, "a1", "", &first[0].CreatedAt, first[0].ID, 1)
	if err != nil || len(second) != 1 || second[0].ID != "e2" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	blocked, err := st.ListAutomationExecutions(ctx, "a1", "blocked", nil, "", 20)
	if err != nil || len(blocked) != 1 || blocked[0].ID != "e3" {
		t.Fatalf("blocked=%+v err=%v", blocked, err)
	}
}

func TestAutomationExecutionStateFilterUsesLinkedRunProjection(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Now().UTC().Truncate(time.Second)
	project := &domain.Project{ID: domain.NewID(), Name: "ledger", CreatedAt: now}
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "svc",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: now,
	}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID,
		Prompt: "work", Status: domain.StatusQueued, Phase: "Queued",
		Attempt: 1, CreatedAt: now,
	}
	execution := &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: "automation", AutomationName: "Nightly",
		ProjectID: project.ID, ServiceID: service.ID, TriggerKind: "manual",
		EventKey: "manual:projected-state", State: domain.AutomationExecutionQueued,
		OutputMode: domain.AutomationOutputRunOnly, RunID: run.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := st.CreateAutomationExecution(ctx, execution, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkFailed(ctx, run.ID, "Failed", domain.FailureAgentError, "boom", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	terminal, err := st.ListAutomationExecutions(ctx, execution.AutomationID, "terminal", nil, "", 20)
	if err != nil || len(terminal) != 1 || terminal[0].ID != execution.ID {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
	queued, err := st.ListAutomationExecutions(ctx, execution.AutomationID, "queued", nil, "", 20)
	if err != nil || len(queued) != 0 {
		t.Fatalf("queued=%+v err=%v", queued, err)
	}
}

func TestAutomationExecutionStateFilterUsesCardRunProjection(t *testing.T) {
	ctx := context.Background()
	st, cardAutomation, trigger := seedPluginKanbanOccurrenceStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	execution := &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: "cron-automation", AutomationName: "Nightly card",
		ProjectID: "project", ServiceID: cardAutomation.ServiceID, TriggerKind: "cron",
		EventKey: "cron:card-projected-state", State: domain.AutomationExecutionAccepted,
		OutputMode:       domain.AutomationOutputCreateCard,
		CardAutomationID: cardAutomation.ID, CardWorkspaceID: "workspace",
		CardDocumentID: "card", CardPath: "jcode-automation/cron-automation/execution.md",
		CardState: "bound", CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := st.CreateAutomationExecution(ctx, execution, nil); err != nil {
		t.Fatal(err)
	}
	observed, err := st.ObservePluginKanbanCard(ctx, PluginKanbanObservation{
		AutomationID: cardAutomation.ID, ServiceID: cardAutomation.ServiceID,
		InstallationID: trigger.InstallationID, WorkspaceID: "workspace",
		DocumentID: execution.CardDocumentID, DocumentPath: execution.CardPath,
		TriggerColumn: trigger.TriggerColumn, DoneColumn: trigger.DoneColumn,
		ObservedColumn: trigger.TriggerColumn, EventKey: "card:event:" + domain.NewID(),
		ObservedAt: now,
	})
	if err != nil || observed.Occurrence == nil {
		t.Fatalf("observe=%+v err=%v", observed, err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: execution.ProjectID, ServiceID: execution.ServiceID,
		Prompt: "work", Status: domain.StatusQueued, Phase: "Queued",
		Origin: domain.RunOriginKanban, OriginAutomationID: cardAutomation.ID,
		OriginEventKey: observed.Occurrence.EventKey, Attempt: 1, CreatedAt: now,
	}
	if attached, err := st.CreatePluginKanbanOccurrenceRun(ctx, observed.Occurrence.ID, run); err != nil || !attached {
		t.Fatalf("attach=%v err=%v", attached, err)
	}
	if _, err := st.MarkFailed(ctx, run.ID, "Failed", domain.FailureAgentError, "boom", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	terminal, err := st.ListAutomationExecutions(ctx, execution.AutomationID, "terminal", nil, "", 20)
	if err != nil || len(terminal) != 1 || terminal[0].ID != execution.ID {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
	accepted, err := st.ListAutomationExecutions(ctx, execution.AutomationID, "accepted", nil, "", 20)
	if err != nil || len(accepted) != 0 {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
}
