package main

// permission_test.go — F8a tests: approval-mode session establishment
// (session/set_mode) and RequestPermission forwarding, exercised end to end
// against the fake ACP agent (fakeagent_test.go) and an httptest mock of the
// (not-yet-built) F8b orchestrator permission-decision endpoint (the
// mockOrchestrator extensions in session_test.go).

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- session/set_mode on establishment ---

// TestApprovalSessionSetsSessionModeAfterNewSession proves that
// PermissionMode: approval makes runSession issue session/set_mode(approval)
// for the freshly created session, AFTER session/new and BEFORE the first
// session/prompt — and that with no permission request raised, the run
// produces zero permission events (nothing to forward).
func TestApprovalSessionSetsSessionModeAfterNewSession(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	events := readNDJSON(t, agentLog)
	var order []string
	var setModeCall map[string]any
	for _, e := range events {
		method, _ := e["method"].(string)
		order = append(order, method)
		if method == "session/set_mode" {
			setModeCall = e
		}
	}
	if setModeCall == nil {
		t.Fatalf("session/set_mode was never called; agent log = %+v", events)
	}
	if setModeCall["session_id"] != "sess_1" {
		t.Fatalf("session/set_mode session_id = %v, want sess_1", setModeCall["session_id"])
	}
	if setModeCall["mode_id"] != "approval" {
		t.Fatalf("session/set_mode mode_id = %v, want approval", setModeCall["mode_id"])
	}
	assertBefore(t, order, "session/new", "session/set_mode")
	assertBefore(t, order, "session/set_mode", "session/prompt")

	if got := filterEventType(orch.events(), eventAgentPermissionRequest); len(got) != 0 {
		t.Fatalf("agent.permission_request events = %+v, want none (no permission was requested)", got)
	}
	if got := filterEventType(orch.events(), eventAgentPermissionResolved); len(got) != 0 {
		t.Fatalf("agent.permission_resolved events = %+v, want none (no permission was requested)", got)
	}
}

// TestApprovalSessionSetsSessionModeAfterResume proves the SAME set_mode
// contract holds on the --resume/session-load path (F8a's "两条路径都要处理
// resume" requirement): session/load happens first, then session/set_mode
// against the RESUMED session id, then session/prompt.
func TestApprovalSessionSetsSessionModeAfterResume(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	cfg.Resume = "sess_prior_warm_wake"

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	events := readNDJSON(t, agentLog)
	var order []string
	var setModeCall map[string]any
	for _, e := range events {
		method, _ := e["method"].(string)
		order = append(order, method)
		if method == "session/set_mode" {
			setModeCall = e
		}
	}
	if setModeCall == nil {
		t.Fatalf("session/set_mode was never called; agent log = %+v", events)
	}
	if setModeCall["session_id"] != "sess_prior_warm_wake" {
		t.Fatalf("session/set_mode session_id = %v, want sess_prior_warm_wake (the resumed id)", setModeCall["session_id"])
	}
	if setModeCall["mode_id"] != "approval" {
		t.Fatalf("session/set_mode mode_id = %v, want approval", setModeCall["mode_id"])
	}
	assertBefore(t, order, "session/load", "session/set_mode")
	assertBefore(t, order, "session/set_mode", "session/prompt")
}

// TestApprovalSetSessionModeFailureIsFailVisible proves a failed
// session/set_mode call fails the whole run (fail-visible: never silently
// continuing in full_access despite an explicit RUN_PERMISSION_MODE=approval
// request), and that the turn loop never starts.
func TestApprovalSetSessionModeFailureIsFailVisible(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	t.Setenv("FAKE_AGENT_SET_SESSION_MODE_ERR", "jcode does not support approval mode")

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
		t.Fatal("runSession: want an error when session/set_mode fails, got nil")
	}
	if !strings.Contains(err.Error(), "set session mode") {
		t.Fatalf("error = %v, want it to mention 'set session mode'", err)
	}
	if !strings.Contains(err.Error(), "jcode does not support approval mode") {
		t.Fatalf("error = %v, want it to carry the underlying agent message", err)
	}
	tcCalls, npCalls := orch.calls()
	if len(tcCalls) != 0 || npCalls != 0 {
		t.Fatalf("turn-complete/next-prompt must never be called after a failed set_mode: tc=%+v np=%d", tcCalls, npCalls)
	}
	events := readNDJSON(t, agentLog)
	for _, e := range events {
		if e["method"] == "session/prompt" {
			t.Fatal("session/prompt was called after a failed set_mode — the loop must never start")
		}
	}
}

