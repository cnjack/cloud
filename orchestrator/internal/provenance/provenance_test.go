package provenance

import (
	"context"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

func provenanceStore(t *testing.T) (*store.MemStore, *domain.User, *domain.PluginAutomation) {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemStore()
	user := &domain.User{ID: "owner", DisplayName: "Jack"}
	identity := &domain.UserIdentity{
		ID: "identity", Provider: domain.ProviderGitea, ProviderUID: "owner",
	}
	if _, err := st.CreateUserWithIdentity(ctx, user, identity); err != nil {
		t.Fatal(err)
	}
	project := &domain.Project{ID: "project", Name: "Commerce"}
	service := &domain.Service{
		ID: "service", ProjectID: project.ID, Name: "payments-api",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "acme/payments",
	}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID: "jtype", ProjectID: project.ID, Provider: domain.PluginJType,
		Status: domain.PluginStatusEnabled, WorkspaceID: "workspace",
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: "automation", ServiceID: service.ID, InstallationID: installation.ID,
		Name: "Agent queue", TriggerKind: "kanban", Enabled: true, CreatedBy: user.ID,
	}
	trigger := &domain.KanbanTrigger{
		AutomationID: automation.ID, InstallationID: installation.ID,
		BoardRef: "delivery", TriggerColumn: "agent",
	}
	if err := st.CreatePluginAutomation(ctx, automation, nil, nil, trigger, nil); err != nil {
		t.Fatal(err)
	}
	return st, user, automation
}

func TestStampManualAndKanbanKeepAuthorizationSeparate(t *testing.T) {
	ctx := context.Background()
	st, user, automation := provenanceStore(t)
	manual := &domain.Run{
		ProjectID: "project", ServiceID: "service",
		Origin: domain.RunOriginAPI, TriggeredByUserID: &user.ID,
	}
	Stamp(ctx, st, manual, nil)
	if manual.ProvenanceSnapshot.RequestedActor == nil ||
		manual.ProvenanceSnapshot.RequestedActor.Label != "Jack" ||
		manual.ProvenanceSnapshot.AccountableActor == nil ||
		manual.ProvenanceSnapshot.Precision != PrecisionExact ||
		manual.ProvenanceSnapshot.WritebackActor != nil {
		t.Fatalf("manual snapshot=%+v", manual.ProvenanceSnapshot)
	}
	if manual.TriggeredByUserID == nil || *manual.TriggeredByUserID != user.ID {
		t.Fatal("provenance stamping changed authorization identity")
	}

	kanban := &domain.Run{
		ProjectID: "project", ServiceID: "service",
		Origin: domain.RunOriginKanban, OriginAutomationID: automation.ID,
		OriginEventKey: "occurrence",
	}
	Stamp(ctx, st, kanban, &ExternalActor{
		Provider: "jtype", Label: "Mei", Source: "kanban_event",
	})
	if kanban.TriggeredByUserID != nil ||
		kanban.ProvenanceSnapshot.RequestedActor == nil ||
		kanban.ProvenanceSnapshot.RequestedActor.Kind != "external_actor" ||
		kanban.ProvenanceSnapshot.AccountableActor == nil ||
		kanban.ProvenanceSnapshot.AccountableActor.ID != user.ID ||
		kanban.ProvenanceSnapshot.Precision != PrecisionRuleOwner ||
		kanban.ProvenanceSnapshot.WritebackActor == nil ||
		kanban.ProvenanceSnapshot.WritebackActor.Provider != "jtype" {
		t.Fatalf("kanban snapshot=%+v", kanban.ProvenanceSnapshot)
	}
}

