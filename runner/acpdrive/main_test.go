package main

// main_test.go — tests for flag parsing (parseFlags) and the single-shot
// run() path (main.go), focused on F9a's --resume support. run() had no
// dedicated test file before F9a (it was only exercised end to end by the
// docker-based test.sh / test-integration.sh); these tests reuse the same
// fake-ACP-agent re-exec harness as session_test.go (mustSelfExe,
// fakeagent_test.go's TestMain hook) to exercise run()'s actual
// conn.Initialize/LoadSession/NewSession/Prompt sequence without a real
// jcode binary.

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runFakeAgentEnv wires the re-exec'd fake agent (fakeagent_test.go) via env
// vars, mirroring session_test.go's fakeAgentConfig but for the single-shot
// run() call (which takes plain arguments, not a sessionConfig).
func runFakeAgentEnv(t *testing.T, stopReasons, logPath string) {
	t.Helper()
	t.Setenv("ACPDRIVE_TEST_FAKE_AGENT", "1")
	t.Setenv("FAKE_AGENT_STOP_REASONS", stopReasons)
	t.Setenv("FAKE_AGENT_LOG", logPath)
	t.Setenv("FAKE_AGENT_SESSION_ID", "sess_1")
}

// TestResumeFlagHasNoEnvFallback pins CONFIRMED-1's root-cause fix at the
// flag layer: --resume must NOT default from RESUME_SESSION_ID (or any env).
// A stale id in the pod env would otherwise leak into single-shot runs —
// whose session store the entrypoint just scrubbed — and hard-fail them on a
// doomed session/load. RESUME_SESSION_ID is consumed by entrypoint.sh, which
// passes --resume explicitly, and only in session mode.
func TestResumeFlagHasNoEnvFallback(t *testing.T) {
	t.Setenv("RESUME_SESSION_ID", "sess_stale_from_pod_env")
	o, err := parseFlags([]string{"--workspace", "/w", "--prompt", "p"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if o.resume != "" {
		t.Fatalf("resume = %q with only RESUME_SESSION_ID in the env, want \"\" (no env fallback)", o.resume)
	}
	// The explicit flag still works, and wins regardless of the env.
	o2, err := parseFlags([]string{"--workspace", "/w", "--prompt", "p", "--resume", "sess_explicit"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if o2.resume != "sess_explicit" {
		t.Fatalf("resume = %q, want sess_explicit (explicit flag)", o2.resume)
	}
}

// TestRunSingleShotIgnoresResumeEnvVar is CONFIRMED-1's end-to-end
// regression: a single-shot run in a pod whose env carries a stale
// RESUME_SESSION_ID (entrypoint does not pass --resume when SESSION_MODE=0)
// must go through the REAL flag parsing to a plain session/new and SUCCEED —
// never a session/load against the freshly scrubbed session store.
func TestRunSingleShotIgnoresResumeEnvVar(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	runFakeAgentEnv(t, "end_turn", agentLog)
	t.Setenv("RESUME_SESSION_ID", "sess_stale_from_pod_env")

	// Real flag parsing, exactly the argv entrypoint.sh builds for a
	// single-shot run (no --resume).
	o, err := parseFlags([]string{"--workspace", dir, "--prompt", "hello"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, mustSelfExe(t), nil, o.workspace, o.prompt, o.resume, false); err != nil {
		t.Fatalf("run: %v (a stale RESUME_SESSION_ID env var must not fail a single-shot run)", err)
	}

	events := readNDJSON(t, agentLog)
	var newSessionCount, loadSessionCount int
	for _, e := range events {
		switch e["method"] {
		case "session/new":
			newSessionCount++
		case "session/load":
			loadSessionCount++
		}
	}
	if newSessionCount != 1 {
		t.Fatalf("session/new called %d times, want 1", newSessionCount)
	}
	if loadSessionCount != 0 {
		t.Fatalf("session/load called %d times, want 0 — the stale RESUME_SESSION_ID env leaked into the run", loadSessionCount)
	}
}

// TestRunSingleShotEmitsNoRunSessionEvent pins run()'s (single-shot,
// no --resume) baseline event stream as bit-for-bit identical to pre-F9a
// (PLAUSIBLE-3): NO run.session event. The fake agent's live text proves the
// emitter pipeline was actually flowing (the absence is a decision, not a
// dead emitter), and that the live gate is open for real turn streaming on
// the session/new path.
func TestRunSingleShotEmitsNoRunSessionEvent(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	runFakeAgentEnv(t, "end_turn", agentLog)
	t.Setenv("FAKE_AGENT_LIVE_TEXT", "LIVE")

	cs := &captureServer{}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, mustSelfExe(t), nil, dir, "hello", "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	// run()'s deferred emitter.Close() drained the queue before returning, so
	// the capture below is complete — safe to assert absence.
	evs := cs.allEvents()
	if got := filterEventType(evs, eventRunSession); len(got) != 0 {
		t.Fatalf("run.session events = %+v, want NONE for a plain single-shot run (bit-for-bit pre-F9a event stream)", got)
	}
	// The stream must contain exactly the live agent.text — nothing added,
	// nothing dropped.
	texts := filterEventType(evs, eventAgentText)
	if len(texts) != 1 || texts[0].Payload["text"] != "LIVE:1" {
		t.Fatalf("agent.text events = %+v, want exactly [LIVE:1]", texts)
	}
	if len(evs) != 1 {
		t.Fatalf("total events = %+v, want exactly the one live agent.text", evs)
	}
}

// TestRunResumeDropsReplayedTranscript is CONFIRMED-2's single-shot
// regression: jcode (per the ACP spec) replays the loaded transcript through
// session/update BEFORE session/load returns; those replayed notifications
// must be dropped (the control plane already holds that history, D13), while
// live updates from the real turn after the load must still flow.
func TestRunResumeDropsReplayedTranscript(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	runFakeAgentEnv(t, "end_turn", agentLog)
	t.Setenv("FAKE_AGENT_REPLAY_TEXTS", "REPLAYED-old-1,REPLAYED-old-2")
	t.Setenv("FAKE_AGENT_LIVE_TEXT", "LIVE")

	cs := &captureServer{}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, mustSelfExe(t), nil, dir, "continue please", "sess_warm", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	evs := cs.allEvents()
	for _, ev := range evs {
		if txt, _ := ev.Payload["text"].(string); strings.HasPrefix(txt, "REPLAYED") {
			t.Fatalf("replayed transcript leaked into the run timeline: %+v", ev)
		}
	}
	texts := filterEventType(evs, eventAgentText)
	if len(texts) != 1 || texts[0].Payload["text"] != "LIVE:1" {
		t.Fatalf("live agent.text events = %+v, want exactly [LIVE:1] (replay dropped, live kept)", texts)
	}
	sessionEvents := filterEventType(evs, eventRunSession)
	if len(sessionEvents) != 1 {
		t.Fatalf("run.session events = %d, want exactly 1: %+v", len(sessionEvents), sessionEvents)
	}
	if resumed, _ := sessionEvents[0].Payload["resumed"].(bool); !resumed {
		t.Fatalf("resumed = %v, want true", sessionEvents[0].Payload["resumed"])
	}
}

// TestRunResumeUsesLoadSessionNotNewSession proves --resume (passed as run()'s
// resume parameter) skips session/new, sends session/load with the given id
// and workspace as cwd, sends session/prompt on that SAME id, and emits
// run.session{resumed:true}.
func TestRunResumeUsesLoadSessionNotNewSession(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	runFakeAgentEnv(t, "end_turn", agentLog)

	cs := &captureServer{}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, mustSelfExe(t), nil, dir, "continue please", "sess_from_prior_run", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	events := readNDJSON(t, agentLog)
	var newSessionCount, loadSessionCount int
	var loadCall, promptCall map[string]any
	for _, e := range events {
		switch e["method"] {
		case "session/new":
			newSessionCount++
		case "session/load":
			loadSessionCount++
			loadCall = e
		case "session/prompt":
			promptCall = e
		}
	}
	if newSessionCount != 0 {
		t.Fatalf("session/new called %d times, want 0 (resume must skip it)", newSessionCount)
	}
	if loadSessionCount != 1 {
		t.Fatalf("session/load called %d times, want 1", loadSessionCount)
	}
	if loadCall["session_id"] != "sess_from_prior_run" {
		t.Fatalf("session/load session_id = %v, want sess_from_prior_run", loadCall["session_id"])
	}
	if loadCall["cwd"] != dir {
		t.Fatalf("session/load cwd = %v, want %v", loadCall["cwd"], dir)
	}
	if promptCall == nil {
		t.Fatal("session/prompt was never called")
	}
	if promptCall["session_id"] != "sess_from_prior_run" {
		t.Fatalf("session/prompt session_id = %v, want the resumed id sess_from_prior_run", promptCall["session_id"])
	}
	if promptCall["prompt"] != "continue please" {
		t.Fatalf("session/prompt prompt = %v, want %q", promptCall["prompt"], "continue please")
	}

	waitForEvents(t, cs, 1)
	sessionEvents := filterEventType(cs.allEvents(), eventRunSession)
	if len(sessionEvents) != 1 {
		t.Fatalf("run.session events = %d, want exactly 1: %+v", len(sessionEvents), sessionEvents)
	}
	if sessionEvents[0].Payload["acp_session_id"] != "sess_from_prior_run" {
		t.Fatalf("acp_session_id = %v, want sess_from_prior_run", sessionEvents[0].Payload["acp_session_id"])
	}
	if resumed, _ := sessionEvents[0].Payload["resumed"].(bool); !resumed {
		t.Fatalf("resumed = %v, want true", sessionEvents[0].Payload["resumed"])
	}
}

// TestRunResumeFailureIsFailVisible proves a failed session/load makes run()
// return a descriptive "session resume failed: ..." error WITHOUT ever
// calling session/prompt or session/new — no silent fallback to a fresh
// session (the fail-visible red line, see CLAUDE.md / D14).
func TestRunResumeFailureIsFailVisible(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	runFakeAgentEnv(t, "end_turn", agentLog)
	t.Setenv("FAKE_AGENT_LOAD_SESSION_ERR", "no such session on disk")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := run(ctx, mustSelfExe(t), nil, dir, "continue please", "sess_missing", false)
	if err == nil {
		t.Fatal("run: want an error when session/load fails, got nil")
	}
	if !strings.Contains(err.Error(), "session resume failed") {
		t.Fatalf("error = %v, want it to mention 'session resume failed'", err)
	}
	if !strings.Contains(err.Error(), "no such session on disk") {
		t.Fatalf("error = %v, want it to carry the underlying agent message", err)
	}

	events := readNDJSON(t, agentLog)
	for _, e := range events {
		if e["method"] == "session/new" {
			t.Fatal("session/new was called after a failed resume — silent fallback forbidden")
		}
		if e["method"] == "session/prompt" {
			t.Fatal("session/prompt was called after a failed resume — no prompt may be sent on a session that failed to load")
		}
	}
}

// waitForEvents polls cs (a captureServer) until at least n events have been
// captured or a short deadline elapses; the emitter's default flush is async
// (500ms ticker / batchMax), so a direct post-run() assertion can race it —
// but run()'s own emitter.Close() (deferred before the function returns)
// already blocks until the queue drains, so in practice this should return
// immediately. Kept as a small guard against flakiness rather than a hard
// synchronization requirement.
func waitForEvents(t *testing.T, cs *captureServer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(cs.allEvents()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
