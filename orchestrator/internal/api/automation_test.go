package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func seedAutomationModel(t *testing.T, f *webhookSetupFixture) string {
	t.Helper()
	model := &domain.Model{
		ID: "model-review", Name: "Review model", BaseURL: "http://model.test/v1",
		ModelName: "provider/review", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := f.st.CreateModel(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if err := f.st.GrantModel(context.Background(), model.ID, f.service.ProjectID); err != nil {
		t.Fatal(err)
	}
	return model.ID
}

func TestCreateReviewAutomationSynchronizesGiteaWithOwnerOAuth(t *testing.T) {
	f := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
		matchingIdentity: true, webhookConfigured: true, role: domain.RoleOwner,
	})
	modelID := seedAutomationModel(t, &f)
	response := do(t, http.MethodPost, f.ts.URL+"/api/v1/services/"+f.service.ID+"/automations", f.token, map[string]any{
		"name": "Gitea PR review", "instructions": "Review security and regressions.",
		"trigger_type": "pr_review", "model_id": modelID,
		"events": []string{"opened", "ready", "synchronize"}, "base_branch": "main",
		"include_drafts": false, "enabled": true,
	})
	if response.StatusCode != http.StatusCreated {
		var body bytes.Buffer
		_, _ = body.ReadFrom(response.Body)
		t.Fatalf("status=%d body=%s", response.StatusCode, body.String())
	}
	var got domain.Automation
	decode(t, response, &got)
	if got.ID == "" || got.ModelID != modelID || got.TriggerType != domain.AutomationTriggerPRReview || !got.Enabled {
		t.Fatalf("automation=%+v", got)
	}
	if len(f.provider.reviewCalls) != 1 {
		t.Fatalf("review webhook calls=%d want 1", len(f.provider.reviewCalls))
	}
	if call := f.factory.calls[0]; call.token != "user-oauth-token" || call.scheme != "Bearer" {
		t.Fatalf("factory call=%+v", call)
	}
	binding, err := f.st.GetWebhookBinding(context.Background(), f.service.ID)
	if err != nil || binding.Status != domain.WebhookBindingActive {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
}

func TestCreateReviewAutomationFailsVisiblyForUnsupportedProvider(t *testing.T) {
	f := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
		matchingIdentity: true, webhookConfigured: true, role: domain.RoleOwner,
	})
	modelID := seedAutomationModel(t, &f)
	f.service.Provider = domain.ProviderGitHub
	f.service.IntegrationID = nil
	if err := f.st.UpdateService(context.Background(), f.service); err != nil {
		t.Fatal(err)
	}
	response := do(t, http.MethodPost, f.ts.URL+"/api/v1/services/"+f.service.ID+"/automations", f.token, map[string]any{
		"name": "PR review", "instructions": "Review changes.", "trigger_type": "pr_review",
		"model_id": modelID, "events": []string{"opened"}, "base_branch": "main", "enabled": true,
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", response.StatusCode)
	}
	if code := webhookSetupErrorCode(t, response); code != "automatic_review_unsupported" {
		t.Fatalf("code=%q", code)
	}
}

