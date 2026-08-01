package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestMemStoreCreateRunResolvesContractWithEffectiveProjectTimeout(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	ConfigureWorkflowTimeoutDefaults(st, 5400, 7200)
	projectTimeout := int64(3600)
	if err := st.CreateProject(ctx, &domain.Project{ID: "p", Name: "P", RunTimeoutSecs: &projectTimeout, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, &domain.Service{ID: "s", ProjectID: "p", Name: "default", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitHub, RepoOwnerName: "cnjack/cloud", DefaultBranch: "main", GitMode: domain.GitModeDraftPR, PRReadyPolicy: domain.PRReadyPolicyLifecycleAware}); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: "r", ProjectID: "p", ServiceID: "s", Prompt: "implement", Status: domain.StatusQueued, Kind: domain.RunKindAgent, ModelName: "zhipu/glm-5.2", CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if run.ExecutionContract == nil {
		t.Fatal("execution contract was not resolved")
	}
	if run.ExecutionContract.Execution.TimeoutSeconds != projectTimeout || run.ExecutionContract.Execution.TimeoutSource != domain.TimeoutSourceProject {
		t.Fatalf("timeout = %#v", run.ExecutionContract.Execution)
	}
	stored, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExecutionContract == nil || stored.ExecutionContract.Hash != run.ExecutionContract.Hash {
		t.Fatal("contract did not round-trip")
	}
}

func TestMemStoreRetryPreservesProvidedContract(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	ConfigureWorkflowTimeoutDefaults(st, 5400, 7200)
	if err := st.CreateProject(ctx, &domain.Project{ID: "p", Name: "P", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: "s", ProjectID: "p", Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "https://example.invalid/repo.git", DefaultBranch: "main", GitMode: domain.GitModeReadonly}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	original := &domain.Run{ID: "r1", ProjectID: "p", ServiceID: "s", Prompt: "one", Status: domain.StatusQueued, Kind: domain.RunKindAgent, ModelName: "zhipu/glm-5.2", CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, original); err != nil {
		t.Fatal(err)
	}
	retry := &domain.Run{ID: "r2", ProjectID: "p", ServiceID: "s", Prompt: "one", Status: domain.StatusQueued, Kind: domain.RunKindAgent, ModelName: "changed/model", RetriedFrom: &original.ID, ExecutionContract: original.ExecutionContract, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if retry.ExecutionContract.Hash != original.ExecutionContract.Hash || retry.ExecutionContract.Execution.LLMSelection.ModelName != "zhipu/glm-5.2" {
		t.Fatal("retry contract was re-resolved")
	}
}

func TestMemStoreSetReviewPlanFirstWriteIdempotency(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	run := &domain.Run{ID: "r", ProjectID: "p", ServiceID: "s", Prompt: "review", Status: domain.StatusRunning, Kind: domain.RunKindReview, PRBaseSHA: strings.Repeat("a", 40), PRHeadSHA: strings.Repeat("b", 40)}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	plan, err := domain.BuildReviewPlan(domain.ReviewPlanInput{BaseSHA: run.PRBaseSHA, HeadSHA: run.PRHeadSHA, MergeBaseSHA: strings.Repeat("c", 40), Diff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"})
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := st.SetReviewPlan(ctx, run.ID, *plan)
	if err != nil || !created {
		t.Fatalf("first write created=%v err=%v", created, err)
	}
	_, created, err = st.SetReviewPlan(ctx, run.ID, *plan)
	if err != nil || created {
		t.Fatalf("identical retry created=%v err=%v", created, err)
	}
	other := *plan
	other.MergeBaseSHA = strings.Repeat("d", 40)
	other.PlanHash, _ = other.CanonicalHash()
	if _, _, err = st.SetReviewPlan(ctx, run.ID, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict err = %v", err)
	}
}
