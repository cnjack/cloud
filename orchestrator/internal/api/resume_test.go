package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// newTestServerPersistent is newTestServer with the cluster PERSISTENT_WORKSPACE
// switch ON, so resume's workspace_not_persistent precondition passes.
func newTestServerPersistent(t *testing.T) (*httptest.Server, *store.MemStore) {
	t.Helper()
	st := store.NewMemStore()
	hub := sse.NewHub()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, PersistentWorkspace: true})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// seedSessionRun creates a session run against the fixture service and drives it
// through its lifecycle. When acpSessionID != "" it is recorded (run.session);
// when terminal it is driven to succeeded. Returns the run id.
func seedSessionRun(t *testing.T, st *store.MemStore, projectID, serviceID, acpSessionID string, session, terminal bool) string {
	t.Helper()
	ctx := context.Background()
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: projectID, ServiceID: serviceID, Prompt: "chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: session,
		Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j-"+run.ID, auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	if acpSessionID != "" {
		if _, err := st.SetRunACPSession(ctx, run.ID, acpSessionID); err != nil {
			t.Fatal(err)
		}
	}
	if terminal {
		if _, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return run.ID
}

// seedViewer creates a viewer member of projectID and returns their session
// token. It first consumes the "first user = cluster admin" slot with a throwaway
// admin so the viewer is a plain viewer (not an admin who would bypass RBAC).
func seedViewer(t *testing.T, st *store.MemStore, projectID string) string {
	t.Helper()
	ctx := context.Background()
	_ = mkUser(t, st, "admin-"+domain.NewID()[:6]) // first user => cluster admin
	viewer := mkUser(t, st, "viewer-"+domain.NewID()[:6])
	if err := st.UpsertMember(ctx, &domain.ProjectMember{
		ProjectID: projectID, UserID: viewer.ID, Role: domain.RoleViewer,
	}); err != nil {
		t.Fatal(err)
	}
	return mkSession(t, st, viewer.ID)
}

func errorCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, resp, &body)
	return body.Error.Code
}

// TestResumeSuccess: resuming a terminal session run (with a recorded ACP session
// id + persistent workspace) creates a NEW queued session run that carries
// resumed_from + the ORIGINAL's acp_session_id (copied for the reconciler to
// inject RESUME_SESSION_ID before the new run emits its own run.session).
func TestResumeSuccess(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	origID := seedSessionRun(t, st, p.Project.ID, p.ServiceID, "acp-abc123", true, true)

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+origID+"/resume", consoleToken,
		map[string]string{"prompt": "keep going"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume: status=%d want 201", resp.StatusCode)
	}
	var resume domain.Run
	decode(t, resp, &resume)
	if resume.ID == origID {
		t.Fatal("resume must create a NEW run, not mutate the original")
	}
	if resume.ResumedFrom == nil || *resume.ResumedFrom != origID {
		t.Fatalf("resumed_from=%v want %q", resume.ResumedFrom, origID)
	}
	if resume.AcpSessionID != "acp-abc123" {
		t.Fatalf("acp_session_id=%q want copied acp-abc123", resume.AcpSessionID)
	}
	if !resume.Session {
		t.Fatal("resume run must be a session")
	}
	if resume.Status != domain.StatusQueued {
		t.Fatalf("status=%s want queued", resume.Status)
	}
	if resume.Prompt != "keep going" {
		t.Fatalf("prompt=%q want the resume body prompt", resume.Prompt)
	}
}

