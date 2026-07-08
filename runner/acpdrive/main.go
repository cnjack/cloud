// acpdrive is a minimal, headless ACP (Agent Client Protocol) client that drives
// one jcode coding turn to completion over stdio — no TTY involved.
//
// It launches `jcode acp` as a subprocess, connects a JSON-RPC ACP client to its
// stdin/stdout, then performs the handshake:
//
//	initialize → session/new(cwd=WORKSPACE) → session/prompt(TASK_PROMPT)
//
// conn.Prompt blocks until the agent loop finishes and returns a StopReason, so
// the run is fully synchronous and deterministic — no polling, no HTTP port, no
// auth token. jcode's tools (write_file, execute, …) run locally against the
// session cwd via its LocalExecutor, so the resulting changes land directly in
// WORKSPACE and are read back as a git diff by the entrypoint.
//
// Because the config forces default_mode=full_access, jcode's ApprovalState is
// in auto mode and NEVER calls back to this client for permission (verified in
// internal/runner/approval.go). The client's reverse-direction methods
// (RequestPermission, fs read/write, terminal) are therefore implemented as safe
// fallbacks and are not expected to fire in the P0 scenario.
//
// Usage:
//
//	acpdrive --workspace /workspace --prompt "…"        # jcode from PATH
//	acpdrive --agent /usr/local/bin/jcode --agent-arg acp --workspace … --prompt …
//
// Session mode (--session / RUN_SESSION=1, see session.go): instead of
// exiting after one session/prompt, acpdrive runs a turn-hook after each turn
// and long-polls the orchestrator for follow-up messages, sending each one as
// another session/prompt on the SAME session (never re-opened). This
// implements the runner side of D22 (docs/02-decision-log.md) /
// docs/14-cloud-v2-design.md §3. It is OFF by default: with RUN_SESSION unset
// (or --session=false), acpdrive's behavior is exactly the single-shot run()
// path below, unchanged.
//
// Resume (--resume <acp-session-id>, F9a, D23 ①②): when set, BOTH the
// single-shot run() path and session mode skip session/new and instead call
// session/load with that id (jcode's ACP handler restores the prior
// transcript from its local session store, internal/command/acp.go
// LoadSession in the jcode repo). A failed load (unknown id / corrupt
// transcript) is FAIL-VISIBLE: the run exits non-zero with a "session resume
// failed: …" message rather than silently falling back to a new session.
// jcode replays the loaded transcript through session/update before
// session/load returns (per the ACP spec); those replayed notifications are
// DROPPED, not re-emitted as run events — the control plane already holds
// the authoritative copy of that history (D13/D23), so re-emitting would
// duplicate the whole timeline on every wake (see driverClient.live).
//
// --resume deliberately has NO env fallback: RESUME_SESSION_ID is consumed by
// entrypoint.sh, which passes --resume explicitly ONLY in session mode. An
// env fallback here would leak a stale resume id from the pod environment
// into single-shot runs (whose session store was just scrubbed by the
// entrypoint's retention matrix) and hard-fail them.
//
// run.session emission: acpdrive emits one `run.session
// {"acp_session_id","resumed"}` event (see emitter.go's EmitSession — the
// contract F9b's ingest will consume) ONLY in session mode (--session, both
// resumed and fresh) or when --resume is set; a plain single-shot run's
// event stream stays bit-for-bit identical to pre-F9a (it is never
// resumable, so the control plane has no use for its session id).
//
// Permission approval (RUN_PERMISSION_MODE=approval, F8a, D22's permission
// half; see docs/02-decision-log.md D22 and permission.go): session mode
// always defaults to jcode's full_access (config.json's default_mode, written
// by entrypoint.sh — unchanged by this feature). When RUN_PERMISSION_MODE=
// approval, runSession additionally issues one session/set_mode(approval)
// call right after the session is established (session/new OR session/load —
// both paths, see session.go) and BEFORE the first session/prompt; from then
// on jcode routes restricted tool calls through ACP RequestPermission, which
// driverClient.RequestPermission forwards to the orchestrator for interactive
// approval instead of the pre-F8a auto-allow fallback (see permission.go).
// approval mode is ONLY meaningful in session mode: RUN_PERMISSION_MODE=
// approval without RUN_SESSION=1 is a fail-visible startup error (see
// checkPermissionModeRequiresSession below), never a silent downgrade to
// full_access.
//
// Env fallbacks: WORKSPACE, TASK_PROMPT, JCODE_BIN, RUN_SESSION, TURN_HOOK,
// RUN_PERMISSION_MODE, PERMISSION_TIMEOUT_SECONDS.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// permissionModeApproval is BOTH (a) the RUN_PERMISSION_MODE env /
// --permission-mode flag value that switches driverClient.RequestPermission
// from auto-allow to interactive-approval forwarding, AND (b) the literal ACP
// SessionModeId jcode advertises for that mode (jcode's
// internal/command/acp.go: acpModeApproval = "approval", matching
// mode.SessionMode.String()). One constant keeps that coupling visible
// instead of two string literals that could silently drift apart.
const permissionModeApproval = "approval"