// --- RequestPermission forwarding ---

// TestApprovalPermissionForwardFullRoundTrip proves the full forwarding
// chain: the fake agent raises session/request_permission mid-turn ->
// acpdrive emits agent.permission_request (with a fresh request_id, the
// tool_call_id/title/options it was given) -> polls the mock decision
// endpoint (204 once, then 200 with a decision) -> emits
// agent.permission_resolved{resolution:"user"} -> hands that EXACT option
// back to the agent, which observes a Selected outcome.
func TestApprovalPermissionForwardFullRoundTrip(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	cfg.PermissionTimeout = 5 * time.Second
	t.Setenv("FAKE_AGENT_REQUEST_PERMISSION", "1")
	t.Setenv("FAKE_AGENT_PERMISSION_TOOL_CALL_ID", "tc_rm")
	t.Setenv("FAKE_AGENT_PERMISSION_TITLE", "Run rm -rf /tmp/scratch")
	t.Setenv("FAKE_AGENT_PERMISSION_OPTIONS", "opt_allow:allow_once:Allow once,opt_reject:reject_once:Reject once")

	orch := newMockOrchestrator()
	orch.permissionDecisionScript = []permissionDecisionStep{
		{status: http.StatusNoContent},
		{status: http.StatusOK, optionID: "opt_allow"},
	}
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	evs := orch.events()
	reqEvents := filterEventType(evs, eventAgentPermissionRequest)
	if len(reqEvents) != 1 {
		t.Fatalf("agent.permission_request events = %d, want exactly 1: %+v", len(reqEvents), reqEvents)
	}
	req := reqEvents[0]
	requestID, _ := req.Payload["request_id"].(string)
	if requestID == "" {
		t.Fatalf("permission_request request_id is empty: %+v", req.Payload)
	}
	if req.Payload["tool_call_id"] != "tc_rm" {
		t.Fatalf("permission_request tool_call_id = %v, want tc_rm", req.Payload["tool_call_id"])
	}
	if req.Payload["title"] != "Run rm -rf /tmp/scratch" {
		t.Fatalf("permission_request title = %v, want the tool call title", req.Payload["title"])
	}
	opts := decodePermissionOptions(t, req)
	if len(opts) != 2 || opts[0]["option_id"] != "opt_allow" || opts[1]["option_id"] != "opt_reject" {
		t.Fatalf("permission_request options = %+v, want [opt_allow opt_reject]", opts)
	}

	resEvents := filterEventType(evs, eventAgentPermissionResolved)
	if len(resEvents) != 1 {
		t.Fatalf("agent.permission_resolved events = %d, want exactly 1: %+v", len(resEvents), resEvents)
	}
	res := resEvents[0]
	if res.Payload["request_id"] != requestID {
		t.Fatalf("permission_resolved request_id = %v, want %v (must pair with the request event)", res.Payload["request_id"], requestID)
	}
	if res.Payload["option_id"] != "opt_allow" {
		t.Fatalf("permission_resolved option_id = %v, want opt_allow", res.Payload["option_id"])
	}
	if res.Payload["resolution"] != "user" {
		t.Fatalf("permission_resolved resolution = %v, want user", res.Payload["resolution"])
	}

	// The mock decision endpoint must have been polled under the SAME
	// request_id acpdrive emitted in the request event (proves the poll and
	// the event are for the same forwarded request, not two independent ids).
	polledIDs := orch.permissionDecisionRequestIDsSnapshot()
	if len(polledIDs) == 0 || polledIDs[0] != requestID {
		t.Fatalf("permission decision endpoint polled request_ids = %v, want first entry = %v", polledIDs, requestID)
	}

	// The fake agent must have observed the SAME option handed back as a
	// Selected outcome (not Cancelled), proving the round trip actually
	// reached jcode's side of the RPC.
	events := readNDJSON(t, agentLog)
	var result map[string]any
	for _, e := range events {
		if e["method"] == "request_permission_result" {
			result = e
		}
	}
	if result == nil {
		t.Fatal("fake agent never logged a request_permission_result")
	}
	if result["outcome"] != "selected" || result["option_id"] != "opt_allow" {
		t.Fatalf("request_permission_result = %+v, want outcome=selected option_id=opt_allow", result)
	}
}

