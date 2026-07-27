package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func seedPluginWebhookAutomation(t *testing.T, kind, action string) (*testPluginWebhookFixture, string) {
	t.Helper()
	ts, st, cfg := newCipherServer(t, nil, "")
	configured := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": "https://gitea.example.test", "plugin_enabled": true,
		"client_id": "cloud", "client_secret": "oauth-secret",
	})
	if configured.StatusCode != http.StatusOK {
		t.Fatalf("configure provider: %d", configured.StatusCode)
	}
	configured.Body.Close()
	projectID := newProject(t, ts, "webhook-plugin")
	now := time.Now().UTC()
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: projectID, Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, ConsentVersion: "v1",
		ConsentedAt: now, CreatedAt: now, ConfigRevision: 1,
	}
	if err := st.CreatePluginInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	repoID := int64(42)
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: projectID, Name: "repo",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "acme/repo", ProviderRepoID: &repoID,
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: now,
	}
	binding := &domain.ServiceRepositoryBinding{
		ServiceID: svc.ID, InstallationID: installation.ID, ProviderRepoID: "42",
		RepositoryPath: "acme/repo", CloneURL: "https://gitea.example.test/acme/repo.git",
		DefaultBranch: "main", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreatePluginBoundService(context.Background(), svc, binding); err != nil {
		t.Fatal(err)
	}
	cipher, err := auth.NewCipher(cfg.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	const (
		hookID     = "fixture-binding"
		hookSecret = "per-binding-hook-secret"
	)
	hookSecretEnc, err := cipher.EncryptString(hookSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWebhookBinding(context.Background(), &domain.WebhookBinding{
		ServiceID: svc.ID, Provider: domain.ProviderGitea,
		Endpoint:  "https://cloud.example/webhooks/gitea/" + hookID,
		HookID:    hookID,
		SecretEnc: hookSecretEnc,
		Status:    domain.WebhookBindingActive,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// This fixture exercises webhook ingress. Create its already-reconciled
	// Automation directly so it does not depend on a real provider hook endpoint;
	// lifecycle behaviour is covered separately against a controllable upstream.
	automation := &domain.PluginAutomation{ID: domain.NewID(), ServiceID: svc.ID, InstallationID: installation.ID,
		Name: "trigger", TriggerKind: "scm", PromptTemplate: "Handle {{event}} in {{repository}}", Enabled: true, IgnoreJCode: true, CreatedAt: now}
	if err := st.CreatePluginAutomation(context.Background(), automation, &domain.SCMTrigger{AutomationID: automation.ID}, []domain.SCMAction{{AutomationID: automation.ID, ServiceID: svc.ID, EventFamily: kind, Action: action}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return &testPluginWebhookFixture{
		tsURL: ts.URL, st: st, projectID: projectID, installationID: installation.ID,
		serviceID: svc.ID, hookID: hookID, hookSecret: hookSecret,
	}, automation.ID
}

type testPluginWebhookFixture struct {
	tsURL          string
	st             *store.MemStore
	projectID      string
	installationID string
	serviceID      string
	hookID         string
	hookSecret     string
}

func (f *testPluginWebhookFixture) postGitea(t *testing.T, event, delivery string, payload map[string]any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(f.hookSecret))
	_, _ = mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, f.tsURL+"/webhooks/gitea/"+f.hookID, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", event)
	req.Header.Set("X-Gitea-Delivery", delivery)
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPluginWebhookHeadersPreferSpecificGiteaEventType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitea/binding-1", nil)
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Event-Type", "pull_request_review_approved")
	req.Header.Set("X-Gitea-Delivery", "delivery")
	provider, eventType, delivery, bindingID := pluginWebhookHeaders(req)
	if provider != "gitea" || eventType != "pull_request_review_approved" ||
		delivery != "delivery" || bindingID != "binding-1" {
		t.Fatalf("headers normalized to %q %q %q %q", provider, eventType, delivery, bindingID)
	}
}

func TestPluginAutomationModelErrorIsActionable(t *testing.T) {
	tests := []struct {
		name    string
		outcome modelcfg.SelectOutcome
		err     error
		want    string
	}{
		{name: "not selected", outcome: modelcfg.SelectNotSelected, want: "Several project models are available, but this Service has no default model."},
		{name: "not configured", outcome: modelcfg.SelectNotConfigured, want: "No model is authorized for this Project."},
		{name: "temporary", outcome: modelcfg.SelectOK, err: errors.New("database unavailable"), want: "Automation model could not be resolved because of a temporary internal error."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pluginAutomationModelError(tt.outcome, tt.err); got != tt.want {
				t.Fatalf("pluginAutomationModelError()=%q want %q", got, tt.want)
			}
		})
	}
}

func TestPluginWebhookExternalCommentUsesWholeBodyAndDeduplicates(t *testing.T) {
	f, automationID := seedPluginWebhookAutomation(t, "comment", "created")
	comment := "Context before the mention.\n@jcode please fix the failing test.\nKeep this constraint."
	payload := map[string]any{
		"action":     "created",
		"sender":     map[string]any{"id": 9001, "login": "external"},
		"repository": map[string]any{"id": 42, "full_name": "acme/repo", "default_branch": "main"},
		"issue":      map[string]any{"id": 7, "number": 7, "title": "bug"},
		"comment":    map[string]any{"id": 99, "body": comment, "html_url": "https://gitea.example/acme/repo/issues/7"},
	}
	resp := f.postGitea(t, "issue_comment", "delivery-comment-1", payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	runs, err := f.st.ListRunsByService(context.Background(), f.serviceID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	if runs[0].Prompt != comment || runs[0].TriggeredByUserID != nil ||
		runs[0].OriginAutomationID != automationID {
		t.Fatalf("run=%+v", runs[0])
	}
	duplicate := f.postGitea(t, "issue_comment", "delivery-comment-1", payload)
	duplicate.Body.Close()
	runs, _ = f.st.ListRunsByService(context.Background(), f.serviceID, 10)
	if len(runs) != 1 {
		t.Fatalf("duplicate delivery created %d runs", len(runs))
	}
}

func TestPluginWebhookAuthenticatedBodyReplayWithDifferentDeliveryIsDuplicate(t *testing.T) {
	f, _ := seedPluginWebhookAutomation(t, "comment", "created")
	payload := map[string]any{
		"action":     "created",
		"sender":     map[string]any{"id": 9001, "login": "external"},
		"repository": map[string]any{"id": 42, "full_name": "acme/repo", "default_branch": "main"},
		"issue":      map[string]any{"id": 7, "number": 7, "title": "bug"},
		"comment":    map[string]any{"id": 99, "body": "@jcode fix this", "html_url": "https://gitea.example/acme/repo/issues/7"},
	}
	for _, delivery := range []string{"delivery-replay-1", "delivery-replay-2"} {
		resp := f.postGitea(t, "issue_comment", delivery, payload)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("delivery %q status=%d", delivery, resp.StatusCode)
		}
		resp.Body.Close()
	}
	runs, err := f.st.ListRunsByService(context.Background(), f.serviceID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("authenticated payload replay created runs=%d err=%v", len(runs), err)
	}
}

func TestPluginWebhookRejectsRepositoryThatDoesNotMatchOpaqueBinding(t *testing.T) {
	f, _ := seedPluginWebhookAutomation(t, "push", "updated")
	payload := map[string]any{
		"ref": "refs/heads/main", "after": "sha-1",
		"sender":     map[string]any{"id": 8, "login": "dev"},
		"repository": map[string]any{"id": 99, "full_name": "other/repo", "default_branch": "main"},
	}
	resp := f.postGitea(t, "push", "delivery-wrong-repo", payload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("repository mismatch status=%d want 401", resp.StatusCode)
	}
	runs, _ := f.st.ListRunsByService(context.Background(), f.serviceID, 10)
	if len(runs) != 0 {
		t.Fatalf("repository mismatch created %d runs", len(runs))
	}
}

func TestPluginWebhookOpaqueBindingDispatchesOnlyItsService(t *testing.T) {
	f, _ := seedPluginWebhookAutomation(t, "push", "updated")
	now := time.Now().UTC()
	secondProject := &domain.Project{ID: domain.NewID(), Name: "second", CreatedAt: now}
	if err := f.st.CreateProject(context.Background(), secondProject); err != nil {
		t.Fatal(err)
	}
	secondInstallation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: secondProject.ID, Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, ConsentVersion: "v1", ConsentedAt: now, CreatedAt: now,
	}
	if err := f.st.CreatePluginInstallation(context.Background(), secondInstallation); err != nil {
		t.Fatal(err)
	}
	repoID := int64(42)
	secondService := &domain.Service{
		ID: domain.NewID(), ProjectID: secondProject.ID, Name: "same-repo",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "acme/repo", ProviderRepoID: &repoID,
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: now,
	}
	if err := f.st.CreatePluginBoundService(context.Background(), secondService, &domain.ServiceRepositoryBinding{
		ServiceID: secondService.ID, InstallationID: secondInstallation.ID, ProviderRepoID: "42",
		RepositoryPath: "acme/repo", CloneURL: "https://gitea.example.test/acme/repo.git",
		DefaultBranch: "main", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	secondAutomation := &domain.PluginAutomation{
		ID: domain.NewID(), ServiceID: secondService.ID, InstallationID: secondInstallation.ID,
		Name: "second", TriggerKind: "scm", PromptTemplate: "handle", Enabled: true, CreatedAt: now,
	}
	if err := f.st.CreatePluginAutomation(context.Background(), secondAutomation,
		&domain.SCMTrigger{AutomationID: secondAutomation.ID},
		[]domain.SCMAction{{AutomationID: secondAutomation.ID, ServiceID: secondService.ID, EventFamily: "push", Action: "updated"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"ref": "refs/heads/main", "after": "sha-isolated",
		"sender":     map[string]any{"id": 8, "login": "dev"},
		"repository": map[string]any{"id": 42, "full_name": "acme/repo", "default_branch": "main"},
	}
	resp := f.postGitea(t, "push", "delivery-isolated", payload)
	resp.Body.Close()
	firstRuns, _ := f.st.ListRunsByService(context.Background(), f.serviceID, 10)
	secondRuns, _ := f.st.ListRunsByService(context.Background(), secondService.ID, 10)
	if len(firstRuns) != 1 || len(secondRuns) != 0 {
		t.Fatalf("opaque binding dispatched first=%d second=%d", len(firstRuns), len(secondRuns))
	}
}

func TestPluginWebhookCoalescesQueuedPushToLatest(t *testing.T) {
	f, _ := seedPluginWebhookAutomation(t, "push", "updated")
	for i := 1; i <= 2; i++ {
		payload := map[string]any{
			"ref": "refs/heads/main", "after": "sha-" + strconv.Itoa(i),
			"sender":     map[string]any{"id": 8, "login": "dev"},
			"repository": map[string]any{"id": 42, "full_name": "acme/repo", "default_branch": "main"},
		}
		resp := f.postGitea(t, "push", "delivery-push-"+strconv.Itoa(i), payload)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("push %d status=%d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	runs, err := f.st.ListRunsByService(context.Background(), f.serviceID, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	var queued, superseded int
	for i := range runs {
		switch {
		case runs[i].Status == domain.StatusQueued:
			queued++
		case runs[i].Status == domain.StatusCanceled && runs[i].Phase == "Superseded":
			superseded++
		}
	}
	if queued != 1 || superseded != 1 {
		t.Fatalf("queued=%d superseded=%d runs=%+v", queued, superseded, runs)
	}
}

func TestPluginGitLabWebhookUsesPerBindingTokenAndDispatches(t *testing.T) {
	ts, st, cfg := newCipherServer(t, nil, "")
	ctx := context.Background()
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{
		Provider: domain.PluginGitLab, BaseURL: "https://gitlab.example.test", PluginEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	projectID := newProject(t, ts, "gitlab-webhook")
	now := time.Now().UTC()
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: projectID, Provider: domain.PluginGitLab,
		Status: domain.PluginStatusEnabled, ConsentVersion: "v1", ConsentedAt: now, CreatedAt: now,
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	repoID := int64(42)
	svc := &domain.Service{
		ID: domain.NewID(), ProjectID: projectID, Name: "gitlab-repo",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitLab,
		RepoOwnerName: "acme/repo", ProviderRepoID: &repoID,
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: now,
	}
	if err := st.CreatePluginBoundService(ctx, svc, &domain.ServiceRepositoryBinding{
		ServiceID: svc.ID, InstallationID: installation.ID, ProviderRepoID: "42",
		RepositoryPath: "acme/repo", CloneURL: "https://gitlab.example.test/acme/repo.git",
		DefaultBranch: "main", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: domain.NewID(), ServiceID: svc.ID, InstallationID: installation.ID,
		Name: "gitlab-push", TriggerKind: "scm", PromptTemplate: "handle {{event}}",
		Enabled: true, CreatedAt: now,
	}
	if err := st.CreatePluginAutomation(ctx, automation, &domain.SCMTrigger{AutomationID: automation.ID},
		[]domain.SCMAction{{AutomationID: automation.ID, ServiceID: svc.ID, EventFamily: "push", Action: "updated"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	cipher, err := auth.NewCipher(cfg.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	const hookSecret = "gitlab-per-binding-token"
	secretEnc, err := cipher.EncryptString(hookSecret)
	if err != nil {
		t.Fatal(err)
	}
	const hookID = "gitlab-binding"
	if err := st.UpsertWebhookBinding(ctx, &domain.WebhookBinding{
		ServiceID: svc.ID, Provider: domain.ProviderGitLab,
		Endpoint: ts.URL + "/webhooks/gitlab/" + hookID, HookID: hookID, SecretEnc: secretEnc,
		Status: domain.WebhookBindingActive, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"object_kind":"push","user":{"id":8,"username":"dev"},"project":{"id":42,"path_with_namespace":"acme/repo","default_branch":"main"},"ref":"refs/heads/main","after":"abc123"}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/gitlab/"+hookID, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	req.Header.Set("X-Gitlab-Event-UUID", "gitlab-delivery")
	req.Header.Set("X-Gitlab-Token", hookSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GitLab webhook status=%d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()
	runs, err := st.ListRunsByService(ctx, svc.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].BaseBranch != "main" {
		t.Fatalf("GitLab runs=%+v err=%v", runs, err)
	}
}

func TestGitHubSCMAutomationCreatesRunnableAutomationOriginRun(t *testing.T) {
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{
		ConsoleToken: consoleToken,
		MasterKey:    validTokenKey(t),
	})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	secret, err := srv.cipher.EncryptString("github-hook-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProviderConfig(context.Background(), &domain.ProviderConfig{
		Provider: domain.PluginGitHub, BaseURL: "https://github.com",
		PluginEnabled: true, WebhookSecretEnc: secret,
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	registerTestServerStore(t, ts, st)

	projectID := newProject(t, ts, "github-scm-dispatch")
	now := time.Now().UTC()
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: projectID, Provider: domain.PluginGitHub,
		Status: domain.PluginStatusEnabled, ConsentVersion: "v1",
		ConsentedAt: now, CreatedAt: now, ConfigRevision: 1,
	}
	if err := st.CreatePluginInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	repoID := int64(42)
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: projectID, Name: "repository",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitHub,
		RepoOwnerName: "acme/repository", ProviderRepoID: &repoID,
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: now,
	}
	binding := &domain.ServiceRepositoryBinding{
		ServiceID: service.ID, InstallationID: installation.ID,
		ProviderRepoID: "42", RepositoryPath: "acme/repository",
		CloneURL:      "https://github.com/acme/repository.git",
		DefaultBranch: "main", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreatePluginBoundService(context.Background(), service, binding); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: domain.NewID(), ServiceID: service.ID, InstallationID: installation.ID,
		Name: "GitHub push", TriggerKind: "scm",
		PromptTemplate: "Handle {{event}} on {{ref}} at {{head_sha}}",
		Enabled:        true, IgnoreJCode: true, CreatedAt: now,
	}
	if err := st.CreatePluginAutomation(context.Background(), automation,
		&domain.SCMTrigger{AutomationID: automation.ID},
		[]domain.SCMAction{{
			AutomationID: automation.ID, ServiceID: service.ID,
			EventFamily: "push", Action: "updated",
		}}, nil, nil); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"ref": "refs/heads/feature/runtime", "after": "abc123",
		"sender": map[string]any{"id": 7, "login": "developer"},
		"repository": map[string]any{
			"id": 42, "full_name": "acme/repository", "default_branch": "main",
		},
		"commits": []map[string]any{{"added": []string{"README.md"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("github-hook-secret"))
	_, _ = mac.Write(payload)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/github", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "github-push-delivery-1")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GitHub webhook status=%d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()
	replayReq, err := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/github", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	replayReq.Header.Set("Content-Type", "application/json")
	replayReq.Header.Set("X-GitHub-Event", "push")
	replayReq.Header.Set("X-GitHub-Delivery", "github-push-forged-delivery-2")
	replayReq.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	replayResp, err := http.DefaultClient.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("GitHub replay status=%d", replayResp.StatusCode)
	}
	replayResp.Body.Close()

	runs, err := st.ListRunsByService(context.Background(), service.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	run := runs[0]
	if run.Status != domain.StatusQueued ||
		run.Origin != domain.RunOriginAutomation ||
		run.OriginAutomationID != automation.ID ||
		run.ModelName == "" ||
		run.BaseBranch != "feature/runtime" ||
		run.PRHeadBranch != "" ||
		run.Prompt != "Handle push.updated on refs/heads/feature/runtime at abc123" {
		t.Fatalf("GitHub Automation run=%+v", run)
	}
}
