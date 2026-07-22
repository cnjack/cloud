package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

const consoleToken = "test-console-token"

func newTestServer(t *testing.T) (*httptest.Server, *store.MemStore, *sse.Hub) {
	t.Helper()
	st := store.NewMemStore()
	hub := sse.NewHub()
	cfg := &config.Config{ConsoleToken: consoleToken}
	withTestModel(cfg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, hub
}

// withTestModel sets the env-source model config so the fail-visible run gate
// (Feature A) treats the server as configured — every run-creating test would
// otherwise 409 model_not_configured. Tests that exercise the unconfigured path
// build their own config without these fields.
func withTestModel(c *config.Config) *config.Config {
	c.ModelBaseURL = "http://model.test/v1"
	c.ModelName = "mock/mock-model"
	c.ModelAPIKey = "test-key"
	return c
}

// newTestServerWithLauncher builds a server wired to a FakeLauncher so cancel's
// Job-deletion behaviour can be asserted.
func newTestServerWithLauncher(t *testing.T, cleaners ...ArchiveCleaner) (*httptest.Server, *store.MemStore, *k8s.FakeLauncher) {
	t.Helper()
	st := store.NewMemStore()
	hub := sse.NewHub()
	fake := k8s.NewFakeLauncher()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, fake)
	if len(cleaners) > 0 {
		srv.WithArchiveCleaner(cleaners[0])
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, fake
}

func do(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestAuthRequired(t *testing.T) {
	ts, _, _ := newTestServer(t)
	// No token.
	resp := do(t, "GET", ts.URL+"/api/v1/projects", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()
	// Wrong token.
	resp = do(t, "GET", ts.URL+"/api/v1/projects", "wrong", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()
	// Health is open.
	resp = do(t, "GET", ts.URL+"/healthz", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestProjectCRUD(t *testing.T) {
	ts, _, _ := newTestServer(t)

	// Create with missing name -> 400 (name is the only required field now).
	resp := do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]string{"repo_url": "https://git/x.git"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing name: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Legacy repo fields are rejected loudly (DisallowUnknownFields), not
	// silently ignored — old simple-mode clients must move to the two-step flow.
	resp = do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]string{
		"name": "demo", "repo_url": "https://git/x.git",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy repo_url field: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Create OK: a project is a pure container — name only, no services yet.
	resp = do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]string{"name": "demo"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d want 201", resp.StatusCode)
	}
	var proj projectView
	decode(t, resp, &proj)
	if proj.ID == "" {
		t.Fatalf("bad project: %+v", proj)
	}
	if len(proj.Services) != 0 {
		t.Fatalf("expected no services on a fresh project, got %+v", proj.Services)
	}

	// Get.
	resp = do(t, "GET", ts.URL+"/api/v1/projects/"+proj.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// List.
	resp = do(t, "GET", ts.URL+"/api/v1/projects", consoleToken, nil)
	var list struct {
		Projects []domain.Project `json:"projects"`
	}
	decode(t, resp, &list)
	if len(list.Projects) != 1 {
		t.Fatalf("list len=%d want 1", len(list.Projects))
	}

	// Delete + confirm 404.
	resp = do(t, "DELETE", ts.URL+"/api/v1/projects/"+proj.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, "GET", ts.URL+"/api/v1/projects/"+proj.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// projFixture is a project with one attached service — the shape most API tests
// need now that project creation is name-only and runs dispatch per-service.
type projFixture struct {
	domain.Project
	ServiceID string
}

func createProject(t *testing.T, ts *httptest.Server) projFixture {
	t.Helper()
	resp := do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]string{"name": "demo"})
	var p domain.Project
	decode(t, resp, &p)
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/services", consoleToken, map[string]string{
		"name": "default", "repo_url": "https://git/x.git",
	})
	var svc domain.Service
	decode(t, resp, &svc)
	if svc.ID == "" {
		t.Fatalf("fixture service not created: %+v", svc)
	}
	return projFixture{Project: p, ServiceID: svc.ID}
}

func TestRunLifecycleAPI(t *testing.T) {
	ts, _, _ := newTestServer(t)
	p := createProject(t, ts)

	// Create run.
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "add a line"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create run: status=%d want 201", resp.StatusCode)
	}
	var run domain.Run
	decode(t, resp, &run)
	if run.Status != domain.StatusQueued {
		t.Fatalf("run status=%s want queued", run.Status)
	}
	if run.TokenHash != "" {
		t.Fatal("token hash must not be serialised to console clients")
	}

	// Empty prompt -> 400.
	resp = do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty prompt: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Get run.
	resp = do(t, "GET", ts.URL+"/api/v1/runs/"+run.ID, consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get run: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// List runs for project.
	resp = do(t, "GET", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken, nil)
	var rl struct {
		Runs []domain.Run `json:"runs"`
	}
	decode(t, resp, &rl)
	if len(rl.Runs) != 1 {
		t.Fatalf("run list len=%d want 1", len(rl.Runs))
	}

	// Global list.
	resp = do(t, "GET", ts.URL+"/api/v1/runs", consoleToken, nil)
	decode(t, resp, &rl)
	if len(rl.Runs) != 1 {
		t.Fatalf("global run list len=%d want 1", len(rl.Runs))
	}
}

func TestRetryLinksRetriedFrom(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)

	// Cannot retry a non-terminal run.
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/retry", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry queued: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// Force terminal (failed) directly in store.
	got, _ := st.GetRun(context.Background(), run.ID)
	_, _ = st.MarkFailed(context.Background(), got.ID, "Failed", domain.FailureCloneFailed, "boom", time.Now())

	// Retry -> new run, retried_from = original (AC-10).
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/retry", consoleToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry: status=%d want 201", resp.StatusCode)
	}
	var retry domain.Run
	decode(t, resp, &retry)
	if retry.ID == run.ID {
		t.Fatal("retry must be a new run")
	}
	if retry.RetriedFrom == nil || *retry.RetriedFrom != run.ID {
		t.Fatalf("retried_from = %v want %s", retry.RetriedFrom, run.ID)
	}
	if retry.Attempt != got.Attempt+1 {
		t.Fatalf("attempt=%d want %d", retry.Attempt, got.Attempt+1)
	}
}

// Regression (M6 live find): retrying a REVIEW run must stay a review run with
// its PR association intact — without copying Kind/PRHeadBranch/PRBaseBranch
// the retry degenerated into an agent run that wrote code and opened a junk PR.
func TestRetryPreservesReviewIdentity(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	ctx := context.Background()
	svc, err := st.GetDefaultService(ctx, p.ID)
	if err != nil {
		t.Fatalf("default service: %v", err)
	}

	// Seed a FAILED review run directly (same pattern as review_test.go).
	rev := &domain.Run{
		ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID,
		Prompt: "AI review of PR x", Status: domain.StatusQueued,
		Kind: domain.RunKindReview, Phase: "Queued", Attempt: 1,
		PRHeadBranch: "jcode/run-abc12345", PRBaseBranch: "main",
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, rev); err != nil {
		t.Fatalf("seed review run: %v", err)
	}
	_, _ = st.MarkFailed(ctx, rev.ID, "Failed", domain.FailureAgentError, "no REVIEW.md", time.Now())

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+rev.ID+"/retry", consoleToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry: status=%d want 201", resp.StatusCode)
	}
	var retry domain.Run
	decode(t, resp, &retry)
	if retry.Kind != domain.RunKindReview {
		t.Fatalf("retry kind=%q want review", retry.Kind)
	}
	if retry.PRHeadBranch != "jcode/run-abc12345" || retry.PRBaseBranch != "main" {
		t.Fatalf("retry PR assoc = %q..%q, want preserved", retry.PRBaseBranch, retry.PRHeadBranch)
	}
}

