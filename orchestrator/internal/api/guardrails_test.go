package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// createGiteaService creates a gitea provider service in the project and returns
// it. gitMode is "readonly" unless overridden.
func createGiteaService(t *testing.T, ts *httptest.Server, pid, name, ownerName string) domain.Service {
	t.Helper()
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": name, "owner_name": ownerName, "provider": "gitea",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create service %q: status=%d want 201", name, resp.StatusCode)
	}
	var svc domain.Service
	decode(t, resp, &svc)
	return svc
}

// setAllowlist PATCHes a project's provider_allowlist (owner via console token).
func setAllowlist(t *testing.T, ts *httptest.Server, pid string, list []string) {
	t.Helper()
	resp := do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{
		"provider_allowlist": list,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set allowlist: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestProjectPatchGuardrails round-trips all four guardrail fields through PATCH
// and GET.
func TestProjectPatchGuardrails(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "guard")

	resp := do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{
		"max_concurrent_runs": 3,
		"run_timeout_secs":    600,
		"provider_allowlist":  []string{"gitea", "raw"},
		"injected_env":        map[string]string{"COMPANY_TOKEN": "abc", "HTTP_PROXY": "http://p:3128"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch guardrails: status=%d want 200", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	if pv.MaxConcurrentRuns == nil || *pv.MaxConcurrentRuns != 3 {
		t.Fatalf("max_concurrent_runs=%v want 3", pv.MaxConcurrentRuns)
	}
	if pv.RunTimeoutSecs == nil || *pv.RunTimeoutSecs != 600 {
		t.Fatalf("run_timeout_secs=%v want 600", pv.RunTimeoutSecs)
	}
	if len(pv.ProviderAllowlist) != 2 {
		t.Fatalf("provider_allowlist=%v want [gitea raw]", pv.ProviderAllowlist)
	}
	if pv.InjectedEnv["COMPANY_TOKEN"] != "abc" || pv.InjectedEnv["HTTP_PROXY"] != "http://p:3128" {
		t.Fatalf("injected_env=%v not persisted", pv.InjectedEnv)
	}

	// GET returns the same.
	resp = do(t, "GET", ts.URL+"/api/v1/projects/"+pid, consoleToken, nil)
	var got projectView
	decode(t, resp, &got)
	if got.MaxConcurrentRuns == nil || *got.MaxConcurrentRuns != 3 || got.InjectedEnv["COMPANY_TOKEN"] != "abc" {
		t.Fatalf("GET did not reflect guardrails: %+v", got)
	}
}

// TestProjectPatchClearsGuardrailsWithNull: sending null (or ≤0) clears a numeric
// guardrail back to "inherit" (omitted from the view).
func TestProjectPatchClearsGuardrailsWithNull(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "clear")

	do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{"max_concurrent_runs": 5}).Body.Close()
	resp := do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{"max_concurrent_runs": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: status=%d want 200", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	if pv.MaxConcurrentRuns != nil {
		t.Fatalf("max_concurrent_runs=%v want nil (cleared to inherit)", *pv.MaxConcurrentRuns)
	}
}

// TestProjectPatchInjectedEnvRejectsReservedKey: a reserved system key in
// injected_env is a typed 400 naming the key; the project is left unchanged.
func TestProjectPatchInjectedEnvRejectsReservedKey(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "reserved")

	// First set a good injected_env so we can prove the rejected PATCH doesn't
	// mutate anything.
	do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{
		"injected_env": map[string]string{"OK_FLAG": "1"},
	}).Body.Close()

	for _, key := range []string{
		"RUN_TOKEN", "MODEL_NAME", "GIT_MODE", "PR_HEAD", "run_token",
		// execution-hijack vectors must also be refused.
		"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES",
		"NODE_OPTIONS", "PYTHONPATH", "BASH_ENV", "IFS", "GIT_SSH_COMMAND",
	} {
		resp := do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{
			"injected_env": map[string]string{key: "x"},
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("reserved key %q: status=%d want 400", key, resp.StatusCode)
		}
		var e errorBody
		decode(t, resp, &e)
		if e.Error.Code != "reserved_env_key" {
			t.Fatalf("reserved key %q: code=%q want reserved_env_key", key, e.Error.Code)
		}
	}

	// The good injected_env survived the rejected PATCHes.
	resp := do(t, "GET", ts.URL+"/api/v1/projects/"+pid, consoleToken, nil)
	var pv projectView
	decode(t, resp, &pv)
	if pv.InjectedEnv["OK_FLAG"] != "1" || len(pv.InjectedEnv) != 1 {
		t.Fatalf("injected_env mutated by a rejected PATCH: %v", pv.InjectedEnv)
	}
}

