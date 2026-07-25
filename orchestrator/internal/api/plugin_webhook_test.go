package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

func seedPluginWebhookAutomation(t *testing.T, kind, action string) (*testPluginWebhookFixture, string) {
	t.Helper()
	ts, st, _ := newCipherServer(t, nil, "")
	configured := do(t, http.MethodPut, ts.URL+"/api/v1/system/providers/gitea", consoleToken, map[string]any{
		"base_url": "https://gitea.example.test", "plugin_enabled": true,
		"client_id": "cloud", "client_secret": "oauth-secret", "webhook_secret": "hook-secret",
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
	// This fixture exercises webhook ingress. Create its already-reconciled
	// Automation directly so it does not depend on a real provider hook endpoint;
	// lifecycle behaviour is covered separately against a controllable upstream.
	automation := &domain.PluginAutomation{ID: domain.NewID(), ServiceID: svc.ID, InstallationID: installation.ID,
		Name: "trigger", TriggerKind: "scm", PromptTemplate: "Handle {{event}} in {{repository}}", Enabled: true, IgnoreJCode: true, CreatedAt: now}
	if err := st.CreatePluginAutomation(context.Background(), automation, &domain.SCMTrigger{AutomationID: automation.ID}, []domain.SCMAction{{AutomationID: automation.ID, ServiceID: svc.ID, EventFamily: kind, Action: action}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return &testPluginWebhookFixture{tsURL: ts.URL, st: st, serviceID: svc.ID}, automation.ID
}

type testPluginWebhookFixture struct {
	tsURL     string
	st        *store.MemStore
	serviceID string
}

func (f *testPluginWebhookFixture) postGitea(t *testing.T, event, delivery string, payload map[string]any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("hook-secret"))
	_, _ = mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, f.tsURL+"/webhooks/gitea", bytes.NewReader(body))
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
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitea", nil)
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Event-Type", "pull_request_review_approved")
	req.Header.Set("X-Gitea-Delivery", "delivery")
	provider, eventType, delivery := pluginWebhookHeaders(req)
	if provider != "gitea" || eventType != "pull_request_review_approved" || delivery != "delivery" {
		t.Fatalf("headers normalized to %q %q %q", provider, eventType, delivery)
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
