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

// sessionTestServer builds a server with a FAST next-prompt hold so the
// long-poll tests do not sleep for 25s. Returns the *Server (to tweak timings),
// the httptest.Server, and the store.
func sessionTestServer(t *testing.T) (*Server, *httptest.Server, *store.MemStore) {
	t.Helper()
	st := store.NewMemStore()
	hub := sse.NewHub()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, hub, nil)
	srv.nextPromptHold = 300 * time.Millisecond
	srv.nextPromptPoll = 20 * time.Millisecond
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, st
}

// makeSessionRun creates a project+service+session run and drives it to `status`
// via the store mutators. Returns the run id and its RUN_TOKEN (for the internal
// endpoints). The run is agent-kind, session=true.
func makeSessionRun(t *testing.T, st *store.MemStore, status domain.RunStatus) (string, string) {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "o/r",
		GitMode: domain.GitModeDraftPR, DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "task",
		Status: domain.StatusQueued, Kind: domain.RunKindAgent, Session: true, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	if status == domain.StatusQueued {
		return run.ID, tok
	}
	if _, err := st.ScheduleRun(ctx, run.ID, "j", auth.HashToken(tok), "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if status == domain.StatusScheduling {
		return run.ID, tok
	}
	if _, err := st.MarkRunning(ctx, run.ID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	if status == domain.StatusRunning {
		return run.ID, tok
	}
	if status == domain.StatusAwaitingInput {
		if _, err := st.SetRunAwaitingInput(ctx, run.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		return run.ID, tok
	}
	if status == domain.StatusSucceeded {
		if _, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", time.Now()); err != nil {
			t.Fatal(err)
		}
		return run.ID, tok
	}
	t.Fatalf("unsupported status %q", status)
	return "", ""
}

// TestSendMessageRequiresSession: a NON-session run rejects a message with 409
// run_not_awaiting.
func TestSendMessageRequiresSession(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = st.CreateService(ctx, svc)
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "t", Status: domain.StatusRunning, Kind: domain.RunKindAgent, Attempt: 1, CreatedAt: time.Now()}
	_ = st.CreateRun(ctx, run)

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+run.ID+"/messages", consoleToken, map[string]string{"prompt": "hi"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("non-session message: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "run_not_awaiting" {
		t.Fatalf("error code=%q want run_not_awaiting", body.Error.Code)
	}
}

// TestSendMessageWrongStatus: a session run in a non-{running,awaiting_input}
// status (succeeded) rejects a message with 409.
func TestSendMessageWrongStatus(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	rid, _ := makeSessionRun(t, st, domain.StatusSucceeded)
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+rid+"/messages", consoleToken, map[string]string{"prompt": "hi"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("succeeded session message: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestSendMessagePermission: a viewer cannot post a message (403). We create a
// real user + viewer membership and a session token so the principal is a
// viewer (not the cluster-admin console token).
func TestSendMessagePermission(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, _ := makeSessionRun(t, st, domain.StatusAwaitingInput)
	run, _ := st.GetRun(ctx, rid)

	// The FIRST user in the store becomes cluster-admin; create a throwaway one so
	// our viewer is a plain (non-admin) user.
	seed := &domain.User{ID: domain.NewID(), DisplayName: "Seed", CreatedAt: time.Now()}
	if _, err := st.CreateUserWithIdentity(ctx, seed, &domain.UserIdentity{ID: domain.NewID(), UserID: seed.ID, Provider: domain.ProviderGitea, ProviderUID: "0", Username: "seed"}); err != nil {
		t.Fatal(err)
	}
	// A viewer user with a live session token.
	u := &domain.User{ID: domain.NewID(), DisplayName: "Vic", CreatedAt: time.Now()}
	idn := &domain.UserIdentity{ID: domain.NewID(), UserID: u.ID, Provider: domain.ProviderGitea, ProviderUID: "1", Username: "vic"}
	if _, err := st.CreateUserWithIdentity(ctx, u, idn); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMember(ctx, &domain.ProjectMember{ProjectID: run.ProjectID, UserID: u.ID, Role: domain.RoleViewer, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sessTok, _ := auth.GenerateRunToken()
	if err := st.CreateSession(ctx, &domain.Session{ID: domain.NewID(), UserID: u.ID, TokenHash: auth.HashToken(sessTok), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+rid+"/messages", sessTok, map[string]string{"prompt": "hi"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer message: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestSendMessageQueuesAndEmitsEvent: a valid message on an awaiting_input
// session run is queued AND appended as a user.message timeline event.
func TestSendMessageQueuesAndEmitsEvent(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, _ := makeSessionRun(t, st, domain.StatusAwaitingInput)

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+rid+"/messages", consoleToken, map[string]string{"prompt": "  do more  "})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("message: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()

	msgs, _ := st.ListRunMessages(ctx, rid)
	if len(msgs) != 1 || msgs[0].Prompt != "do more" || msgs[0].OfferedAt != nil || msgs[0].ConsumedAt != nil {
		t.Fatalf("queued messages = %+v", msgs)
	}
	// user.message event present.
	evs, _ := st.ListEvents(ctx, rid, 0, 100)
	found := false
	for _, e := range evs {
		if e.Type == domain.EventUserMessage && e.Payload["prompt"] == "do more" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no user.message event in %+v", evs)
	}
}

// TestNextPromptDelivers (C1 full chain, incl. lost-response re-delivery): a
// pending message is OFFERED (200 + resume to running); a re-poll BEFORE the
// next turn-complete re-delivers the SAME message verbatim (the previous
// response was lost); turn-complete CONSUMES it; only then does the next poll
// move on (here: 204, nothing left).
func TestNextPromptDelivers(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusAwaitingInput)
	if _, err := st.AppendRunMessage(ctx, rid, "next turn", ""); err != nil {
		t.Fatal(err)
	}

	resp := do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("next-prompt: status=%d want 200", resp.StatusCode)
	}
	var got nextPromptResp
	decode(t, resp, &got)
	if got.Prompt != "next turn" || got.MessageID == "" {
		t.Fatalf("next-prompt body=%+v", got)
	}
	// Run resumed to running; message offered but NOT yet consumed.
	run, _ := st.GetRun(ctx, rid)
	if run.Status != domain.StatusRunning {
		t.Fatalf("run status=%q want running", run.Status)
	}
	msgs, _ := st.ListRunMessages(ctx, rid)
	if msgs[0].OfferedAt == nil || msgs[0].ConsumedAt != nil {
		t.Fatalf("after offer: offered=%v consumed=%v want offered/!consumed", msgs[0].OfferedAt, msgs[0].ConsumedAt)
	}

	// Lost response: the runner re-polls WITHOUT a turn-complete → the SAME
	// message is re-delivered verbatim (same id), immediately (no hold).
	resp = do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-poll: status=%d want 200 (idempotent re-delivery)", resp.StatusCode)
	}
	var again nextPromptResp
	decode(t, resp, &again)
	if again.MessageID != got.MessageID || again.Prompt != got.Prompt {
		t.Fatalf("re-poll delivered a DIFFERENT message: %+v vs %+v", again, got)
	}

	// The turn runs and completes → the offered message is consumed.
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+rid+"/turn-complete", tok, map[string]any{"turn": 2, "stop_reason": "end_turn"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn-complete: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	msgs, _ = st.ListRunMessages(ctx, rid)
	if msgs[0].ConsumedAt == nil {
		t.Fatal("turn-complete did not consume the offered message")
	}

	// Nothing left: the next poll holds then 204 — the consumed message is never
	// re-delivered.
	resp = do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("post-consume next-prompt: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestNextPromptConsumeThenNextMessage (C1): with TWO queued messages, the
// offered first message is pinned across re-polls until its turn-complete; only
// after the consume does the second message get offered.
func TestNextPromptConsumeThenNextMessage(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusAwaitingInput)
	if _, err := st.AppendRunMessage(ctx, rid, "first", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendRunMessage(ctx, rid, "second", ""); err != nil {
		t.Fatal(err)
	}

	// Offer + re-poll: both must be "first" (never "second").
	var m1 nextPromptResp
	resp := do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	decode(t, resp, &m1)
	resp = do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	var re nextPromptResp
	decode(t, resp, &re)
	if m1.Prompt != "first" || re.MessageID != m1.MessageID {
		t.Fatalf("offer/re-poll: %+v / %+v want the pinned 'first'", m1, re)
	}

	// turn-complete consumes "first" → next poll offers "second".
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+rid+"/turn-complete", tok, map[string]any{"turn": 2, "stop_reason": "end_turn"})
	resp.Body.Close()
	var m2 nextPromptResp
	resp = do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-consume poll: status=%d want 200", resp.StatusCode)
	}
	decode(t, resp, &m2)
	if m2.Prompt != "second" || m2.MessageID == m1.MessageID {
		t.Fatalf("post-consume poll delivered %+v, want 'second'", m2)
	}
}

// TestNextPromptHoldThen204: no message, the hold elapses, 204.
func TestNextPromptHoldThen204(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	rid, tok := makeSessionRun(t, st, domain.StatusAwaitingInput)
	start := time.Now()
	resp := do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("next-prompt (empty): status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("204 returned too fast (%s) — the server did not hold", time.Since(start))
	}
}

// TestNextPromptFinalized410: once the session is finalizing, next-prompt 410s.
func TestNextPromptFinalized410(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusAwaitingInput)
	if _, err := st.MarkSessionFinalizing(ctx, rid); err != nil {
		t.Fatal(err)
	}
	resp := do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("finalized next-prompt: status=%d want 410", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestTurnCompleteParksAwaitingInput: turn-complete moves a running session run
// to awaiting_input and is idempotent (a duplicate keeps awaiting_since).
func TestTurnCompleteParksAwaitingInput(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusRunning)

	resp := do(t, "POST", ts.URL+"/internal/v1/runs/"+rid+"/turn-complete", tok, map[string]any{"turn": 1, "stop_reason": "end_turn"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn-complete: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	run, _ := st.GetRun(ctx, rid)
	if run.Status != domain.StatusAwaitingInput || run.AwaitingSince == nil {
		t.Fatalf("after turn-complete: status=%q awaiting_since=%v", run.Status, run.AwaitingSince)
	}
	firstSince := *run.AwaitingSince

	// Duplicate turn-complete: still awaiting_input, awaiting_since unchanged.
	time.Sleep(5 * time.Millisecond)
	resp = do(t, "POST", ts.URL+"/internal/v1/runs/"+rid+"/turn-complete", tok, map[string]any{"turn": 1, "stop_reason": "end_turn"})
	resp.Body.Close()
	run, _ = st.GetRun(ctx, rid)
	if !run.AwaitingSince.Equal(firstSince) {
		t.Fatalf("duplicate turn-complete reset awaiting_since: %v != %v", run.AwaitingSince, firstSince)
	}
}

// runStatusChain returns the run.status values in seq order (the heal timeline).
func runStatusChain(t *testing.T, st *store.MemStore, rid string) []string {
	t.Helper()
	evs, _ := st.ListEvents(context.Background(), rid, 0, 1000)
	var out []string
	for _, e := range evs {
		if e.Type == domain.EventRunStatus {
			if s, ok := e.Payload["status"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// hasSubsequence reports whether want appears as an (not necessarily contiguous)
// ordered subsequence of got — the chain may carry extra states around it.
func hasSubsequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// TestTurnCompleteHealsFromScheduling is the F7b regression: a very fast first
// turn can POST turn-complete while the run is still `scheduling` (the reconciler
// has not observed the pod Running yet). Before the fix SetRunAwaitingInput hit
// ErrInvalidTransition and was silently dropped, hanging the session until TTL.
// Now the handler HEALS scheduling→running first, then parks — and the emitted
// timeline shows the real running→awaiting_input chain.
func TestTurnCompleteHealsFromScheduling(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusScheduling)

	resp := do(t, "POST", ts.URL+"/internal/v1/runs/"+rid+"/turn-complete", tok, map[string]any{"turn": 1, "stop_reason": "end_turn"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn-complete: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	run, _ := st.GetRun(ctx, rid)
	if run.Status != domain.StatusAwaitingInput || run.AwaitingSince == nil {
		t.Fatalf("after turn-complete from scheduling: status=%q awaiting_since=%v want awaiting_input", run.Status, run.AwaitingSince)
	}
	chain := runStatusChain(t, st, rid)
	if !hasSubsequence(chain, []string{"running", "awaiting_input"}) {
		t.Fatalf("status chain %v missing the healed running→awaiting_input transition", chain)
	}
}

// TestTurnCompleteHealsFromQueued exercises the full queued→scheduling→running→
// awaiting_input heal chain. A queued run is normally unreachable through the
// RUN_TOKEN gate (no token_hash yet), so we hand-build a queued run WITH a token
// hash to drive the defensive branch and confirm it still converges rather than
// hanging.
func TestTurnCompleteHealsFromQueued(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "o/r",
		GitMode: domain.GitModeDraftPR, DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.GenerateRunToken()
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "task",
		Status: domain.StatusQueued, TokenHash: auth.HashToken(tok), Kind: domain.RunKindAgent,
		Session: true, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	resp := do(t, "POST", ts.URL+"/internal/v1/runs/"+run.ID+"/turn-complete", tok, map[string]any{"turn": 1, "stop_reason": "end_turn"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn-complete: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	got, _ := st.GetRun(ctx, run.ID)
	if got.Status != domain.StatusAwaitingInput {
		t.Fatalf("after turn-complete from queued: status=%q want awaiting_input", got.Status)
	}
	// The token hash the run authenticated with must be intact (heal must never
	// clobber it via ScheduleRun).
	if got.TokenHash != auth.HashToken(tok) {
		t.Fatalf("heal clobbered token_hash: %q", got.TokenHash)
	}
	chain := runStatusChain(t, st, run.ID)
	if !hasSubsequence(chain, []string{"scheduling", "running", "awaiting_input"}) {
		t.Fatalf("status chain %v missing the full heal chain", chain)
	}
}

// TestTurnCompleteTerminalConcurrentIsVisibleNoOp: a turn-complete that races a
// concurrent terminal transition (cancel / dead pod) finds nothing to park. It
// must be a tolerated 200 no-op (not a 4xx/5xx that kills the runner), and it
// must NOT append a spurious awaiting_input — the run stays terminal.
func TestTurnCompleteTerminalConcurrentIsVisibleNoOp(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusSucceeded)

	resp := do(t, "POST", ts.URL+"/internal/v1/runs/"+rid+"/turn-complete", tok, map[string]any{"turn": 1, "stop_reason": "end_turn"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn-complete on terminal run: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	run, _ := st.GetRun(ctx, rid)
	if run.Status != domain.StatusSucceeded {
		t.Fatalf("terminal run moved: status=%q want succeeded", run.Status)
	}
	for _, s := range runStatusChain(t, st, rid) {
		if s == string(domain.StatusAwaitingInput) {
			t.Fatalf("turn-complete parked a terminal run: chain=%v", runStatusChain(t, st, rid))
		}
	}
}

// TestTurnCompleteReconcilerRacedRunningFirst is the interleaving where the
// reconciler WINS the scheduling→running race before the turn-complete arrives:
// the handler's heal is then a clean no-op (running is already the target) and
// the park succeeds. Either ordering of the concurrent MarkRunning converges to
// awaiting_input (the other ordering is TestTurnCompleteHealsFromScheduling).
func TestTurnCompleteReconcilerRacedRunningFirst(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusScheduling)
	// Simulate the reconciler tick landing MarkRunning first.
	if _, err := st.MarkRunning(ctx, rid, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}

	resp := do(t, "POST", ts.URL+"/internal/v1/runs/"+rid+"/turn-complete", tok, map[string]any{"turn": 1, "stop_reason": "end_turn"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn-complete: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	run, _ := st.GetRun(ctx, rid)
	if run.Status != domain.StatusAwaitingInput || run.AwaitingSince == nil {
		t.Fatalf("status=%q awaiting_since=%v want awaiting_input", run.Status, run.AwaitingSince)
	}
}

// TestNextPromptHealsFirstMessageFromScheduling: the SAME fast-turn race can hit
// the first next-prompt poll — a message queued while the run is still scheduling
// must be delivered AND heal the run to running (before the fix ResumeRun failed
// from scheduling and the run was left stuck below running while a turn ran).
func TestNextPromptHealsFirstMessageFromScheduling(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusScheduling)
	if _, err := st.AppendRunMessage(ctx, rid, "first prompt", ""); err != nil {
		t.Fatal(err)
	}

	resp := do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("next-prompt: status=%d want 200", resp.StatusCode)
	}
	var got nextPromptResp
	decode(t, resp, &got)
	if got.Prompt != "first prompt" {
		t.Fatalf("delivered %+v want 'first prompt'", got)
	}
	run, _ := st.GetRun(ctx, rid)
	if run.Status != domain.StatusRunning {
		t.Fatalf("after offer from scheduling: status=%q want running (healed)", run.Status)
	}
	if !hasSubsequence(runStatusChain(t, st, rid), []string{"running"}) {
		t.Fatalf("no running status emitted for the healed first turn: %v", runStatusChain(t, st, rid))
	}
}

// TestFinishSetsFinalize: finish sets the finalize flag (member+) so next-prompt
// then 410s; a repeat finish is idempotent.
func TestFinishSetsFinalize(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusAwaitingInput)

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+rid+"/finish", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("finish: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	run, _ := st.GetRun(ctx, rid)
	if !run.SessionFinalizing {
		t.Fatal("finish did not set session_finalizing")
	}
	// next-prompt now 410.
	resp = do(t, "GET", ts.URL+"/internal/v1/runs/"+rid+"/next-prompt", tok, nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("post-finish next-prompt: status=%d want 410", resp.StatusCode)
	}
	resp.Body.Close()

	// Idempotent repeat.
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+rid+"/finish", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repeat finish: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestBundleUpsertBumpsRevForSession: uploading a bundle for a session run stores
// it (upsert on re-upload) and bumps bundle_rev each time so the session-push
// pass re-pushes.
func TestBundleUpsertBumpsRevForSession(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, tok := makeSessionRun(t, st, domain.StatusRunning)

	resp := postRaw(t, ts.URL+"/internal/v1/runs/"+rid+"/bundle", tok, "application/octet-stream", []byte("bundle-v1"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bundle v1: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()
	run, _ := st.GetRun(ctx, rid)
	if run.BundleRev != 1 || run.GitBranch == "" {
		t.Fatalf("after v1: bundle_rev=%d git_branch=%q", run.BundleRev, run.GitBranch)
	}

	resp = postRaw(t, ts.URL+"/internal/v1/runs/"+rid+"/bundle", tok, "application/octet-stream", []byte("bundle-v2-bigger"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bundle v2: status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()
	run, _ = st.GetRun(ctx, rid)
	if run.BundleRev != 2 {
		t.Fatalf("after v2: bundle_rev=%d want 2", run.BundleRev)
	}
	got, _ := st.GetRunBundle(ctx, rid)
	if string(got) != "bundle-v2-bigger" {
		t.Fatalf("bundle not upserted: %q", got)
	}
}

// TestCreateRunWithSessionFlag: POST /services/{id}/runs {session:true} creates a
// session run; the default (no field) stays a single-shot run. A retry of a
// session run preserves session-ness (run identity).
func TestCreateRunWithSessionFlag(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/y.git", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = st.CreateService(ctx, svc)

	// session:true → run.session set.
	resp := do(t, "POST", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken,
		map[string]any{"prompt": "chat", "session": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session run: status=%d want 201", resp.StatusCode)
	}
	var created domain.Run
	decode(t, resp, &created)
	if !created.Session {
		t.Fatal("run.session not set from the create body")
	}

	// Omitted → single-shot.
	resp = do(t, "POST", ts.URL+"/api/v1/services/"+svc.ID+"/runs", consoleToken,
		map[string]any{"prompt": "one shot"})
	var plain domain.Run
	decode(t, resp, &plain)
	if plain.Session {
		t.Fatal("run.session must default to false")
	}

	// Retry of a (terminal) session run preserves session-ness.
	if _, err := st.ScheduleRun(ctx, created.ID, "j", "h", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, created.ID, "x", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, created.ID, "x", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp = do(t, "POST", ts.URL+"/api/v1/runs/"+created.ID+"/retry", consoleToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry session run: status=%d want 201", resp.StatusCode)
	}
	var retried domain.Run
	decode(t, resp, &retried)
	if !retried.Session {
		t.Fatal("retry of a session run must stay a session")
	}
}

// TestSendMessageAfterFinish409 (C2): a finalizing session must not accept new
// messages — they would never be processed (next-prompt answers 410). Typed 409
// run_finalizing; nothing lands on the queue or the timeline.
func TestSendMessageAfterFinish409(t *testing.T) {
	_, ts, st := sessionTestServer(t)
	ctx := context.Background()
	rid, _ := makeSessionRun(t, st, domain.StatusAwaitingInput)
	if _, err := st.MarkSessionFinalizing(ctx, rid); err != nil {
		t.Fatal(err)
	}

	resp := do(t, "POST", ts.URL+"/api/v1/runs/"+rid+"/messages", consoleToken, map[string]string{"prompt": "too late"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("finalizing message: status=%d want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "run_finalizing" {
		t.Fatalf("error code=%q want run_finalizing", body.Error.Code)
	}

	// Nothing queued, no user.message event.
	msgs, _ := st.ListRunMessages(ctx, rid)
	if len(msgs) != 0 {
		t.Fatalf("message queued despite finalizing: %+v", msgs)
	}
	evs, _ := st.ListEvents(ctx, rid, 0, 100)
	for _, e := range evs {
		if e.Type == domain.EventUserMessage {
			t.Fatalf("user.message event appended despite finalizing: %+v", e)
		}
	}
}
