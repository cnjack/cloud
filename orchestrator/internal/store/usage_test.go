package store

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func usageInt64(value int64) *int64 { return &value }

func TestUsageRollupDeduplicatesAndAggregatesUTCHour(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	at := time.Date(2026, time.January, 2, 3, 12, 0, 0, time.UTC)
	first := &domain.UsageEvent{
		ID: "event-1", RequestID: "request-1",
		SubjectKind: domain.UsageSubjectRun, SubjectID: "run-1", RunID: "run-1",
		ProjectID: "project-1", ModelID: "model-1",
		InputTokens: usageInt64(10), OutputTokens: usageInt64(4),
		CaptureStatus: domain.UsageCaptureReported, OccurredAt: at, CreatedAt: at, Version: 1,
	}
	if recorded, err := st.RecordUsageEvent(ctx, first); err != nil || !recorded {
		t.Fatalf("record first = %v, %v", recorded, err)
	}
	if recorded, err := st.RecordUsageEvent(ctx, first); err != nil || recorded {
		t.Fatalf("record duplicate = %v, %v; want no-op", recorded, err)
	}
	second := *first
	second.ID = "event-2"
	second.RequestID = "request-2"
	second.InputTokens = usageInt64(7)
	second.OutputTokens = nil
	second.CaptureStatus = domain.UsageCapturePartial
	second.OccurredAt = at.Add(30 * time.Minute)
	if recorded, err := st.RecordUsageEvent(ctx, &second); err != nil || !recorded {
		t.Fatalf("record second = %v, %v", recorded, err)
	}

	if len(st.usageRollups) != 1 {
		t.Fatalf("rollups=%d want one UTC-hour bucket", len(st.usageRollups))
	}
	summary, err := st.GetUsageSummary(ctx, domain.UsageSummaryQuery{ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 2 || summary.Capture.Reported != 1 || summary.Capture.Partial != 1 {
		t.Fatalf("summary request/capture = %+v", summary)
	}
	if summary.Tokens.Input == nil || *summary.Tokens.Input != 17 ||
		summary.Tokens.Output == nil || *summary.Tokens.Output != 4 {
		t.Fatalf("summary tokens = %+v", summary.Tokens)
	}
}

func TestUsageCleanupRetainsRollupHistory(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	old := now.Add(-100 * 24 * time.Hour)
	event := &domain.UsageEvent{
		ID: "event-old", RequestID: "request-old",
		SubjectKind: domain.UsageSubjectRun, SubjectID: "run-old", RunID: "run-old",
		ProjectID: "project-1", InputTokens: usageInt64(23),
		CaptureStatus: domain.UsageCaptureReported, OccurredAt: old, CreatedAt: old, Version: 1,
	}
	if _, err := st.RecordUsageEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	rawDeleted, rollupsDeleted, err := st.CleanupUsage(
		ctx,
		now.Add(-90*24*time.Hour).Truncate(time.Hour),
		now.Add(-365*24*time.Hour).Truncate(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if rawDeleted != 1 || rollupsDeleted != 0 || len(st.usageEvents) != 0 {
		t.Fatalf("cleanup raw=%d rollups=%d raw-left=%d", rawDeleted, rollupsDeleted, len(st.usageEvents))
	}
	summary, err := st.GetUsageSummary(ctx, domain.UsageSummaryQuery{ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 1 || summary.Tokens.Input == nil || *summary.Tokens.Input != 23 {
		t.Fatalf("rollup history disappeared after raw cleanup: %+v", summary)
	}
	if recorded, err := st.RecordUsageEvent(ctx, event); err != nil || recorded {
		t.Fatalf("old request replay after raw cleanup = %v, %v; want durable no-op", recorded, err)
	}

	_, rollupsDeleted, err = st.CleanupUsage(ctx, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if rollupsDeleted != 1 {
		t.Fatalf("deleted rollups=%d want 1", rollupsDeleted)
	}
	summary, err = st.GetUsageSummary(ctx, domain.UsageSummaryQuery{ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Availability != "unavailable" || summary.Requests != 0 {
		t.Fatalf("expired rollup summary=%+v", summary)
	}
}

func TestUsageSummaryTreatsProviderCostWithoutTokensAsAvailable(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	at := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	event := &domain.UsageEvent{
		ID: "event-cost-only", RequestID: "request-cost-only",
		SubjectKind: domain.UsageSubjectRun, SubjectID: "run-cost-only", RunID: "run-cost-only",
		ReportedCostMicros: usageInt64(4200), ReportedCurrency: "USD",
		CaptureStatus: domain.UsageCapturePartial, OccurredAt: at, CreatedAt: at, Version: 1,
	}
	if _, err := st.RecordUsageEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	summary, err := st.GetUsageSummary(ctx, domain.UsageSummaryQuery{RunID: event.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Availability != "available" || summary.Reason != "" ||
		len(summary.Costs.Reported) != 1 || summary.Costs.Reported[0].Micros != 4200 {
		t.Fatalf("cost-only usage hidden as unavailable: %+v", summary)
	}
}

func TestPricingRevisionTieUsesLatestCreatedRevision(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	model := &domain.Model{
		ID: "model-priced", Name: "Priced", ModelName: "provider/priced",
		BaseURL: "https://example.invalid/v1", CreatedAt: now,
	}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	effective := now.Add(-time.Hour)
	older := &domain.ModelPricingRevision{
		ID: "zz-random-id", ModelResourceID: model.ID, ModelName: model.ModelName,
		Currency: "USD", InputMicrosPerMillion: usageInt64(1),
		EffectiveAt: effective, CreatedAt: now.Add(-time.Minute),
	}
	newer := &domain.ModelPricingRevision{
		ID: "aa-random-id", ModelResourceID: model.ID, ModelName: model.ModelName,
		Currency: "USD", InputMicrosPerMillion: usageInt64(2),
		EffectiveAt: effective, CreatedAt: now,
	}
	for _, revision := range []*domain.ModelPricingRevision{older, newer} {
		if err := st.CreateModelPricingRevision(ctx, revision); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := st.ResolveModelPricingRevision(ctx, model.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != newer.ID {
		t.Fatalf("resolved revision=%q want latest-created %q", resolved.ID, newer.ID)
	}
	if err := st.DeleteModel(ctx, model.ID); err != nil {
		t.Fatal(err)
	}
	resolved, err = st.ResolveModelPricingRevision(ctx, model.ID, now)
	if err != nil || resolved.ID != newer.ID {
		t.Fatalf("deleted-model pricing snapshot=%+v err=%v", resolved, err)
	}
}

func TestUsageGroupsCollapseRenamedIdentityToLatestSnapshot(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	hour := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	for i, snapshot := range []struct {
		name string
		at   time.Time
	}{
		{name: "Old device name", at: hour},
		{name: "Current device name", at: hour.Add(time.Hour)},
	} {
		event := &domain.UsageEvent{
			ID: "event-group-" + snapshot.name, RequestID: "request-group-" + snapshot.name,
			SubjectKind: domain.UsageSubjectDevice, SubjectID: "device-1",
			UserID: "user-1", DeviceID: "device-1", DeviceName: snapshot.name,
			InputTokens: usageInt64(int64(i + 1)), CaptureStatus: domain.UsageCapturePartial,
			OccurredAt: snapshot.at, CreatedAt: snapshot.at, Version: 1,
		}
		if _, err := st.RecordUsageEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := st.ListUsageGroups(ctx, domain.UsageSummaryQuery{
		SubjectKind: domain.UsageSubjectDevice, UserID: "user-1",
	}, "device")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != "device-1" ||
		groups[0].Name != "Current device name" ||
		groups[0].Summary.Tokens.Input == nil || *groups[0].Summary.Tokens.Input != 3 {
		t.Fatalf("renamed device groups=%+v", groups)
	}
}

func TestRunUsageDimensionsDistinguishOrdinaryAndAutomationCreatedCards(t *testing.T) {
	ctx := context.Background()
	st, cardAutomation, trigger := seedPluginKanbanOccurrenceStore(t)
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)

	observeAndAttach := func(documentID, documentPath string) *domain.Run {
		t.Helper()
		observed, err := st.ObservePluginKanbanCard(ctx, PluginKanbanObservation{
			AutomationID: cardAutomation.ID, ServiceID: cardAutomation.ServiceID,
			InstallationID: trigger.InstallationID, WorkspaceID: "workspace",
			DocumentID: documentID, DocumentPath: documentPath,
			TriggerColumn: trigger.TriggerColumn, DoneColumn: trigger.DoneColumn,
			ObservedColumn: trigger.TriggerColumn, EventKey: "card:event:" + documentID,
			ObservedAt: now,
		})
		if err != nil || observed.Occurrence == nil {
			t.Fatalf("observe %q=%+v err=%v", documentPath, observed, err)
		}
		run := &domain.Run{
			ID: domain.NewID(), ProjectID: "project", ServiceID: cardAutomation.ServiceID,
			Prompt: "work", Status: domain.StatusQueued, Phase: "Queued",
			Origin: domain.RunOriginKanban, OriginAutomationID: cardAutomation.ID,
			OriginEventKey: observed.Occurrence.EventKey, Attempt: 1, CreatedAt: now,
		}
		if attached, attachErr := st.CreatePluginKanbanOccurrenceRun(
			ctx, observed.Occurrence.ID, run,
		); attachErr != nil || !attached {
			t.Fatalf("attach %q=%v err=%v", documentPath, attached, attachErr)
		}
		return run
	}

	ordinary := observeAndAttach("ordinary", "cards/ordinary.md")
	ordinaryDimensions, err := st.GetRunUsageDimensions(ctx, ordinary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ordinaryDimensions.CardPath != "cards/ordinary.md" ||
		ordinaryDimensions.AutomationID != "" {
		t.Fatalf("ordinary Card dimensions=%+v", ordinaryDimensions)
	}

	const generatedPath = "jcode-automation/nightly/generated.md"
	execution := &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: "cron-automation", AutomationName: "Nightly",
		ProjectID: "project", ServiceID: cardAutomation.ServiceID, TriggerKind: "cron",
		EventKey: "cron:generated", State: domain.AutomationExecutionAccepted,
		OutputMode:       domain.AutomationOutputCreateCard,
		CardAutomationID: cardAutomation.ID, CardWorkspaceID: "workspace",
		CardDocumentID: "generated", CardPath: generatedPath, CardState: "bound",
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := st.CreateAutomationExecution(ctx, execution, nil); err != nil {
		t.Fatal(err)
	}
	generated := observeAndAttach(execution.CardDocumentID, generatedPath)
	generatedDimensions, err := st.GetRunUsageDimensions(ctx, generated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if generatedDimensions.CardPath != generatedPath ||
		generatedDimensions.AutomationID != execution.AutomationID ||
		generatedDimensions.AutomationName != execution.AutomationName {
		t.Fatalf("automation-created Card dimensions=%+v", generatedDimensions)
	}
}
