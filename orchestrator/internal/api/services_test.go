package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnjack/jcloud/internal/domain"
)

// newProject creates a pure (service-less) project and returns its id.
func newProject(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	resp := do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]any{"name": name})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status=%d want 201", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	if pv.ID == "" {
		t.Fatal("project has no id")
	}
	if len(pv.Services) != 0 {
		t.Fatalf("pure project should have no services, got %d", len(pv.Services))
	}
	return pv.ID
}

// TestServiceCRUDAPI exercises the primary service endpoints end to end.
func TestServiceCRUDAPI(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "svc-crud")

	// Create a provider service via smart-parsed repo_url.
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "web", "repo_url": "https://github.com/acme/web.git",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create service: status=%d want 201", resp.StatusCode)
	}
	var svc domain.Service
	decode(t, resp, &svc)
	if svc.RepoKind != domain.RepoKindProvider || svc.Provider != domain.ProviderGitHub || svc.RepoOwnerName != "acme/web" {
		t.Fatalf("service repo not classified: %+v", svc)
	}

	// Duplicate name -> 409.
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "web", "repo_url": "https://github.com/acme/web.git",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("dup service: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// List.
	resp = do(t, "GET", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, nil)
	var list struct {
		Services []domain.Service `json:"services"`
	}
	decode(t, resp, &list)
	if len(list.Services) != 1 {
		t.Fatalf("list services len=%d want 1", len(list.Services))
	}

	// PATCH: switch to explicit provider owner_name + draft_pr.
	resp = do(t, "PATCH", ts.URL+"/api/v1/services/"+svc.ID, consoleToken, map[string]any{
		"provider": "gitea", "owner_name": "acme/web2", "git_mode": "draft_pr", "default_branch": "trunk",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch service: status=%d want 200", resp.StatusCode)
	}
	var patched domain.Service
	decode(t, resp, &patched)
	if patched.Provider != domain.ProviderGitea || patched.RepoOwnerName != "acme/web2" ||
		patched.GitMode != domain.GitModeDraftPR || patched.DefaultBranch != "trunk" {
		t.Fatalf("patch not applied: %+v", patched)
	}

	// Create a run against the service, then delete must 409 (service has runs).
	resp = do(t, "POST", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken, map[string]any{"prompt": "do it"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create service run: status=%d want 201", resp.StatusCode)
	}
	var run domain.Run
	decode(t, resp, &run)
	if run.ServiceID != svc.ID || run.ProjectID != pid || run.Kind != domain.RunKindAgent {
		t.Fatalf("run not linked to service: %+v", run)
	}

	// GET /services/{id}/runs lists it.
	resp = do(t, "GET", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken, nil)
	var rl struct {
		Runs []domain.Run `json:"runs"`
	}
	decode(t, resp, &rl)
	if len(rl.Runs) != 1 || rl.Runs[0].ID != run.ID {
		t.Fatalf("service runs = %+v want [%s]", rl.Runs, run.ID)
	}

	// Delete with a run present -> 409.
	resp = do(t, "DELETE", ts.URL+"/api/v1/services/"+svc.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete service with runs: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestServiceDraftPRRequiresProvider proves the API enforces the blueprint §1
// constraint: draft_pr requires a provider repo (raw repos are read-only).
func TestServiceDraftPRRequiresProvider(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "svc-draft")

	// Raw repo (git://) + draft_pr -> 400.
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "raw", "repo_url": "git://git/seed.git", "git_mode": "draft_pr",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("draft_pr on raw repo: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Provider repo + draft_pr -> 201.
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "prov", "owner_name": "o/n", "provider": "gitea", "git_mode": "draft_pr",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("draft_pr on provider repo: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestProjectShimFlattensDefaultService covers the compat shim: POST /projects
// with repo_url auto-creates a default service, GET flattens it, and POST
// /projects/{id}/runs routes to that default service.
func TestProjectShimFlattensDefaultService(t *testing.T) {
	ts, st, _ := newTestServer(t)

	resp := do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]any{
		"name": "shim", "repo_url": "git://git.jcloud.svc/seed.git", "default_branch": "main",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project via shim: status=%d want 201", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	if pv.RepoURL != "git://git.jcloud.svc/seed.git" || pv.GitMode != domain.GitModeReadonly {
		t.Fatalf("flatten missing repo_url/git_mode: %+v", pv)
	}
	if len(pv.Services) != 1 || pv.Services[0].Name != "default" {
		t.Fatalf("expected a default service: %+v", pv.Services)
	}
	defaultServiceID := pv.Services[0].ID

	// POST /projects/{id}/runs routes to the default service.
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pv.ID+"/runs", consoleToken, map[string]any{"prompt": "go"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("shim create run: status=%d want 201", resp.StatusCode)
	}
	var run domain.Run
	decode(t, resp, &run)
	if run.ServiceID != defaultServiceID {
		t.Fatalf("shim run.service_id=%q want default service %q", run.ServiceID, defaultServiceID)
	}

	// GET /projects/{id} flattens too.
	resp = do(t, "GET", ts.URL+"/api/v1/projects/"+pv.ID, consoleToken, nil)
	var got projectView
	decode(t, resp, &got)
	if got.RepoURL != "git://git.jcloud.svc/seed.git" || len(got.Services) != 1 {
		t.Fatalf("GET flatten wrong: %+v", got)
	}
	_ = st

	// A project with no default service rejects the shim run route with 400.
	pid := newProject(t, ts, "no-svc")
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/runs", consoleToken, map[string]any{"prompt": "go"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("shim run on service-less project: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestProjectPatchRetargetsDefaultService is the e2e J2-S5 shim: PATCH the
// project's repo_url updates the default service in place, so a retry (same
// service_id) picks up the fix.
func TestProjectPatchRetargetsDefaultService(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]any{
		"name": "patch", "repo_url": "git://bad/repo.git",
	})
	var pv projectView
	decode(t, resp, &pv)
	svcID := pv.Services[0].ID

	resp = do(t, "PATCH", ts.URL+"/api/v1/projects/"+pv.ID, consoleToken, map[string]any{
		"repo_url": "git://good/seed.git",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch project repo_url: status=%d want 200", resp.StatusCode)
	}
	var got projectView
	decode(t, resp, &got)
	if got.RepoURL != "git://good/seed.git" {
		t.Fatalf("flatten repo_url=%q want the fixed url", got.RepoURL)
	}
	// The SAME default service was retargeted (id unchanged), so a retry keeps
	// working against the same service_id.
	if len(got.Services) != 1 || got.Services[0].ID != svcID {
		t.Fatalf("default service id changed on patch: %+v", got.Services)
	}
	if got.Services[0].RawRepoURL != "git://good/seed.git" {
		t.Fatalf("service raw url=%q want fixed", got.Services[0].RawRepoURL)
	}
}
