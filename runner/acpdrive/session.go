package main

// session.go — the multi-turn ACP session loop (F7a, implements D22's runner
// side; see docs/02-decision-log.md D22 and docs/14-cloud-v2-design.md §3).
//
// In session mode acpdrive does NOT exit after the first session/prompt.
// Instead, after each turn it:
//  1. synchronously runs a "turn hook" (typically entrypoint.sh's diff/commit/
//     bundle logic, extracted into runner/turn-hook.sh) so the run's git state
//     (and, in draft_pr mode, the PR branch) is updated after every turn, not
//     just once at the end;
//  2. reports the turn outcome to the orchestrator (POST turn-complete);
//  3. long-polls the orchestrator for the next user message (GET next-prompt);
//  4. on a new message, sends another session/prompt on the SAME ACP session
//     (jcode's tool/context state must survive turn to turn — a session is
//     NEVER re-opened); on 410 the run was finalized elsewhere (user Finish /
//     idle timeout) and the loop exits cleanly.
//
// The F7b server side (turn-complete/next-prompt) does not exist yet: this is
// implemented strictly against the contract in the F7a task and exercised with
// httptest mocks (session_test.go).
//
// Resume (cfg.Resume / --resume / RESUME_SESSION_ID, F9a, D23 ①②): when set,
// step 0 above (session/new) is replaced by session/load against that id —
// see the fail-visible contract in main.go's package doc comment and in the
// session/new-or-load block below. Everything after that (the turn loop) is
// unchanged: a resumed session is driven exactly like a freshly created one.
//
// This file is deliberately independent of run() (main.go): run() is the
// hard-backward-compat, RUN_SESSION-unset path and MUST NOT change behavior,
// so runSession() below duplicates the small amount of agent-launch
// boilerplate rather than sharing a refactored helper with run().

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// controlPlaneLostAfter bounds how long the session loop keeps retrying a
// consecutive run of network/5xx failures against turn-complete/next-prompt
// before giving up as "control plane lost" (per the F7a contract: "> ~5 min of
// consecutive failures = control plane lost → exit as agent_error"). Tests
// override this via sessionConfig with a much smaller value.
const controlPlaneLostAfter = 5 * time.Minute

// nextPromptPollTimeout bounds one next-prompt HTTP round trip. It must
// comfortably exceed the server's long-poll hold (contract: "server holds ≤ ~
// 25s") so a legitimate 204 (or a slow-but-healthy hold) is never mistaken for
// a client-side timeout/network error.
const nextPromptPollTimeout = 35 * time.Second

// turnCompleteTimeout bounds one turn-complete HTTP round trip (not a
// long-poll, so a short timeout is appropriate).
const turnCompleteTimeout = 10 * time.Second

// minPollInterval is the FLOOR between consecutive next-prompt polls after a
// 204. The server is EXPECTED to long-poll (hold ≤ ~25s before answering 204),
// in which case this floor is invisible noise — but a server that answers 204
// immediately (an early F7b build, a misconfiguration, or a proxy that
// short-circuits the hold) must not turn "poll again immediately" into a hot
// loop hammering the control plane: 250ms caps the worst case at ~4 req/s per
// run instead of thousands.
const minPollInterval = 250 * time.Millisecond

