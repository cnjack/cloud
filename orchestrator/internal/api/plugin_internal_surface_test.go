package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

func TestCloudSCMSurfaceKeepsSourceRetiredAndAcceptsDeliveryBundles(t *testing.T) {
	ts, st, _ := newTestServer(t)
	runID, token := createActiveInternalTestRun(t, st)

	resp := do(t, http.MethodGet, ts.URL+"/internal/v1/runs/"+runID+"/source", token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET source status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPost, ts.URL+"/internal/v1/runs/"+runID+"/bundle", token, map[string]any{"data": "delivery"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST bundle status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()

	run, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.GitBranch != domain.RunBranchName(runID) {
		t.Fatalf("git_branch=%q want %q", run.GitBranch, domain.RunBranchName(runID))
	}
}

func createActiveInternalTestRun(t *testing.T, st *store.MemStore) (string, string) {
	t.Helper()
	ctx := context.Background()
	project := &domain.Project{ID: domain.NewID(), Name: "internal-surface", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "service",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitHub,
		RepoOwnerName: "acme/repo", DefaultBranch: "main",
		GitMode: domain.GitModeDraftPR, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID,
		Prompt: "test", Kind: domain.RunKindAgent, Status: domain.StatusQueued,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	token := "active-run-token"
	if _, err := st.ScheduleRun(ctx, run.ID, "job", auth.HashToken(token), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	return run.ID, token
}