// defaultPermissionTimeout is PERMISSION_TIMEOUT_SECONDS' fallback (F8a
// contract): how long an approval-mode RequestPermission waits for a user
// decision before defaulting to a deny-safe outcome (a reject-kind option,
// or Cancelled when none was offered — see permission.go's
// timeoutDenyPermission). Keep the configured value SIGNIFICANTLY smaller
// than RUN_TIMEOUT — see the operational note in permission.go's package
// doc.
const defaultPermissionTimeout = 300 * time.Second

// cliOpts is acpdrive's parsed command line. Kept as a struct (rather than
// main()-local vars) so parseFlags is testable — in particular the property
// that --resume has NO env fallback (CONFIRMED-1: a stale RESUME_SESSION_ID
// inherited from the pod env must never turn a single-shot run into a doomed
// session/load against a freshly scrubbed session store).
type cliOpts struct {
	agentBin  string
	agentArgs multiFlag
	workspace string
	prompt    string
	timeout   time.Duration
	verbose   bool
	session   bool
	turnHook  string
	resume    string
	// permissionMode / permissionTimeout (F8a, D22 permission half): see the
	// package doc comment and permission.go. permissionMode == "" (or
	// anything other than permissionModeApproval) is the pre-F8a, unchanged
	// full_access auto-allow behavior.
	permissionMode    string
	permissionTimeout time.Duration
}