// sessionConfig bundles runSession's dependencies, including the bits tests
// need to swap out (control-plane base URL/token, and the failure-budget/
// backoff timings, which default to production values when zero).
type sessionConfig struct {
	AgentBin  string
	AgentArgs []string
	Workspace string
	Prompt    string
	TurnHook  string // path to the turn-hook script/executable; "" = no hook
	Verbose   bool
	// Resume, if non-empty, is an existing ACP session id (F9a / D23 ①②): the
	// loop skips session/new and instead resumes via session/load, then
	// continues the SAME turn loop below (never re-opening the session). "" =
	// default, session/new (unchanged pre-F9a behavior). See main.go's package
	// doc comment for the fail-visible contract on a failed resume.
	Resume string

	OrchBaseURL string // ORCH_BASE_URL
	RunID       string // RUN_ID
	RunToken    string // RUN_TOKEN

	// PermissionMode ("" or permissionModeApproval, F8a / D22 permission
	// half): approval switches the session into jcode's approval ACP mode
	// right after it is established (session/set_mode, below) and makes
	// driverClient.RequestPermission forward requests for interactive
	// approval instead of auto-allowing (permission.go). See main.go's
	// checkPermissionModeRequiresSession for the fail-visible contract that
	// keeps this out of the single-shot (non-session) path entirely.
	PermissionMode string
	// PermissionTimeout bounds how long an approval-mode RequestPermission
	// waits for a user decision before defaulting to a reject-leaning option
	// (env PERMISSION_TIMEOUT_SECONDS). Zero falls back to
	// defaultPermissionTimeout (main.go).
	PermissionTimeout time.Duration

	// Overridable for tests; zero values fall back to the package defaults.
	ControlPlaneLostAfter time.Duration
	InitialBackoff        time.Duration
	MaxBackoff            time.Duration
	MinPollInterval       time.Duration
}

func (c *sessionConfig) lostAfter() time.Duration {
	if c.ControlPlaneLostAfter > 0 {
		return c.ControlPlaneLostAfter
	}
	return controlPlaneLostAfter
}

func (c *sessionConfig) minPollInterval() time.Duration {
	if c.MinPollInterval > 0 {
		return c.MinPollInterval
	}
	return minPollInterval
}

func (c *sessionConfig) initialBackoff() time.Duration {
	if c.InitialBackoff > 0 {
		return c.InitialBackoff
	}
	return 200 * time.Millisecond
}

func (c *sessionConfig) maxBackoff() time.Duration {
	if c.MaxBackoff > 0 {
		return c.MaxBackoff
	}
	return 5 * time.Second
}

func (c *sessionConfig) permissionTimeout() time.Duration {
	if c.PermissionTimeout > 0 {
		return c.PermissionTimeout
	}
	return defaultPermissionTimeout
}

