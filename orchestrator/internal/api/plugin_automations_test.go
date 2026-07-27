package api

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func TestPluginAutomationPathFilterIsLimitedToPush(t *testing.T) {
	base := pluginAutomationReq{
		ServiceID:      "service",
		Name:           "filtered",
		PromptTemplate: "handle event",
		SCM: &pluginSCMReq{
			PathPattern: "src/**",
			Actions:     []pluginSCMActionReq{{EventFamily: "pull_request", Action: "synchronized"}},
		},
	}
	_, _, _, _, _, message := pluginAutomationFromReq(base, "automation")
	if !strings.Contains(message, "only for push.updated") {
		t.Fatalf("validation message = %q", message)
	}
	base.SCM.Actions = []pluginSCMActionReq{{EventFamily: "push", Action: "updated"}}
	_, scm, actions, _, _, message := pluginAutomationFromReq(base, "automation")
	if message != "" || scm == nil || len(actions) != 1 {
		t.Fatalf("valid push path filter rejected: message=%q scm=%+v actions=%+v", message, scm, actions)
	}
}

func TestPluginAutomationTypedFiltersRejectIncompatibleActions(t *testing.T) {
	tests := []struct {
		name    string
		scm     pluginSCMReq
		message string
	}{
		{
			name: "conclusion on push",
			scm: pluginSCMReq{Conclusion: "failure",
				Actions: []pluginSCMActionReq{{EventFamily: "push", Action: "updated"}}},
			message: "only for check.completed",
		},
		{
			name: "branch on comment",
			scm: pluginSCMReq{Branch: "main",
				Actions: []pluginSCMActionReq{{EventFamily: "comment", Action: "created"}}},
			message: "only for push, pull_request, and check",
		},
		{
			name: "invalid branch glob",
			scm: pluginSCMReq{Branch: "release/[",
				Actions: []pluginSCMActionReq{{EventFamily: "push", Action: "updated"}}},
			message: "invalid glob pattern",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := pluginAutomationReq{ServiceID: "service", Name: "filtered", PromptTemplate: "handle", SCM: &tt.scm}
			_, _, _, _, _, message := pluginAutomationFromReq(req, "automation")
			if !strings.Contains(message, tt.message) {
				t.Fatalf("validation message = %q, want %q", message, tt.message)
			}
		})
	}
}

func TestPluginAutomationCarriesModelAndReasoningEffort(t *testing.T) {
	modelID, effort := "glm-52", "high"
	req := pluginAutomationReq{
		ServiceID: "service", Name: "reasoning", PromptTemplate: "handle",
		ModelID: &modelID, ModelEffort: &effort,
		Cron: &pluginCronReq{CronExpr: "0 9 * * *"},
	}
	automation, _, _, _, _, message := pluginAutomationFromReq(req, "automation")
	if message != "" {
		t.Fatalf("valid model settings rejected: %q", message)
	}
	if automation.ModelID != modelID || automation.ModelEffort != effort {
		t.Fatalf("automation model=%q effort=%q", automation.ModelID, automation.ModelEffort)
	}

	auto := "auto"
	req.ModelEffort = &auto
	automation, _, _, _, _, message = pluginAutomationFromReq(req, "automation")
	if message != "" || automation.ModelEffort != "" {
		t.Fatalf("auto effort must clear persisted effort: automation=%+v message=%q", automation, message)
	}

	invalid := "extreme"
	req.ModelEffort = &invalid
	if _, _, _, _, _, message = pluginAutomationFromReq(req, "automation"); !strings.Contains(message, "model_effort") {
		t.Fatalf("invalid effort message=%q", message)
	}
}

func TestPluginAutomationEffortRequiresReasoningCapability(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cfg := &config.Config{ConsoleToken: consoleToken}
	server := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	now := time.Now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "automation-model", CreatedAt: now}
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "svc",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "https://example.invalid/repo.git",
		DefaultBranch: "main", CreatedAt: now,
	}
	provider := &domain.ModelProvider{
		ID: domain.NewID(), Name: "provider", Kind: "openai", BaseURL: "https://models.invalid/v1",
		AuthType: domain.ModelProviderAuthNone, CatalogMode: domain.ModelProviderCatalogDisabled, CreatedAt: now,
	}
	for _, create := range []func() error{
		func() error { return st.CreateProject(ctx, project) },
		func() error { return st.CreateService(ctx, service) },
		func() error { return st.CreateModelProvider(ctx, provider) },
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	plain := &domain.Model{
		ID: domain.NewID(), ProviderID: provider.ID, Name: "plain", ModelName: "provider/plain",
		ModelID: "plain", BaseURL: provider.BaseURL, Source: "custom", CreatedAt: now,
	}
	reasoner := &domain.Model{
		ID: domain.NewID(), ProviderID: provider.ID, Name: "reasoner", ModelName: "provider/reasoner",
		ModelID: "reasoner", BaseURL: provider.BaseURL, Source: "custom",
		Capabilities: domain.ModelCapabilities{Reasoning: true}, CreatedAt: now,
	}
	for _, model := range []*domain.Model{plain, reasoner} {
		if err := st.CreateModel(ctx, model); err != nil {
			t.Fatal(err)
		}
		if err := st.GrantModel(ctx, model.ID, project.ID); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest("POST", "/api/v1/projects/"+project.ID+"/automations", nil)
	automation := &domain.PluginAutomation{ModelID: plain.ID, ModelEffort: "high"}
	if got := server.validatePluginAutomationModel(request, service, automation, true); got == nil || got.code != "model_effort_unsupported" {
		t.Fatalf("plain model validation=%+v want model_effort_unsupported", got)
	}
	automation.ModelID = reasoner.ID
	if got := server.validatePluginAutomationModel(request, service, automation, true); got != nil {
		t.Fatalf("reasoning model rejected: %+v", got)
	}
}
