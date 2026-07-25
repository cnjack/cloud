package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

func newProject(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	resp := do(t, http.MethodPost, ts.URL+"/api/v1/projects", consoleToken, map[string]any{"name": name})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status=%d want 201", resp.StatusCode)
	}
	var project projectView
	decode(t, resp, &project)
	if project.ID == "" || len(project.Services) != 0 {
		t.Fatalf("fresh Project is invalid: %+v", project)
	}
	return project.ID
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newCipherServer is the common test control plane for the clean-cut Plugin
// API. It deliberately does not enable a legacy Integration or raw Git route.
func newCipherServer(t *testing.T, allowed []string, giteaURL string) (*httptest.Server, *store.MemStore, *config.Config) {
	t.Helper()
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{
		ConsoleToken: consoleToken, MasterKey: validTokenKey(t),
		AllowedGitHosts: allowed, GiteaURL: giteaURL,
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	return ts, st, cfg
}
