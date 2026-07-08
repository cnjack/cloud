package main

// session_test.go — tests for the multi-turn session loop (session.go),
// exercising it end to end: a real fake-ACP-agent subprocess (fakeagent_test.go)
// on one side, and an httptest mock of the (not-yet-built) F7b orchestrator
// endpoints (turn-complete / next-prompt) on the other.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAgentConfig returns a sessionConfig pointed at the current test binary,
// re-exec'd as a fake ACP agent (see fakeagent_test.go's TestMain hook).
// stopReasons controls the scripted stop reason per Prompt call (comma-joined
// into FAKE_AGENT_STOP_REASONS); logPath (may be "") captures the fake
// agent's NDJSON call log for assertions.
func fakeAgentConfig(t *testing.T, workspace, stopReasons, logPath string) sessionConfig {
	t.Helper()
	t.Setenv("ACPDRIVE_TEST_FAKE_AGENT", "1")
	t.Setenv("FAKE_AGENT_STOP_REASONS", stopReasons)
	t.Setenv("FAKE_AGENT_LOG", logPath)
	t.Setenv("FAKE_AGENT_SESSION_ID", "sess_1")

	return sessionConfig{
		AgentBin:  mustSelfExe(t),
		AgentArgs: nil, // no -test.* flags: TestMain intercepts before flag parsing
		Workspace: workspace,
		Prompt:    "first prompt",
		Verbose:   false,
		// Small failure budget for fast, deterministic tests; overridden per test.
		ControlPlaneLostAfter: 2 * time.Second,
		InitialBackoff:        5 * time.Millisecond,
		MaxBackoff:            20 * time.Millisecond,
		// Tiny 204→poll floor so multi-204 scripts stay fast; the floor's own
		// behavior (and its 250ms production default) is pinned by
		// TestSessionLoopNextPromptPollFloor / TestMinPollIntervalDefault.
		MinPollInterval: time.Millisecond,
	}
}

func mustSelfExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

// readNDJSON parses a fake-agent (or hook) NDJSON log file into a slice of
// generic maps, tolerating a not-yet-created file (empty result).
func readNDJSON(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode NDJSON line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// --- mock orchestrator (F7b session endpoints) ---

// turnCompleteCall records one turn-complete POST body.
type turnCompleteCall struct {
	Turn       int    `json:"turn"`
	StopReason string `json:"stop_reason"`
}

// mockOrchestrator is a scriptable httptest server for
// /internal/v1/runs/{id}/turn-complete and /internal/v1/runs/{id}/next-prompt.
type mockOrchestrator struct {
	mu sync.Mutex

	turnCompleteCalls []turnCompleteCall
	turnCompleteAuth  []string
	// turnCompleteStatus, if set, overrides the response status for the Nth
	// call (1-based); default 200.
	turnCompleteStatus func(n int) int

	nextPromptCalls int32
	// nextPromptScript is consumed in order; each entry is either a status
	// code (204/410/500/...) or, for 200, a nextPromptResponse to return. When
	// exhausted, the LAST entry repeats (so tests can script "N failures then
	// success" without enumerating every retry).
	nextPromptScript []nextPromptStep
	nextPromptAuth   []string

	// eventBatches records every batch POSTed to /internal/v1/runs/{id}/events
	// by the emitter (F9a: used to assert the run.session event payload).
	eventBatches [][]event
}

type nextPromptStep struct {
	status int
	body   nextPromptResponse
}

func newMockOrchestrator() *mockOrchestrator {
	return &mockOrchestrator{}
}

func (m *mockOrchestrator) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/runs/run-test/turn-complete", m.handleTurnComplete)
	mux.HandleFunc("/internal/v1/runs/run-test/next-prompt", m.handleNextPrompt)
	mux.HandleFunc("/internal/v1/runs/run-test/events", m.handleEvents)
	return httptest.NewServer(mux)
}