// TestProjectPatchRejectsBadAllowlist: an unknown provider name in the allowlist
// is a 400.
func TestProjectPatchRejectsBadAllowlist(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "badlist")
	resp := do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{
		"provider_allowlist": []string{"gitea", "bitbucket"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad allowlist entry: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestProjectPatchNameCaseInsensitive: a legacy {"Name":...} still renames (the
// old stdlib struct decoder matched field names case-insensitively — don't
// regress that when switching to explicit field routing).
func TestProjectPatchNameCaseInsensitive(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "case")
	resp := do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{"Name": "renamed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("case-variant name PATCH: status=%d want 200", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	if pv.Name != "renamed" {
		t.Fatalf("name=%q want renamed", pv.Name)
	}

	// A genuinely unknown field is still a loud 400 (repo config lives on services).
	resp = do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{"repo_url": "git://x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestInjectedEnvVisibleToOwnerOnly: injected_env values can hold secrets, so the
// project view returns them ONLY to an owner (cluster-admin/service report owner);
// a member/viewer gets the non-secret guardrails but never the injected_env values.
func TestInjectedEnvVisibleToOwnerOnly(t *testing.T) {
	f := setupRBAC(t)
	pid := f.projectID

	r := do(t, "PATCH", f.ts.URL+"/api/v1/projects/"+pid, f.tokens["owner"], map[string]any{
		"injected_env":        map[string]string{"COMPANY_TOKEN": "s3cr3t"},
		"max_concurrent_runs": 2,
	})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("owner set guardrails: status=%d want 200", r.StatusCode)
	}
	r.Body.Close()

	getView := func(tok string) projectView {
		resp := do(t, "GET", f.ts.URL+"/api/v1/projects/"+pid, tok, nil)
		var pv projectView
		decode(t, resp, &pv)
		return pv
	}

	// Owner + cluster-admin see the value.
	for _, role := range []string{"owner", "admin", "service"} {
		v := getView(f.tokens[role])
		if v.InjectedEnv["COMPANY_TOKEN"] != "s3cr3t" {
			t.Errorf("role=%s should see injected_env value, got %v", role, v.InjectedEnv)
		}
	}

	// Member + viewer get the non-secret guardrail but NOT the injected_env value.
	for _, role := range []string{"member", "viewer"} {
		v := getView(f.tokens[role])
		if len(v.InjectedEnv) != 0 {
			t.Errorf("role=%s leaked injected_env values: %v", role, v.InjectedEnv)
		}
		if v.MaxConcurrentRuns == nil || *v.MaxConcurrentRuns != 2 {
			t.Errorf("role=%s should still see max_concurrent_runs, got %v", role, v.MaxConcurrentRuns)
		}
	}
}

// TestProjectPatchPreservesGuardrailsOnRename: a name-only PATCH must NOT wipe the
// guardrails (presence semantics — omitted fields are left unchanged).
func TestProjectPatchPreservesGuardrailsOnRename(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "keep")

	do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{
		"max_concurrent_runs": 2,
		"injected_env":        map[string]string{"OK": "1"},
	}).Body.Close()

	resp := do(t, "PATCH", ts.URL+"/api/v1/projects/"+pid, consoleToken, map[string]any{"name": "renamed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: status=%d want 200", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	if pv.Name != "renamed" {
		t.Fatalf("name=%q want renamed", pv.Name)
	}
	if pv.MaxConcurrentRuns == nil || *pv.MaxConcurrentRuns != 2 || pv.InjectedEnv["OK"] != "1" {
		t.Fatalf("guardrails wiped by a rename PATCH: %+v", pv)
	}
}

// TestServiceCreateProviderNotAllowed: creating a service whose provider is not in
// the project's allowlist is a 400 provider_not_allowed; an allowed provider (and
// the "raw" sentinel) still work.
func TestServiceCreateProviderNotAllowed(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "svc-allow")
	setAllowlist(t, ts, pid, []string{"github", "raw"}) // gitea NOT allowed

	// gitea provider service -> 400.
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "g", "owner_name": "acme/x", "provider": "gitea",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("disallowed gitea service: status=%d want 400", resp.StatusCode)
	}
	var e errorBody
	decode(t, resp, &e)
	if e.Error.Code != "provider_not_allowed" {
		t.Fatalf("code=%q want provider_not_allowed", e.Error.Code)
	}

	// github provider service -> 201 (in the allowlist).
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "gh", "repo_url": "https://github.com/acme/x.git",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("allowed github service: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()

	// raw repo (sentinel "raw") -> 201.
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+pid+"/services", consoleToken, map[string]any{
		"name": "raw", "repo_url": "git://git/seed.git",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("allowed raw service: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestRunDispatchProviderNotAllowed: an allowlist tightened AFTER a service exists
// blocks new run dispatch with a 403.
func TestRunDispatchProviderNotAllowed(t *testing.T) {
	ts, _, _ := newTestServer(t)
	pid := newProject(t, ts, "run-allow")
	svc := createGiteaService(t, ts, pid, "default", "acme/x") // allowed at create time

	// Now forbid gitea.
	setAllowlist(t, ts, pid, []string{"github"})

	resp := do(t, "POST", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken, map[string]any{"prompt": "go"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dispatch on now-disallowed provider: status=%d want 403", resp.StatusCode)
	}
	var e errorBody
	decode(t, resp, &e)
	if e.Error.Code != "provider_not_allowed" {
		t.Fatalf("code=%q want provider_not_allowed", e.Error.Code)
	}
}

// TestRetryProviderNotAllowed: retrying a run whose provider is now disallowed is
// a 403.
func TestRetryProviderNotAllowed(t *testing.T) {
	ts, st, _ := newTestServer(t)
	pid := newProject(t, ts, "retry-allow")
	svc := createGiteaService(t, ts, pid, "default", "acme/x")

	// A terminal (failed) run exists (crafted directly; a queued->failed is a legal
	// transition, but here we just persist a failed run for the retry path).
	ctx := context.Background()
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: pid, ServiceID: svc.ID, Prompt: "boom",
		Status: domain.StatusFailed, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	setAllowlist(t, ts, pid, []string{"github"}) // forbid gitea

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/retry", consoleToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("retry on disallowed provider: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestReviewProviderNotAllowed: requesting a review when the provider is now
// disallowed is a 403.
func TestReviewProviderNotAllowed(t *testing.T) {
	ts, st, _ := newTestServer(t)
	pid := newProject(t, ts, "review-allow")
	svc := createGiteaService(t, ts, pid, "default", "acme/x")

	// A succeeded agent run WITH a PR (all review preconditions satisfied).
	ctx := context.Background()
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: pid, ServiceID: svc.ID, Prompt: "did it",
		Status: domain.StatusSucceeded, Kind: domain.RunKindAgent, Attempt: 1,
		GitBranch: "jcode/run-x", PRURL: "http://gitea/acme/x/pulls/1", PRNumber: 1,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	setAllowlist(t, ts, pid, []string{"github"}) // forbid gitea

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/review", consoleToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("review on disallowed provider: status=%d want 403", resp.StatusCode)
	}
	var e errorBody
	decode(t, resp, &e)
	if e.Error.Code != "provider_not_allowed" {
		t.Fatalf("code=%q want provider_not_allowed", e.Error.Code)
	}
}
