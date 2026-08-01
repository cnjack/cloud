package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// Run with JCLOUD_TEST_DATABASE_URL to verify the JSONB contract/plan path
// against a real PostgreSQL instance. Ordinary unit suites skip it.
func TestPGWorkflowContractAndReviewPlanRoundTrip(t *testing.T) {
	dsn := os.Getenv("JCLOUD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("JCLOUD_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	ConfigureWorkflowTimeoutDefaults(st, 1800, 7200)

	project := &domain.Project{ID: domain.NewID(), Name: "workflow-contract-pg", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, project.ID) })
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "default", RepoKind: domain.RepoKindProvider,
		Provider: domain.ProviderGitHub, RepoOwnerName: "acme/repo", DefaultBranch: "main", GitMode: domain.GitModeDraftPR,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID, Prompt: "review exact pair",
		Status: domain.StatusQueued, Kind: domain.RunKindReview, PRNumber: 42, PRTitle: "Fix pagination contract",
		PRHeadBranch: "feature", PRBaseBranch: "main",
		PRHeadSHA: "2222222222222222222222222222222222222222", PRBaseSHA: "1111111111111111111111111111111111111111",
		ModelName: "test/glm-5.2", Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ScheduleRun(ctx, run.ID, "job-roundtrip", "token-hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	plan, err := domain.BuildReviewPlan(domain.ReviewPlanInput{
		BaseSHA: run.PRBaseSHA, HeadSHA: run.PRHeadSHA, MergeBaseSHA: run.PRBaseSHA,
		Diff:      "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1,2 @@\n package main\n+var enabled = true\n",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := st.SetReviewPlan(ctx, run.ID, *plan); err != nil || !created {
		t.Fatalf("set review plan: created=%v err=%v", created, err)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionContract == nil || got.ExecutionContract.Workflow.ID != domain.BuiltinPullRequestReviewID {
		t.Fatalf("execution contract did not round-trip: %+v", got.ExecutionContract)
	}
	if got.ReviewPlan == nil || got.ReviewPlan.PlanHash != plan.PlanHash || !got.ReviewPlan.AllowsAnchor("main.go", 2, 2) {
		t.Fatalf("review plan did not round-trip with private anchors: %+v", got.ReviewPlan)
	}
	if got.PRTitle != run.PRTitle {
		t.Fatalf("pr title did not round-trip: got %q want %q", got.PRTitle, run.PRTitle)
	}
}
