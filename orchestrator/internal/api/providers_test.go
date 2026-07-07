package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// TestListProviderRepos covers the Drone-style repo picker endpoint:
// bad provider → 400; a provider with no credential → 403 with a link hint;
// gitea via the PAT fallback → 200 with the mapped repo list.
func TestListProviderRepos(t *testing.T) {
	// A fake gitea /repos/search the PAT-fallback path will hit.
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": []map[string]any{
				{"id": 11, "full_name": "ai/app", "default_branch": "main", "private": true},
			},
		})
	}))
	defer gitea.Close()

	st := store.NewMemStore()
	hub := sse.NewHub()
	cfg := &config.Config{ConsoleToken: consoleToken, GiteaURL: gitea.URL, GiteaToken: "pat"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(st, cfg, log, hub, nil).Handler())
	t.Cleanup(ts.Close)

	// Unknown provider → 400.
	resp := do(t, "GET", ts.URL+"/api/v1/providers/bitbucket/repos", consoleToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad provider: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// github: service principal has no github credential (PAT is gitea-only) → 403.
	resp = do(t, "GET", ts.URL+"/api/v1/providers/github/repos", consoleToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("github without credential: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// gitea via the PAT fallback → 200 + mapped repos.
	resp = do(t, "GET", ts.URL+"/api/v1/providers/gitea/repos?q=app", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gitea list: status=%d want 200", resp.StatusCode)
	}
	var out struct {
		Repos []provider.Repo `json:"repos"`
	}
	decode(t, resp, &out)
	if len(out.Repos) != 1 || out.Repos[0].ID != 11 || out.Repos[0].FullName != "ai/app" || !out.Repos[0].Private {
		t.Fatalf("bad repos payload: %+v", out.Repos)
	}
}

// TestCreateServicePersistsProviderRepoID pins that the picker's numeric repo id
// lands on the created service (rename-proof identity, migration 0009).
func TestCreateServicePersistsProviderRepoID(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "picker")

	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "app", "provider": "gitea", "owner_name": "ai/app",
		"git_mode": "draft_pr", "default_branch": "main", "provider_repo_id": 12345,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d want 201", resp.StatusCode)
	}
	var svc struct {
		ProviderRepoID *int64 `json:"provider_repo_id"`
	}
	decode(t, resp, &svc)
	if svc.ProviderRepoID == nil || *svc.ProviderRepoID != 12345 {
		t.Fatalf("provider_repo_id not persisted: %+v", svc.ProviderRepoID)
	}
}