func TestCancelRun(t *testing.T) {
	ts, _, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)

	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/cancel", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: status=%d want 200", resp.StatusCode)
	}
	var canceled domain.Run
	decode(t, resp, &canceled)
	if canceled.Status != domain.StatusCanceled {
		t.Fatalf("status=%s want canceled", canceled.Status)
	}
	// Cancel again -> 409 (already terminal).
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/cancel", consoleToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-cancel: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestCancelDeletesCommittedJobAndKeepsFields is the regression for the cancel
// half of the "cancel racing reconciler orphans Jobs + stale full-row write
// wipes k8s_job_name/token_hash" finding. Cancel must (1) NOT wipe
// k8s_job_name/token_hash (the old full-row write did), and (2) delete the Job
// named in the COMMITTED row so no orphan keeps running.
func TestCancelDeletesCommittedJobAndKeepsFields(t *testing.T) {
	ts, st, fake := newTestServerWithLauncher(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// Drive to scheduling with a real job name + token hash.
	if _, err := st.ScheduleRun(ctx, run.ID, "jcloud-run-"+run.ID, "tokhash", "PreparingWorkspace"); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/cancel", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	got, _ := st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status=%s want canceled", got.Status)
	}
	if got.K8sJobName == "" {
		t.Fatal("cancel wiped k8s_job_name (stale full-row write regression)")
	}
	if got.TokenHash == "" {
		t.Fatal("cancel wiped token_hash (stale full-row write regression)")
	}
	// The Job named in the committed row must have been deleted.
	deleted := fake.Deleted
	if len(deleted) != 1 || deleted[0] != "jcloud-run-"+run.ID {
		t.Fatalf("cancel deleted jobs=%v want [jcloud-run-%s]", deleted, run.ID)
	}
}

