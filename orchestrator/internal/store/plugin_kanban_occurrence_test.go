package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func seedPluginKanbanOccurrenceStore(t *testing.T) (*MemStore, *domain.PluginAutomation, *domain.KanbanTrigger) {
	t.Helper()
	ctx := context.Background()
	st := NewMemStore()
	if err := st.CreateProject(ctx, &domain.Project{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, &domain.Service{
		ID: "service", ProjectID: "project", Name: "payments", RepoKind: domain.RepoKindRaw,
	}); err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID: "jtype", ProjectID: "project", Provider: domain.PluginJType,
		Status: domain.PluginStatusEnabled, WorkspaceID: "workspace",
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: "automation", ServiceID: "service", InstallationID: installation.ID,
		Name: "Agent queue", TriggerKind: "kanban", RunKind: domain.RunKindAgent,
		Enabled: true,
	}
	trigger := &domain.KanbanTrigger{
		AutomationID: automation.ID, InstallationID: installation.ID,
		BoardRef: "delivery", TriggerColumn: "agent", DoneColumn: "done",
	}
	if err := st.CreatePluginAutomation(ctx, automation, nil, nil, trigger, nil); err != nil {
		t.Fatal(err)
	}
	return st, automation, trigger
}

func TestPGPluginKanbanOccurrenceRunClaimConcurrent(t *testing.T) {
	ctx := context.Background()
	st, seedRunID := pgTestStore(t)
	seedRun, err := st.GetRun(ctx, seedRunID)
	if err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: seedRun.ProjectID, Provider: domain.PluginJType,
		Status: domain.PluginStatusEnabled, WorkspaceID: "workspace", Scopes: []string{},
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: domain.NewID(), ServiceID: seedRun.ServiceID, InstallationID: installation.ID,
		Name: "Agent queue", TriggerKind: "kanban", RunKind: domain.RunKindAgent, Enabled: true,
	}
	trigger := &domain.KanbanTrigger{
		AutomationID: automation.ID, InstallationID: installation.ID,
		BoardRef: "delivery", TriggerColumn: "agent", DoneColumn: "done",
	}
	if err := st.CreatePluginAutomation(ctx, automation, nil, nil, trigger, nil); err != nil {
		t.Fatal(err)
	}
	result, err := st.ObservePluginKanbanCard(ctx, PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: automation.ServiceID,
		InstallationID: installation.ID, WorkspaceID: installation.WorkspaceID,
		DocumentID: "card", DocumentPath: "cards/card.md",
		TriggerColumn: "agent", DoneColumn: "done", ObservedColumn: "agent",
		EventKey: "event:1", EventSequence: int64PtrStore(1), ObservedAt: time.Now().UTC(),
	})
	if err != nil || result.Occurrence == nil {
		t.Fatalf("observe = %+v, %v", result, err)
	}

	const concurrent = 12
	start := make(chan struct{})
	results := make(chan bool, concurrent)
	errs := make(chan error, concurrent)
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			run := &domain.Run{
				ID: domain.NewID(), ProjectID: seedRun.ProjectID, ServiceID: seedRun.ServiceID,
				Status: domain.StatusQueued, Origin: domain.RunOriginKanban,
				OriginAutomationID: automation.ID, OriginEventKey: result.Occurrence.ID,
				CreatedAt: time.Now().UTC(),
			}
			attached, claimErr := st.CreatePluginKanbanOccurrenceRun(ctx, result.Occurrence.ID, run)
			results <- attached
			errs <- claimErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	attachedCount := 0
	for attached := range results {
		if attached {
			attachedCount++
		}
	}
	for claimErr := range errs {
		if claimErr != nil {
			t.Fatalf("concurrent claim: %v", claimErr)
		}
	}
	if attachedCount != 1 {
		t.Fatalf("attached count = %d, want 1", attachedCount)
	}
	history, err := st.ListPluginKanbanOccurrences(ctx, automation.ID, "card", 10)
	if err != nil || len(history) != 1 || history[0].RunID == "" {
		t.Fatalf("history = %+v, %v", history, err)
	}
	page, err := st.ListPluginKanbanCardExecutions(
		ctx, automation.ID, automation.ServiceID, installation.WorkspaceID,
		"cards/card.md", nil, 10,
	)
	if err != nil || len(page) != 1 || page[0].ID != result.Occurrence.ID {
		t.Fatalf("Card page=%+v err=%v", page, err)
	}
	runID := history[0].RunID
	if _, err := st.ScheduleRun(ctx, runID, "job", "token", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, runID, "Running", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, runID, "Succeeded", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if marked, err := st.MarkPluginKanbanWritebackUnavailable(
		ctx, automation.ID, "card", result.Occurrence.ID,
		domain.StatusSucceeded, nil,
		"Card was deleted.", time.Now().UTC(),
	); err != nil || !marked {
		t.Fatalf("mark unavailable=%v err=%v", marked, err)
	}
	claim, err := st.GetPluginKanbanClaimByPath(
		ctx, automation.ID, installation.WorkspaceID, "cards/card.md",
	)
	if err != nil || claim.ExternalRefAvailable {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	history, err = st.ListPluginKanbanOccurrences(ctx, automation.ID, "card", 10)
	if err != nil || history[0].WritebackState != "unavailable" ||
		history[0].Outcome != string(domain.StatusSucceeded) {
		t.Fatalf("unavailable history=%+v err=%v", history, err)
	}
	if active, err := st.HasActivePluginKanbanOccurrences(ctx, automation.ID); err != nil || active {
		t.Fatalf("terminal unavailable occurrence active=%v err=%v", active, err)
	}
	restored, err := st.ObservePluginKanbanCard(ctx, PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: automation.ServiceID,
		InstallationID: installation.ID, WorkspaceID: installation.WorkspaceID,
		DocumentID: "card", DocumentPath: "cards/card.md",
		TriggerColumn: "agent", DoneColumn: "done", ObservedColumn: "agent",
		EventKey: "event:restored", EventSequence: int64PtrStore(2),
		ObservedAt: time.Now().UTC(),
	})
	if err != nil || !restored.Created || restored.Occurrence == nil ||
		restored.Occurrence.ID == result.Occurrence.ID {
		t.Fatalf("restored Card occurrence=%+v err=%v", restored, err)
	}
}