// parseFlags defines and parses acpdrive's flags on a private FlagSet.
// Env-fallback policy: WORKSPACE/TASK_PROMPT/JCODE_BIN/RUN_SESSION/TURN_HOOK
// intentionally default from the environment (the F7a contract); --resume
// intentionally does NOT (see the package doc comment) — it must be passed
// explicitly, which only entrypoint.sh's session-mode branch does.
func parseFlags(args []string) (*cliOpts, error) {
	fs := flag.NewFlagSet("acpdrive", flag.ContinueOnError)
	o := &cliOpts{}
	fs.StringVar(&o.agentBin, "agent", envOr("JCODE_BIN", "jcode"), "path to the jcode binary")
	fs.Var(&o.agentArgs, "agent-arg", "extra arg passed to the agent (repeatable); defaults to [acp]")
	fs.StringVar(&o.workspace, "workspace", os.Getenv("WORKSPACE"), "working directory the session runs against")
	fs.StringVar(&o.prompt, "prompt", os.Getenv("TASK_PROMPT"), "task prompt to send (first turn, in --session mode)")
	fs.DurationVar(&o.timeout, "timeout", 5*time.Minute, "hard ceiling for the whole run")
	fs.BoolVar(&o.verbose, "verbose", false, "log ACP protocol details to stderr")
	fs.BoolVar(&o.session, "session", os.Getenv("RUN_SESSION") == "1", "multi-turn session mode: loop session/prompt over long-polled follow-up messages instead of exiting after one turn (env RUN_SESSION=1); see docs/14-cloud-v2-design.md §3 (D22)")
	fs.StringVar(&o.turnHook, "turn-hook", os.Getenv("TURN_HOOK"), "path to a script run synchronously after each turn in --session mode (env TURN_HOOK); ignored when --session is false")
	fs.StringVar(&o.resume, "resume", "", "resume an existing ACP session via session/load instead of creating a new one with session/new; the id must already exist in the agent's local session store (typically produced by a prior run.session event for the SAME persistent workspace, D23 ①②); a failed load is fail-visible (non-zero exit), never a silent fallback to a new session; deliberately NO env fallback — entrypoint.sh passes this explicitly, and only in session mode")
	fs.StringVar(&o.permissionMode, "permission-mode", os.Getenv("RUN_PERMISSION_MODE"), "session permission mode (env RUN_PERMISSION_MODE): \"approval\" makes driverClient.RequestPermission forward each ACP permission request to the orchestrator for interactive user approval instead of auto-allowing; any other value (default \"\") is the unchanged full_access auto-allow fallback. Only meaningful with --session/RUN_SESSION=1 (D22) — approval without session mode is a fail-visible startup error, see checkPermissionModeRequiresSession.")
	fs.DurationVar(&o.permissionTimeout, "permission-timeout", envSecondsOr("PERMISSION_TIMEOUT_SECONDS", defaultPermissionTimeout), "how long an approval-mode RequestPermission waits for a user decision (env PERMISSION_TIMEOUT_SECONDS, in seconds) before defaulting to a deny-safe outcome (a reject-kind option, or Cancelled if none was offered) with resolution=timeout; keep it well below --timeout/RUN_TIMEOUT (see permission.go)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return o, nil
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		// fs.Parse already printed the error + usage to stderr; mirror
		// flag.ExitOnError's exit status.
		os.Exit(2)
	}
	agentBin, agentArgs := opts.agentBin, opts.agentArgs
	workspace, prompt := opts.workspace, opts.prompt
	timeout, verbose := opts.timeout, opts.verbose
	session, turnHook, resume := opts.session, opts.turnHook, opts.resume

	if workspace == "" {
		fatal("workspace is required (--workspace or $WORKSPACE)")
	}
	if prompt == "" {
		fatal("prompt is required (--prompt or $TASK_PROMPT)")
	}
	// Fail-visible (F8a / D22): RUN_PERMISSION_MODE=approval only means
	// anything in session mode. Checked here, BEFORE either the session or
	// single-shot path starts, so a misconfigured run never gets partway
	// through a single-shot turn under a silently-ignored approval request.
	if err := checkPermissionModeRequiresSession(opts.permissionMode, session); err != nil {
		fatal("%v", err)
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		fatal("resolve workspace: %v", err)
	}
	workspace = abs
	if len(agentArgs) == 0 {
		agentArgs = multiFlag{"acp"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// Cancel the run on SIGINT/SIGTERM so a killed container tears down cleanly.
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Session mode (RUN_SESSION=1 / --session): loop session/prompt over
	// long-polled follow-up messages instead of exiting after one turn (F7a /
	// D22). When --session is NOT set, behavior is EXACTLY the single-shot
	// path below, unchanged (hard backward-compat requirement).
	if session {
		orchBase := os.Getenv("ORCH_BASE_URL")
		runID := os.Getenv("RUN_ID")
		runToken := os.Getenv("RUN_TOKEN")
		if orchBase == "" || runID == "" || runToken == "" {
			fatal("--session requires ORCH_BASE_URL, RUN_ID, and RUN_TOKEN in the environment (there is no control plane to long-poll otherwise)")
		}
		cfg := sessionConfig{
			AgentBin: agentBin, AgentArgs: agentArgs, Workspace: workspace, Prompt: prompt,
			TurnHook: turnHook, Verbose: verbose, Resume: resume,
			OrchBaseURL: orchBase, RunID: runID, RunToken: runToken,
			PermissionMode: opts.permissionMode, PermissionTimeout: opts.permissionTimeout,
		}
		if err := runSession(sigCtx, cfg); err != nil {
			// Same --timeout / exit-124 handling as the single-shot path below.
			if ctx.Err() == context.DeadlineExceeded {
				fmt.Fprintf(os.Stderr, "[acpdrive] error: session run exceeded --timeout=%s: %v\n", timeout, err)
				os.Exit(124)
			}
			fatal("%v", err)
		}
		return
	}

	if err := run(sigCtx, agentBin, agentArgs, workspace, prompt, resume, verbose); err != nil {
		// Distinguish "hit our own --timeout deadline" from any other agent
		// error with a dedicated exit code (124, the conventional timeout exit
		// status used by GNU coreutils' `timeout(1)`) so entrypoint.sh can
		// report run.failure reason=timeout instead of the generic agent_error.
		if ctx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(os.Stderr, "[acpdrive] error: run exceeded --timeout=%s: %v\n", timeout, err)
			os.Exit(124)
		}
		fatal("%v", err)
	}
}

func run(ctx context.Context, agentBin string, agentArgs []string, workspace, prompt, resume string, verbose bool) error {
	cmd := exec.CommandContext(ctx, agentBin, agentArgs...)
	cmd.Dir = workspace
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
		return fmt.Errorf("start %s: %w", agentBin, err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	// Event emitter: ships agent text + tool activity to the orchestrator. Nil
	// (safe no-op) when ORCH_BASE_URL/RUN_ID/RUN_TOKEN are absent, e.g. the pure
	// headless proof, so acpdrive keeps working standalone.
	emitter := NewEmitterFromEnv()
	defer emitter.Close()
	if emitter != nil {
		logf("event emitter active -> %s (run %s)", os.Getenv("ORCH_BASE_URL"), os.Getenv("RUN_ID"))
	}

	client := &driverClient{workspace: workspace, emitter: emitter}
	conn := acp.NewClientSideConnection(client, stdin, stdout)
	if verbose {
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

	// session/new (default) or session/load (--resume, F9a / D23 ①②): see the
	// package doc comment above for the fail-visible contract on a failed
	// resume. client.live stays false until the session is established:
	// session/load REPLAYS the prior transcript via session/update before it
	// returns (ACP spec; the acp-go-sdk's notification barrier guarantees every
	// replayed notification's handler completes BEFORE LoadSession returns, see
	// its TestLoadSession_NotificationReplayOrdering), and those replayed
	// events must be dropped, not re-emitted — the control plane already holds
	// that history (D13), so re-emitting would duplicate the whole timeline on
	// every wake. Flipping live AFTER LoadSession returns is therefore
	// race-free. The session/new path has no replay; live flips before any
	// session/prompt either way, so live-turn streaming is unaffected.
	var sessionID acp.SessionId
	if resume != "" {
		logf("[session] resuming acp session %s", resume)
		sessionID = acp.SessionId(resume)
		if _, err := conn.LoadSession(ctx, acp.LoadSessionRequest{
			Cwd:        workspace,
			McpServers: []acp.McpServer{},
			SessionId:  sessionID,
		}); err != nil {
			return fmt.Errorf("session resume failed: %s", describeErr(err))
		}
		logf("session resumed: %s", sessionID)
	} else {
		newSess, err := conn.NewSession(ctx, acp.NewSessionRequest{
			Cwd:        workspace,
			McpServers: []acp.McpServer{},
		})
		if err != nil {
			return fmt.Errorf("session/new: %s", describeErr(err))
		}
		sessionID = newSess.SessionId
		logf("session created: %s", sessionID)
	}
	client.live.Store(true)
	// run.session is emitted ONLY for resumed single-shot runs: a plain
	// single-shot run (resume == "") keeps its event stream bit-for-bit
	// identical to pre-F9a — it is never resumable, so the control plane has
	// no use for its session id. Session mode (runSession) always emits.
	if resume != "" {
		emitter.EmitSession(string(sessionID), true)
	}

	logf("prompting: %q", truncate(prompt, 200))
	promptResp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
	})
	if err != nil {
		return fmt.Errorf("session/prompt: %s", describeErr(err))
	}
	logf("run finished, stop_reason=%s", promptResp.StopReason)

	// Best-effort clean close so the agent flushes its recorder.
	_ = stdin.Close()

	switch promptResp.StopReason {
	case acp.StopReasonEndTurn, acp.StopReasonMaxTokens, acp.StopReasonMaxTurnRequests:
		return nil
	case acp.StopReasonCancelled:
		return fmt.Errorf("run cancelled by agent")
	case acp.StopReasonRefusal:
		return fmt.Errorf("run refused by agent")
	default:
		// Unknown stop reasons are treated as success for the P0 proof, since the
		// diff is inspected independently by the entrypoint.
		return nil
	}
}

// driverClient implements acp.Client. In full_access mode (the default —
// permissionMode == "") jcode never calls RequestPermission, and jcode uses
// its own LocalExecutor for file/terminal ops (not the client fs methods), so
// most of these are safe fallbacks. In approval mode (permissionMode ==
// permissionModeApproval, F8a / D22, session mode only — see main.go's
// checkPermissionModeRequiresSession) RequestPermission instead forwards each
// request to the orchestrator for interactive approval; see permission.go.
type driverClient struct {
	workspace string
	emitter   *Emitter
	// live gates SessionUpdate (F9a): false until the ACP session is
	// established (session/new returns, or session/load returns — the SDK's
	// notification barrier guarantees all of session/load's transcript-replay
	// session/update handlers complete before that return). While false,
	// notifications are dropped entirely: they are the OLD transcript being
	// replayed per the ACP spec, and the control plane already has that
	// history (D13) — re-emitting it would pollute the run timeline with the
	// entire prior conversation on every warm wake. RequestPermission (F8a)
	// reuses this same gate defensively: a permission request arriving while
	// live is still false (not expected — LoadSession's transcript replay is
	// notification-only per the ACP spec) falls back to the pre-F8a
	// auto-allow behavior rather than forwarding a request the control plane
	// has no live session context to show yet.
	live atomic.Bool

	// permissionMode/permissionTimeout/cp (F8a / D22 permission half, see
	// permission.go): permissionMode == permissionModeApproval switches
	// RequestPermission from auto-allow to forwarding via cp (the same
	// control-plane client runSession uses for turn-complete/next-prompt).
	// Left at their zero values by run()'s single-shot driverClient — the
	// single-shot path never legitimately runs in approval mode (main.go
	// rejects that combination at startup), so RequestPermission there always
	// takes the unchanged auto-allow branch and cp is never dereferenced.
	permissionMode    string
	permissionTimeout time.Duration
	cp                *controlPlaneClient
}

var _ acp.Client = (*driverClient)(nil)

// SessionUpdate is on jcode's hot path. It logs to stderr for local debugging
// and hands the notification to the mapper, which queues run events on the
// non-blocking emitter (never blocks the agent loop). Replayed notifications
// delivered during session/load (live == false) are dropped — see the live
// field's comment.
func (c *driverClient) SessionUpdate(_ context.Context, params acp.SessionNotification) error {
	if !c.live.Load() {
		return nil
	}
	u := params.Update
	switch {
	case u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil:
		fmt.Fprintf(os.Stderr, "[agent] %s\n", strings.TrimRight(u.AgentMessageChunk.Content.Text.Text, "\n"))
	case u.ToolCall != nil:
		fmt.Fprintf(os.Stderr, "[tool] %s (%s)\n", u.ToolCall.Title, u.ToolCall.Status)
	case u.ToolCallUpdate != nil:
		fmt.Fprintf(os.Stderr, "[tool] %s -> %v\n", u.ToolCallUpdate.ToolCallId, u.ToolCallUpdate.Status)
	}
	mapSessionUpdate(c.emitter, u)
	return nil
}

// RequestPermission's default (full_access, permissionMode != approval)
// should never fire — jcode's ApprovalState is in auto mode. If it somehow
// does anyway, auto-select the first "allow"-like option so an unattended run
// cannot hang. In approval mode (F8a / D22, see permission.go) this instead
// forwards the request to the orchestrator for interactive approval — but
// only once the session is live (see the live field's comment); a request
// arriving before that (not expected) falls back to this same auto-allow
// path, defensively, rather than forwarding a request with no live run
// context.
//
// Per the ACP spec/SDK, each inbound request (including this one) is
// dispatched in its own goroutine (acp-go-sdk's Connection.receive, see
// connection.go) — so blocking here (the whole point of the approval-mode
// long-poll below) does NOT stall the connection's read loop or any other
// in-flight request/response, including the session/prompt call this
// permission request was raised in service of.
func (c *driverClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if !c.live.Load() {
		fmt.Fprintln(os.Stderr, "[warn] agent requested permission before the session was live; auto-allowing (not forwarded)")
		return autoAllowPermission(params.Options), nil
	}
	if c.permissionMode != permissionModeApproval {
		fmt.Fprintln(os.Stderr, "[warn] agent requested permission unexpectedly; auto-allowing")
		return autoAllowPermission(params.Options), nil
	}
	return c.forwardPermissionRequest(ctx, params)
}

// autoAllowPermission is the pre-F8a fallback: auto-select the first
// "allow"-like option, else the first option, else Cancelled if there are
// none. Used by RequestPermission whenever approval-mode forwarding does not
// apply (permissionMode != approval, or the defensive live==false guard).
func autoAllowPermission(options []acp.PermissionOption) acp.RequestPermissionResponse {
	for _, opt := range options {
		if opt.Kind == acp.PermissionOptionKindAllowOnce || opt.Kind == acp.PermissionOptionKindAllowAlways {
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
			}}
		}
	}
	if len(options) > 0 {
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{OptionId: options[0].OptionId},
		}}
	}
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
		Cancelled: &acp.RequestPermissionOutcomeCancelled{},
	}}
}