func TestEventsListAfterSeq(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// Seed events 2..5 (seq 1 already exists from run.status(queued)).
	var inputs []store.EventInput
	for i := int64(2); i <= 5; i++ {
		inputs = append(inputs, store.EventInput{Seq: i, Type: domain.EventAgentText, Payload: map[string]any{"n": i}})
	}
	if _, err := st.AppendEvents(ctx, run.ID, inputs); err != nil {
		t.Fatal(err)
	}

	resp = do(t, "GET", ts.URL+"/api/v1/runs/"+run.ID+"/events?after_seq=3", consoleToken, nil)
	var el struct {
		Events []domain.RunEvent `json:"events"`
	}
	decode(t, resp, &el)
	if len(el.Events) != 2 { // seq 4,5
		t.Fatalf("events after seq 3 = %d want 2", len(el.Events))
	}
	if el.Events[0].Seq != 4 {
		t.Fatalf("first seq=%d want 4", el.Events[0].Seq)
	}
}

func TestIngestAuthAndIdempotency(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// Give the run a token (as the reconciler would at Job creation).
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatalf("setup schedule: %v", err)
	}

	body := map[string]any{"events": []map[string]any{
		{"seq": 10, "type": "agent.text", "payload": map[string]any{"text": "hi"}},
		{"seq": 11, "type": "agent.tool_call", "payload": map[string]any{"tool": "read"}},
	}}

	// Console token must NOT work on the internal endpoint.
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", consoleToken, body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("console token on internal: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Correct run token: accepts 2.
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest: status=%d want 200", resp.StatusCode)
	}
	var res struct {
		Accepted int `json:"accepted"`
	}
	decode(t, resp, &res)
	if res.Accepted != 2 {
		t.Fatalf("accepted=%d want 2", res.Accepted)
	}

	// Re-post same batch: idempotent, accepts 0.
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	decode(t, resp, &res)
	if res.Accepted != 0 {
		t.Fatalf("replayed accepted=%d want 0 (idempotent)", res.Accepted)
	}
}