// TestApprovalPermissionRequestDeliveredBeforeFirstDecisionPoll pins the P1
// ordering guarantee: the agent.permission_request event is POSTed to (and
// ACCEPTED by) the events endpoint BEFORE the first decision GET is issued.
// Without this, the request event could still be sitting in the emitter's
// async batch queue (up to its 500ms flush interval) when the first poll
// lands — and a server answering 404 for a request_id it has never ingested
// would instantly convert a pending approval into a timeout-deny.
func TestApprovalPermissionRequestDeliveredBeforeFirstDecisionPoll(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	cfg.PermissionTimeout = 5 * time.Second
	t.Setenv("FAKE_AGENT_REQUEST_PERMISSION", "1")

	orch := newMockOrchestrator()
	orch.permissionDecisionScript = []permissionDecisionStep{{status: http.StatusOK, optionID: "opt_allow"}}
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	// The mock records every events POST and decision GET in arrival order;
	// the accepted permission_request batch must precede the first decision
	// poll.
	order := orch.callOrderSnapshot()
	assertBefore(t, order, "events:permission_request", "decision")
}

// TestApprovalPermissionUndeliverableRequestIsTimeoutDenyWithoutPolling
// proves the P1 failure half: when the events endpoint keeps REJECTING the
// permission_request event (5xx through the whole permission budget), the
// request resolves as a timeout-deny WITHOUT the decision endpoint ever
// being polled — polling for a request the control plane never accepted
// could only misread 404-for-unknown as "expired".
func TestApprovalPermissionUndeliverableRequestIsTimeoutDenyWithoutPolling(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	cfg.PermissionTimeout = 150 * time.Millisecond
	t.Setenv("FAKE_AGENT_REQUEST_PERMISSION", "1")
	// Default options (opt_allow allow_once, opt_reject reject_once).

	orch := newMockOrchestrator()
	// Reject ONLY batches carrying the permission_request event; everything
	// else (run.session, permission_resolved, agent text) flows normally, so
	// the assertions below are not trivially true because of a dead pipeline.
	orch.eventsStatus = func(_ int, batch []event) int {
		for _, ev := range batch {
			if ev.Type == eventAgentPermissionRequest {
				return http.StatusServiceUnavailable
			}
		}
		return http.StatusOK
	}
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v (an undeliverable permission_request must NOT fail the run)", err)
	}

	// The decision endpoint must NEVER have been polled.
	if polled := orch.permissionDecisionRequestIDsSnapshot(); len(polled) != 0 {
		t.Fatalf("decision endpoint was polled (%v) despite the request event never being accepted", polled)
	}
	for _, label := range orch.callOrderSnapshot() {
		if label == "decision" {
			t.Fatal("decision endpoint was polled despite the request event never being accepted")
		}
	}

	// The (async, unaffected) resolved event still records the timeout-deny.
	resEvents := filterEventType(orch.events(), eventAgentPermissionResolved)
	if len(resEvents) != 1 {
		t.Fatalf("agent.permission_resolved events = %d, want exactly 1: %+v", len(resEvents), resEvents)
	}
	if resEvents[0].Payload["resolution"] != "timeout" || resEvents[0].Payload["option_id"] != "opt_reject" {
		t.Fatalf("resolved payload = %+v, want resolution=timeout option_id=opt_reject", resEvents[0].Payload)
	}

	// And the agent observed the reject option as a normal Selected outcome.
	events := readNDJSON(t, agentLog)
	var result map[string]any
	for _, e := range events {
		if e["method"] == "request_permission_result" {
			result = e
		}
	}
	if result == nil {
		t.Fatal("fake agent never logged a request_permission_result")
	}
	if result["outcome"] != "selected" || result["option_id"] != "opt_reject" {
		t.Fatalf("request_permission_result = %+v, want outcome=selected option_id=opt_reject", result)
	}
}

