package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func TestUpdateServiceRunnerProfileUsesClusterAllowlist(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{
		ConsoleToken: consoleToken,
		RunnerImage:  "runner:default",
		RunnerProfiles: map[string]string{
			"default": "runner:default",
			"go-node": "runner:go-node",
		},
	})
	server := httptest.NewServer(New(
		st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil,
	).Handler())
	t.Cleanup(server.Close)
	project := &domain.Project{ID: domain.NewID(), Name: "runtime", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "app",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "https://example.test/app.git",
		DefaultBranch: "main", GitMode: domain.GitModeReadonly, CreatedAt: time.Now(),
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}

	resp := do(t, "PATCH", server.URL+"/api/v1/services/"+service.ID, consoleToken, map[string]any{
		"runner_profile": "go-node",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed profile status=%d, want 200", resp.StatusCode)
	}
	var updated domain.Service
	decode(t, resp, &updated)
	if updated.RunnerProfile != "go-node" {
		t.Fatalf("runner_profile=%q, want go-node", updated.RunnerProfile)
	}

	resp = do(t, "PATCH", server.URL+"/api/v1/services/"+service.ID, consoleToken, map[string]any{
		"runner_profile": "user-controlled-image",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown profile status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	stored, _ := st.GetService(ctx, service.ID)
	if stored.RunnerProfile != "go-node" {
		t.Fatalf("rejected profile changed stored service to %q", stored.RunnerProfile)
	}
}