func TestIngestRunFailureRefinesReason(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	body := map[string]any{"events": []map[string]any{
		{"seq": 20, "type": "run.failure", "payload": map[string]any{"reason": "clone_failed", "message": "repo not found"}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	resp.Body.Close()

	got, _ := st.GetRun(ctx, run.ID)
	if got.FailureReason != domain.FailureCloneFailed {
		t.Fatalf("reason=%s want clone_failed", got.FailureReason)
	}
	if got.FailureMessage == "" {
		t.Fatal("failure message should be recorded")
	}
}

func TestSSEReplayThenTerminal(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// Seed events and drive the run terminal (via a legal transition path) so
	// the stream replays and closes.
	_, _ = st.AppendEvents(ctx, run.ID, []store.EventInput{
		{Seq: 2, Type: domain.EventAgentText, Payload: map[string]any{"text": "working"}},
	})
	if _, err := st.ScheduleRun(ctx, run.ID, "j", "h", "PreparingWorkspace"); err != nil {
		t.Fatalf("drive scheduling: %v", err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "StreamingTurn", time.Now()); err != nil {
		t.Fatalf("drive running: %v", err)
	}
	if _, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", time.Now()); err != nil {
		t.Fatalf("drive terminal: %v", err)
	}
	// Emit terminal status event (seq 3).
	_, _ = st.AppendEvents(ctx, run.ID, []store.EventInput{
		{Seq: 3, Type: domain.EventRunStatus, Payload: map[string]any{"status": "succeeded"}},
	})

	// Stream from after_seq=0; since run is terminal, it should replay all and
	// close. Bound with a client-side timeout so a regression can't hang the test.
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	req, _ := http.NewRequestWithContext(sctx, "GET",
		ts.URL+"/api/v1/runs/"+run.ID+"/stream?after_seq=0", nil)
	req.Header.Set("Authorization", "Bearer "+consoleToken)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	if ct := sresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q want text/event-stream", ct)
	}
	b, _ := io.ReadAll(sresp.Body)
	body := string(b)
	// Must contain the replayed events and the terminal completion comment.
	for _, want := range []string{"event: agent.text", "event: run.status", "run terminal"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream body missing %q; got:\n%s", want, body)
		}
	}
	// Frame format: event: line, id: line, data: JSON.
	if !strings.Contains(body, "data: {") {
		t.Errorf("stream missing data frame; got:\n%s", body)
	}
}

// TestSSEClosesWhenTerminalDuringReplay is the regression for "SSE stream never
// terminates when the run goes terminal during the replay window". The
// connect-time run snapshot is NON-terminal (running), but a terminal run.status
// event is already in the durable log (it committed after the client's GetRun
// but is delivered via replay). The old code decided closure only from the
// stale snapshot, entered the live loop, and hung forever emitting heartbeats.
// The fix closes the stream when a terminal run.status appears in replay (and
// re-checks the run after replay).
func TestSSEClosesWhenTerminalDuringReplay(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// Drive the run to running (connect-time snapshot is non-terminal).
	if _, err := st.ScheduleRun(ctx, run.ID, "j", "h", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A terminal run.status(succeeded) event is already durable (it committed
	// just after the client's GetRun would have run). The run ROW is left running
	// here to model the snapshot being observed before the terminal commit landed
	// on the row; the terminal EVENT is what must close the stream.
	if _, err := st.AppendInternalEvent(ctx, run.ID, domain.EventRunStatus,
		map[string]any{"status": "succeeded", "phase": "Succeeded"}); err != nil {
		t.Fatal(err)
	}

	// The stream must complete (not hang). A client-side timeout guards the
	// regression: a hang would exceed it and the read would return a truncated
	// body without the terminal marker.
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	defer scancel()
	req, _ := http.NewRequestWithContext(sctx, "GET",
		ts.URL+"/api/v1/runs/"+run.ID+"/stream?after_seq=0", nil)
	req.Header.Set("Authorization", "Bearer "+consoleToken)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	b, _ := io.ReadAll(sresp.Body) // returns only when the server closes the stream
	if sctx.Err() != nil {
		t.Fatal("stream did not close: hung until client timeout (terminal-during-replay regression)")
	}
	if !strings.Contains(string(b), "run terminal") {
		t.Fatalf("stream did not emit terminal marker; got:\n%s", string(b))
	}
}

// TestSSEOutOfOrderLiveEventsRecovered is the regression for "live SSE fan-out
// discards out-of-order publishes as already-replayed". A live event published
// with a seq beyond the next contiguous one (an earlier event was published out
// of order or dropped by the hub buffer) used to advance the high-water mark and
// cause the skipped seqs to be dropped as "duplicates", so a connected console
// permanently missed them. The fix backfills the gap from the durable log.
func TestSSEOutOfOrderLiveEventsRecovered(t *testing.T) {
	ts, st, hub := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", "h", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}

	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	defer scancel()
	req, _ := http.NewRequestWithContext(sctx, "GET",
		ts.URL+"/api/v1/runs/"+run.ID+"/stream?after_seq=0", nil)
	req.Header.Set("Authorization", "Bearer "+consoleToken)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()

	// Wait until the stream has a live subscriber (replay done, in live loop).
	waitForSubscriber(t, hub, run.ID)

	// Durably append seqs 2,3,4 but publish ONLY seq 4 first (out of order: 2,3
	// have not been published). The old code would set lastSent=4 and drop 2,3.
	ev2, _ := st.AppendInternalEvent(ctx, run.ID, domain.EventAgentText, map[string]any{"n": 2})
	ev3, _ := st.AppendInternalEvent(ctx, run.ID, domain.EventAgentText, map[string]any{"n": 3})
	ev4, _ := st.AppendInternalEvent(ctx, run.ID, domain.EventAgentText, map[string]any{"n": 4})
	hub.Publish(run.ID, ev4) // out-of-order: gap at 2,3 triggers backfill
	_ = ev2
	_ = ev3

	// Then a terminal event to close the stream deterministically.
	evT, _ := st.AppendInternalEvent(ctx, run.ID, domain.EventRunStatus,
		map[string]any{"status": "succeeded", "phase": "Succeeded"})
	hub.Publish(run.ID, evT)

	b, _ := io.ReadAll(sresp.Body)
	body := string(b)
	if sctx.Err() != nil {
		t.Fatal("stream hung")
	}
	// Every seq 1..4 plus the terminal frame must appear; none dropped.
	for _, want := range []string{`"n":2`, `"n":3`, `"n":4`, "run terminal"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q (out-of-order event dropped); got:\n%s", want, body)
		}
	}
}

// waitForSubscriber blocks until the hub has at least one live subscriber for
// runID (i.e. handleStream finished replay and entered the live loop), so the
// test can publish live events deterministically.
func waitForSubscriber(t *testing.T, hub *sse.Hub, runID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.SubscriberCount(runID) > 0 {
			// Give the handler a beat to reach the blocking select on the channel.
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("stream never established a live subscriber")
}

// TestIngestSeqIsServerAllocated proves the runner's client seq does not become
// the durable/SSE seq: the server renumbers events monotonically and does not
// collide with the run.status(queued) event emitted at creation.
func TestIngestSeqIsServerAllocated(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// run.status(queued) already took seq 1 at creation.
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatalf("setup schedule: %v", err)
	}

	// Runner posts with high client seqs that WOULD have collided with the
	// internal seq space under the old first-writer-wins scheme.
	body := map[string]any{"events": []map[string]any{
		{"seq": 1, "type": "agent.text", "payload": map[string]any{"text": "hi"}},
		{"seq": 2, "type": "agent.tool_call", "payload": map[string]any{"tool": "read"}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	var res struct {
		Accepted int `json:"accepted"`
	}
	decode(t, resp, &res)
	if res.Accepted != 2 {
		t.Fatalf("accepted=%d want 2", res.Accepted)
	}

	// Durable log: seq 1 = internal run.status, seq 2,3 = runner events. No drop.
	events, _ := st.ListEvents(ctx, run.ID, 0, 100)
	if len(events) != 3 {
		t.Fatalf("events=%d want 3 (queued + 2 runner)", len(events))
	}
	if events[0].Type != domain.EventRunStatus || events[0].Seq != 1 {
		t.Fatalf("event0 = %s seq %d want run.status seq 1", events[0].Type, events[0].Seq)
	}
	if events[1].Seq != 2 || events[2].Seq != 3 {
		t.Fatalf("runner seqs = %d,%d want 2,3", events[1].Seq, events[2].Seq)
	}
}

func TestSSEStreamAcceptsQueryToken(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// Drive terminal so the stream replays and closes without hanging.
	if _, err := st.ScheduleRun(ctx, run.ID, "j", "h", "PreparingWorkspace"); err != nil {
		t.Fatalf("drive scheduling: %v", err)
	}
	if _, err := st.CancelRun(ctx, run.ID, "CanceledByOperator", time.Now()); err != nil {
		t.Fatalf("drive canceled: %v", err)
	}
	_, _ = st.AppendInternalEvent(ctx, run.ID, domain.EventRunStatus, map[string]any{"status": "canceled"})

	// No Authorization header at all: browser EventSource can't send one. The
	// ?access_token= query param must authenticate.
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	req, _ := http.NewRequestWithContext(sctx, "GET",
		ts.URL+"/api/v1/runs/"+run.ID+"/stream?after_seq=0&access_token="+consoleToken, nil)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("stream with access_token: status=%d want 200", sresp.StatusCode)
	}
	if ct := sresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q want text/event-stream", ct)
	}

	// A bad access_token must be rejected.
	req2, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/"+run.ID+"/stream?access_token=wrong", nil)
	r2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad access_token: status=%d want 401", r2.StatusCode)
	}
}

// TestSSEStreamClosesOnServerShutdown is the regression for "graceful shutdown
// never completes while any SSE client is connected". A live SSE handler blocks
// on its request context; http.Server.Shutdown does not cancel that. main wires
// the server's BaseContext to a context it cancels on shutdown so streams
// observe it, write a final comment, and return. This test reproduces that
// wiring: it cancels the BaseContext and asserts the stream returns promptly
// with the shutdown marker instead of hanging until Shutdown's deadline.
func TestSSEStreamClosesOnServerShutdown(t *testing.T) {
	st := store.NewMemStore()
	hub := sse.NewHub()
	cfg := &config.Config{ConsoleToken: consoleToken}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)

	// Mirror main.go: BaseContext returns a cancelable context; canceling it must
	// unblock in-flight streams.
	baseCtx, cancelStreams := context.WithCancel(context.Background())
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.BaseContext = func(net.Listener) context.Context { return baseCtx }
	ts.Start()
	t.Cleanup(ts.Close)

	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = st.CreateService(ctx, svc)
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "x", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	_ = st.CreateRun(ctx, run)
	// Non-terminal so the stream enters the live loop and blocks.
	if _, err := st.ScheduleRun(ctx, run.ID, "j", "h", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/"+run.ID+"/stream?after_seq=0", nil)
	req.Header.Set("Authorization", "Bearer "+consoleToken)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()

	waitForSubscriber(t, hub, run.ID) // stream is live and blocking

	// Simulate graceful shutdown: cancel the base context.
	cancelStreams()

	// The read must return promptly (not hang) with the shutdown marker.
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(sresp.Body)
		done <- string(b)
	}()
	select {
	case body := <-done:
		if !strings.Contains(body, "server shutting down") {
			t.Fatalf("stream closed without shutdown marker; got:\n%s", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream did not close after server shutdown (graceful-shutdown regression)")
	}
}

func TestArtifactRoundTrip(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	diff := "--- a\n+++ b\n@@ -1 +1 @@\n+Hello\n"
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/artifact", tok,
		map[string]string{"kind": "diff", "content": diff})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put artifact: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()

	// Get as JSON.
	resp = do(t, "GET", ts.URL+"/api/v1/runs/"+run.ID+"/artifact", consoleToken, nil)
	var art domain.RunArtifact
	decode(t, resp, &art)
	if art.Content != diff {
		t.Fatalf("artifact content mismatch")
	}

	// Download variant.
	resp = do(t, "GET", ts.URL+"/api/v1/runs/"+run.ID+"/artifact?download=1", consoleToken, nil)
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("download content-disposition=%q", cd)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != diff {
		t.Fatalf("download content mismatch")
	}
}

// TestIngestRunGitRecordsBranch is the ingest regression for ST-1: a run.git
// event from the runner records the pushed branch/commit on the run so the
// reconciler's PR pass can find it.
func TestIngestRunGitRecordsBranch(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	body := map[string]any{"events": []map[string]any{
		{"seq": 5, "type": "run.git", "payload": map[string]any{"branch": "agent/run-" + run.ID, "commit_sha": "cafef00d"}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	resp.Body.Close()

	got, _ := st.GetRun(ctx, run.ID)
	if got.GitBranch != "agent/run-"+run.ID {
		t.Fatalf("git_branch=%q want agent/run-%s", got.GitBranch, run.ID)
	}
	if got.CommitSHA != "cafef00d" {
		t.Fatalf("commit_sha=%q want cafef00d", got.CommitSHA)
	}
}

// TestIngestPushFailedClassification proves the runner can report a push_failed
// reason via run.failure and it is accepted as a valid classification (ST-1).
func TestIngestPushFailedClassification(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	body := map[string]any{"events": []map[string]any{
		{"seq": 9, "type": "run.failure", "payload": map[string]any{"reason": "push_failed", "message": "remote rejected: protected branch"}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	resp.Body.Close()

	got, _ := st.GetRun(ctx, run.ID)
	if got.FailureReason != domain.FailurePushFailed {
		t.Fatalf("reason=%s want push_failed (must not be downgraded to agent_error)", got.FailureReason)
	}
	if got.FailureMessage == "" {
		t.Fatal("push_failed message should be recorded")
	}
}

// TestIngestRunResultRecordsOutcome is the ingest regression for D18: a
// run.result{outcome:no_changes} event records the first-class outcome on the
// run (runs.result) WITHOUT changing status, and the run's API JSON then
// serialises "result":"no_changes". A run that never reported a result
// serialises "result":null.
func TestIngestRunResultRecordsOutcome(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	body := map[string]any{"events": []map[string]any{
		{"seq": 7, "type": "run.result", "payload": map[string]any{"outcome": "no_changes"}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	resp.Body.Close()

	got, _ := st.GetRun(ctx, run.ID)
	if got.Result == nil || *got.Result != domain.RunResultNoChanges {
		t.Fatalf("run.result=%v want no_changes", got.Result)
	}
	// Must not have flipped status — the reconciler still drives it from the Job.
	if got.Status == domain.StatusSucceeded || got.Status == domain.StatusFailed {
		t.Fatalf("status=%s; run.result must not change status", got.Status)
	}

	// API serialisation carries "result":"no_changes".
	resp = do(t, "GET", ts.URL+"/api/v1/runs/"+run.ID, consoleToken, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `"result":"no_changes"`) {
		t.Fatalf("run JSON missing result field: %s", raw)
	}

	// A run that reported no result serialises result:null (field always present).
	resp = do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "other"})
	var run2 domain.Run
	decode(t, resp, &run2)
	resp = do(t, "GET", ts.URL+"/api/v1/runs/"+run2.ID, consoleToken, nil)
	raw2, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw2), `"result":null`) {
		t.Fatalf("run-without-result JSON should carry result:null: %s", raw2)
	}
}

// TestIngestRunResultUnknownOutcomeIgnored proves an unrecognised outcome is not
// persisted (we never store garbage in runs.result).
func TestIngestRunResultUnknownOutcomeIgnored(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	body := map[string]any{"events": []map[string]any{
		{"seq": 8, "type": "run.result", "payload": map[string]any{"outcome": "bogus"}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	resp.Body.Close()

	got, _ := st.GetRun(ctx, run.ID)
	if got.Result != nil {
		t.Fatalf("unknown outcome persisted as %v; want nil", *got.Result)
	}
}

// TestCreateDraftPRService proves service creation accepts + persists git
// integration config and validates draft_pr requirements (this validation moved
// from the removed POST /projects repo shim to the services endpoint).
func TestCreateDraftPRService(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]string{"name": "gp"})
	var proj domain.Project
	decode(t, resp, &proj)
	svcURL := ts.URL + "/api/v1/projects/" + proj.ID + "/services"

	// Happy path: draft_pr with gitea + owner_name.
	resp = do(t, "POST", svcURL, consoleToken, map[string]any{
		"name": "gp", "repo_url": "http://git/x.git",
		"git_mode": "draft_pr", "provider": "gitea", "owner_name": "jcloud/seed",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create draft_pr service: status=%d want 201", resp.StatusCode)
	}
	var svc domain.Service
	decode(t, resp, &svc)
	if svc.GitMode != domain.GitModeDraftPR || svc.Provider != domain.ProviderGitea || svc.RepoOwnerName != "jcloud/seed" {
		t.Fatalf("git config not persisted: %+v", svc)
	}

	// draft_pr WITHOUT a provider repo (raw single-segment URL) -> 400.
	resp = do(t, "POST", svcURL, consoleToken, map[string]any{
		"name": "bad", "repo_url": "http://git/x.git", "git_mode": "draft_pr",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("draft_pr without repo: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown git_mode -> 400.
	resp = do(t, "POST", svcURL, consoleToken, map[string]any{
		"name": "bad2", "repo_url": "http://git/x.git", "git_mode": "merge",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad git_mode: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Default (omit git_mode) -> readonly.
	resp = do(t, "POST", svcURL, consoleToken, map[string]any{
		"name": "ro", "repo_url": "http://git/x.git",
	})
	decode(t, resp, &svc)
	if svc.GitMode != domain.GitModeReadonly {
		t.Fatalf("default git_mode=%q want readonly", svc.GitMode)
	}
}

// TestSystemSnapshot exercises GET /api/v1/system: it requires the console token,
// reports build/guardrail/provider/runner config, reflects live run counts from
// the store, and — the load-bearing invariant — NEVER leaks a secret (no token,
// no DSN, no console token) into the response body.
func TestSystemSnapshot(t *testing.T) {
	st := store.NewMemStore()
	hub := sse.NewHub()
	// A config with real secrets set, so the no-leak assertion is meaningful.
	cfg := &config.Config{
		ConsoleToken:        consoleToken,
		DatabaseURL:         "postgres://user:s3cr3t-dsn@db/jcloud",
		MaxConcurrentRuns:   4,
		RunTimeoutSecs:      1800,
		JobTTLSeconds:       3600,
		Namespace:           "jcloud",
		RunnerImage:         "ghcr.io/acme/runner:v1",
		JobLauncher:         "kubernetes",
		GiteaURL:            "http://gitea.jcloud.svc.cluster.local:3000",
		GiteaToken:          "gitea-pat-DO-NOT-LEAK",
		PersistentWorkspace: true,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Requires auth.
	resp := do(t, "GET", ts.URL+"/api/v1/system", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Seed some runs across statuses: 1 running, 1 scheduling, 2 queued.
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	mkRun := func() *domain.Run {
		r := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "x", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	running := mkRun()
	if _, err := st.ScheduleRun(ctx, running.ID, "j1", "h1", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, running.ID, "Running", time.Now()); err != nil {
		t.Fatal(err)
	}
	sched := mkRun()
	if _, err := st.ScheduleRun(ctx, sched.ID, "j2", "h2", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	mkRun() // queued
	mkRun() // queued

	// Fetch snapshot and read the RAW body for the no-leak scan.
	resp = do(t, "GET", ts.URL+"/api/v1/system", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("system: status=%d want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// No secret may appear anywhere in the serialized snapshot.
	for _, secret := range []string{cfg.GiteaToken, cfg.ConsoleToken, cfg.DatabaseURL, "s3cr3t-dsn"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("SECRET LEAK: response body contains %q\nbody: %s", secret, raw)
		}
	}

	var got systemResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, raw)
	}
	if got.Capacity.MaxConcurrentRuns != 4 {
		t.Fatalf("max_concurrent_runs=%d want 4", got.Capacity.MaxConcurrentRuns)
	}
	if got.Capacity.Running != 1 {
		t.Fatalf("running=%d want 1", got.Capacity.Running)
	}
	if got.Capacity.Scheduling != 1 {
		t.Fatalf("scheduling=%d want 1", got.Capacity.Scheduling)
	}
	if got.Capacity.Queued != 2 {
		t.Fatalf("queued=%d want 2", got.Capacity.Queued)
	}
	if got.Guardrails.RunTimeoutSeconds != 1800 || got.Guardrails.JobTTLSeconds != 3600 {
		t.Fatalf("guardrails=%+v want {1800,3600}", got.Guardrails)
	}
	if !got.Provider.GiteaEnabled {
		t.Fatal("gitea_enabled=false want true (GiteaToken set)")
	}
	if got.Provider.GiteaURL != cfg.GiteaURL {
		t.Fatalf("gitea_url=%q want %q", got.Provider.GiteaURL, cfg.GiteaURL)
	}
	if got.Runner.Image != cfg.RunnerImage {
		t.Fatalf("runner.image=%q want %q", got.Runner.Image, cfg.RunnerImage)
	}
	if !got.Runner.PersistentWorkspace {
		t.Fatal("runner.persistent_workspace=false want true (Feature C flag on)")
	}
	if got.Namespace != "jcloud" || got.Launcher != "kubernetes" {
		t.Fatalf("namespace/launcher = %q/%q want jcloud/kubernetes", got.Namespace, got.Launcher)
	}
	if got.Version.Version == "" {
		t.Fatal("version empty")
	}

	// With no Gitea token, gitea_enabled must be false.
	cfg.GiteaToken = ""
	resp = do(t, "GET", ts.URL+"/api/v1/system", consoleToken, nil)
	decode(t, resp, &got)
	if got.Provider.GiteaEnabled {
		t.Fatal("gitea_enabled=true want false (GiteaToken empty)")
	}
}
