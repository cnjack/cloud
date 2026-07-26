package store

import (
	"context"
	"errors"
	"strconv"
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

func TestDeleteExpiredWebhookReceiptsHonorsThirtyDayCutoff(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	old := &domain.WebhookReceipt{ID: "old", Provider: domain.PluginGitHub, DeliveryID: "old", ReceivedAt: now.Add(-31 * 24 * time.Hour)}
	fresh := &domain.WebhookReceipt{ID: "fresh", Provider: domain.PluginGitHub, DeliveryID: "fresh", ReceivedAt: now.Add(-29 * 24 * time.Hour)}
	if claimed, err := st.ClaimWebhookReceipt(ctx, old); err != nil || !claimed {
		t.Fatalf("claim old receipt = %v, %v", claimed, err)
	}
	if claimed, err := st.ClaimWebhookReceipt(ctx, fresh); err != nil || !claimed {
		t.Fatalf("claim fresh receipt = %v, %v", claimed, err)
	}
	deleted, err := st.DeleteExpiredWebhookReceipts(ctx, now)
	if err != nil || deleted != 1 {
		t.Fatalf("delete expired = %d, %v; want 1, nil", deleted, err)
	}
	if _, ok := st.webhookReceipts[string(domain.PluginGitHub)+"|old"]; ok {
		t.Fatal("expired receipt was retained")
	}
	if _, ok := st.webhookReceipts[string(domain.PluginGitHub)+"|fresh"]; !ok {
		t.Fatal("fresh receipt was deleted")
	}
}

func TestDeleteExpiredWebhookReceiptsUsesBoundedBatch(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	for i := 0; i < webhookReceiptDeleteBatchSize+1; i++ {
		delivery := "old-" + strconv.Itoa(i)
		claimed, err := st.ClaimWebhookReceipt(ctx, &domain.WebhookReceipt{
			ID: delivery, Provider: domain.PluginGitHub, DeliveryID: delivery, ReceivedAt: now.Add(-31 * 24 * time.Hour),
		})
		if err != nil || !claimed {
			t.Fatalf("seed %d: claimed=%v err=%v", i, claimed, err)
		}
	}
	deleted, err := st.DeleteExpiredWebhookReceipts(ctx, now)
	if err != nil || deleted != webhookReceiptDeleteBatchSize {
		t.Fatalf("first batch=%d,%v want %d,nil", deleted, err, webhookReceiptDeleteBatchSize)
	}
	deleted, err = st.DeleteExpiredWebhookReceipts(ctx, now)
	if err != nil || deleted != 1 {
		t.Fatalf("second batch=%d,%v want 1,nil", deleted, err)
	}
}

func TestUpdatePluginInstallationDoesNotAppendCredentialVersionForMetadataOnlyChange(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	installation := &domain.PluginInstallation{
		ID: "i", ProjectID: "p", Provider: domain.PluginGitLab, Status: domain.PluginStatusEnabled,
		AccessTokenEnc: []byte("access"), RefreshTokenEnc: []byte("refresh"), CreatedAt: time.Now().UTC(),
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	originalVersion := installation.CredentialVersionID
	installation.Status = domain.PluginStatusActionRequired
	installation.LastHealthError = "provider unavailable"
	installation.ConsentVersion = "2026-07"
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if installation.CredentialVersionID != originalVersion || len(st.pluginCredentialVersions) != 1 {
		t.Fatalf("metadata update rotated credential version: installation=%q versions=%d", installation.CredentialVersionID, len(st.pluginCredentialVersions))
	}
	installation.AccessTokenEnc = []byte("new-access")
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if installation.CredentialVersionID == originalVersion || len(st.pluginCredentialVersions) != 2 {
		t.Fatalf("credential change did not append immutable version: installation=%q versions=%d", installation.CredentialVersionID, len(st.pluginCredentialVersions))
	}
}

func seedPluginSecretRetentionRun(t *testing.T, st *MemStore, runID string) (*domain.PluginInstallation, domain.Run) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.GetProject(ctx, "p"); errors.Is(err, ErrNotFound) {
		if err := st.CreateProject(ctx, &domain.Project{ID: "p", Name: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	serviceID := "s-" + runID
	if err := st.CreateService(ctx, &domain.Service{ID: serviceID, ProjectID: "p", Name: serviceID, RepoKind: domain.RepoKindRaw}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: runID, ProjectID: "p", ServiceID: serviceID, Status: domain.StatusQueued}
	if err := st.CreateRun(ctx, &run); err != nil {
		t.Fatal(err)
	}
	installation, err := st.GetPluginInstallation(ctx, "i")
	if errors.Is(err, ErrNotFound) {
		if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginGitLab, PluginEnabled: true, BaseURL: "https://gitlab.one"}); err != nil {
			t.Fatal(err)
		}
		cfg, err := st.GetProviderConfig(ctx, domain.PluginGitLab)
		if err != nil {
			t.Fatal(err)
		}
		installation = &domain.PluginInstallation{ID: "i", ProjectID: "p", Provider: domain.PluginGitLab, Status: domain.PluginStatusEnabled, AccessTokenEnc: []byte("v1"), ConfigRevision: cfg.ConfigRevision}
		if err := st.CreatePluginInstallation(ctx, installation); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{RunID: run.ID, InstallationID: installation.ID}}); err != nil {
		t.Fatal(err)
	}
	return installation, run
}

func TestPluginSecretVersionGCRetainsActiveRunAndTerminalSnapshotAudit(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	installation, run := seedPluginSecretRetentionRun(t, st, "r")
	originalCredentialVersion := installation.CredentialVersionID
	originalProviderKey := providerConfigVersionKey(domain.PluginGitLab, installation.ConfigRevision)
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginGitLab, PluginEnabled: true, BaseURL: "https://gitlab.two"}); err != nil {
		t.Fatal(err)
	}
	installation, _ = st.GetPluginInstallation(ctx, installation.ID)
	installation.AccessTokenEnc = []byte("v2")
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if credentials, providers, err := st.DeleteUnreferencedPluginSecretVersions(ctx, 100); err != nil || credentials != 0 || providers != 0 {
		t.Fatalf("active run GC=(%d,%d,%v), want 0,0,nil", credentials, providers, err)
	}
	if _, ok := st.pluginCredentialVersions[originalCredentialVersion]; !ok {
		t.Fatal("active run lost its credential version")
	}
	if _, ok := st.providerConfigVersions[originalProviderKey]; !ok {
		t.Fatal("active run lost its provider configuration version")
	}
	st.runs[run.ID] = domain.Run{ID: run.ID, ProjectID: run.ProjectID, ServiceID: run.ServiceID, Status: domain.StatusSucceeded}
	pendingClaim := domain.PluginKanbanClaim{AutomationID: "kanban", DocumentID: "card", RunID: run.ID}
	st.pluginKanbanClaims[pluginKanbanClaimKey(pendingClaim.AutomationID, pendingClaim.DocumentID)] = pendingClaim
	if credentials, providers, err := st.DeleteUnreferencedPluginSecretVersions(ctx, 100); err != nil || credentials != 0 || providers != 0 {
		t.Fatalf("pending writeback GC=(%d,%d,%v), want 0,0,nil", credentials, providers, err)
	}
	if _, ok := st.pluginCredentialVersions[originalCredentialVersion]; !ok {
		t.Fatal("pending Kanban writeback lost its credential version")
	}
	if _, ok := st.providerConfigVersions[originalProviderKey]; !ok {
		t.Fatal("pending Kanban writeback lost its provider configuration version")
	}
	if wrote, err := st.MarkPluginKanbanWriteback(ctx, pendingClaim.AutomationID, pendingClaim.DocumentID, time.Now().UTC()); err != nil || !wrote {
		t.Fatalf("mark writeback wrote=%v err=%v", wrote, err)
	}
	if credentials, providers, err := st.DeleteUnreferencedPluginSecretVersions(ctx, 100); err != nil || credentials != 1 || providers != 1 {
		t.Fatalf("completed writeback GC=(%d,%d,%v), want 1,1,nil", credentials, providers, err)
	}
	snapshots, err := st.ListRunPluginSnapshots(ctx, run.ID)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("terminal snapshot=%+v err=%v", snapshots, err)
	}
	snapshot := snapshots[0]
	if snapshot.CredentialVersionID != originalCredentialVersion || snapshot.ProviderConfigRevision != 1 || len(snapshot.AccessTokenEnc) != 0 || snapshot.ProviderBaseURL != "" {
		t.Fatalf("terminal snapshot did not retain only audit identifiers: %+v", snapshot)
	}
}