// handleEvents accepts the emitter's batched POST body ({"events":[...]})
// exactly like a real orchestrator ingest endpoint would, recording every
// batch so tests can assert on emitted events (e.g. run.session, F9a).
func (m *mockOrchestrator) handleEvents(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Events []event `json:"events"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.mu.Lock()
	m.eventBatches = append(m.eventBatches, body.Events)
	m.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// events flattens every recorded batch into one ordered slice.
func (m *mockOrchestrator) events() []event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []event
	for _, b := range m.eventBatches {
		out = append(out, b...)
	}
	return out
}

func (m *mockOrchestrator) handleTurnComplete(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.turnCompleteCalls) + 1
	m.turnCompleteAuth = append(m.turnCompleteAuth, r.Header.Get("Authorization"))
	var call turnCompleteCall
	_ = json.NewDecoder(r.Body).Decode(&call)
	m.turnCompleteCalls = append(m.turnCompleteCalls, call)

	status := http.StatusOK
	if m.turnCompleteStatus != nil {
		status = m.turnCompleteStatus(n)
	}
	w.WriteHeader(status)
}

func (m *mockOrchestrator) handleNextPrompt(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	n := atomic.AddInt32(&m.nextPromptCalls, 1)
	m.nextPromptAuth = append(m.nextPromptAuth, r.Header.Get("Authorization"))
	var step nextPromptStep
	if len(m.nextPromptScript) == 0 {
		step = nextPromptStep{status: http.StatusNoContent}
	} else if int(n) <= len(m.nextPromptScript) {
		step = m.nextPromptScript[n-1]
	} else {
		step = m.nextPromptScript[len(m.nextPromptScript)-1]
	}
	m.mu.Unlock()

	if step.status == http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(step.body)
		return
	}
	w.WriteHeader(step.status)
}

func (m *mockOrchestrator) calls() (turnComplete []turnCompleteCall, nextPrompt int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]turnCompleteCall{}, m.turnCompleteCalls...), atomic.LoadInt32(&m.nextPromptCalls)
}

// --- tests ---

// TestSessionLoopHappyPathMultipleTurns proves the core state machine: turn 1
// (using --prompt) completes, 204s a couple of times (poll again immediately,
// no backoff wait), a 200 delivers turn 2's prompt on the SAME session, turn 2
// completes, and a 410 ends the loop gracefully (nil error). The fake agent's
// own log proves session/new was called exactly once and both prompts landed
// on the same sessionId — the "never re-open a session" requirement.
func TestSessionLoopHappyPathMultipleTurns(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn,end_turn", agentLog)

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{
		{status: http.StatusNoContent},
		{status: http.StatusNoContent},
		{status: http.StatusOK, body: nextPromptResponse{MessageID: "m1", Prompt: "second prompt"}},
		{status: http.StatusGone},
	}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	tcCalls, npCalls := orch.calls()
	if len(tcCalls) != 2 {
		t.Fatalf("turn-complete calls = %+v, want 2", tcCalls)
	}
	if tcCalls[0].Turn != 1 || tcCalls[0].StopReason != "end_turn" {
		t.Fatalf("turn 1 turn-complete = %+v", tcCalls[0])
	}
	if tcCalls[1].Turn != 2 || tcCalls[1].StopReason != "end_turn" {
		t.Fatalf("turn 2 turn-complete = %+v", tcCalls[1])
	}
	if npCalls != 4 {
		t.Fatalf("next-prompt calls = %d, want 4 (204,204,200,410)", npCalls)
	}

	events := readNDJSON(t, agentLog)
	var newSessionCount int
	var prompts []map[string]any
	for _, e := range events {
		switch e["method"] {
		case "session/new":
			newSessionCount++
		case "session/prompt":
			prompts = append(prompts, e)
		}
	}
	if newSessionCount != 1 {
		t.Fatalf("session/new called %d times, want 1 (a session must never be re-opened)", newSessionCount)
	}
	if len(prompts) != 2 {
		t.Fatalf("session/prompt called %d times, want 2", len(prompts))
	}
	if prompts[0]["prompt"] != "first prompt" || prompts[1]["prompt"] != "second prompt" {
		t.Fatalf("prompts = %+v", prompts)
	}
	if prompts[0]["session_id"] != "sess_1" || prompts[1]["session_id"] != "sess_1" {
		t.Fatalf("prompts landed on different sessions: %+v", prompts)
	}
}

// --- F9a: session resume (--resume / RESUME_SESSION_ID, D23 ①②) ---

// TestSessionLoopNewSessionEmitsRunSessionEvent pins the "new session" half of
// the F9b ingest contract: after a plain session/new establishment, exactly
// one run.session event is emitted with resumed=false and the id session/new
// returned.
func TestSessionLoopNewSessionEmitsRunSessionEvent(t *testing.T) {
	dir := t.TempDir()
	cfg := fakeAgentConfig(t, dir, "end_turn", "")

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	// The event EMITTER (unlike the turn-complete/next-prompt control-plane
	// client) reads ORCH_BASE_URL/RUN_ID/RUN_TOKEN from the PROCESS environment
	// (NewEmitterFromEnv), not from cfg — set them too so run.session actually
	// ships to the mock server instead of being a silent no-op.
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	sessionEvents := filterEventType(orch.events(), eventRunSession)
	if len(sessionEvents) != 1 {
		t.Fatalf("run.session events = %d, want exactly 1: %+v", len(sessionEvents), sessionEvents)
	}
	ev := sessionEvents[0]
	if ev.Payload["acp_session_id"] != "sess_1" {
		t.Fatalf("run.session payload acp_session_id = %v, want sess_1", ev.Payload["acp_session_id"])
	}
	if resumed, _ := ev.Payload["resumed"].(bool); resumed {
		t.Fatalf("run.session payload resumed = %v, want false (session/new path)", ev.Payload["resumed"])
	}
}

// TestSessionLoopResumeSendsLoadSessionNotNewSession proves --resume/cfg.Resume
// skips session/new entirely, sends session/load carrying the given id and the
// workspace as cwd, reuses that SAME id for every session/prompt in the loop,
// and emits a run.session event with resumed=true.
func TestSessionLoopResumeSendsLoadSessionNotNewSession(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn,end_turn", agentLog)
	cfg.Resume = "sess_prior_warm_wake"

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{
		{status: http.StatusOK, body: nextPromptResponse{MessageID: "m1", Prompt: "second prompt"}},
		{status: http.StatusGone},
	}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	// See the comment in TestSessionLoopNewSessionEmitsRunSessionEvent: the
	// emitter needs these set as PROCESS env, separately from cfg.
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	events := readNDJSON(t, agentLog)
	var newSessionCount, loadSessionCount int
	var loadCall map[string]any
	var prompts []map[string]any
	for _, e := range events {
		switch e["method"] {
		case "session/new":
			newSessionCount++
		case "session/load":
			loadSessionCount++
			loadCall = e
		case "session/prompt":
			prompts = append(prompts, e)
		}
	}
	if newSessionCount != 0 {
		t.Fatalf("session/new called %d times, want 0 (resume must skip it)", newSessionCount)
	}
	if loadSessionCount != 1 {
		t.Fatalf("session/load called %d times, want 1", loadSessionCount)
	}
	if loadCall["session_id"] != "sess_prior_warm_wake" {
		t.Fatalf("session/load session_id = %v, want sess_prior_warm_wake", loadCall["session_id"])
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if loadCall["cwd"] != absDir && loadCall["cwd"] != dir {
		t.Fatalf("session/load cwd = %v, want the workspace (%v or %v)", loadCall["cwd"], absDir, dir)
	}
	if len(prompts) != 2 {
		t.Fatalf("session/prompt called %d times, want 2", len(prompts))
	}
	for i, p := range prompts {
		if p["session_id"] != "sess_prior_warm_wake" {
			t.Fatalf("prompt %d landed on session %v, want the resumed id sess_prior_warm_wake", i, p["session_id"])
		}
	}

	sessionEvents := filterEventType(orch.events(), eventRunSession)
	if len(sessionEvents) != 1 {
		t.Fatalf("run.session events = %d, want exactly 1: %+v", len(sessionEvents), sessionEvents)
	}
	ev := sessionEvents[0]
	if ev.Payload["acp_session_id"] != "sess_prior_warm_wake" {
		t.Fatalf("run.session payload acp_session_id = %v, want sess_prior_warm_wake", ev.Payload["acp_session_id"])
	}
	if resumed, _ := ev.Payload["resumed"].(bool); !resumed {
		t.Fatalf("run.session payload resumed = %v, want true (session/load path)", ev.Payload["resumed"])
	}
}

// TestSessionLoopResumeDropsReplayedTranscript is CONFIRMED-2's session-mode
// regression: session/load replays the prior transcript via session/update
// before it returns (ACP spec — the fake agent mimics jcode here); acpdrive
// must DROP those replayed notifications (the control plane already holds
// that history, D13) while still emitting the LIVE updates of every real
// turn after the gate opens.
func TestSessionLoopResumeDropsReplayedTranscript(t *testing.T) {
	dir := t.TempDir()
	cfg := fakeAgentConfig(t, dir, "end_turn,end_turn", "")
	cfg.Resume = "sess_warm_wake"
	t.Setenv("FAKE_AGENT_REPLAY_TEXTS", "REPLAYED-old-1,REPLAYED-old-2")
	t.Setenv("FAKE_AGENT_LIVE_TEXT", "LIVE")

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{
		{status: http.StatusOK, body: nextPromptResponse{MessageID: "m1", Prompt: "second prompt"}},
		{status: http.StatusGone},
	}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	// Live emitter (see TestSessionLoopNewSessionEmitsRunSessionEvent).
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	evs := orch.events()
	for _, ev := range evs {
		if txt, _ := ev.Payload["text"].(string); strings.HasPrefix(txt, "REPLAYED") {
			t.Fatalf("replayed transcript leaked into the run timeline: %+v", ev)
		}
	}
	// Both real turns' live updates must have flowed (gate open after load).
	texts := filterEventType(evs, eventAgentText)
	if len(texts) != 2 || texts[0].Payload["text"] != "LIVE:1" || texts[1].Payload["text"] != "LIVE:2" {
		t.Fatalf("live agent.text events = %+v, want exactly [LIVE:1 LIVE:2] (replay dropped, live kept)", texts)
	}
	sessionEvents := filterEventType(evs, eventRunSession)
	if len(sessionEvents) != 1 {
		t.Fatalf("run.session events = %d, want exactly 1: %+v", len(sessionEvents), sessionEvents)
	}
	if resumed, _ := sessionEvents[0].Payload["resumed"].(bool); !resumed {
		t.Fatalf("resumed = %v, want true", sessionEvents[0].Payload["resumed"])
	}
}

// TestSessionLoopResumeFailureIsFailVisible proves a failed session/load
// (unknown id / corrupt transcript) is fail-visible per the F9a contract: the
// loop never starts (turn-complete/next-prompt are never called), runSession
// returns a descriptive "session resume failed: ..." error, and there is NO
// silent fallback to a fresh session (session/new is never called either).
func TestSessionLoopResumeFailureIsFailVisible(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.Resume = "sess_corrupt"
	t.Setenv("FAKE_AGENT_LOAD_SESSION_ERR", "transcript checksum mismatch")

	orch := newMockOrchestrator()
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	// A LIVE emitter (see the comment above): proves the "no event on failure"
	// assertion below isn't trivially true because the emitter was nil.
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runSession(ctx, cfg)
	if err == nil {
		t.Fatal("runSession: want an error when session/load fails, got nil")
	}
	if !strings.Contains(err.Error(), "session resume failed") {
		t.Fatalf("error = %v, want it to mention 'session resume failed'", err)
	}
	if !strings.Contains(err.Error(), "transcript checksum mismatch") {
		t.Fatalf("error = %v, want it to carry the underlying agent message", err)
	}

	tcCalls, npCalls := orch.calls()
	if len(tcCalls) != 0 || npCalls != 0 {
		t.Fatalf("turn-complete/next-prompt must never be called after a failed resume: tc=%+v np=%d", tcCalls, npCalls)
	}
	if len(orch.events()) != 0 {
		t.Fatalf("no run.session (or any) event should be emitted for a failed resume: %+v", orch.events())
	}

	events := readNDJSON(t, agentLog)
	for _, e := range events {
		if e["method"] == "session/new" {
			t.Fatal("session/new was called after a failed resume — silent fallback to a new session is forbidden (fail-visible red line)")
		}
		if e["method"] == "session/prompt" {
			t.Fatal("session/prompt was called after a failed resume — the loop must never start")
		}
	}
}

// filterEventType returns the subset of evs whose Type matches typ, in order.
func filterEventType(evs []event, typ string) []event {
	var out []event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// TestSessionLoopTurnCompleteRetries503ThenSucceeds proves turn-complete
// retries network/5xx failures with backoff before succeeding, mirroring the
// emitter's retry philosophy.
func TestSessionLoopTurnCompleteRetries503ThenSucceeds(t *testing.T) {
	dir := t.TempDir()
	cfg := fakeAgentConfig(t, dir, "end_turn", "")

	orch := newMockOrchestrator()
	orch.turnCompleteStatus = func(n int) int {
		if n < 3 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}
	// The mock records every ATTEMPT (503,503,200), proving the client
	// actually retried rather than giving up after the first 503.
	tcCalls, _ := orch.calls()
	if len(tcCalls) != 3 {
		t.Fatalf("turn-complete attempts recorded = %d, want 3 (503,503,200)", len(tcCalls))
	}
	for _, c := range tcCalls {
		if c.Turn != 1 || c.StopReason != "end_turn" {
			t.Fatalf("retried attempt body = %+v, want the same {turn:1,stop_reason:end_turn} each time", c)
		}
	}
}

// TestSessionLoopNextPromptRetriesThenDelivers proves next-prompt retries a
// transient 500 before a later poll delivers 204/200 as scripted.
func TestSessionLoopNextPromptRetriesThenDelivers(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn,end_turn", agentLog)

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{
		{status: http.StatusInternalServerError},
		{status: http.StatusInternalServerError},
		{status: http.StatusOK, body: nextPromptResponse{MessageID: "m1", Prompt: "second prompt"}},
		{status: http.StatusGone},
	}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}
	_, npCalls := orch.calls()
	if npCalls != 4 {
		t.Fatalf("next-prompt calls = %d, want 4 (500,500,200,410)", npCalls)
	}
}

// TestSessionLoopControlPlaneLost proves that a next-prompt poll which NEVER
// succeeds is abandoned once the (test-shortened) failure budget elapses,
// returning a descriptive "control plane lost" error rather than retrying
// forever.
func TestSessionLoopControlPlaneLost(t *testing.T) {
	dir := t.TempDir()
	cfg := fakeAgentConfig(t, dir, "end_turn", "")
	cfg.ControlPlaneLostAfter = 150 * time.Millisecond
	cfg.InitialBackoff = 10 * time.Millisecond
	cfg.MaxBackoff = 20 * time.Millisecond

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusInternalServerError}} // always fails
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := runSession(ctx, cfg)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("runSession: want an error (control plane lost), got nil")
	}
	if !strings.Contains(err.Error(), "control plane unreachable") {
		t.Fatalf("error = %v, want it to mention control plane unreachable", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("runSession took %s to give up, want well under a few seconds (lostAfter=150ms)", elapsed)
	}
}

// TestSessionLoopTurnCompletePermanentFailureIsFatal proves a non-retryable
// 4xx from turn-complete fails the run immediately (no retry storm).
func TestSessionLoopTurnCompletePermanentFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	cfg := fakeAgentConfig(t, dir, "end_turn", "")

	orch := newMockOrchestrator()
	orch.turnCompleteStatus = func(int) int { return http.StatusBadRequest }
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err := runSession(ctx, cfg)
	if err == nil {
		t.Fatal("runSession: want an error for a 400 turn-complete response")
	}
	if !strings.Contains(err.Error(), "non-retryable") {
		t.Fatalf("error = %v, want it to mention non-retryable", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("a non-retryable 400 should fail fast, took %s", time.Since(start))
	}
}

// TestSessionLoopTurnHookRunsWithContractEnv proves the turn-hook subprocess
// is invoked synchronously after each turn with TURN_INDEX/ACP_SESSION_ID/
// ACP_STOP_REASON set, and that the loop waits for it (a sleeping hook delays
// the subsequent turn-complete POST).
func TestSessionLoopTurnHookRunsWithContractEnv(t *testing.T) {
	dir := t.TempDir()
	hookLog := filepath.Join(dir, "hook.ndjson")
	hookScript := writeHookScript(t, dir, fmt.Sprintf(`#!/bin/sh
printf '{"turn_index":"%%s","session_id":"%%s","stop_reason":"%%s"}\n' "$TURN_INDEX" "$ACP_SESSION_ID" "$ACP_STOP_REASON" >> %s
exit 0
`, shellQuote(hookLog)))

	cfg := fakeAgentConfig(t, dir, "end_turn,max_turn_requests", "")
	cfg.TurnHook = hookScript

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{
		{status: http.StatusOK, body: nextPromptResponse{MessageID: "m1", Prompt: "second prompt"}},
		{status: http.StatusGone},
	}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	events := readNDJSON(t, hookLog)
	if len(events) != 2 {
		t.Fatalf("turn-hook ran %d times, want 2 (once per turn): %+v", len(events), events)
	}
	if events[0]["turn_index"] != "1" || events[0]["session_id"] != "sess_1" || events[0]["stop_reason"] != "end_turn" {
		t.Fatalf("turn 1 hook env = %+v", events[0])
	}
	if events[1]["turn_index"] != "2" || events[1]["stop_reason"] != "max_turn_requests" {
		t.Fatalf("turn 2 hook env = %+v", events[1])
	}
}

// TestSessionLoopTurnHookFailureIsFatal proves a non-zero turn-hook exit
// aborts the whole run: turn-complete/next-prompt for that turn must never be
// called, and runSession must return a descriptive error.
func TestSessionLoopTurnHookFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	hookScript := writeHookScript(t, dir, "#!/bin/sh\nexit 7\n")

	cfg := fakeAgentConfig(t, dir, "end_turn", "")
	cfg.TurnHook = hookScript

	orch := newMockOrchestrator()
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runSession(ctx, cfg)
	if err == nil {
		t.Fatal("runSession: want an error when the turn-hook exits non-zero")
	}
	if !strings.Contains(err.Error(), "turn-hook failed") {
		t.Fatalf("error = %v, want it to mention turn-hook failed", err)
	}
	tcCalls, npCalls := orch.calls()
	if len(tcCalls) != 0 || npCalls != 0 {
		t.Fatalf("turn-complete/next-prompt must not be called after a fatal hook failure: tc=%+v np=%d", tcCalls, npCalls)
	}
}

// TestSessionLoopAuthHeader proves both endpoints receive Bearer RUN_TOKEN.
func TestSessionLoopAuthHeader(t *testing.T) {
	dir := t.TempDir()
	cfg := fakeAgentConfig(t, dir, "end_turn", "")

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "s3cr3t"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}
	if len(orch.turnCompleteAuth) != 1 || orch.turnCompleteAuth[0] != "Bearer s3cr3t" {
		t.Fatalf("turn-complete auth = %v, want [Bearer s3cr3t]", orch.turnCompleteAuth)
	}
	for _, a := range orch.nextPromptAuth {
		if a != "Bearer s3cr3t" {
			t.Fatalf("next-prompt auth = %v, want all Bearer s3cr3t", orch.nextPromptAuth)
		}
	}
}

// TestSessionLoopNextPromptPollFloor proves the 204→re-poll path enforces a
// minimum interval between polls (P2 hardening): a server that answers 204
// IMMEDIATELY (i.e. does not long-poll as contracted) must not be hammered in
// a hot loop. With a 60ms floor and 5 scripted 204s, the elapsed wall time for
// the poll phase must be at least ~5*60ms.
func TestSessionLoopNextPromptPollFloor(t *testing.T) {
	dir := t.TempDir()
	cfg := fakeAgentConfig(t, dir, "end_turn", "")
	const floor = 60 * time.Millisecond
	cfg.MinPollInterval = floor

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{
		{status: http.StatusNoContent},
		{status: http.StatusNoContent},
		{status: http.StatusNoContent},
		{status: http.StatusNoContent},
		{status: http.StatusNoContent},
		{status: http.StatusGone},
	}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}
	elapsed := time.Since(start)

	_, npCalls := orch.calls()
	if npCalls != 6 {
		t.Fatalf("next-prompt calls = %d, want 6 (5x204 + 410)", npCalls)
	}
	// 5 x 204 → 5 floor sleeps before the 410 poll. Allow generous slop below
	// (timers can only overshoot, never undershoot, but leave margin for the
	// non-poll portions being counted in start/elapsed).
	if minWant := 5 * floor; elapsed < minWant {
		t.Fatalf("poll loop took %s for five 204s, want >= %s (floor=%s not enforced — hot loop)", elapsed, minWant, floor)
	}
}

// TestMinPollIntervalDefault pins the production default of the 204 poll
// floor: an unset MinPollInterval must fall back to the package-level 250ms,
// not zero (zero would silently reintroduce the hot loop).
func TestMinPollIntervalDefault(t *testing.T) {
	var cfg sessionConfig
	if got := cfg.minPollInterval(); got != 250*time.Millisecond {
		t.Fatalf("default minPollInterval = %s, want 250ms", got)
	}
	cfg.MinPollInterval = 5 * time.Millisecond
	if got := cfg.minPollInterval(); got != 5*time.Millisecond {
		t.Fatalf("explicit minPollInterval = %s, want 5ms", got)
	}
}

// --- non-session regression: RUN_SESSION unset must not enable session mode ---

// TestSessionFlagDefaultsFalseWithoutEnv proves the --session flag's env
// fallback (RUN_SESSION) only enables session mode when explicitly "1" — the
// hard backward-compat requirement that single-shot behavior is unchanged
// when the env var is absent. main()'s own run() call path is exercised by
// the pre-existing emitter/mapper tests (unmodified by this feature); this
// test pins the flag-default predicate itself.
func TestSessionFlagDefaultsFalseWithoutEnv(t *testing.T) {
	t.Setenv("RUN_SESSION", "")
	if got := os.Getenv("RUN_SESSION") == "1"; got {
		t.Fatalf("RUN_SESSION=%q must not enable session mode", os.Getenv("RUN_SESSION"))
	}
	t.Setenv("RUN_SESSION", "1")
	if got := os.Getenv("RUN_SESSION") == "1"; !got {
		t.Fatal("RUN_SESSION=1 must enable session mode")
	}
}

// --- helpers ---

func writeHookScript(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}
	// Sanity-check the script is actually executable via /bin/sh on this host
	// (guards against a bad shebang silently no-op'ing the whole test).
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