func TestDisableReviewAutomationDoesNotRequireCurrentModelAccess(t *testing.T) {
	f := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
		matchingIdentity: true, webhookConfigured: true, role: domain.RoleOwner,
	})
	now := time.Now().UTC()
	a := &domain.Automation{
		ID: domain.NewID(), ServiceID: f.service.ID, Name: "PR guard", Instructions: "Review this PR.",
		TriggerType: domain.AutomationTriggerPRReview, ModelID: "removed-model",
		Events: []domain.AutomationEvent{domain.AutomationEventOpened}, BaseBranch: "main", Enabled: true,
		CreatedBy: &f.user.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.st.CreateAutomation(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	response := do(t, http.MethodPatch, f.ts.URL+"/api/v1/automations/"+a.ID, f.token, map[string]any{
		"enabled": false,
	})
	if response.StatusCode != http.StatusOK {
		var body bytes.Buffer
		_, _ = body.ReadFrom(response.Body)
		t.Fatalf("status=%d body=%s", response.StatusCode, body.String())
	}
	var got domain.Automation
	decode(t, response, &got)
	if got.Enabled {
		t.Fatalf("automation remained enabled: %+v", got)
	}
}

func signGiteaBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func deliverGiteaEvent(t *testing.T, f *webhookSetupFixture, event string, payload any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+"/webhooks/gitea", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", event)
	req.Header.Set("X-Gitea-Signature", signGiteaBody("webhook-secret", body))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestGiteaPREventDispatchesOneReviewRunPerHeadSHA(t *testing.T) {
	f := newWebhookSetupFixture(t, webhookSetupFixtureOptions{
		matchingIdentity: true, webhookConfigured: true, role: domain.RoleOwner,
	})
	modelID := seedAutomationModel(t, &f)
	now := time.Now().UTC()
	a := &domain.Automation{
		ID: domain.NewID(), ServiceID: f.service.ID, Name: "PR guard", Instructions: "Review this PR.",
		TriggerType: domain.AutomationTriggerPRReview, ModelID: modelID,
		Events: []domain.AutomationEvent{domain.AutomationEventOpened}, BaseBranch: "main", Enabled: true,
		CreatedBy: &f.user.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.st.CreateAutomation(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertWebhookBinding(context.Background(), &domain.WebhookBinding{
		ServiceID: f.service.ID, Provider: domain.ProviderGitea,
		Endpoint: "http://orchestrator.test/webhooks/gitea", Status: domain.WebhookBindingActive, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"action": "opened", "number": 17,
		"repository": map[string]any{"full_name": "acme/repo"},
		"pull_request": map[string]any{
			"html_url": "http://gitea.test/acme/repo/pulls/17", "draft": false,
			"head": map[string]any{"ref": "feature", "sha": "abc123"},
			"base": map[string]any{"ref": "main"},
		},
	}
	for i := 0; i < 2; i++ {
		response := deliverGiteaEvent(t, &f, "pull_request", payload)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("delivery %d status=%d", i, response.StatusCode)
		}
		response.Body.Close()
	}
	runs, err := f.st.ListRunsByService(context.Background(), f.service.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	run := runs[0]
	if run.Kind != domain.RunKindReview || run.Origin != domain.RunOriginAutomation || run.OriginAutomationID != a.ID || run.OriginEventKey == "" {
		t.Fatalf("run provenance=%+v", run)
	}
	if run.Prompt != a.Instructions || run.PRHeadBranch != "feature" || run.PRBaseBranch != "main" || run.ModelID == nil || *run.ModelID != modelID {
		t.Fatalf("review run=%+v", run)
	}
	binding, _ := f.st.GetWebhookBinding(context.Background(), f.service.ID)
	if binding.LastDeliveryStatus != "duplicate" {
		t.Fatalf("binding status=%q want duplicate", binding.LastDeliveryStatus)
	}
}

func TestParseGiteaReviewEvent(t *testing.T) {
	tests := []struct {
		name, header, action string
		changes              any
		want                 domain.AutomationEvent
	}{
		{name: "opened", header: "pull_request", action: "opened", want: domain.AutomationEventOpened},
		{name: "reopened", header: "pull_request", action: "reopened", want: domain.AutomationEventReopened},
		{name: "synchronize", header: "pull_request_sync", action: "synchronized", want: domain.AutomationEventSynchronize},
		{name: "ready", header: "pull_request", action: "edited", changes: map[string]any{"draft": map[string]any{"from": true}}, want: domain.AutomationEventReady},
		{name: "unrelated edit", header: "pull_request", action: "edited", changes: map[string]any{"title": map[string]any{"from": "old"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{
				"action": tt.action, "number": 1, "changes": tt.changes,
				"repository":   map[string]any{"full_name": "acme/repo"},
				"pull_request": map[string]any{"html_url": "http://gitea/pr/1", "draft": false, "head": map[string]any{"ref": "x", "sha": "sha"}, "base": map[string]any{"ref": "main"}},
			}
			body, _ := json.Marshal(payload)
			got, _ := parseGiteaReviewEvent(tt.header, body)
			if tt.want == "" && got != nil {
				t.Fatalf("got=%+v want ignored", got)
			}
			if tt.want != "" && (got == nil || got.event != tt.want) {
				t.Fatalf("got=%+v want %q", got, tt.want)
			}
		})
	}
}