func TestPluginSecretVersionGCWaitsForLastActiveSnapshotReference(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	installation, first := seedPluginSecretRetentionRun(t, st, "r1")
	originalVersion := installation.CredentialVersionID
	_, second := seedPluginSecretRetentionRun(t, st, "r2")
	installation, _ = st.GetPluginInstallation(ctx, installation.ID)
	installation.AccessTokenEnc = []byte("v2")
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	st.runs[first.ID] = domain.Run{ID: first.ID, ProjectID: first.ProjectID, ServiceID: first.ServiceID, Status: domain.StatusSucceeded}
	if _, _, err := st.DeleteUnreferencedPluginSecretVersions(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.pluginCredentialVersions[originalVersion]; !ok {
		t.Fatal("shared credential version was deleted while second run remained active")
	}
	st.runs[second.ID] = domain.Run{ID: second.ID, ProjectID: second.ProjectID, ServiceID: second.ServiceID, Status: domain.StatusSucceeded}
	if credentials, _, err := st.DeleteUnreferencedPluginSecretVersions(ctx, 100); err != nil || credentials != 1 {
		t.Fatalf("last reference GC credentials=%d err=%v, want 1,nil", credentials, err)
	}
	if _, ok := st.pluginCredentialVersions[originalVersion]; ok {
		t.Fatal("shared credential version survived after its final active reference ended")
	}
}

func TestPGPluginSecretVersionGCAndSnapshotFKMigration(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)
	var snapshotFKs int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname IN ('run_plugin_snapshots_provider_version_fk','run_plugin_snapshots_credential_version_fk')`).Scan(&snapshotFKs); err != nil {
		t.Fatal(err)
	}
	if snapshotFKs != 0 {
		t.Fatalf("0052 left %d terminal snapshot secret FKs", snapshotFKs)
	}
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	config := &domain.ProviderConfig{Provider: domain.PluginGitLab, PluginEnabled: true, BaseURL: "https://gitlab.one"}
	if err := st.UpsertProviderConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: run.ProjectID, Provider: domain.PluginGitLab, Status: domain.PluginStatusEnabled, Scopes: []string{}, AccessTokenEnc: []byte("v1"), ConfigRevision: config.ConfigRevision}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	originalVersion := installation.CredentialVersionID
	installation.LastHealthError = "transient health observation"
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if installation.CredentialVersionID != originalVersion {
		t.Fatalf("metadata-only PG update rotated credential: got %q want %q", installation.CredentialVersionID, originalVersion)
	}
	installation.LastHealthError = ""
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{RunID: run.ID, InstallationID: installation.ID}}); err != nil {
		t.Fatal(err)
	}
	snapshotProviderRevision := config.ConfigRevision
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginGitLab, PluginEnabled: true, BaseURL: "https://gitlab.two"}); err != nil {
		t.Fatal(err)
	}
	installation, err = st.GetPluginInstallation(ctx, installation.ID)
	if err != nil {
		t.Fatal(err)
	}
	installation.AccessTokenEnc = []byte("v2")
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteUnreferencedPluginSecretVersions(ctx, 100); err != nil {
		t.Fatalf("active PG run GC: %v", err)
	}
	var originalStillPinned int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM plugin_credential_versions WHERE id=$1`, originalVersion).Scan(&originalStillPinned); err != nil || originalStillPinned != 1 {
		t.Fatalf("active snapshot credential retained=%d err=%v, want 1,nil", originalStillPinned, err)
	}
	if _, err := st.ScheduleRun(ctx, run.ID, "job", "token", "launch"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, run.ID, "done", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	automationID, documentID := "pending-writeback-"+domain.NewID(), "card-"+domain.NewID()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO automation_kanban_claims(automation_id,installation_id,document_id,run_id) VALUES($1,$2,$3,$4)`, automationID, installation.ID, documentID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteUnreferencedPluginSecretVersions(ctx, 100); err != nil {
		t.Fatalf("pending PG writeback GC: %v", err)
	}
	var originalStillPinnedByWriteback, providerStillPinnedByWriteback int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM plugin_credential_versions WHERE id=$1`, originalVersion).Scan(&originalStillPinnedByWriteback); err != nil || originalStillPinnedByWriteback != 1 {
		t.Fatalf("pending writeback credential retained=%d err=%v, want 1,nil", originalStillPinnedByWriteback, err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM provider_config_versions WHERE provider=$1 AND config_revision=$2`, domain.PluginGitLab, snapshotProviderRevision).Scan(&providerStillPinnedByWriteback); err != nil || providerStillPinnedByWriteback != 1 {
		t.Fatalf("pending writeback provider version retained=%d err=%v, want 1,nil", providerStillPinnedByWriteback, err)
	}
	if wrote, err := st.MarkPluginKanbanWriteback(ctx, automationID, documentID, time.Now().UTC()); err != nil || !wrote {
		t.Fatalf("mark PG writeback wrote=%v err=%v", wrote, err)
	}
	if _, _, err := st.DeleteUnreferencedPluginSecretVersions(ctx, 100); err != nil {
		t.Fatalf("completed PG writeback GC: %v", err)
	}
	var originalReclaimed, providerReclaimed int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM plugin_credential_versions WHERE id=$1`, originalVersion).Scan(&originalReclaimed); err != nil || originalReclaimed != 0 {
		t.Fatalf("completed writeback credential retained=%d err=%v, want 0,nil", originalReclaimed, err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM provider_config_versions WHERE provider=$1 AND config_revision=$2`, domain.PluginGitLab, snapshotProviderRevision).Scan(&providerReclaimed); err != nil || providerReclaimed != 0 {
		t.Fatalf("completed writeback provider version retained=%d err=%v, want 0,nil", providerReclaimed, err)
	}
	snapshots, err := st.ListRunPluginSnapshots(ctx, run.ID)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("terminal PG snapshots=%+v err=%v", snapshots, err)
	}
	if snapshots[0].CredentialVersionID != originalVersion || len(snapshots[0].AccessTokenEnc) != 0 {
		t.Fatalf("terminal PG snapshot lost audit or retained secret: %+v", snapshots[0])
	}
}

func TestWebhookReceiptAuthenticatedPayloadDigestDeduplicatesAndExpires(t *testing.T) {
	ctx, st := context.Background(), NewMemStore()
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	first := &domain.WebhookReceipt{
		ID: "first", Provider: domain.PluginGitea, DeliveryID: "delivery-1",
		PayloadDigest: "authenticated-body", ReceivedAt: now.Add(-31 * 24 * time.Hour),
	}
	if claimed, err := st.ClaimWebhookReceipt(ctx, first); err != nil || !claimed {
		t.Fatalf("claim first = %v, %v", claimed, err)
	}
	replay := *first
	replay.ID = "replay"
	replay.DeliveryID = "forged-delivery-id"
	replay.ReceivedAt = now
	if claimed, err := st.ClaimWebhookReceipt(ctx, &replay); err != nil || claimed {
		t.Fatalf("claim replay = %v, %v; want duplicate", claimed, err)
	}
	if deleted, err := st.DeleteExpiredWebhookReceipts(ctx, now); err != nil || deleted != 1 {
		t.Fatalf("delete expired = %d, %v", deleted, err)
	}
	if claimed, err := st.ClaimWebhookReceipt(ctx, &replay); err != nil || !claimed {
		t.Fatalf("digest was not released after 30-day cleanup: %v, %v", claimed, err)
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
	if len(st.pluginCredentialVersions) != 0 {
		t.Fatalf("uninstall retained unreferenced credential ciphertext: %d versions", len(st.pluginCredentialVersions))
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