// runSession drives the multi-turn ACP session loop to completion. It returns
// nil on a graceful session end (410 from next-prompt), and a non-nil error
// for any fatal condition (ACP transport error, turn-hook failure, control
// plane lost, ctx cancellation/--timeout). main() treats run() and
// runSession() errors identically (fatal(), non-zero exit), so entrypoint.sh's
// existing "RUN_RC != 0 → die agent_error" wrapper reports the failure the
// same way regardless of mode — session mode needs no bespoke reporting path.
func runSession(ctx context.Context, cfg sessionConfig) error {
	cmd := exec.CommandContext(ctx, cfg.AgentBin, cfg.AgentArgs...)
	cmd.Dir = cfg.Workspace
	cmd.Stderr = os.Stderr // jcode logs go to stderr; stdout is the JSON-RPC channel
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cfg.AgentBin, err)
	}
	// Kill/reap the agent on any exit from this function. Registered FIRST so
	// it runs LAST (defers are LIFO): the best-effort graceful stdin.Close()
	// below runs before this, giving jcode a chance to flush its recorder.
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	defer func() { _ = stdin.Close() }()

	// Event emitter: ships agent text + tool activity to the orchestrator,
	// exactly as in single-shot mode (nil/no-op if ORCH_BASE_URL/RUN_ID/
	// RUN_TOKEN are absent — though session mode requires them anyway, see the
	// validation in main()).
	emitter := NewEmitterFromEnv()
	defer emitter.Close()
	if emitter != nil {
		logf("event emitter active -> %s (run %s)", cfg.OrchBaseURL, cfg.RunID)
	}

	// cp (the F7b control-plane client) is constructed here, BEFORE the
	// driverClient, rather than down by the turn loop where it used to live:
	// approval-mode RequestPermission forwarding (F8a) needs it too, via
	// driverClient.cp, for the SAME /internal/v1/runs/{id}/permissions/{...}
	// long-poll style as turn-complete/next-prompt below.
	cp := newControlPlaneClient(cfg)

	client := &driverClient{
		workspace:         cfg.Workspace,
		emitter:           emitter,
		permissionMode:    cfg.PermissionMode,
		permissionTimeout: cfg.permissionTimeout(),
		cp:                cp,
	}
	conn := acp.NewClientSideConnection(client, stdin, stdout)
	if cfg.Verbose {
		conn.SetLogger(slog.Default())
	}

	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %s", describeErr(err))
	}
	logf("connected to agent (protocol v%v)", initResp.ProtocolVersion)

	// session/new (default) OR session/load (cfg.Resume != "", F9a / D23 ①②) is
	// called EXACTLY ONCE for the whole session, regardless of how many turns
	// follow — the loop below reuses sessionID on every subsequent
	// session/prompt so jcode's context/tool state survives turn to turn (the
	// whole point of D22). A resume failure (unknown id / corrupt transcript)
	// is fail-visible: the loop is never entered and a descriptive error is
	// returned instead of silently falling back to a new session.
	//
	// client.live stays false until the session is established: session/load
	// replays the prior transcript via session/update before returning (ACP
	// spec; the SDK's notification barrier makes the post-LoadSession flip
	// race-free — see driverClient.live in main.go), and that replayed history
	// must be dropped, not re-emitted into the run timeline.
	var sessionID acp.SessionId
	if cfg.Resume != "" {
		logf("[session] resuming acp session %s", cfg.Resume)
		sessionID = acp.SessionId(cfg.Resume)
		if _, err := conn.LoadSession(ctx, acp.LoadSessionRequest{
			Cwd:        cfg.Workspace,
			McpServers: []acp.McpServer{},
			SessionId:  sessionID,
		}); err != nil {
			return fmt.Errorf("session resume failed: %s", describeErr(err))
		}
		logf("session resumed: %s (session mode)", sessionID)
	} else {
		newSess, err := conn.NewSession(ctx, acp.NewSessionRequest{
			Cwd:        cfg.Workspace,
			McpServers: []acp.McpServer{},
		})
		if err != nil {
			return fmt.Errorf("session/new: %s", describeErr(err))
		}
		sessionID = newSess.SessionId
		logf("session created: %s (session mode)", sessionID)
	}
	client.live.Store(true)

	// Approval mode (F8a / D22 permission half): switch the session into
	// jcode's "approval" ACP mode right after it is established — covers
	// BOTH the resume and fresh-session paths above, since sessionID is set
	// either way by this point — and BEFORE the first session/prompt below,
	// so no tool call in turn 1 can slip through under the config-file
	// default_mode=full_access jcode started with (entrypoint.sh's
	// config.json; unchanged by this feature — full_access stays the
	// process-wide default, approval is an explicit per-session upgrade).
	// This SDK version's NewSession/LoadSession requests have no mode
	// parameter (see acp-go-sdk types_gen.go), so session/set_mode is the
	// only path — there is no "session/new with mode" alternative to prefer
	// here. A failure is fail-visible: silently continuing in full_access
	// would contradict the run's explicit RUN_PERMISSION_MODE=approval
	// request (CLAUDE.md's fail-visible rule), so this is as fatal as a
	// failed resume.
	if cfg.PermissionMode == permissionModeApproval {
		logf("[permission] setting session %s to approval mode (RUN_PERMISSION_MODE=approval)", sessionID)
		if _, err := conn.SetSessionMode(ctx, acp.SetSessionModeRequest{
			SessionId: sessionID,
			ModeId:    acp.SessionModeId(permissionModeApproval),
		}); err != nil {
			return fmt.Errorf("set session mode to approval: %s", describeErr(err))
		}
		logf("[permission] session %s is now in approval mode; RequestPermission will be forwarded (timeout=%s)", sessionID, cfg.permissionTimeout())
	}

	// Session mode always emits run.session (both resumed=true and =false):
	// every session-mode run is a candidate for a later warm wake, so F9b
	// needs its id either way. (The single-shot path in main.go emits only
	// when resuming — see the comment there.)
	emitter.EmitSession(string(sessionID), cfg.Resume != "")

	nextPrompt := cfg.Prompt
	for turn := 1; ; turn++ {
		logf("[turn %d] started", turn)
		promptResp, err := conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock(nextPrompt)},
		})
		if err != nil {
			return fmt.Errorf("[turn %d] session/prompt: %s", turn, describeErr(err))
		}
		stopReason := promptResp.StopReason
		logf("[turn %d] completed stop_reason=%s", turn, stopReason)

		if cfg.TurnHook != "" {
			if err := runTurnHook(ctx, cfg.TurnHook, turn, sessionID, stopReason); err != nil {
				// Fatal per the hook contract: a non-zero turn-hook exit means the
				// per-turn git/upload bookkeeping is in an unknown state, so the
				// whole run must fail rather than silently continuing.
				return fmt.Errorf("[turn %d] turn-hook failed: %w", turn, err)
			}
		}

		if err := cp.postTurnComplete(ctx, turn, stopReason); err != nil {
			return fmt.Errorf("[turn %d] turn-complete: %w", turn, err)
		}

		logf("[turn %d] waiting for the next message (long-poll)", turn)
		done, prompt, err := cp.waitForNextPrompt(ctx)
		if err != nil {
			return fmt.Errorf("[turn %d] next-prompt: %w", turn, err)
		}
		if done {
			logf("session finalized by the control plane (410); exiting cleanly")
			return nil
		}
		nextPrompt = prompt
	}
}