func TestStampCreateCardRunUsesOriginatingCronOwner(t *testing.T) {
	ctx := context.Background()
	st, _, kanbanAutomation := provenanceStore(t)
	cronOwner := &domain.User{ID: "cron-owner", DisplayName: "Cron Owner"}
	if _, err := st.CreateUserWithIdentity(ctx, cronOwner, &domain.UserIdentity{
		ID: "cron-owner-identity", UserID: cronOwner.ID,
		Provider: domain.ProviderGitea, ProviderUID: cronOwner.ID,
	}); err != nil {
		t.Fatal(err)
	}
	cronAutomation := &domain.PluginAutomation{
		ID: "cron-automation", ServiceID: "service", Name: "Daily triage",
		TriggerKind: "cron", PromptTemplate: "triage", Enabled: true,
		CreatedBy: cronOwner.ID,
	}
	if err := st.CreatePluginAutomation(ctx, cronAutomation, nil, nil, nil, &domain.CronTrigger{
		AutomationID: cronAutomation.ID, CronExpr: "0 9 * * *",
		OutputMode: domain.AutomationOutputCreateCard,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cardPath := "jcode-automation/cron-automation/execution.md"
	execution := &domain.AutomationExecution{
		ID: "cron-execution", AutomationID: cronAutomation.ID, AutomationName: cronAutomation.Name,
		ProjectID: "project", ServiceID: "service", TriggerKind: "cron",
		EventKey: "cron:daily", State: domain.AutomationExecutionAccepted,
		OutputMode:       domain.AutomationOutputCreateCard,
		CardAutomationID: kanbanAutomation.ID, CardWorkspaceID: "workspace",
		CardDocumentID: "card", CardPath: cardPath, CardState: "bound",
		CreatedAt: now, UpdatedAt: now,
	}
	if _, created, err := st.CreateAutomationExecution(ctx, execution, nil); err != nil || !created {
		t.Fatalf("create execution created=%v err=%v", created, err)
	}
	observed, err := st.ObservePluginKanbanCard(ctx, store.PluginKanbanObservation{
		AutomationID: kanbanAutomation.ID, ServiceID: "service",
		InstallationID: "jtype", WorkspaceID: "workspace",
		DocumentID: "card", DocumentPath: cardPath,
		TriggerColumn: "agent", ObservedColumn: "agent",
		EventKey: "card:entered", ObservedAt: now,
	})
	if err != nil || observed.Occurrence == nil {
		t.Fatalf("observe generated Card=%+v err=%v", observed, err)
	}
	run := &domain.Run{
		ProjectID: "project", ServiceID: "service",
		Origin: domain.RunOriginKanban, OriginAutomationID: kanbanAutomation.ID,
		OriginEventKey: observed.Occurrence.ID,
	}
	Stamp(ctx, st, run, nil)
	if run.ProvenanceSnapshot.AccountableActor == nil ||
		run.ProvenanceSnapshot.AccountableActor.ID != cronOwner.ID ||
		run.ProvenanceSnapshot.AccountableActor.Label != cronOwner.DisplayName {
		t.Fatalf("create-card provenance=%+v", run.ProvenanceSnapshot)
	}
}

func TestResolveUsesFrozenIdentityAndCurrentExecutionLabels(t *testing.T) {
	ctx := context.Background()
	st, user, _ := provenanceStore(t)
	run := &domain.Run{
		ProjectID: "project", ServiceID: "service", ModelName: "anthropic/sonnet",
		Origin: domain.RunOriginAPI, TriggeredByUserID: &user.ID,
	}
	Stamp(ctx, st, run, nil)
	user.DisplayName = "Renamed"
	got := Resolve(ctx, st, run)
	if got.RequestedActor == nil || got.RequestedActor.Label != "Jack" ||
		got.ExecutedFor.ProjectLabel != "Commerce" ||
		got.ExecutedFor.ServiceLabel != "payments-api" ||
		got.ExecutedFor.Model != "anthropic/sonnet" ||
		got.Trigger.Kind != "api" {
		t.Fatalf("resolved=%+v", got)
	}
}

func TestRuleOwnerIsAccountableButNeverInventedAsRequester(t *testing.T) {
	ctx := context.Background()
	st, user, automation := provenanceStore(t)
	run := &domain.Run{
		ProjectID: "project", ServiceID: "service",
		Origin: domain.RunOriginAutomation, OriginAutomationID: automation.ID,
		TriggeredByUserID: &user.ID,
	}
	Stamp(ctx, st, run, nil)
	if run.ProvenanceSnapshot.RequestedActor != nil ||
		run.ProvenanceSnapshot.AccountableActor == nil ||
		run.ProvenanceSnapshot.AccountableActor.ID != user.ID ||
		run.ProvenanceSnapshot.Precision != PrecisionRuleOwner {
		t.Fatalf("rule snapshot=%+v", run.ProvenanceSnapshot)
	}
	if run.TriggeredByUserID == nil || *run.TriggeredByUserID != user.ID {
		t.Fatal("display projection changed the legacy authorization identity")
	}

	legacy := *run
	legacy.ProvenanceSnapshot = domain.RunProvenanceSnapshot{}
	resolved := Resolve(ctx, st, &legacy)
	if resolved.RequestedActor != nil ||
		resolved.AccountableActor == nil ||
		resolved.AccountableActor.ID != user.ID ||
		resolved.Precision != PrecisionRuleOwner {
		t.Fatalf("legacy fallback=%+v", resolved)
	}
	if !legacy.ProvenanceSnapshot.Empty() {
		t.Fatal("legacy resolver mutated the stored Run")
	}
}

func TestManualReviewSeparatesRequesterFromProviderBot(t *testing.T) {
	ctx := context.Background()
	st, user, _ := provenanceStore(t)
	run := &domain.Run{
		ProjectID: "project", ServiceID: "service", Kind: domain.RunKindReview,
		Origin: domain.RunOriginAPI, PRURL: "https://gitea.example/acme/payments/pulls/7",
		TriggeredByUserID: &user.ID,
	}
	Stamp(ctx, st, run, nil)
	if run.ProvenanceSnapshot.RequestedActor == nil ||
		run.ProvenanceSnapshot.RequestedActor.ID != user.ID ||
		run.ProvenanceSnapshot.WritebackActor == nil ||
		run.ProvenanceSnapshot.WritebackActor.Kind != "provider_bot" ||
		run.ProvenanceSnapshot.WritebackActor.Provider != "gitea" {
		t.Fatalf("manual review snapshot=%+v", run.ProvenanceSnapshot)
	}
}

func TestSourceMatrixKeepsHumanRuleAndProviderIdentitiesDistinct(t *testing.T) {
	ctx := context.Background()
	st, user, automation := provenanceStore(t)

	servicePrincipal := &domain.Run{
		ProjectID: "project", ServiceID: "service", Origin: domain.RunOriginAPI,
	}
	Stamp(ctx, st, servicePrincipal, nil)
	if servicePrincipal.ProvenanceSnapshot.RequestedActor != nil ||
		servicePrincipal.ProvenanceSnapshot.AccountableActor != nil ||
		servicePrincipal.ProvenanceSnapshot.Precision != PrecisionUnattributed ||
		servicePrincipal.ProvenanceSnapshot.RuntimePrincipal.Kind != "service_principal" {
		t.Fatalf("service-principal snapshot=%+v", servicePrincipal.ProvenanceSnapshot)
	}

	cron := &domain.Run{
		ProjectID: "project", ServiceID: "service", Origin: domain.RunOriginSchedule,
		OriginAutomationID: automation.ID,
	}
	Stamp(ctx, st, cron, nil)
	if cron.ProvenanceSnapshot.RequestedActor != nil ||
		cron.ProvenanceSnapshot.AccountableActor == nil ||
		cron.ProvenanceSnapshot.AccountableActor.ID != user.ID ||
		cron.ProvenanceSnapshot.Precision != PrecisionRuleOwner ||
		cron.ProvenanceSnapshot.RuntimePrincipal.Kind != "automation_principal" {
		t.Fatalf("cron snapshot=%+v", cron.ProvenanceSnapshot)
	}

	legacyCron := &domain.Run{
		ProjectID: "project", ServiceID: "service", Origin: domain.RunOriginSchedule,
	}
	Stamp(ctx, st, legacyCron, &ExternalActor{
		Source: "automation_rule", AccountableUserID: user.ID,
	})
	if legacyCron.ProvenanceSnapshot.RequestedActor != nil ||
		legacyCron.ProvenanceSnapshot.AccountableActor == nil ||
		legacyCron.ProvenanceSnapshot.AccountableActor.ID != user.ID ||
		legacyCron.ProvenanceSnapshot.Precision != PrecisionRuleOwner {
		t.Fatalf("legacy cron snapshot=%+v", legacyCron.ProvenanceSnapshot)
	}

	mapped := &domain.Run{
		ProjectID: "project", ServiceID: "service", Origin: domain.RunOriginAutomation,
		OriginAutomationID: automation.ID, TriggeredByUserID: &user.ID,
		OriginEventKey: "gitea:pull_request:7", PRURL: "https://gitea.example/acme/payments/pulls/7",
	}
	Stamp(ctx, st, mapped, &ExternalActor{
		Provider: "gitea", ID: "42", Label: "mei-git", Source: "scm_event",
	})
	mappedResolved := Resolve(ctx, st, mapped)
	if mappedResolved.RequestedActor == nil ||
		mappedResolved.RequestedActor.Kind != "cloud_user" ||
		mappedResolved.RequestedActor.ExternalLabel != "mei-git" ||
		mappedResolved.AccountableActor == nil ||
		mappedResolved.AccountableActor.ID != user.ID ||
		mappedResolved.Precision != PrecisionExact ||
		mappedResolved.Trigger.Href != mapped.PRURL {
		t.Fatalf("mapped SCM provenance=%+v", mappedResolved)
	}

	unmapped := &domain.Run{
		ProjectID: "project", ServiceID: "service", Origin: domain.RunOriginAutomation,
		OriginAutomationID: automation.ID,
	}
	Stamp(ctx, st, unmapped, &ExternalActor{
		Provider: "gitea", ID: "99", Label: "outside-contributor", Source: "scm_event",
	})
	if unmapped.ProvenanceSnapshot.RequestedActor == nil ||
		unmapped.ProvenanceSnapshot.RequestedActor.Kind != "external_actor" ||
		unmapped.ProvenanceSnapshot.AccountableActor == nil ||
		unmapped.ProvenanceSnapshot.AccountableActor.ID != user.ID ||
		unmapped.ProvenanceSnapshot.Precision != PrecisionRuleOwner {
		t.Fatalf("unmapped SCM snapshot=%+v", unmapped.ProvenanceSnapshot)
	}
}

func TestExistingUserWithoutDisplayNameDoesNotLookDeleted(t *testing.T) {
	ctx := context.Background()
	st, _, _ := provenanceStore(t)
	user := &domain.User{ID: "blank-name"}
	identity := &domain.UserIdentity{
		ID: "blank-identity", Provider: domain.ProviderGitea, ProviderUID: "blank-name",
	}
	if _, err := st.CreateUserWithIdentity(ctx, user, identity); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ProjectID: "project", ServiceID: "service", Origin: domain.RunOriginAPI,
		TriggeredByUserID: &user.ID,
	}
	Stamp(ctx, st, run, nil)
	if run.ProvenanceSnapshot.RequestedActor == nil ||
		run.ProvenanceSnapshot.RequestedActor.Label != user.ID {
		t.Fatalf("blank-name actor=%+v", run.ProvenanceSnapshot.RequestedActor)
	}
}
