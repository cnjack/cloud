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

func TestRetiredCloudSCMSourceAndBundleEndpointsAreAbsent(t *testing.T) {
	ts, st, _ := newTestServer(t)
	runID, token := createActiveInternalTestRun(t, st)
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/internal/v1/runs/" + runID + "/source", nil},
		{http.MethodPost, "/internal/v1/runs/" + runID + "/bundle", map[string]any{"data": "retired"}},
	} {
		resp := do(t, tc.method, ts.URL+tc.path, token, tc.body)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status=%d want 404", tc.method, tc.path, resp.StatusCode)
		}
		resp.Body.Close()
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
		GitMode: domain.GitModeReadonly, CreatedAt: time.Now().UTC(),
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
