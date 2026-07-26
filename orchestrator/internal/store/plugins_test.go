package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestQualifiedPluginAutomationColsQualifiesEveryJoinedColumn(t *testing.T) {
	got := qualifiedPluginAutomationCols("a")
	columns := strings.Split(got, ",")
	if len(columns) != 14 {
		t.Fatalf("qualified columns=%d want 14: %q", len(columns), got)
	}
	for _, column := range columns {
		if !strings.HasPrefix(column, "a.") {
			t.Fatalf("joined Automation column is ambiguous: %q", column)
		}
	}
}

func TestPluginInstallationIsUniqueAndUninstallCascadesBoundService(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	p := &domain.Project{ID: "p", Name: "project", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{ID: "pi", ProjectID: p.ID, Provider: domain.PluginGitea, Status: domain.PluginStatusEnabled, CreatedAt: time.Now()}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginInstallation(ctx, &domain.PluginInstallation{ID: "pi-2", ProjectID: p.ID, Provider: domain.PluginGitea}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate project/provider error=%v want ErrAlreadyExists", err)
	}
	svc := &domain.Service{ID: "s", ProjectID: p.ID, Name: "repo", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "o/r", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertServiceRepositoryBinding(ctx, &domain.ServiceRepositoryBinding{ServiceID: svc.ID, InstallationID: installation.ID, ProviderRepoID: "42", RepositoryPath: "o/r", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginAutomation(ctx, &domain.PluginAutomation{ID: "a", ServiceID: svc.ID, InstallationID: installation.ID, Name: "push", TriggerKind: "scm", PromptTemplate: "review", Enabled: true, CreatedAt: time.Now()}, &domain.SCMTrigger{AutomationID: "a"}, []domain.SCMAction{{AutomationID: "a", ServiceID: svc.ID, EventFamily: "push", Action: "updated"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginAutomation(ctx, &domain.PluginAutomation{ID: "a-cron", ServiceID: svc.ID, Name: "cron", TriggerKind: "cron", PromptTemplate: "review", Enabled: true, CreatedAt: time.Now()}, nil, nil, nil, &domain.CronTrigger{AutomationID: "a-cron", CronExpr: "0 * * * *"}); err != nil {
		t.Fatal(err)
	}
	services, automations, err := st.CountPluginInstallationImpact(ctx, installation.ID)
	if err != nil || services != 1 || automations != 2 {
		t.Fatalf("impact=(%d,%d,%v) want 1,2,nil", services, automations, err)
	}
	if err := st.UninstallPlugin(ctx, installation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetService(ctx, svc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bound service remains: %v", err)
	}
	if _, err := st.GetPluginInstallation(ctx, installation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("installation remains: %v", err)
	}
}

func TestPluginSCMActionCannotOverlapWithinService(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	_ = st.CreateProject(ctx, &domain.Project{ID: "p", Name: "project"})
	installation := &domain.PluginInstallation{ID: "i", ProjectID: "p", Provider: domain.PluginGitea}
	_ = st.CreatePluginInstallation(ctx, installation)
	_ = st.CreateService(ctx, &domain.Service{ID: "s", ProjectID: "p", Name: "repo", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea})
	_ = st.UpsertServiceRepositoryBinding(ctx, &domain.ServiceRepositoryBinding{ServiceID: "s", InstallationID: installation.ID, ProviderRepoID: "1"})
	create := func(id string) error {
		return st.CreatePluginAutomation(ctx, &domain.PluginAutomation{ID: id, ServiceID: "s", InstallationID: installation.ID, Name: id, TriggerKind: "scm", PromptTemplate: "x"}, &domain.SCMTrigger{AutomationID: id}, []domain.SCMAction{{AutomationID: id, ServiceID: "s", EventFamily: "pull_request", Action: "opened"}}, nil, nil)
	}
	if err := create("a"); err != nil {
		t.Fatal(err)
	}
	if err := create("b"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("overlap error=%v want ErrAlreadyExists", err)
	}
}

func TestServiceRepositoryBindingRequiresSameProjectAndProvider(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	for _, id := range []string{"p1", "p2"} {
		if err := st.CreateProject(ctx, &domain.Project{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	github := &domain.PluginInstallation{ID: "github", ProjectID: "p1", Provider: domain.PluginGitHub}
	giteaElsewhere := &domain.PluginInstallation{ID: "gitea-p2", ProjectID: "p2", Provider: domain.PluginGitea}
	if err := st.CreatePluginInstallation(ctx, github); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginInstallation(ctx, giteaElsewhere); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: "s", ProjectID: "p1", Name: "repo", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []*domain.ServiceRepositoryBinding{
		{ServiceID: svc.ID, InstallationID: github.ID, ProviderRepoID: "1"},
		{ServiceID: svc.ID, InstallationID: giteaElsewhere.ID, ProviderRepoID: "1"},
	} {
		if err := st.UpsertServiceRepositoryBinding(ctx, binding); err == nil {
			t.Fatalf("binding %+v unexpectedly succeeded", binding)
		}
	}
	if err := st.CreatePluginBoundService(ctx,
		&domain.Service{ID: "bad", ProjectID: "p1", Name: "bad", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea},
		&domain.ServiceRepositoryBinding{ServiceID: "bad", InstallationID: github.ID, ProviderRepoID: "2"}); err == nil {
		t.Fatal("CreatePluginBoundService accepted provider mismatch")
	}
}

func TestPluginAutomationRequiresExactlyOneMatchingChild(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	if err := st.CreateProject(ctx, &domain.Project{ID: "p", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, &domain.Service{ID: "s", ProjectID: "p", Name: "s", RepoKind: domain.RepoKindProvider}); err != nil {
		t.Fatal(err)
	}
	base := &domain.PluginAutomation{ID: "a", ServiceID: "s", Name: "a", TriggerKind: "scm", PromptTemplate: "x"}
	cases := []struct {
		name    string
		scm     *domain.SCMTrigger
		actions []domain.SCMAction
		kanban  *domain.KanbanTrigger
		cron    *domain.CronTrigger
	}{
		{name: "missing", actions: []domain.SCMAction{{AutomationID: "a", ServiceID: "s"}}},
		{name: "two children", scm: &domain.SCMTrigger{AutomationID: "a"}, actions: []domain.SCMAction{{AutomationID: "a", ServiceID: "s"}}, cron: &domain.CronTrigger{AutomationID: "a", CronExpr: "* * * * *"}},
		{name: "wrong child", cron: &domain.CronTrigger{AutomationID: "a", CronExpr: "* * * * *"}},
		{name: "wrong action aggregate", scm: &domain.SCMTrigger{AutomationID: "a"}, actions: []domain.SCMAction{{AutomationID: "other", ServiceID: "s"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := st.CreatePluginAutomation(ctx, base, tc.scm, tc.actions, tc.kanban, tc.cron); err == nil {
				t.Fatal("invalid Automation aggregate unexpectedly succeeded")
			}
		})
	}
}

func TestUninstallPluginCascadesPluginAggregateButKeepsUnrelatedRun(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	for _, id := range []string{"p1", "p2"} {
		if err := st.CreateProject(ctx, &domain.Project{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	gitea := &domain.PluginInstallation{ID: "gitea", ProjectID: "p1", Provider: domain.PluginGitea}
	jtype := &domain.PluginInstallation{ID: "jtype", ProjectID: "p1", Provider: domain.PluginJType}
	if err := st.CreatePluginInstallation(ctx, gitea); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginInstallation(ctx, jtype); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: "s1", ProjectID: "p1", Name: "repo", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertServiceRepositoryBinding(ctx, &domain.ServiceRepositoryBinding{ServiceID: svc.ID, InstallationID: gitea.ID, ProviderRepoID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginAutomation(ctx, &domain.PluginAutomation{ID: "scm", ServiceID: svc.ID, InstallationID: gitea.ID, Name: "scm", TriggerKind: "scm", PromptTemplate: "x"}, &domain.SCMTrigger{AutomationID: "scm"}, []domain.SCMAction{{AutomationID: "scm", ServiceID: svc.ID, EventFamily: "push", Action: "updated"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginAutomation(ctx, &domain.PluginAutomation{ID: "kanban", ServiceID: svc.ID, InstallationID: jtype.ID, Name: "kanban", TriggerKind: "kanban", PromptTemplate: "x"}, nil, nil, &domain.KanbanTrigger{AutomationID: "kanban", InstallationID: jtype.ID, BoardRef: "b", TriggerColumn: "todo"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsurePluginKanbanClaim(ctx, "kanban", "card", "card", "workspace", "done"); err != nil {
		t.Fatal(err)
	}
	boundRun := &domain.Run{ID: "bound", ProjectID: "p1", ServiceID: svc.ID}
	if err := st.CreateRun(ctx, boundRun); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{RunID: boundRun.ID, InstallationID: gitea.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, &domain.Service{ID: "s2", ProjectID: "p2", Name: "other", RepoKind: domain.RepoKindRaw}); err != nil {
		t.Fatal(err)
	}
	unrelated := &domain.Run{ID: "other", ProjectID: "p2", ServiceID: "s2"}
	if err := st.CreateRun(ctx, unrelated); err != nil {
		t.Fatal(err)
	}
	if err := st.UninstallPlugin(ctx, gitea.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetService(ctx, svc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bound Service remains: %v", err)
	}
	if _, err := st.GetPluginAutomation(ctx, "scm"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bound Automation remains: %v", err)
	}
	if _, err := st.GetPluginAutomation(ctx, "kanban"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("service Automation remains: %v", err)
	}
	if _, err := st.GetRun(ctx, boundRun.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bound run remains: %v", err)
	}
	if _, err := st.GetRun(ctx, unrelated.ID); err != nil {
		t.Fatalf("unrelated run was deleted: %v", err)
	}
	if len(st.pluginKanbanClaims) != 0 || len(st.pluginSCMActions) != 0 || len(st.runPluginSnapshots) != 0 {
		t.Fatalf("Plugin child state remains: claims=%d actions=%d snapshots=%d", len(st.pluginKanbanClaims), len(st.pluginSCMActions), len(st.runPluginSnapshots))
	}
}

func TestDeleteProjectCascadesPluginAggregates(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	if err := st.CreateProject(ctx, &domain.Project{ID: "p", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{ID: "i", ProjectID: "p", Provider: domain.PluginGitea}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: "s", ProjectID: "p", Name: "s", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertServiceRepositoryBinding(ctx, &domain.ServiceRepositoryBinding{ServiceID: svc.ID, InstallationID: installation.ID, ProviderRepoID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePluginAutomation(ctx, &domain.PluginAutomation{ID: "a", ServiceID: svc.ID, InstallationID: installation.ID, Name: "a", TriggerKind: "scm", PromptTemplate: "x"}, &domain.SCMTrigger{AutomationID: "a"}, []domain.SCMAction{{AutomationID: "a", ServiceID: svc.ID, EventFamily: "push", Action: "updated"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProject(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if len(st.pluginInstallations) != 0 || len(st.serviceRepoBindings) != 0 || len(st.pluginAutomations) != 0 || len(st.pluginSCMActions) != 0 || len(st.pluginSCMTriggers) != 0 {
		t.Fatalf("project Plugin aggregates remain: installations=%d bindings=%d automations=%d actions=%d triggers=%d", len(st.pluginInstallations), len(st.serviceRepoBindings), len(st.pluginAutomations), len(st.pluginSCMActions), len(st.pluginSCMTriggers))
	}
}

func TestUpsertProviderConfigAndInvalidateUpdatesInstallationsAtomically(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	if err := st.CreateProject(ctx, &domain.Project{ID: "p", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{ID: "i", ProjectID: "p", Provider: domain.PluginGitLab, Status: domain.PluginStatusEnabled, ConfigRevision: 1}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	cfg := &domain.ProviderConfig{Provider: domain.PluginGitLab, PluginEnabled: true}
	if err := st.UpsertProviderConfigAndInvalidate(ctx, cfg, false, ""); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetPluginInstallation(ctx, installation.ID)
	if err != nil || current.ConfigRevision != cfg.ConfigRevision || current.Status != domain.PluginStatusEnabled {
		t.Fatalf("healthy config sync = %+v, %v", current, err)
	}
	cfg.PluginEnabled = false
	if err := st.UpsertProviderConfigAndInvalidate(ctx, cfg, true, "Plugin disabled by cluster admin"); err != nil {
		t.Fatal(err)
	}
	current, err = st.GetPluginInstallation(ctx, installation.ID)
	if err != nil || current.Status != domain.PluginStatusActionRequired || current.LastHealthError != "Plugin disabled by cluster admin" || current.ConfigRevision != 1 {
		t.Fatalf("invalidated installation = %+v, %v", current, err)
	}
}

func TestClaimRunDispatchIsAtomicAndFailureClearsCredentials(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	if err := st.CreateProject(ctx, &domain.Project{ID: "p", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, &domain.Service{ID: "s", ProjectID: "p", Name: "s", RepoKind: domain.RepoKindRaw}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginGitea, PluginEnabled: true, Capabilities: []string{}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetProviderConfig(ctx, domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{ID: "i", ProjectID: "p", Provider: domain.PluginGitea, Status: domain.PluginStatusEnabled, AccessTokenEnc: []byte("ciphertext"), ConfigRevision: cfg.ConfigRevision}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: "r", ProjectID: "p", ServiceID: "s", Status: domain.StatusQueued}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimRunDispatch(ctx, run.ID, "job", "hash", "PreparingWorkspace", installation.ID, []domain.RunPluginSnapshot{{RunID: run.ID, InstallationID: installation.ID}}); err != nil {
		t.Fatal(err)
	}
	claimed, _ := st.GetRun(ctx, run.ID)
	if claimed.Status != domain.StatusScheduling || claimed.TokenHash != "hash" || claimed.K8sJobName != "job" {
		t.Fatalf("claim=%+v", claimed)
	}
	if got, _ := st.ListRunPluginSnapshots(ctx, run.ID); len(got) != 1 {
		t.Fatalf("snapshots=%v", got)
	}
	if _, err := st.FailRunDispatch(ctx, run.ID, "job", "Failed", "job create failed", time.Now()); err != nil {
		t.Fatal(err)
	}
	failed, _ := st.GetRun(ctx, run.ID)
	if failed.Status != domain.StatusFailed || failed.TokenHash != "" {
		t.Fatalf("failure cleanup=%+v", failed)
	}
	if got, _ := st.ListRunPluginSnapshots(ctx, run.ID); len(got) != 0 {
		t.Fatalf("snapshots remain after failed claim: %v", got)
	}
}

func TestCreateRunPluginSnapshotsIsAtomicInMemory(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	_ = st.CreateProject(ctx, &domain.Project{ID: "p", Name: "p"})
	_ = st.CreateService(ctx, &domain.Service{ID: "s", ProjectID: "p", Name: "s", RepoKind: domain.RepoKindRaw})
	_ = st.CreateRun(ctx, &domain.Run{ID: "r", ProjectID: "p", ServiceID: "s", Status: domain.StatusQueued})
	_ = st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginGitea, PluginEnabled: true})
	cfg, err := st.GetProviderConfig(ctx, domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.CreatePluginInstallation(ctx, &domain.PluginInstallation{ID: "i", ProjectID: "p", Provider: domain.PluginGitea, Status: domain.PluginStatusEnabled, AccessTokenEnc: []byte("ciphertext"), ConfigRevision: cfg.ConfigRevision})
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{RunID: "r", InstallationID: "i"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{RunID: "r", InstallationID: "i"}, {RunID: "r", InstallationID: "missing"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
	if got, _ := st.ListRunPluginSnapshots(ctx, "r"); len(got) != 1 || got[0].InstallationID != "i" {
		t.Fatalf("partial batch mutation: %v", got)
	}
}

func TestPGPluginIntegrityGuards(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: run.ProjectID, Name: "provider-repo", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "owner/repo", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	github := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: run.ProjectID, Provider: domain.PluginGitHub, Status: domain.PluginStatusEnabled, Scopes: []string{}, CreatedAt: time.Now()}
	if err := st.CreatePluginInstallation(ctx, github); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertServiceRepositoryBinding(ctx, &domain.ServiceRepositoryBinding{ServiceID: svc.ID, InstallationID: github.ID, ProviderRepoID: "1", RepositoryPath: "owner/repo", CreatedAt: time.Now()}); err == nil {
		t.Fatal("PostgreSQL accepted a provider-mismatched repository binding")
	}
	gitea := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: run.ProjectID, Provider: domain.PluginGitea, Status: domain.PluginStatusEnabled, Scopes: []string{}, CreatedAt: time.Now()}
	if err := st.CreatePluginInstallation(ctx, gitea); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertServiceRepositoryBinding(ctx, &domain.ServiceRepositoryBinding{ServiceID: svc.ID, InstallationID: gitea.ID, ProviderRepoID: "1", RepositoryPath: "owner/repo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO automations_v2(id,service_id,installation_id,name,trigger_kind,prompt_template,created_at,updated_at) VALUES($1,$2,$3,'incomplete','scm','x',now(),now())`, domain.NewID(), svc.ID, gitea.ID); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err == nil {
		t.Fatal("PostgreSQL committed an Automation without its matching SCM child")
	}
}

// TestPGPluginAggregateDeletePaths executes the database-level delete paths,
// not just Store helpers.  The deferred aggregate trigger must use OLD rows on
// DELETE: using NEW makes a valid direct Automation delete (and Service FK
// cascade) fail only at commit time.
func TestPGPluginAggregateDeletePaths(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID:        domain.NewID(),
		ProjectID: run.ProjectID,
		Provider:  domain.PluginGitea,
		Status:    domain.PluginStatusEnabled,
		Scopes:    []string{},
		CreatedAt: time.Now(),
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	createServiceAndAutomation := func(repoID string) (*domain.Service, *domain.PluginAutomation) {
		t.Helper()
		svc := &domain.Service{
			ID:            domain.NewID(),
			ProjectID:     run.ProjectID,
			Name:          "repo-" + repoID,
			RepoKind:      domain.RepoKindProvider,
			Provider:      domain.ProviderGitea,
			RepoOwnerName: "owner/" + repoID,
			DefaultBranch: "main",
			CreatedAt:     time.Now(),
		}
		if err := st.CreateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertServiceRepositoryBinding(ctx, &domain.ServiceRepositoryBinding{
			ServiceID: svc.ID, InstallationID: installation.ID, ProviderRepoID: repoID,
			RepositoryPath: "owner/" + repoID, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		automation := &domain.PluginAutomation{
			ID: domain.NewID(), ServiceID: svc.ID, InstallationID: installation.ID,
			Name: "push", TriggerKind: "scm", PromptTemplate: "x", CreatedAt: time.Now(),
		}
		if err := st.CreatePluginAutomation(ctx, automation,
			&domain.SCMTrigger{AutomationID: automation.ID},
			[]domain.SCMAction{{AutomationID: automation.ID, ServiceID: svc.ID, EventFamily: "push", Action: "updated"}},
			nil, nil); err != nil {
			t.Fatal(err)
		}
		return svc, automation
	}

	// Direct parent deletion is the precise case that used to dereference NEW
	// from a DELETE trigger and fail when the transaction committed.
	_, directAutomation := createServiceAndAutomation("direct-automation")
	if _, err := st.Pool().Exec(ctx, `DELETE FROM automations_v2 WHERE id=$1`, directAutomation.ID); err != nil {
		t.Fatalf("direct Automation DELETE: %v", err)
	}
	if _, err := st.GetPluginAutomation(ctx, directAutomation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("directly deleted Automation remains: %v", err)
	}

	// A Service delete cascades the Automation and all of its typed children;
	// deferred checks must accept that the parent is no longer present.
	directService, directServiceAutomation := createServiceAndAutomation("direct-service")
	if _, err := st.Pool().Exec(ctx, `DELETE FROM services WHERE id=$1`, directService.ID); err != nil {
		t.Fatalf("direct Service DELETE with Plugin cascades: %v", err)
	}
	if _, err := st.GetPluginAutomation(ctx, directServiceAutomation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Service-cascaded Automation remains: %v", err)
	}

	// The public uninstall path performs several deletes in one transaction.
	// It must retain the same aggregate guarantees and complete cleanly.
	boundService, boundAutomation := createServiceAndAutomation("uninstall")
	if err := st.UninstallPlugin(ctx, installation.ID); err != nil {
		t.Fatalf("uninstall Plugin cascade: %v", err)
	}
	if _, err := st.GetService(ctx, boundService.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uninstalled bound Service remains: %v", err)
	}
	if _, err := st.GetPluginAutomation(ctx, boundAutomation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uninstalled Automation remains: %v", err)
	}
}

// TestPGClaimRunDispatchFencesConcurrentDisableAndUninstall exercises the
// real row-lock boundary used by the reconciler.  A claim begun before a
// disable must wait for the conflicting Installation update, revalidate the
// committed state, and never publish a token/snapshot.  Once a claim does
// commit, uninstall refuses to erase the scheduling run before Job creation.
func TestPGClaimRunDispatchFencesConcurrentDisableAndUninstall(t *testing.T) {
	ctx := context.Background()
	st, fixtureRunID := pgTestStore(t)
	fixtureRun, err := st.GetRun(ctx, fixtureRunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginGitea, PluginEnabled: true, Capabilities: []string{}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetProviderConfig(ctx, domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: fixtureRun.ProjectID, Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, AccessTokenEnc: []byte("ciphertext"),
		Scopes: []string{}, ConfigRevision: cfg.ConfigRevision, CreatedAt: time.Now(),
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: fixtureRun.ProjectID, Name: "claimed-repo",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "owner/claimed", DefaultBranch: "main", CreatedAt: time.Now(),
	}
	if err := st.CreatePluginBoundService(ctx, svc, &domain.ServiceRepositoryBinding{
		ServiceID: svc.ID, InstallationID: installation.ID, ProviderRepoID: "claimed",
		RepositoryPath: "owner/claimed", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: fixtureRun.ProjectID, ServiceID: svc.ID, Status: domain.StatusQueued, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Hold a conflicting update lock.  Claim's preliminary lookup can observe
	// the old row, but its FOR SHARE lock must wait and then see disabled.
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET status='disabled' WHERE id=$1`, installation.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	claimResult := make(chan error, 1)
	go func() {
		_, claimErr := st.ClaimRunDispatch(context.Background(), run.ID, "claim-blocked", "token-hash", "PreparingWorkspace", installation.ID,
			[]domain.RunPluginSnapshot{{RunID: run.ID, InstallationID: installation.ID}})
		claimResult <- claimErr
	}()
	select {
	case err := <-claimResult:
		t.Fatalf("claim escaped conflicting installation lock: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Expected: the claim remains blocked on the Installation row.
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-claimResult; !errors.Is(err, ErrDispatchClaimUnavailable) {
		t.Fatalf("claim after concurrent disable err=%v want ErrDispatchClaimUnavailable", err)
	}
	stillQueued, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillQueued.Status != domain.StatusQueued || stillQueued.TokenHash != "" {
		t.Fatalf("failed claim mutated run: %+v", stillQueued)
	}
	if snapshots, err := st.ListRunPluginSnapshots(ctx, run.ID); err != nil || len(snapshots) != 0 {
		t.Fatalf("failed claim snapshots=%v err=%v", snapshots, err)
	}

	if _, err := st.Pool().Exec(ctx, `UPDATE plugin_installations SET status='enabled' WHERE id=$1`, installation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimRunDispatch(ctx, run.ID, "claim-durable", "token-hash", "PreparingWorkspace", installation.ID,
		[]domain.RunPluginSnapshot{{RunID: run.ID, InstallationID: installation.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UninstallPlugin(ctx, installation.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("uninstall erased scheduling run: err=%v want ErrConflict", err)
	}
	if _, err := st.FailRunDispatch(ctx, run.ID, "claim-durable", "Failed", "test cleanup", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UninstallPlugin(ctx, installation.ID); err != nil {
		t.Fatalf("uninstall after terminal dispatch: %v", err)
	}
}