// TestApprovalPermissionTimeoutDenyPicksRejectOption proves the
// PERMISSION_TIMEOUT_SECONDS contract: when the decision endpoint never
// decides (unconditional 204), the request times out and a reject-KIND
// option is chosen — even when that option is NOT the last one offered,
// proving the selection is driven by kind, not merely list position — and
// the run continues (a Selected outcome, never Cancelled or a fatal error).
func TestApprovalPermissionTimeoutDenyPicksRejectOption(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	cfg.PermissionTimeout = 80 * time.Millisecond
	t.Setenv("FAKE_AGENT_REQUEST_PERMISSION", "1")
	// Reject option deliberately FIRST, allow options after — proves
	// pickRejectOption prefers the reject KIND over "last in the list".
	t.Setenv("FAKE_AGENT_PERMISSION_OPTIONS", "opt_reject:reject_once:Reject once,opt_allow:allow_once:Allow once")

	orch := newMockOrchestrator() // permissionDecisionScript unset => always 204
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v (a timed-out permission must NOT fail the run)", err)
	}

	resEvents := filterEventType(orch.events(), eventAgentPermissionResolved)
	if len(resEvents) != 1 {
		t.Fatalf("agent.permission_resolved events = %d, want exactly 1: %+v", len(resEvents), resEvents)
	}
	if resEvents[0].Payload["option_id"] != "opt_reject" {
		t.Fatalf("timeout-deny option_id = %v, want opt_reject (kind=reject_once preferred over list order)", resEvents[0].Payload["option_id"])
	}
	if resEvents[0].Payload["resolution"] != "timeout" {
		t.Fatalf("timeout-deny resolution = %v, want timeout", resEvents[0].Payload["resolution"])
	}

	events := readNDJSON(t, agentLog)
	var result map[string]any
	for _, e := range events {
		if e["method"] == "request_permission_result" {
			result = e
		}
	}
	if result == nil {
		t.Fatal("fake agent never logged a request_permission_result")
	}
	if result["outcome"] != "selected" || result["option_id"] != "opt_reject" {
		t.Fatalf("request_permission_result = %+v, want outcome=selected option_id=opt_reject (a normal Selected outcome, never Cancelled, so jcode's own turn handling continues)", result)
	}
}

// TestApprovalPermissionExpiredDecisionIsTimeoutDenyFast proves 404/410 from
// the decision endpoint is treated exactly like a timeout (same resolution,
// same reject-option selection) WITHOUT waiting out the full
// PERMISSION_TIMEOUT_SECONDS budget — it should resolve almost immediately.
func TestApprovalPermissionExpiredDecisionIsTimeoutDenyFast(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	cfg.PermissionTimeout = 10 * time.Second // deliberately long; 410 must short-circuit it
	t.Setenv("FAKE_AGENT_REQUEST_PERMISSION", "1")
	// Default options (opt_allow allow_once, opt_reject reject_once).

	orch := newMockOrchestrator()
	orch.permissionDecisionScript = []permissionDecisionStep{{status: http.StatusGone}} // 410 immediately
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("run took %s, want well under the 10s PermissionTimeout (410 must short-circuit, not wait it out)", elapsed)
	}

	resEvents := filterEventType(orch.events(), eventAgentPermissionResolved)
	if len(resEvents) != 1 {
		t.Fatalf("agent.permission_resolved events = %d, want exactly 1: %+v", len(resEvents), resEvents)
	}
	if resEvents[0].Payload["resolution"] != "timeout" {
		t.Fatalf("resolution = %v, want timeout (404/410 is indistinguishable from a client timeout per the F8a contract)", resEvents[0].Payload["resolution"])
	}
	if resEvents[0].Payload["option_id"] != "opt_reject" {
		t.Fatalf("option_id = %v, want opt_reject", resEvents[0].Payload["option_id"])
	}
}