func int64PtrStore(value int64) *int64 { return &value }

func observePluginCard(
	t *testing.T,
	st *MemStore,
	automation *domain.PluginAutomation,
	trigger *domain.KanbanTrigger,
	eventKey, column string,
	sequence *int64,
) *PluginKanbanObservationResult {
	t.Helper()
	result, err := st.ObservePluginKanbanCard(context.Background(), PluginKanbanObservation{
		AutomationID:   automation.ID,
		ServiceID:      automation.ServiceID,
		InstallationID: trigger.InstallationID,
		WorkspaceID:    "workspace",
		DocumentID:     "card",
		DocumentPath:   "cards/payment.md",
		TriggerColumn:  trigger.TriggerColumn,
		DoneColumn:     trigger.DoneColumn,
		ObservedColumn: column,
		EventKey:       eventKey,
		EventSequence:  sequence,
		ActorDisplay:   "External editor",
		ObservedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPluginKanbanOccurrenceLifecycle(t *testing.T) {
	st, automation, trigger := seedPluginKanbanOccurrenceStore(t)

	first := observePluginCard(t, st, automation, trigger, "bootstrap:automation:card", "agent", nil)
	if !first.Created || first.Occurrence == nil {
		t.Fatalf("bootstrap result = %+v, want one occurrence", first)
	}
	if first.Occurrence.State != domain.KanbanOccurrenceReceived {
		t.Fatalf("bootstrap state = %q, want received", first.Occurrence.State)
	}

	replay := observePluginCard(t, st, automation, trigger, "bootstrap:automation:card", "agent", nil)
	if replay.Created || replay.Occurrence == nil || replay.Occurrence.ID != first.Occurrence.ID {
		t.Fatalf("replay result = %+v, want existing occurrence %q", replay, first.Occurrence.ID)
	}

	editSequence := int64(2)
	edit := observePluginCard(t, st, automation, trigger, "event:2", "agent", &editSequence)
	if edit.Created || edit.Occurrence != nil || edit.SuppressedReason != PluginKanbanAlreadyInTrigger {
		t.Fatalf("in-column edit result = %+v", edit)
	}

	run := &domain.Run{
		ID: "run-1", ProjectID: "project", ServiceID: "service", Status: domain.StatusQueued,
		Origin: domain.RunOriginKanban, OriginAutomationID: automation.ID,
		OriginEventKey: first.Occurrence.ID, CreatedAt: time.Now().UTC(),
	}
	attached, err := st.CreatePluginKanbanOccurrenceRun(context.Background(), first.Occurrence.ID, run)
	if err != nil || !attached {
		t.Fatalf("attach first run = %v, %v", attached, err)
	}

	leaveSequence := int64(3)
	leave := observePluginCard(t, st, automation, trigger, "event:3", "", &leaveSequence)
	if leave.Created || leave.Occurrence != nil {
		t.Fatalf("leave result = %+v, want observation only", leave)
	}

	reenterSequence := int64(4)
	active := observePluginCard(t, st, automation, trigger, "event:4", "agent", &reenterSequence)
	if active.Created || active.SuppressedReason != PluginKanbanAlreadyRunning {
		t.Fatalf("active re-entry result = %+v", active)
	}

	if _, err := st.ScheduleRun(context.Background(), run.ID, "job", "token", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(context.Background(), run.ID, "Running", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(context.Background(), run.ID, "Succeeded", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	pendingLeaveSequence := int64(5)
	observePluginCard(t, st, automation, trigger, "event:5", "todo", &pendingLeaveSequence)
	pendingSequence := int64(6)
	pending := observePluginCard(t, st, automation, trigger, "event:6", "agent", &pendingSequence)
	if pending.Created || pending.SuppressedReason != PluginKanbanWritebackPending {
		t.Fatalf("writeback-pending re-entry result = %+v", pending)
	}

	if wrote, err := st.MarkPluginKanbanWriteback(
		context.Background(), automation.ID, "card", first.Occurrence.ID,
		domain.StatusSucceeded, nil, time.Now().UTC(),
	); err != nil || !wrote {
		t.Fatalf("mark writeback = %v, %v", wrote, err)
	}
	secondLeaveSequence := int64(7)
	observePluginCard(t, st, automation, trigger, "event:7", "todo", &secondLeaveSequence)
	secondEntrySequence := int64(8)
	second := observePluginCard(t, st, automation, trigger, "event:8", "agent", &secondEntrySequence)
	if !second.Created || second.Occurrence == nil || second.Occurrence.ID == first.Occurrence.ID {
		t.Fatalf("second entry result = %+v, want a new occurrence", second)
	}

	history, err := st.ListPluginKanbanOccurrences(context.Background(), automation.ID, "card", 10)
	if err != nil || len(history) != 2 {
		t.Fatalf("history = %+v, %v; want two occurrences", history, err)
	}
}

func TestPluginKanbanStaleWritebackCannotCompleteNewOccurrence(t *testing.T) {
	ctx := context.Background()
	st, automation, trigger := seedPluginKanbanOccurrenceStore(t)
	first := observePluginCard(t, st, automation, trigger, "event:1", "agent", int64PtrStore(1))
	run := &domain.Run{
		ID: "run-stale-writeback", ProjectID: "project", ServiceID: "service",
		Status: domain.StatusQueued, Origin: domain.RunOriginKanban,
		OriginAutomationID: automation.ID, OriginEventKey: first.Occurrence.ID,
		CreatedAt: time.Now().UTC(),
	}
	if attached, err := st.CreatePluginKanbanOccurrenceRun(ctx, first.Occurrence.ID, run); err != nil || !attached {
		t.Fatalf("attach first run=%v err=%v", attached, err)
	}
	if _, err := st.ScheduleRun(ctx, run.ID, "job", "token", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListPluginKanbanRunsAwaitingWriteback(ctx)
	if err != nil || len(pending) != 1 || pending[0].Occurrence == nil {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	staleOccurrenceID := pending[0].Occurrence.ID
	if wrote, err := st.MarkPluginKanbanWriteback(
		ctx, automation.ID, "card", staleOccurrenceID,
		domain.StatusSucceeded, nil, time.Now().UTC(),
	); err != nil || !wrote {
		t.Fatalf("first writeback=%v err=%v", wrote, err)
	}

	observePluginCard(t, st, automation, trigger, "event:2", "todo", int64PtrStore(2))
	second := observePluginCard(t, st, automation, trigger, "event:3", "agent", int64PtrStore(3))
	if !second.Created || second.Occurrence == nil {
		t.Fatalf("second occurrence=%+v", second)
	}
	if wrote, err := st.MarkPluginKanbanWriteback(
		ctx, automation.ID, "card", staleOccurrenceID,
		domain.StatusSucceeded, nil, time.Now().UTC(),
	); err != nil || wrote {
		t.Fatalf("stale writeback=%v err=%v, want false,nil", wrote, err)
	}
	claim, err := st.GetPluginKanbanClaimByPath(
		ctx, automation.ID, "workspace", "cards/payment.md",
	)
	if err != nil || claim.WritebackAt != nil || claim.LatestOccurrenceID != second.Occurrence.ID {
		t.Fatalf("new claim was corrupted by stale writeback: %+v err=%v", claim, err)
	}
	history, err := st.ListPluginKanbanOccurrences(ctx, automation.ID, "card", 10)
	if err != nil || history[0].ID != second.Occurrence.ID ||
		history[0].State != domain.KanbanOccurrenceReceived {
		t.Fatalf("new occurrence was corrupted by stale writeback: %+v err=%v", history, err)
	}
}

func TestPluginKanbanOccurrenceRunClaimIsAtomic(t *testing.T) {
	st, automation, trigger := seedPluginKanbanOccurrenceStore(t)
	first := observePluginCard(t, st, automation, trigger, "bootstrap:automation:card", "agent", nil)

	run1 := &domain.Run{
		ID: "run-1", ProjectID: "project", ServiceID: "service", Status: domain.StatusQueued,
		Origin: domain.RunOriginKanban, OriginAutomationID: automation.ID,
		OriginEventKey: first.Occurrence.ID, CreatedAt: time.Now().UTC(),
	}
	run2 := *run1
	run2.ID = "run-2"

	attached, err := st.CreatePluginKanbanOccurrenceRun(context.Background(), first.Occurrence.ID, run1)
	if err != nil || !attached {
		t.Fatalf("first claim = %v, %v", attached, err)
	}
	attached, err = st.CreatePluginKanbanOccurrenceRun(context.Background(), first.Occurrence.ID, &run2)
	if err != nil || attached {
		t.Fatalf("second claim = %v, %v; want a harmless no-op", attached, err)
	}
	if _, err := st.GetRun(context.Background(), run2.ID); err != ErrNotFound {
		t.Fatalf("losing run was persisted: %v", err)
	}
}

func TestManualPluginKanbanOccurrenceCanRerunAfterTerminalUnavailable(t *testing.T) {
	st, automation, trigger := seedPluginKanbanOccurrenceStore(t)
	ctx := context.Background()
	first, err := st.ObservePluginKanbanCard(ctx, PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: automation.ServiceID,
		InstallationID: trigger.InstallationID, WorkspaceID: "workspace",
		DocumentID: "card", DocumentPath: "cards/payment.md",
		TriggerColumn: trigger.TriggerColumn, DoneColumn: trigger.DoneColumn,
		ObservedColumn: "backlog", EventKey: "manual:first", Manual: true,
		ObservedAt: time.Now().UTC(),
	})
	if err != nil || first.Occurrence == nil {
		t.Fatalf("first manual occurrence=%+v err=%v", first, err)
	}
	st.mu.Lock()
	terminal := st.pluginKanbanOccurrences[first.Occurrence.ID]
	terminal.State = domain.KanbanOccurrenceTerminal
	terminal.WritebackState = "unavailable"
	st.pluginKanbanOccurrences[terminal.ID] = terminal
	st.mu.Unlock()

	if active, err := st.HasActivePluginKanbanOccurrences(ctx, automation.ID); err != nil || active {
		t.Fatalf("terminal unavailable occurrence active=%v err=%v", active, err)
	}
	second, err := st.ObservePluginKanbanCard(ctx, PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: automation.ServiceID,
		InstallationID: trigger.InstallationID, WorkspaceID: "workspace",
		DocumentID: "card", DocumentPath: "cards/payment.md",
		TriggerColumn: trigger.TriggerColumn, DoneColumn: trigger.DoneColumn,
		ObservedColumn: "backlog", EventKey: "manual:second", Manual: true,
		ObservedAt: time.Now().UTC().Add(time.Second),
	})
	if err != nil || !second.Created || second.Occurrence == nil || second.Occurrence.ID == first.Occurrence.ID {
		t.Fatalf("second manual occurrence=%+v err=%v", second, err)
	}
}

func TestPluginKanbanCardExecutionClaimAvailabilityAndCursor(t *testing.T) {
	st, automation, trigger := seedPluginKanbanOccurrenceStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 31, 2, 0, 0, 0, time.UTC)

	st.mu.Lock()
	st.pluginKanbanClaims[pluginKanbanClaimKey(automation.ID, "card")] = domain.PluginKanbanClaim{
		AutomationID: automation.ID, InstallationID: trigger.InstallationID,
		DocumentID: "card", DocumentPath: "cards/payment.md", WorkspaceID: "workspace",
		ExternalRefAvailable: true, CreatedAt: base, UpdatedAt: base,
	}
	for index, id := range []string{"occ-a", "occ-b", "occ-c"} {
		createdAt := base.Add(time.Duration(index) * time.Minute)
		st.pluginKanbanOccurrences[id] = domain.PluginKanbanOccurrence{
			ID: id, AutomationID: automation.ID, ServiceID: automation.ServiceID,
			InstallationID: trigger.InstallationID, WorkspaceID: "workspace",
			DocumentID: "card", DocumentPath: "cards/payment.md", EventKey: id,
			EntryColumn: trigger.TriggerColumn, State: domain.KanbanOccurrenceTerminal,
			WritebackState: "complete", CreatedAt: createdAt, UpdatedAt: createdAt,
		}
	}
	st.mu.Unlock()

	page, err := st.ListPluginKanbanCardExecutions(
		ctx, automation.ID, automation.ServiceID, "workspace", "cards/payment.md", nil, 2,
	)
	if err != nil || len(page) != 2 || page[0].ID != "occ-c" || page[1].ID != "occ-b" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	before := &PluginKanbanOccurrenceCursor{CreatedAt: page[1].CreatedAt, ID: page[1].ID}
	page, err = st.ListPluginKanbanCardExecutions(
		ctx, automation.ID, automation.ServiceID, "workspace", "cards/payment.md", before, 2,
	)
	if err != nil || len(page) != 1 || page[0].ID != "occ-a" {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
	marked, err := st.MarkPluginKanbanCardUnavailable(
		ctx, automation.ID, "workspace", "cards/payment.md", base.Add(5*time.Minute),
	)
	if err != nil || !marked {
		t.Fatalf("mark unavailable=%v err=%v", marked, err)
	}
	claim, err := st.GetPluginKanbanClaimByPath(
		ctx, automation.ID, "workspace", "cards/payment.md",
	)
	if err != nil || claim.ExternalRefAvailable {
		t.Fatalf("claim=%+v err=%v; want unavailable", claim, err)
	}
}

func TestPluginKanbanHistorySurvivesServiceAndRunDeletion(t *testing.T) {
	st, automation, trigger := seedPluginKanbanOccurrenceStore(t)
	ctx := context.Background()
	first := observePluginCard(
		t, st, automation, trigger, "event:1", trigger.TriggerColumn, int64PtrStore(1),
	)
	run := &domain.Run{
		ID: "deleting-run", ProjectID: "project", ServiceID: automation.ServiceID,
		Status: domain.StatusQueued, Origin: domain.RunOriginKanban,
		OriginAutomationID: automation.ID, OriginEventKey: first.Occurrence.ID,
		CreatedAt: time.Now().UTC(),
	}
	if attached, err := st.CreatePluginKanbanOccurrenceRun(ctx, first.Occurrence.ID, run); err != nil || !attached {
		t.Fatalf("attach=%v err=%v", attached, err)
	}
	if err := st.DeleteService(ctx, automation.ServiceID); err != nil {
		t.Fatal(err)
	}
	history, err := st.ListPluginKanbanOccurrences(ctx, automation.ID, "card", 10)
	if err != nil || len(history) != 1 || history[0].RunID != "" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	claim, err := st.GetPluginKanbanClaimByPath(
		ctx, automation.ID, "workspace", "cards/payment.md",
	)
	if err != nil || claim.LatestOccurrenceID != first.Occurrence.ID {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
}

func TestPluginKanbanBlockedOccurrenceResumesAndTracksReceiptPhase(t *testing.T) {
	st, automation, trigger := seedPluginKanbanOccurrenceStore(t)
	first := observePluginCard(t, st, automation, trigger, "event:1", "agent", int64PtrStore(1))

	blocked, err := st.SetPluginKanbanOccurrenceBlocked(
		context.Background(), first.Occurrence.ID,
		"model_not_configured", "Choose a model for this Repository.", "repository_owner",
	)
	if err != nil || blocked.State != domain.KanbanOccurrenceBlocked ||
		blocked.ReceiptPhase != "blocked" || blocked.ReasonCode != "model_not_configured" ||
		blocked.WritebackState != "not_required" {
		t.Fatalf("blocked occurrence = %+v, %v", blocked, err)
	}
	pending, err := st.ListPluginKanbanDispatchableOccurrences(context.Background(), automation.ID, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != first.Occurrence.ID {
		t.Fatalf("dispatchable = %+v, %v", pending, err)
	}
	receipts, err := st.ListPluginKanbanReceiptPending(context.Background(), automation.ID, 10)
	if err != nil || len(receipts) != 1 || receipts[0].ReceiptPhase != "blocked" {
		t.Fatalf("pending receipts = %+v, %v", receipts, err)
	}
	if err := st.MarkPluginKanbanOccurrenceReceipt(
		context.Background(), first.Occurrence.ID, "blocked", nil, "temporary JType failure",
	); err != nil {
		t.Fatal(err)
	}
	receipts, _ = st.ListPluginKanbanReceiptPending(context.Background(), automation.ID, 10)
	if len(receipts) != 1 || receipts[0].WritebackError == "" {
		t.Fatalf("failed receipt was hidden: %+v", receipts)
	}
	writtenAt := time.Now().UTC()
	if err := st.MarkPluginKanbanOccurrenceReceipt(
		context.Background(), first.Occurrence.ID, "blocked", &writtenAt, "",
	); err != nil {
		t.Fatal(err)
	}
	receipts, _ = st.ListPluginKanbanReceiptPending(context.Background(), automation.ID, 10)
	if len(receipts) != 0 {
		t.Fatalf("written blocked receipt remained pending: %+v", receipts)
	}
	changed, err := st.SetPluginKanbanOccurrenceBlocked(
		context.Background(), first.Occurrence.ID,
		"repository_not_configured", "Configure a repository.", "repository_owner",
	)
	if err != nil || changed.ReceiptWrittenAt != nil {
		t.Fatalf("changed blocker did not reopen external receipt: %+v, %v", changed, err)
	}

	run := &domain.Run{
		ID: "blocked-run", ProjectID: "project", ServiceID: "service", Status: domain.StatusQueued,
		Origin: domain.RunOriginKanban, OriginAutomationID: automation.ID,
		OriginEventKey: first.Occurrence.ID, CreatedAt: time.Now().UTC(),
	}
	attached, err := st.CreatePluginKanbanOccurrenceRun(context.Background(), first.Occurrence.ID, run)
	if err != nil || !attached {
		t.Fatalf("resume = %v, %v", attached, err)
	}
	active, err := st.SetPluginKanbanOccurrenceReceiptPhase(
		context.Background(), first.Occurrence.ID, PluginKanbanAlreadyRunning,
	)
	if err != nil || active.ReceiptPhase != PluginKanbanAlreadyRunning ||
		active.ReceiptWrittenAt != nil {
		t.Fatalf("active receipt phase=%+v err=%v", active, err)
	}
	activeWrittenAt := time.Now().UTC()
	if err := st.MarkPluginKanbanOccurrenceReceipt(
		context.Background(), first.Occurrence.ID,
		PluginKanbanAlreadyRunning, &activeWrittenAt, "",
	); err != nil {
		t.Fatal(err)
	}
	active, err = st.SetPluginKanbanOccurrenceReceiptPhase(
		context.Background(), first.Occurrence.ID, PluginKanbanAlreadyRunning,
	)
	if err != nil || active.ReceiptWrittenAt == nil {
		t.Fatalf("same receipt phase lost idempotency marker: %+v err=%v", active, err)
	}
	writebackPending, err := st.SetPluginKanbanOccurrenceReceiptPhase(
		context.Background(), first.Occurrence.ID, PluginKanbanWritebackPending,
	)
	if err != nil || writebackPending.ReceiptWrittenAt != nil {
		t.Fatalf("new receipt phase did not reopen projection: %+v err=%v", writebackPending, err)
	}
	history, err := st.ListPluginKanbanOccurrences(context.Background(), automation.ID, "card", 10)
	if err != nil || len(history) != 1 || history[0].State != domain.KanbanOccurrenceQueued ||
		history[0].ReceiptPhase != PluginKanbanWritebackPending ||
		history[0].ReceiptWrittenAt != nil {
		t.Fatalf("resumed history = %+v, %v", history, err)
	}
}

func TestHasActivePluginKanbanOccurrences(t *testing.T) {
	st, automation, trigger := seedPluginKanbanOccurrenceStore(t)
	result, err := st.ObservePluginKanbanCard(context.Background(), PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: automation.ServiceID,
		InstallationID: trigger.InstallationID, WorkspaceID: "workspace",
		DocumentID: "card-active", DocumentPath: "cards/active.md",
		TriggerColumn: trigger.TriggerColumn, ObservedColumn: trigger.TriggerColumn,
		EventKey: "active-event", ObservedAt: time.Now().UTC(),
	})
	if err != nil || result.Occurrence == nil {
		t.Fatalf("observe = %+v, %v", result, err)
	}
	active, err := st.HasActivePluginKanbanOccurrences(context.Background(), automation.ID)
	if err != nil || !active {
		t.Fatalf("active = %v, %v; want true", active, err)
	}
}
