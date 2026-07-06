package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

const consoleToken = "test-console-token"

func newTestServer(t *testing.T) (*httptest.Server, *store.MemStore, *sse.Hub) {
	t.Helper()
	st := store.NewMemStore()
	hub := sse.NewHub()
	cfg := &config.Config{ConsoleToken: consoleToken}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, hub
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

	// Create with missing fields -> 400.
	resp := do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]string{"name": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing repo_url: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Create OK.
	resp = do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]string{
		"name": "demo", "repo_url": "https://git/x.git",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d want 201", resp.StatusCode)
	}
	var proj domain.Project
	decode(t, resp, &proj)
	if proj.ID == "" || proj.DefaultBranch != "main" {
		t.Fatalf("bad project: %+v", proj)
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

func createProject(t *testing.T, ts *httptest.Server) domain.Project {
	t.Helper()
	resp := do(t, "POST", ts.URL+"/api/v1/projects", consoleToken, map[string]string{
		"name": "demo", "repo_url": "https://git/x.git",
	})
	var p domain.Project
	decode(t, resp, &p)
	return p
}

func TestRunLifecycleAPI(t *testing.T) {
	ts, _, _ := newTestServer(t)
	p := createProject(t, ts)

	// Create run.
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
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
	resp = do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
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
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
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
	got.Status = domain.StatusFailed
	got.FailureReason = domain.FailureCloneFailed
	got.FailureMessage = "boom"
	_ = st.UpdateRun(context.Background(), got)

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

func TestCancelRun(t *testing.T) {
	ts, _, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
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

func TestEventsListAfterSeq(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
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
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// Give the run a token (as the reconciler would at Job creation).
	tok, _ := auth.GenerateRunToken()
	got, _ := st.GetRun(ctx, run.ID)
	got.TokenHash = auth.HashToken(tok)
	got.Status = domain.StatusScheduling
	got.K8sJobName = "j"
	_ = st.UpdateRun(ctx, got)

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
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	tok, _ := auth.GenerateRunToken()
	got, _ := st.GetRun(ctx, run.ID)
	got.TokenHash = auth.HashToken(tok)
	got.Status = domain.StatusScheduling // legal from queued
	got.K8sJobName = "j"
	if err := st.UpdateRun(ctx, got); err != nil {
		t.Fatalf("setup token: %v", err)
	}

	body := map[string]any{"events": []map[string]any{
		{"seq": 20, "type": "run.failure", "payload": map[string]any{"reason": "clone_failed", "message": "repo not found"}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	resp.Body.Close()

	got, _ = st.GetRun(ctx, run.ID)
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
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	// Seed events and drive the run terminal (via a legal transition path) so
	// the stream replays and closes.
	_, _ = st.AppendEvents(ctx, run.ID, []store.EventInput{
		{Seq: 2, Type: domain.EventAgentText, Payload: map[string]any{"text": "working"}},
	})
	got, _ := st.GetRun(ctx, run.ID)
	got.Status = domain.StatusScheduling
	_ = st.UpdateRun(ctx, got)
	got, _ = st.GetRun(ctx, run.ID)
	got.Status = domain.StatusRunning
	got.StartedAt = ptr(time.Now())
	_ = st.UpdateRun(ctx, got)
	got, _ = st.GetRun(ctx, run.ID)
	got.Status = domain.StatusSucceeded
	got.FinishedAt = ptr(time.Now())
	if err := st.UpdateRun(ctx, got); err != nil {
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

func TestArtifactRoundTrip(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/projects/"+p.ID+"/runs", consoleToken,
		map[string]string{"prompt": "task"})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()

	tok, _ := auth.GenerateRunToken()
	got, _ := st.GetRun(ctx, run.ID)
	got.TokenHash = auth.HashToken(tok)
	got.Status = domain.StatusScheduling // legal from queued
	got.K8sJobName = "j"
	if err := st.UpdateRun(ctx, got); err != nil {
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

func ptr[T any](v T) *T { return &v }