// TestApprovalPermissionTimeoutAllowOnlyOptionsAnswersCancelled pins the P2
// fail-open fix: when a timed-out request offered ONLY allow-kind options
// (no reject choice at all), the answer must be ACP's Cancelled outcome — a
// safe refusal — and NEVER one of the allow options (which would silently
// turn timeout-DENY into timeout-ALLOW).
func TestApprovalPermissionTimeoutAllowOnlyOptionsAnswersCancelled(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	cfg.PermissionTimeout = 80 * time.Millisecond
	t.Setenv("FAKE_AGENT_REQUEST_PERMISSION", "1")
	// Allow-only list: the pre-P2 "fall back to the LAST option" behavior
	// would have selected opt_allow_always here.
	t.Setenv("FAKE_AGENT_PERMISSION_OPTIONS", "opt_allow_once:allow_once:Allow once,opt_allow_always:allow_always:Always allow")

	orch := newMockOrchestrator() // decision endpoint always 204 (never decided)
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	resEvents := filterEventType(orch.events(), eventAgentPermissionResolved)
	if len(resEvents) != 1 {
		t.Fatalf("agent.permission_resolved events = %d, want exactly 1: %+v", len(resEvents), resEvents)
	}
	if resEvents[0].Payload["resolution"] != "timeout" || resEvents[0].Payload["option_id"] != "" {
		t.Fatalf("resolved payload = %+v, want resolution=timeout option_id=\"\" (no option selected)", resEvents[0].Payload)
	}

	events := readNDJSON(t, agentLog)
	var result map[string]any
	for _, e := range events {
		if e["method"] == "request_permission_result" {
			result = e
		}
	}
	if result == nil {
		t.Fatal("fake agent never logged a request_permission_result")
	}
	if result["outcome"] != "cancelled" {
		t.Fatalf("request_permission_result = %+v, want outcome=cancelled — an allow-only option list must NEVER be auto-selected on timeout (fail-open)", result)
	}
}

// TestApprovalPermissionTimeoutZeroOptionsAnswersCancelled covers the
// degenerate zero-options case of the same P2 rule: nothing to select, so
// the timed-out request resolves as Cancelled with an empty option_id.
func TestApprovalPermissionTimeoutZeroOptionsAnswersCancelled(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	cfg.PermissionMode = permissionModeApproval
	cfg.PermissionTimeout = 80 * time.Millisecond
	t.Setenv("FAKE_AGENT_REQUEST_PERMISSION", "1")
	t.Setenv("FAKE_AGENT_PERMISSION_OPTIONS", "none") // zero options (see fakeagent_test.go)

	orch := newMockOrchestrator() // decision endpoint always 204 (never decided)
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	resEvents := filterEventType(orch.events(), eventAgentPermissionResolved)
	if len(resEvents) != 1 {
		t.Fatalf("agent.permission_resolved events = %d, want exactly 1: %+v", len(resEvents), resEvents)
	}
	if resEvents[0].Payload["resolution"] != "timeout" || resEvents[0].Payload["option_id"] != "" {
		t.Fatalf("resolved payload = %+v, want resolution=timeout option_id=\"\"", resEvents[0].Payload)
	}

	events := readNDJSON(t, agentLog)
	var result map[string]any
	for _, e := range events {
		if e["method"] == "request_permission_result" {
			result = e
		}
	}
	if result == nil {
		t.Fatal("fake agent never logged a request_permission_result")
	}
	if result["outcome"] != "cancelled" {
		t.Fatalf("request_permission_result = %+v, want outcome=cancelled", result)
	}
}

// --- non-approval regression ---

