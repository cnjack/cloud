package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

type giteaHookUpstream struct {
	mu          sync.Mutex
	hook        bool
	postCount   int
	deleteCount int
	lastAuth    string
	lastBody    map[string]any
	failCreate  bool
}

func newGiteaHookUpstream(t *testing.T) (*httptest.Server, *giteaHookUpstream) {
	t.Helper()
	state := &giteaHookUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/acme/repo/hooks", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.lastAuth = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodGet:
			if !state.hook {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 19, "active": true, "events": []string{"push", "pull_request", "pull_request_sync", "pull_request_review", "issues", "issue_comment", "pull_request_comment", "status", "create", "delete", "release"}, "config": map[string]string{"url": "https://cloud.example/webhooks/gitea"}}})
		case http.MethodPost:
			if state.failCreate {
				http.Error(w, "provider rejected hook", http.StatusForbidden)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&state.lastBody)
			state.hook = true
			state.postCount++
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/repos/acme/repo/hooks/19", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.lastAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodDelete {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		state.hook = false
		state.deleteCount++
		w.WriteHeader(http.StatusNoContent)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	return upstream, state
}

func seedGiteaLifecycle(t *testing.T, upstreamURL string) (*httptest.Server, *store.MemStore, string, string) {
	t.Helper()
	ts, st, cfg := newCipherServer(t, nil, "")
	cipher, err := auth.NewCipher(cfg.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	access, err := cipher.EncryptString("installation-token")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := cipher.EncryptString("cluster-hook-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.UpsertClusterSettings(ctx, &domain.ClusterSettings{PublicURL: "https://cloud.example", SetupComplete: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginGitea, BaseURL: upstreamURL, PluginEnabled: true, WebhookSecretEnc: secret}); err != nil {
		t.Fatal(err)
	}
	providerConfig, err := st.GetProviderConfig(ctx, domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	projectID := newProject(t, ts, "scm-webhook-lifecycle")
	now := time.Now().UTC()
	installation := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: projectID, Provider: domain.PluginGitea, Status: domain.PluginStatusEnabled, AccessTokenEnc: access, ConsentVersion: "v1", ConsentedAt: now, ConfigRevision: providerConfig.ConfigRevision, CreatedAt: now}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	repoID := int64(42)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: projectID, Name: "repo", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "acme/repo", ProviderRepoID: &repoID, DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: now}
	binding := &domain.ServiceRepositoryBinding{ServiceID: svc.ID, InstallationID: installation.ID, ProviderRepoID: "42", RepositoryPath: "acme/repo", CloneURL: upstreamURL + "/acme/repo.git", DefaultBranch: "main", CreatedAt: now}
	if err := st.CreatePluginBoundService(ctx, svc, binding); err != nil {
		t.Fatal(err)
	}
	return ts, st, projectID, svc.ID
}

func createSCMAutomation(t *testing.T, ts *httptest.Server, projectID, serviceID, family, action string) domain.PluginAutomationSpec {
	t.Helper()
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/automations", consoleToken, map[string]any{
		"service_id": serviceID, "name": family + "-" + action, "prompt_template": "handle event",
		"scm": map[string]any{"actions": []map[string]string{{"event_family": family, "action": action}}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create SCM Automation: status=%d", resp.StatusCode)
	}
	var spec domain.PluginAutomationSpec
	decode(t, resp, &spec)
	return spec
}

func TestPluginSCMWebhookLifecycleGitea(t *testing.T) {
	upstream, hook := newGiteaHookUpstream(t)
	ts, st, projectID, serviceID := seedGiteaLifecycle(t, upstream.URL)
	first := createSCMAutomation(t, ts, projectID, serviceID, "push", "updated")
	hook.mu.Lock()
	if hook.postCount != 1 || hook.lastAuth != "Bearer installation-token" {
		t.Fatalf("hook create=%d auth=%q", hook.postCount, hook.lastAuth)
	}
	config, _ := hook.lastBody["config"].(map[string]any)
	if config["secret"] != "cluster-hook-secret" {
		t.Fatalf("expected DB-backed webhook secret, body=%v", hook.lastBody)
	}
	hook.mu.Unlock()
	bound, err := st.GetWebhookBinding(context.Background(), serviceID)
	if err != nil || bound.Status != domain.WebhookBindingActive {
		t.Fatalf("binding=%+v err=%v", bound, err)
	}

	second := createSCMAutomation(t, ts, projectID, serviceID, "pull_request", "opened")
	hook.mu.Lock()
	if hook.postCount != 1 { // the second enabled SCM Automation reuses the hook
		t.Fatalf("hook created %d times", hook.postCount)
	}
	hook.mu.Unlock()

	// Disabling one of two enabled SCM Automations must retain the shared hook.
	disabled := false
	resp := do(t, http.MethodPatch, ts.URL+"/api/v1/automations/"+first.Automation.ID, consoleToken, map[string]any{"enabled": disabled})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable first Automation: %d", resp.StatusCode)
	}
	resp.Body.Close()
	hook.mu.Lock()
	if hook.deleteCount != 0 {
		t.Fatalf("deleted shared hook while one Automation remains")
	}
	hook.mu.Unlock()

	// Removing the final enabled SCM Automation removes both the provider hook
	// and the inspectable local binding.
	resp = do(t, http.MethodDelete, ts.URL+"/api/v1/automations/"+second.Automation.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete final Automation: %d", resp.StatusCode)
	}
	resp.Body.Close()
	hook.mu.Lock()
	if hook.deleteCount != 1 || hook.hook {
		t.Fatalf("hook delete_count=%d exists=%v", hook.deleteCount, hook.hook)
	}
	hook.mu.Unlock()
	if _, err := st.GetWebhookBinding(context.Background(), serviceID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("binding remains after final removal: %v", err)
	}
}

func TestPluginSCMWebhookCreateFailureIsVisible(t *testing.T) {
	upstream, hook := newGiteaHookUpstream(t)
	hook.failCreate = true
	ts, st, projectID, serviceID := seedGiteaLifecycle(t, upstream.URL)
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/automations", consoleToken, map[string]any{
		"service_id": serviceID, "name": "push", "prompt_template": "handle event",
		"scm": map[string]any{"actions": []map[string]string{{"event_family": "push", "action": "updated"}}},
	})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("create failure status=%d want 502", resp.StatusCode)
	}
	resp.Body.Close()
	items, err := st.ListPluginAutomationsByProject(context.Background(), projectID)
	if err != nil || len(items) != 1 || items[0].LastError != pluginWebhookLifecycleError {
		t.Fatalf("Automation failure not visible: items=%+v err=%v", items, err)
	}
	binding, err := st.GetWebhookBinding(context.Background(), serviceID)
	if err != nil || binding.Status != domain.WebhookBindingError || binding.LastError != pluginWebhookLifecycleError {
		t.Fatalf("webhook failure not visible: binding=%+v err=%v", binding, err)
	}
}