// runTurnHook synchronously runs the turn-hook subprocess after a turn
// completes. Contract (docs/14-cloud-v2-design.md §3, D22 / F7a task spec):
//
//	env:  TURN_INDEX=<1-based turn number>
//	      ACP_SESSION_ID=<the ACP session id>
//	      ACP_STOP_REASON=<the ACP stop reason for this turn>
//	exit 0   = ok, continue the loop
//	exit !=0 = fatal: report agent_error and stop the whole run (the caller
//	           treats this exactly like an ACP transport error)
//
// The hook inherits acpdrive's own environment (which in production is
// whatever entrypoint.sh exported: WORKSPACE, OUT_DIR, BASE_REF, GIT_MODE,
// BRANCH_NAME, RUN_ID, ORCH_BASE_URL, RUN_TOKEN, TASK_PROMPT, …) plus the
// three turn-specific vars above. stdout/stderr are passed through so the
// hook's own logging (and the diff-marker stdout contract) is preserved.
func runTurnHook(ctx context.Context, hookPath string, turn int, sessionID acp.SessionId, stopReason acp.StopReason) error {
	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("TURN_INDEX=%d", turn),
		"ACP_SESSION_ID="+string(sessionID),
		"ACP_STOP_REASON="+string(stopReason),
	)
	logf("[turn %d] running turn-hook %s", turn, hookPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("turn-hook %s: %w", hookPath, err)
	}
	return nil
}

// controlPlaneClient talks to the orchestrator's F7b session endpoints
// (turn-complete / next-prompt). Reuses the exact env-derived names
// (ORCH_BASE_URL/RUN_ID/RUN_TOKEN) the emitter and orchclient already use.
type controlPlaneClient struct {
	baseURL string
	runID   string
	token   string
	http    *http.Client

	lostAfter       time.Duration
	initialBackoff  time.Duration
	maxBackoff      time.Duration
	minPollInterval time.Duration
}

func newControlPlaneClient(cfg sessionConfig) *controlPlaneClient {
	return &controlPlaneClient{
		baseURL:         cfg.OrchBaseURL,
		runID:           cfg.RunID,
		token:           cfg.RunToken,
		http:            &http.Client{}, // no client-level timeout: each call sets its own context deadline
		lostAfter:       cfg.lostAfter(),
		initialBackoff:  cfg.initialBackoff(),
		maxBackoff:      cfg.maxBackoff(),
		minPollInterval: cfg.minPollInterval(),
	}
}