// TestNonApprovalModePermissionRegressionNoNewEvents pins the pre-F8a
// behavior for permissionMode == "" (unset/other): even if the agent DOES
// call RequestPermission (which should never happen in full_access, but is
// exercised here defensively), acpdrive auto-allows exactly as before and
// emits ZERO new permission_request/permission_resolved events — and never
// calls session/set_mode either, since approval was never requested.
func TestNonApprovalModePermissionRegressionNoNewEvents(t *testing.T) {
	dir := t.TempDir()
	agentLog := filepath.Join(dir, "agent.ndjson")
	cfg := fakeAgentConfig(t, dir, "end_turn", agentLog)
	// cfg.PermissionMode left at its zero value "".
	t.Setenv("FAKE_AGENT_REQUEST_PERMISSION", "1")
	t.Setenv("FAKE_AGENT_PERMISSION_OPTIONS", "opt_reject:reject_once:Reject once,opt_allow:allow_once:Allow once")

	orch := newMockOrchestrator()
	orch.nextPromptScript = []nextPromptStep{{status: http.StatusGone}}
	ts := orch.server()
	defer ts.Close()
	cfg.OrchBaseURL = ts.URL
	cfg.RunID = "run-test"
	cfg.RunToken = "tok"
	t.Setenv("ORCH_BASE_URL", ts.URL)
	t.Setenv("RUN_ID", "run-test")
	t.Setenv("RUN_TOKEN", "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runSession(ctx, cfg); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	evs := orch.events()
	if got := filterEventType(evs, eventAgentPermissionRequest); len(got) != 0 {
		t.Fatalf("agent.permission_request events = %+v, want NONE (permissionMode is not approval)", got)
	}
	if got := filterEventType(evs, eventAgentPermissionResolved); len(got) != 0 {
		t.Fatalf("agent.permission_resolved events = %+v, want NONE (permissionMode is not approval)", got)
	}

	events := readNDJSON(t, agentLog)
	for _, e := range events {
		if e["method"] == "session/set_mode" {
			t.Fatalf("session/set_mode was called with permissionMode unset: %+v", e)
		}
	}
	var result map[string]any
	for _, e := range events {
		if e["method"] == "request_permission_result" {
			result = e
		}
	}
	if result == nil {
		t.Fatal("fake agent never logged a request_permission_result")
	}
	// autoAllowPermission prefers an allow-kind option regardless of its
	// position in the list (opt_allow is listed SECOND here).
	if result["outcome"] != "selected" || result["option_id"] != "opt_allow" {
		t.Fatalf("request_permission_result = %+v, want outcome=selected option_id=opt_allow (unchanged auto-allow fallback)", result)
	}
}

// --- helpers ---

// assertBefore fails the test unless "before" appears strictly earlier than
// "after" in order (both must be present).
func assertBefore(t *testing.T, order []string, before, after string) {
	t.Helper()
	bi, ai := -1, -1
	for i, m := range order {
		if m == before && bi == -1 {
			bi = i
		}
		if m == after && ai == -1 {
			ai = i
		}
	}
	if bi == -1 {
		t.Fatalf("%q never happened; call order = %v", before, order)
	}
	if ai == -1 {
		t.Fatalf("%q never happened; call order = %v", after, order)
	}
	if bi >= ai {
		t.Fatalf("want %q before %q, got call order = %v", before, after, order)
	}
}

// decodePermissionOptions extracts the agent.permission_request event's
// "options" payload field back into plain string maps. It comes back from
// the mock orchestrator's JSON round trip as []interface{} of
// map[string]interface{}, not the []map[string]any
// EmitPermissionRequestSync built it from.
func decodePermissionOptions(t *testing.T, ev event) []map[string]string {
	t.Helper()
	raw, ok := ev.Payload["options"].([]interface{})
	if !ok {
		t.Fatalf("options payload = %#v (%T), want []interface{}", ev.Payload["options"], ev.Payload["options"])
	}
	out := make([]map[string]string, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]interface{})
		if !ok {
			t.Fatalf("option entry = %#v, want map", r)
		}
		out = append(out, map[string]string{
			"option_id": fmt.Sprint(m["option_id"]),
			"name":      fmt.Sprint(m["name"]),
			"kind":      fmt.Sprint(m["kind"]),
		})
	}
	return out
}
