package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestAutomationAndWebhookBindingRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Now().UTC().Truncate(time.Second)
	a := &domain.Automation{
		ID: "auto-1", ServiceID: "svc-1", Name: "Gitea PR review",
		Instructions: "Review security and regressions.", TriggerType: domain.AutomationTriggerPRReview,
		ModelID: "model-1", Events: []domain.AutomationEvent{domain.AutomationEventOpened, domain.AutomationEventSynchronize},
		BaseBranch: "main", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAutomation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != a.Name || got.ModelID != a.ModelID || len(got.Events) != 2 || !got.Enabled {
		t.Fatalf("automation round trip=%+v", got)
	}
	listed, err := st.ListAutomationsByService(ctx, a.ServiceID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list=%+v err=%v", listed, err)
	}

	triggered := now.Add(time.Minute)
	if err := st.RecordAutomationDispatch(ctx, a.ID, triggered, "run-1", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetAutomation(ctx, a.ID)
	if got.LastTriggeredAt == nil || !got.LastTriggeredAt.Equal(triggered) || got.LastRunID != "run-1" || got.LastError != "" {
		t.Fatalf("dispatch state=%+v", got)
	}

	binding := &domain.WebhookBinding{
		ServiceID: a.ServiceID, Provider: domain.ProviderGitea,
		Endpoint: "https://cloud.example/webhooks/gitea", Status: domain.WebhookBindingActive,
		LastSyncedAt: &now, UpdatedAt: now,
	}
	if err := st.UpsertWebhookBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordWebhookDelivery(ctx, a.ServiceID, triggered, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	bound, err := st.GetWebhookBinding(ctx, a.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Status != domain.WebhookBindingActive || bound.LastDeliveryAt == nil || bound.LastDeliveryStatus != "accepted" {
		t.Fatalf("binding=%+v", bound)
	}
	resynced := &domain.WebhookBinding{
		ServiceID: a.ServiceID, Provider: domain.ProviderGitea,
		Endpoint: binding.Endpoint, Status: domain.WebhookBindingActive,
		LastSyncedAt: &triggered, UpdatedAt: triggered,
	}
	if err := st.UpsertWebhookBinding(ctx, resynced); err != nil {
		t.Fatal(err)
	}
	bound, _ = st.GetWebhookBinding(ctx, a.ServiceID)
	if bound.LastDeliveryAt == nil || !bound.LastDeliveryAt.Equal(triggered) || bound.LastDeliveryStatus != "accepted" {
		t.Fatalf("webhook resync cleared delivery observation: %+v", bound)
	}

	got.Enabled = false
	got.UpdatedAt = now.Add(2 * time.Minute)
	if err := st.UpdateAutomation(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAutomation(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAutomation(ctx, got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted err=%v", err)
	}
}