// postTurnComplete POSTs {turn, stop_reason} to
// /internal/v1/runs/{id}/turn-complete with Bearer RUN_TOKEN. 200 = recorded.
// Network errors / 5xx are retried with capped exponential backoff (mirrors
// the emitter's retry philosophy); once a consecutive-failure run exceeds
// lostAfter (default 5min) it is reported as "control plane lost", which is
// fatal to the run. A non-retryable 4xx is also fatal: unlike the best-effort
// event emitter, this call gates whether the loop may safely ask for the next
// turn, so the orchestrator must have durably recorded this one first.
func (c *controlPlaneClient) postTurnComplete(ctx context.Context, turn int, stopReason acp.StopReason) error {
	body, err := json.Marshal(map[string]any{"turn": turn, "stop_reason": string(stopReason)})
	if err != nil {
		return fmt.Errorf("marshal turn-complete body: %w", err)
	}
	url := fmt.Sprintf("%s/internal/v1/runs/%s/turn-complete", c.baseURL, c.runID)

	backoff := c.initialBackoff
	deadline := time.Now().Add(c.lostAfter)
	for {
		status, rerr := c.doOnce(ctx, http.MethodPost, url, body, turnCompleteTimeout)
		if rerr == nil && status >= 200 && status < 300 {
			return nil
		}
		retryable := rerr != nil || status == http.StatusTooManyRequests || status >= 500
		if rerr == nil {
			rerr = fmt.Errorf("unexpected status %d", status)
		}
		if !retryable {
			return fmt.Errorf("turn-complete rejected (non-retryable): %w", rerr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("control plane unreachable for >%s reporting turn-complete: %w", c.lostAfter, rerr)
		}
		logf("turn-complete POST failed (%v); retrying in %s", rerr, backoff)
		if !sleepCtx(ctx, backoff) {
			return ctx.Err()
		}
		backoff = nextBackoff(backoff, c.maxBackoff)
	}
}

// nextPromptResponse is the 200 body of GET next-prompt (contract:
// {"message_id":"...","prompt":"..."}).
type nextPromptResponse struct {
	MessageID string `json:"message_id"`
	Prompt    string `json:"prompt"`
}

// waitForNextPrompt GETs /internal/v1/runs/{id}/next-prompt with Bearer
// RUN_TOKEN. The contract server long-polls (holds the connection for up to
// ~25s) before answering:
//
//	204 = no message yet — poll again promptly (no exponential backoff; the
//	      server is expected to have done the waiting), but never faster than
//	      minPollInterval so a server that does NOT hold can't induce a hot loop
//	200 {message_id,prompt} = the next turn's input
//	410 = the run was finalized elsewhere (user Finish / idle timeout) — exit
//	      the loop gracefully
//
// Network errors and any other status are treated as transient failures and
// retried with capped backoff, bounded overall by lostAfter ("control plane
// lost").
func (c *controlPlaneClient) waitForNextPrompt(ctx context.Context) (done bool, prompt string, err error) {
	url := fmt.Sprintf("%s/internal/v1/runs/%s/next-prompt", c.baseURL, c.runID)
	backoff := c.initialBackoff
	var failingSince time.Time // zero = currently healthy

	for {
		status, body, rerr := c.getOnce(ctx, url)
		if rerr == nil {
			switch status {
			case http.StatusNoContent:
				failingSince = time.Time{}
				// Floor between polls: invisible when the server long-polls as
				// contracted, but prevents a hot loop against a server that
				// answers 204 immediately (P2 hardening).
				if !sleepCtx(ctx, c.minPollInterval) {
					return false, "", ctx.Err()
				}
				continue
			case http.StatusOK:
				var msg nextPromptResponse
				if jerr := json.Unmarshal(body, &msg); jerr != nil {
					rerr = fmt.Errorf("decode next-prompt response: %w", jerr)
				} else {
					return false, msg.Prompt, nil
				}
			case http.StatusGone:
				return true, "", nil
			default:
				rerr = fmt.Errorf("unexpected status %d", status)
			}
		}
		// rerr != nil here: network error, decode error, or unexpected status.
		if failingSince.IsZero() {
			failingSince = time.Now()
		}
		if time.Since(failingSince) > c.lostAfter {
			return false, "", fmt.Errorf("control plane unreachable for >%s polling next-prompt: %w", c.lostAfter, rerr)
		}
		logf("next-prompt poll failed (%v); retrying in %s", rerr, backoff)
		if !sleepCtx(ctx, backoff) {
			return false, "", ctx.Err()
		}
		backoff = nextBackoff(backoff, c.maxBackoff)
	}
}

// permissionDecisionResponse is the 200 body of GET
// .../permissions/{request_id}/decision (contract: {"option_id":"..."}).
type permissionDecisionResponse struct {
	OptionID string `json:"option_id"`
}

// waitForPermissionDecision long-polls
// /internal/v1/runs/{id}/permissions/{request_id}/decision with Bearer
// RUN_TOKEN (F8a / D22 permission half; F8b builds the server side). It
// reuses waitForNextPrompt's exact retry/backoff/204-floor philosophy:
//
//	204 = not decided yet — poll again, floored at minPollInterval
//	200 {option_id} = the user's decision -> (optionID, true)
//	404/410 = the request expired/is invalid -> ("", false), the SAME return
//	          as a plain timeout: the caller (forwardPermissionRequest) can't
//	          act on the two differently and per the F8a contract doesn't
//	          need to.
//
// F8b contract requirement (see permission.go's package doc): an UNKNOWN
// request_id MUST be answered 204 (pending), never 404 — 404/410 are
// reserved for requests that once existed and have since expired or been
// invalidated. The caller only ever polls AFTER the agent.permission_request
// event was synchronously acknowledged by the events endpoint
// (EmitPermissionRequestSync), so by the time the first GET lands here the
// server has already ingested the id.
//
// UNLIKE waitForNextPrompt there is no separate "control plane lost" budget
// here: the poll is bounded ENTIRELY by ctx, which the caller wraps in
// PERMISSION_TIMEOUT_SECONDS (see driverClient.forwardPermissionRequest) —
// once ctx is done (network flakiness exhausted the wall clock, or the
// timeout simply elapsed with no decision), this returns ("", false) exactly
// like an explicit 404/410, and the caller's timeout-deny path applies
// either way.
func (c *controlPlaneClient) waitForPermissionDecision(ctx context.Context, requestID string) (optionID string, decided bool) {
	url := fmt.Sprintf("%s/internal/v1/runs/%s/permissions/%s/decision", c.baseURL, c.runID, requestID)
	backoff := c.initialBackoff

	for {
		status, body, rerr := c.getOnce(ctx, url)
		if rerr == nil {
			switch status {
			case http.StatusNoContent:
				if !sleepCtx(ctx, c.minPollInterval) {
					return "", false
				}
				continue
			case http.StatusOK:
				var resp permissionDecisionResponse
				if jerr := json.Unmarshal(body, &resp); jerr == nil && resp.OptionID != "" {
					return resp.OptionID, true
				}
				logf("[permission] request %s: decision 200 body did not decode an option_id (%s); treating as not-yet-decided", requestID, string(body))
			case http.StatusNotFound, http.StatusGone:
				logf("[permission] request %s: decision endpoint returned %d (expired/invalid)", requestID, status)
				return "", false
			default:
				rerr = fmt.Errorf("unexpected status %d", status)
			}
		}
		if rerr != nil {
			logf("[permission] request %s: decision poll failed (%v); retrying in %s", requestID, rerr, backoff)
		}
		if !sleepCtx(ctx, backoff) {
			return "", false
		}
		backoff = nextBackoff(backoff, c.maxBackoff)
	}
}

// doOnce issues one request with no body decoding (used by postTurnComplete).
func (c *controlPlaneClient) doOnce(ctx context.Context, method, url string, body []byte, timeout time.Duration) (status int, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// getOnce issues one GET and returns the raw body (used by waitForNextPrompt,
// which needs to decode a 200 body).
func (c *controlPlaneClient) getOnce(ctx context.Context, url string) (status int, body []byte, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, nextPromptPollTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// sleepCtx sleeps for d or returns false early if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// nextBackoff doubles cur, capped at max (mirrors emitter.go / orchclient's
// backoff style).
func nextBackoff(cur, max time.Duration) time.Duration {
	cur *= 2
	if cur > max {
		return max
	}
	return cur
}
