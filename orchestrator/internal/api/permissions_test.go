package api

import (
	"context"
	"errors"
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

// F8b permission-approval tests. Contract under test (docs/11-api.md; the F8a
// runner half is runner/acpdrive/permission.go):
//   - ingest: agent.permission_request upserts a run_permissions row
//     idempotently (a duplicate NEVER resets decided/resolved state);
//     agent.permission_resolved stamps the resolved_* fields.
//   - decision endpoint: 204 for UNKNOWN request_id (hard constraint — never
//     404), 200 {option_id} once decided, 410 once resolved or once the run is
//     finalizing/terminal.
//   - permission-response: member+ (viewer 403), 409 permission_already_resolved,
//     400 for an option not offered, 404 for an unknown request.
//   - run creation: permission_mode="approval" only valid with session:true.

// makeApprovalRun creates a project + service + session run in approval mode,
// driven to `status`. Returns run id + RUN_TOKEN.
func makeApprovalRun(t *testing.T, st *store.MemStore, status domain.RunStatus) (string, string) {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/y.git",
		GitMode: domain.GitModeReadonly, DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "chat",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true,
		PermissionMode: domain.PermissionModeApproval, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	if status == domain.StatusSucceeded {
		if _, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return run.ID, tok
}

// permissionRequestEvent is the wire shape of one agent.permission_request
// (seq = the runner's per-source idempotency key).
func permissionRequestEvent(seq int64, requestID string) map[string]any {
	return map[string]any{
		"seq":  seq,
		"type": "agent.permission_request",
		"payload": map[string]any{
			"request_id":   requestID,
			"tool_call_id": "tc-1",
			"title":        "Run `make deploy`",
			"options": []map[string]any{
				{"option_id": "allow", "name": "Allow", "kind": "allow_once"},
				{"option_id": "reject", "name": "Reject", "kind": "reject_once"},
			},
		},
	}
}

func ingest(t *testing.T, tsURL, rid, tok string, events ...map[string]any) *http.Response {
	t.Helper()
	return do(t, "POST", tsURL+"/internal/v1/runs/"+rid+"/events", tok, map[string]any{"events": events})
}

// TestIngestPermissionRequestUpsertIdempotent: the ingest hook records the
// request; an at-least-once re-delivery (same request_id, same or new client
// seq) never resets an already-decided row.
func TestIngestPermissionRequestUpsertIdempotent(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()
	rid, tok := makeApprovalRun(t, st, domain.StatusRunning)

	resp := ingest(t, ts.URL, rid, tok, permissionRequestEvent(1, "req-1"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest request event: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	perm, err := st.GetRunPermission(ctx, rid, "req-1")
	if err != nil {
		t.Fatalf("row not upserted: %v", err)
	}
	if perm.Title != "Run `make deploy`" || perm.ToolCallID != "tc-1" || len(perm.Options) != 2 {
		t.Fatalf("row mismatch: %+v", perm)
	}
	if perm.Options[0].OptionID != "allow" || perm.Options[1].Kind != "reject_once" {
		t.Fatalf("options mismatch: %+v", perm.Options)
	}

	// The user decides; then the runner re-sends the SAME event (network retry,
	// same client seq) AND a fresh duplicate (new seq, same request_id): the
	// decided state must survive both.
	if _, won, err := st.DecideRunPermission(ctx, rid, "req-1", "allow", "", time.Now()); err != nil || !won {
		t.Fatalf("decide: won=%v err=%v", won, err)
	}
	resp = ingest(t, ts.URL, rid, tok, permissionRequestEvent(1, "req-1"), permissionRequestEvent(2, "req-1"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-ingest: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	perm, _ = st.GetRunPermission(ctx, rid, "req-1")
	if !perm.Decided() || *perm.DecidedOptionID != "allow" {
		t.Fatalf("duplicate request event reset the decision: %+v", perm)
	}
}

// TestIngestPermissionResolvedRecords: the resolved event stamps the resolved_*
// fields (first-writer-wins) and tolerates an unknown request_id silently.
func TestIngestPermissionResolvedRecords(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()
	rid, tok := makeApprovalRun(t, st, domain.StatusRunning)

	resp := ingest(t, ts.URL, rid, tok,
		permissionRequestEvent(1, "req-1"),
		map[string]any{"seq": 2, "type": "agent.permission_resolved", "payload": map[string]any{
			"request_id": "req-1", "option_id": "reject", "resolution": "timeout",
		}},
		// Orphan resolved (request never delivered): must not 500 the batch.
		map[string]any{"seq": 3, "type": "agent.permission_resolved", "payload": map[string]any{
			"request_id": "req-ghost", "option_id": "", "resolution": "timeout",
		}},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	perm, _ := st.GetRunPermission(ctx, rid, "req-1")
	if !perm.Resolved() || *perm.ResolvedOptionID != "reject" || *perm.Resolution != "timeout" {
		t.Fatalf("resolved fields not recorded: %+v", perm)
	}
}

// TestPermissionDecisionFourStates pins the decision endpoint's contract:
//   - UNKNOWN request_id → 204 (the F8a HARD constraint: pending, NEVER 404 —
//     a 404 would instantly convert a pending approval into a deny),
//   - pending (row exists, undecided) → 204,
//   - decided → 200 {"option_id"},
//   - resolved → 410,
//   - session finalizing / terminal run → 410 even for unknown ids.
func TestPermissionDecisionFourStates(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()
	rid, tok := makeApprovalRun(t, st, domain.StatusRunning)
	url := ts.URL + "/internal/v1/runs/" + rid + "/permissions/"

	// 1. Unknown request_id → 204 pending (hard constraint).
	resp := do(t, "GET", url+"req-unknown/decision", tok, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unknown request_id: status=%d want 204 (NEVER 404)", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Known but undecided → 204.
	r1 := ingest(t, ts.URL, rid, tok, permissionRequestEvent(1, "req-1"))
	r1.Body.Close()
	resp = do(t, "GET", url+"req-1/decision", tok, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("pending: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. Decided → 200 {"option_id"}.
	if _, won, err := st.DecideRunPermission(ctx, rid, "req-1", "allow", "", time.Now()); err != nil || !won {
		t.Fatalf("decide: won=%v err=%v", won, err)
	}
	resp = do(t, "GET", url+"req-1/decision", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decided: status=%d want 200", resp.StatusCode)
	}
	var body struct {
		OptionID string `json:"option_id"`
	}
	decode(t, resp, &body)
	if body.OptionID != "allow" {
		t.Fatalf("option_id=%q want allow", body.OptionID)
	}

	// 4. Resolved (runner already self-resolved, e.g. timeout) → 410.
	if err := st.ResolveRunPermission(ctx, rid, "req-1", "reject", "timeout", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp = do(t, "GET", url+"req-1/decision", tok, nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("resolved: status=%d want 410", resp.StatusCode)
	}
	resp.Body.Close()

	// 5. Session finalizing → 410 for ANY id (the run is winding down; polls
	// must stop instead of burning the timeout on 204s).
	if _, err := st.MarkSessionFinalizing(ctx, rid); err != nil {
		t.Fatal(err)
	}
	resp = do(t, "GET", url+"req-other/decision", tok, nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("finalizing: status=%d want 410", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestPermissionDecisionTerminalRun410: a terminal run's decision endpoint
// answers 410 (expired), not 204 — nothing will ever decide it.
func TestPermissionDecisionTerminalRun410(t *testing.T) {
	ts, st, _ := newTestServer(t)
	rid, tok := makeApprovalRun(t, st, domain.StatusSucceeded)
	_ = st
	resp := do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/permissions/req-x/decision", tok, nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("terminal run: status=%d want 410", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestPermissionResponseFlow: a member answers a pending request → 200 and the
// decision poll now returns it; a second answer → 409; an option not offered →
// 400; an unknown request → 404.
func TestPermissionResponseFlow(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()
	rid, tok := makeApprovalRun(t, st, domain.StatusRunning)
	r := ingest(t, ts.URL, rid, tok, permissionRequestEvent(1, "req-1"))
	r.Body.Close()
	url := ts.URL + "/api/v1/runs/" + rid + "/permission-response"

	// Unknown request_id → 404.
	resp := do(t, "POST", url, consoleToken, map[string]string{"request_id": "req-ghost", "option_id": "allow"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown request: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Option not in the offered set → 400.
	resp = do(t, "POST", url, consoleToken, map[string]string{"request_id": "req-1", "option_id": "sudo-everything"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign option: status=%d want 400", resp.StatusCode)
	}
	var e1 errorBody
	decode(t, resp, &e1)
	if e1.Error.Code != "invalid_option" {
		t.Fatalf("error code=%q want invalid_option", e1.Error.Code)
	}

	// Missing fields → 400.
	resp = do(t, "POST", url, consoleToken, map[string]string{"request_id": "req-1"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing option_id: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Valid answer → 200, row decided.
	resp = do(t, "POST", url, consoleToken, map[string]string{"request_id": "req-1", "option_id": "allow"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answer: status=%d want 200", resp.StatusCode)
	}
	var perm domain.RunPermission
	decode(t, resp, &perm)
	if !perm.Decided() || *perm.DecidedOptionID != "allow" {
		t.Fatalf("response row: %+v", perm)
	}

	// Second answer → 409 permission_already_resolved.
	resp = do(t, "POST", url, consoleToken, map[string]string{"request_id": "req-1", "option_id": "reject"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second answer: status=%d want 409", resp.StatusCode)
	}
	var e2 errorBody
	decode(t, resp, &e2)
	if e2.Error.Code != "permission_already_resolved" {
		t.Fatalf("error code=%q want permission_already_resolved", e2.Error.Code)
	}

	// A runner-resolved request (timeout self-deny) also 409s.
	r = ingest(t, ts.URL, rid, tok, permissionRequestEvent(2, "req-2"))
	r.Body.Close()
	if err := st.ResolveRunPermission(ctx, rid, "req-2", "reject", "timeout", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp = do(t, "POST", url, consoleToken, map[string]string{"request_id": "req-2", "option_id": "allow"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("answer on resolved: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestPermissionResponseRBAC: a viewer is 403'd (read-only role — the UI also
// disables the buttons); a member is allowed.
func TestPermissionResponseRBAC(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()
	rid, tok := makeApprovalRun(t, st, domain.StatusRunning)
	r := ingest(t, ts.URL, rid, tok, permissionRequestEvent(1, "req-1"))
	r.Body.Close()
	run, _ := st.GetRun(ctx, rid)
	url := ts.URL + "/api/v1/runs/" + rid + "/permission-response"

	// First user in the store becomes cluster-admin; burn that slot so the two
	// test principals below are plain users.
	seed := &domain.User{ID: domain.NewID(), DisplayName: "Seed", CreatedAt: time.Now()}
	if _, err := st.CreateUserWithIdentity(ctx, seed, &domain.UserIdentity{ID: domain.NewID(), UserID: seed.ID, Provider: domain.ProviderGitea, ProviderUID: "0", Username: "seed"}); err != nil {
		t.Fatal(err)
	}
	mkUser := func(uid, uname string, role domain.Role) string {
		u := &domain.User{ID: domain.NewID(), DisplayName: uname, CreatedAt: time.Now()}
		idn := &domain.UserIdentity{ID: domain.NewID(), UserID: u.ID, Provider: domain.ProviderGitea, ProviderUID: uid, Username: uname}
		if _, err := st.CreateUserWithIdentity(ctx, u, idn); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertMember(ctx, &domain.ProjectMember{ProjectID: run.ProjectID, UserID: u.ID, Role: role, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		sessTok, _ := auth.GenerateRunToken()
		if err := st.CreateSession(ctx, &domain.Session{ID: domain.NewID(), UserID: u.ID, TokenHash: auth.HashToken(sessTok), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return sessTok
	}
	viewerTok := mkUser("1", "vic", domain.RoleViewer)
	memberTok := mkUser("2", "mel", domain.RoleMember)

	resp := do(t, "POST", url, viewerTok, map[string]string{"request_id": "req-1", "option_id": "allow"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer answer: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, "POST", url, memberTok, map[string]string{"request_id": "req-1", "option_id": "allow"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member answer: status=%d want 200", resp.StatusCode)
	}
	var perm domain.RunPermission
	decode(t, resp, &perm)
	if perm.DecidedBy == nil {
		t.Fatal("decided_by not recorded for a user principal")
	}
}

// TestCreateRunPermissionMode: "approval" needs session:true (else 400); an
// unknown mode is 400; a valid approval session echoes the mode in the run
// JSON; retry preserves it.
func TestCreateRunPermissionMode(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/y.git",
		GitMode: domain.GitModeReadonly, DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	url := ts.URL + "/api/v1/services/" + svc.ID + "/runs"

	// approval without session → 400 (a single-shot has nobody to ask).
	resp := do(t, "POST", url, consoleToken, map[string]any{"prompt": "t", "permission_mode": "approval"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("approval without session: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown mode → 400.
	resp = do(t, "POST", url, consoleToken, map[string]any{"prompt": "t", "session": true, "permission_mode": "yolo"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown mode: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Valid: session + approval → 201, run echoes permission_mode.
	resp = do(t, "POST", url, consoleToken, map[string]any{"prompt": "t", "session": true, "permission_mode": "approval"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("valid approval session: status=%d want 201", resp.StatusCode)
	}
	var run domain.Run
	decode(t, resp, &run)
	if !run.Session || run.PermissionMode != domain.PermissionModeApproval {
		t.Fatalf("created run: session=%v permission_mode=%q", run.Session, run.PermissionMode)
	}

	// Retry of a finished approval session preserves the mode (run identity).
	if _, err := st.CancelRun(ctx, run.ID, "CanceledByOperator", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/retry", consoleToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry: status=%d want 201", resp.StatusCode)
	}
	var retry domain.Run
	decode(t, resp, &retry)
	if !retry.Session || retry.PermissionMode != domain.PermissionModeApproval {
		t.Fatalf("retry lost identity: session=%v permission_mode=%q", retry.Session, retry.PermissionMode)
	}

	// Default path unchanged: no permission_mode → "" (full_access).
	resp = do(t, "POST", url, consoleToken, map[string]any{"prompt": "t", "session": true})
	var plain domain.Run
	decode(t, resp, &plain)
	if plain.PermissionMode != "" {
		t.Fatalf("default permission_mode=%q want \"\"", plain.PermissionMode)
	}
}

// flakyPermStore wraps a Store and fails UpsertRunPermission a configured
// number of times — the transient-store-failure injection for the regression
// test below.
type flakyPermStore struct {
	store.Store
	failuresLeft int
}

func (f *flakyPermStore) UpsertRunPermission(ctx context.Context, p *domain.RunPermission) error {
	if f.failuresLeft > 0 {
		f.failuresLeft--
		return errFlakyUpsert
	}
	return f.Store.UpsertRunPermission(ctx, p)
}

var errFlakyUpsert = errors.New("injected transient upsert failure")

// TestIngestPermissionRequestUpsertBeforeAppend is the regression test for the
// upsert-vs-append ordering: the request upsert runs BEFORE AppendRunnerEvents
// and they are not one transaction, so a transient upsert failure must leave
// NOTHING persisted (500, no durable event, no ledger row). The runner's
// re-send of the SAME batch (same client seq) must then take the full
// append+publish path — live SSE subscribers see the permission_request event.
// Were the order reversed, the retry would hit the seq dedupe, publish nothing,
// and connected consoles would never render the pending card until a refresh.
func TestIngestPermissionRequestUpsertBeforeAppend(t *testing.T) {
	mem := store.NewMemStore()
	rid, tok := makeApprovalRun(t, mem, domain.StatusRunning)
	flaky := &flakyPermStore{Store: mem, failuresLeft: 1}

	hub := sse.NewHub()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(flaky, cfg, log, hub, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// A live SSE-style subscriber (what handleStream's live phase consumes).
	ch, unsub := hub.Subscribe(rid)
	defer unsub()

	ctx := context.Background()

	// First delivery: the injected upsert failure must 500 the batch and leave
	// nothing behind — no ledger row AND no durable event (the append must not
	// have run yet, or the retry below would be dedupe-swallowed).
	resp := ingest(t, ts.URL, rid, tok, permissionRequestEvent(1, "req-1"))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("flaky first ingest: status=%d want 500", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := mem.GetRunPermission(ctx, rid, "req-1"); err == nil {
		t.Fatal("ledger row persisted despite the failed batch")
	}
	evs, _ := mem.ListEvents(ctx, rid, 0, 100)
	for _, e := range evs {
		if e.Type == domain.EventPermissionRequest {
			t.Fatalf("durable event persisted despite the failed batch: %+v", e)
		}
	}

	// The runner re-sends the SAME batch (same client seq): full recovery — row
	// upserted, event appended, AND published to the live hub.
	resp = ingest(t, ts.URL, rid, tok, permissionRequestEvent(1, "req-1"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-sent ingest: status=%d want 200", resp.StatusCode)
	}
	var ack struct {
		Accepted int `json:"accepted"`
	}
	decode(t, resp, &ack)
	if ack.Accepted != 1 {
		t.Fatalf("re-sent ingest accepted=%d want 1 (append must not be dedupe-swallowed)", ack.Accepted)
	}
	if _, err := mem.GetRunPermission(ctx, rid, "req-1"); err != nil {
		t.Fatalf("ledger row missing after retry: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.Type != domain.EventPermissionRequest {
			t.Fatalf("hub published %q, want %q", ev.Type, domain.EventPermissionRequest)
		}
		if got, _ := ev.Payload["request_id"].(string); got != "req-1" {
			t.Fatalf("published request_id=%q want req-1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no live publish after the retried batch — SSE viewers would never see the pending card")
	}
}