func (c *driverClient) WriteTextFile(_ context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acp.WriteTextFileResponse{}, fmt.Errorf("path must be absolute: %s", params.Path)
	}
	if err := os.MkdirAll(filepath.Dir(params.Path), 0o755); err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	return acp.WriteTextFileResponse{}, os.WriteFile(params.Path, []byte(params.Content), 0o644)
}

func (c *driverClient) ReadTextFile(_ context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acp.ReadTextFileResponse{}, fmt.Errorf("path must be absolute: %s", params.Path)
	}
	b, err := os.ReadFile(params.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	return acp.ReadTextFileResponse{Content: string(b)}, nil
}

func (c *driverClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{TerminalId: "term-noop"}, nil
}
func (c *driverClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}
func (c *driverClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}
func (c *driverClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}
func (c *driverClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, nil
}

// ---- helpers ----

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, " ") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envSecondsOr reads an env var as a whole number of seconds (the
// PERMISSION_TIMEOUT_SECONDS contract), falling back to def when unset,
// non-numeric, or non-positive. A malformed value is not itself fail-visible
// (it is an operational tuning knob, not a red-line dependency per
// CLAUDE.md's fail-visible rule) — it just falls back to the documented
// default rather than producing a zero/negative timeout.
func envSecondsOr(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

// checkPermissionModeRequiresSession enforces the fail-visible contract for
// F8a / D22's permission approval mode: RUN_PERMISSION_MODE=approval only
// does anything in session mode (jcode's session/set_mode + the
// RequestPermission forwarding loop both live in runSession/session.go).
// Requesting approval mode without session mode must never silently
// downgrade to the full_access auto-allow fallback — it is a startup
// configuration error instead (nil is returned when the combination is
// valid, including "approval not requested at all").
func checkPermissionModeRequiresSession(permissionMode string, sessionMode bool) error {
	if permissionMode == permissionModeApproval && !sessionMode {
		return fmt.Errorf("RUN_PERMISSION_MODE=approval requires session mode (RUN_SESSION=1 / --session) — approval-mode permission forwarding only applies to multi-turn sessions (D22); refusing to silently fall back to full_access")
	}
	return nil
}

func describeErr(err error) string {
	if re, ok := err.(*acp.RequestError); ok {
		if b, mErr := json.Marshal(re); mErr == nil {
			return string(b)
		}
		return fmt.Sprintf("code=%d %s", re.Code, re.Message)
	}
	return err.Error()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[acpdrive] "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[acpdrive] error: "+format+"\n", args...)
	os.Exit(1)
}