// TestResumeInheritsPermissionMode: an approval-mode session resumes as an
// approval-mode session (dropping it would drop the user's guardrail).
func TestResumeInheritsPermissionMode(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	ctx := context.Background()
	// Seed directly so we can set permission_mode=approval on the original.
	orig := &domain.Run{
		ID: domain.NewID(), ProjectID: p.Project.ID, ServiceID: p.ServiceID, Prompt: "chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true,
		PermissionMode: domain.PermissionModeApproval, AcpSessionID: "acp-perm", Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, orig); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ScheduleRun(ctx, orig.ID, "j", "h", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkFailed(ctx, orig.ID, "Failed", domain.FailureAgentError, "boom", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+orig.ID+"/resume", consoleToken,
		map[string]string{"prompt": "again"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume from FAILED session: status=%d want 201 (failed is terminal, resumable)", resp.StatusCode)
	}
	var resume domain.Run
	decode(t, resp, &resume)
	if resume.PermissionMode != domain.PermissionModeApproval {
		t.Fatalf("permission_mode=%q want approval (inherited)", resume.PermissionMode)
	}
}

func TestResumeAllowsPermissionAndModelOverrides(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	ctx := context.Background()
	orig := &domain.Run{
		ID: domain.NewID(), ProjectID: p.Project.ID, ServiceID: p.ServiceID, Prompt: "chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true,
		PermissionMode: domain.PermissionModeApproval, AcpSessionID: "acp-override", Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, orig); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ScheduleRun(ctx, orig.ID, "j", "h", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, orig.ID, "Succeeded", time.Now()); err != nil {
		t.Fatal(err)
	}

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+orig.ID+"/resume", consoleToken,
		map[string]string{"prompt": "again", "model_id": "", "permission_mode": "full_access"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume override: status=%d want 201", resp.StatusCode)
	}
	var resume domain.Run
	decode(t, resp, &resume)
	if resume.PermissionMode != "" {
		t.Fatalf("permission_mode=%q want full_access default", resume.PermissionMode)
	}
}

func TestResumeRejectsModelThatIsNotGranted(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	origID := seedSessionRun(t, st, p.Project.ID, p.ServiceID, "acp-model", true, true)
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+origID+"/resume", consoleToken,
		map[string]string{"prompt": "again", "model_id": "not-granted"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resume model override: status=%d want 403", resp.StatusCode)
	}
	if code := errorCode(t, resp); code != "model_not_granted" {
		t.Fatalf("code=%q want model_not_granted", code)
	}
}

// TestResumeNotSession: a non-session terminal run cannot be resumed.
func TestResumeNotSession(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	origID := seedSessionRun(t, st, p.Project.ID, p.ServiceID, "acp-x", false /*session*/, true)
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+origID+"/resume", consoleToken,
		map[string]string{"prompt": "go"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", resp.StatusCode)
	}
	if code := errorCode(t, resp); code != "run_not_resumable" {
		t.Fatalf("code=%q want run_not_resumable", code)
	}
}

// TestResumeStillActive: a session run that is NOT terminal (awaiting_input) is
// resumed via the message box, not a fresh resume run — 409 run_not_resumable.
func TestResumeStillActive(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	ctx := context.Background()
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: p.Project.ID, ServiceID: p.ServiceID, Prompt: "chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true, Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ScheduleRun(ctx, run.ID, "j", "h", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunACPSession(ctx, run.ID, "acp-live"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunAwaitingInput(ctx, run.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/resume", consoleToken,
		map[string]string{"prompt": "go"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", resp.StatusCode)
	}
	if code := errorCode(t, resp); code != "run_not_resumable" {
		t.Fatalf("code=%q want run_not_resumable", code)
	}
}

// TestResumeSessionNotRecorded: a terminal session run with no recorded ACP
// session id cannot be resumed — 409 session_not_recorded.
func TestResumeSessionNotRecorded(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	origID := seedSessionRun(t, st, p.Project.ID, p.ServiceID, "" /*no acp id*/, true, true)
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+origID+"/resume", consoleToken,
		map[string]string{"prompt": "go"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", resp.StatusCode)
	}
	if code := errorCode(t, resp); code != "session_not_recorded" {
		t.Fatalf("code=%q want session_not_recorded", code)
	}
}

// TestResumeWorkspaceNotPersistent: with the cluster persistent-workspace switch
// OFF, a resume is refused — 409 workspace_not_persistent (the transcript never
// survived on a PVC, so session/load would fail).
func TestResumeWorkspaceNotPersistent(t *testing.T) {
	ts, st, _ := newTestServer(t) // PersistentWorkspace defaults false
	p := createProject(t, ts)
	origID := seedSessionRun(t, st, p.Project.ID, p.ServiceID, "acp-x", true, true)
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+origID+"/resume", consoleToken,
		map[string]string{"prompt": "go"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", resp.StatusCode)
	}
	if code := errorCode(t, resp); code != "workspace_not_persistent" {
		t.Fatalf("code=%q want workspace_not_persistent", code)
	}
}

// TestResumeEmptyPrompt: an empty prompt is a 400 (like every dispatch path).
func TestResumeEmptyPrompt(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	origID := seedSessionRun(t, st, p.Project.ID, p.ServiceID, "acp-x", true, true)
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+origID+"/resume", consoleToken,
		map[string]string{"prompt": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty prompt: status=%d want 400", resp.StatusCode)
	}
}

// TestResumeViewerForbidden: a viewer on the run's project cannot resume.
func TestResumeViewerForbidden(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	origID := seedSessionRun(t, st, p.Project.ID, p.ServiceID, "acp-x", true, true)
	// A viewer principal: seed a user + viewer membership, log in via a session token.
	viewerTok := seedViewer(t, st, p.Project.ID)
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+origID+"/resume", viewerTok,
		map[string]string{"prompt": "go"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer resume: status=%d want 403", resp.StatusCode)
	}
}

// TestIngestRunSessionRecordsID: a run.session event records acp_session_id on
// the run (first-writer-wins) without changing status.
func TestIngestRunSessionRecordsID(t *testing.T) {
	ts, st, _ := newTestServer(t)
	p := createProject(t, ts)
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+p.ServiceID+"/runs", consoleToken,
		map[string]any{"prompt": "chat", "session": true})
	var run domain.Run
	decode(t, resp, &run)
	ctx := context.Background()
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}

	// First run.session (resumed=false) records the id.
	body := map[string]any{"events": []map[string]any{
		{"seq": 5, "type": "run.session", "payload": map[string]any{"acp_session_id": "acp-first", "resumed": false}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	resp.Body.Close()
	got, _ := st.GetRun(ctx, run.ID)
	if got.AcpSessionID != "acp-first" {
		t.Fatalf("acp_session_id=%q want acp-first", got.AcpSessionID)
	}
	if got.Status != domain.StatusScheduling {
		t.Fatalf("status=%s want unchanged (scheduling)", got.Status)
	}

	// A second run.session with a DIFFERENT id (e.g. resumed=true carrying a fresh
	// value) must NOT overwrite — first-writer-wins.
	body = map[string]any{"events": []map[string]any{
		{"seq": 6, "type": "run.session", "payload": map[string]any{"acp_session_id": "acp-second", "resumed": true}},
	}}
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/events", tok, body)
	resp.Body.Close()
	got, _ = st.GetRun(ctx, run.ID)
	if got.AcpSessionID != "acp-first" {
		t.Fatalf("acp_session_id=%q want acp-first (first-writer-wins)", got.AcpSessionID)
	}
}

// seedResumeRunWithToken creates a resume-shaped run (ResumedFrom set +
// acp_session_id pre-filled at creation, as handleResumeRun does) in scheduling
// state, and returns the run + its plaintext RUN_TOKEN for the ingest endpoint.
func seedResumeRunWithToken(t *testing.T, st *store.MemStore, projectID, serviceID, acpSessionID string) (*domain.Run, string) {
	t.Helper()
	ctx := context.Background()
	origID := domain.NewID()
	resume := &domain.Run{
		ID: domain.NewID(), ProjectID: projectID, ServiceID: serviceID, Prompt: "again",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true,
		ResumedFrom: &origID, AcpSessionID: acpSessionID, Attempt: 1, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, resume); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, resume.ID, "j", auth.HashToken(tok), "x"); err != nil {
		t.Fatal(err)
	}
	return resume, tok
}

// findMismatchEvents returns the run's internal run.session events carrying the
// acp_session_id_mismatch warning (the defense-in-depth marker).
func findMismatchEvents(t *testing.T, st *store.MemStore, runID string) []domain.RunEvent {
	t.Helper()
	events, err := st.ListEvents(context.Background(), runID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.RunEvent
	for _, ev := range events {
		if ev.Type != domain.EventRunSession {
			continue
		}
		if w, _ := ev.Payload["warning"].(string); w == "acp_session_id_mismatch" {
			out = append(out, ev)
		}
	}
	return out
}

// TestIngestRunSessionResumedFirstWriterWins: a resume run whose acp_session_id
// was pre-filled at creation keeps it even when the runner re-emits the SAME id
// with resumed=true (the store write is a no-op), and the matching path emits NO
// mismatch warning event.
func TestIngestRunSessionResumedPreserved(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	ctx := context.Background()
	resume, tok := seedResumeRunWithToken(t, st, p.Project.ID, p.ServiceID, "acp-shared")
	body := map[string]any{"events": []map[string]any{
		{"seq": 1, "type": "run.session", "payload": map[string]any{"acp_session_id": "acp-shared", "resumed": true}},
	}}
	resp := do(t, "POST", ts.URL+"/internal/v1/runs/"+resume.ID+"/events", tok, body)
	resp.Body.Close()
	got, _ := st.GetRun(ctx, resume.ID)
	if got.AcpSessionID != "acp-shared" {
		t.Fatalf("acp_session_id=%q want acp-shared", got.AcpSessionID)
	}
	if evs := findMismatchEvents(t, st, resume.ID); len(evs) != 0 {
		t.Fatalf("matching run.session must emit NO mismatch event, got %d", len(evs))
	}
}

// TestIngestRunSessionResumeMismatchIsVisible pins the defense-in-depth path: a
// RESUME run (ResumedFrom set, acp_session_id pre-filled) whose runner reports a
// DIFFERENT acp_session_id keeps the expected id (first-writer-wins) — but the
// anomaly must be VISIBLE, not a silent no-op: an internal run.session event
// with warning=acp_session_id_mismatch carrying both ids lands on the timeline.
func TestIngestRunSessionResumeMismatchIsVisible(t *testing.T) {
	ts, st := newTestServerPersistent(t)
	p := createProject(t, ts)
	ctx := context.Background()
	resume, tok := seedResumeRunWithToken(t, st, p.Project.ID, p.ServiceID, "acp-expected")

	body := map[string]any{"events": []map[string]any{
		{"seq": 1, "type": "run.session", "payload": map[string]any{"acp_session_id": "acp-DIFFERENT", "resumed": true}},
	}}
	resp := do(t, "POST", ts.URL+"/internal/v1/runs/"+resume.ID+"/events", tok, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest: status=%d want 200 (mismatch is a warning, not an ingest failure)", resp.StatusCode)
	}
	resp.Body.Close()

	// The expected (injected) id stays on the row.
	got, _ := st.GetRun(ctx, resume.ID)
	if got.AcpSessionID != "acp-expected" {
		t.Fatalf("acp_session_id=%q want acp-expected (first-writer-wins keeps the injected id)", got.AcpSessionID)
	}
	// The anomaly is timeline-visible: exactly one internal mismatch event with
	// both ids in the payload.
	evs := findMismatchEvents(t, st, resume.ID)
	if len(evs) != 1 {
		t.Fatalf("mismatch events = %d want exactly 1", len(evs))
	}
	pl := evs[0].Payload
	if pl["expected_acp_session_id"] != "acp-expected" || pl["acp_session_id"] != "acp-DIFFERENT" {
		t.Fatalf("mismatch payload = %+v want expected_acp_session_id=acp-expected acp_session_id=acp-DIFFERENT", pl)
	}
}
