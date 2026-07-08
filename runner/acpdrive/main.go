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
// Env fallbacks: WORKSPACE, TASK_PROMPT, JCODE_BIN, RUN_SESSION, TURN_HOOK.
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
	"strings"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func main() {
	var (
		agentBin  string
		agentArgs multiFlag
		workspace string
		prompt    string
		timeout   time.Duration
		verbose   bool
		session   bool
		turnHook  string
	)
	flag.StringVar(&agentBin, "agent", envOr("JCODE_BIN", "jcode"), "path to the jcode binary")
	flag.Var(&agentArgs, "agent-arg", "extra arg passed to the agent (repeatable); defaults to [acp]")
	flag.StringVar(&workspace, "workspace", os.Getenv("WORKSPACE"), "working directory the session runs against")
	flag.StringVar(&prompt, "prompt", os.Getenv("TASK_PROMPT"), "task prompt to send (first turn, in --session mode)")
	flag.DurationVar(&timeout, "timeout", 5*time.Minute, "hard ceiling for the whole run")
	flag.BoolVar(&verbose, "verbose", false, "log ACP protocol details to stderr")
	flag.BoolVar(&session, "session", os.Getenv("RUN_SESSION") == "1", "multi-turn session mode: loop session/prompt over long-polled follow-up messages instead of exiting after one turn (env RUN_SESSION=1); see docs/14-cloud-v2-design.md §3 (D22)")
	flag.StringVar(&turnHook, "turn-hook", os.Getenv("TURN_HOOK"), "path to a script run synchronously after each turn in --session mode (env TURN_HOOK); ignored when --session is false")
	flag.Parse()

	if workspace == "" {
		fatal("workspace is required (--workspace or $WORKSPACE)")
	}
	if prompt == "" {
		fatal("prompt is required (--prompt or $TASK_PROMPT)")
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
			TurnHook: turnHook, Verbose: verbose,
			OrchBaseURL: orchBase, RunID: runID, RunToken: runToken,
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

	if err := run(sigCtx, agentBin, agentArgs, workspace, prompt, verbose); err != nil {
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

func run(ctx context.Context, agentBin string, agentArgs []string, workspace, prompt string, verbose bool) error {
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

	newSess, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        workspace,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		return fmt.Errorf("session/new: %s", describeErr(err))
	}
	logf("session created: %s", newSess.SessionId)

	logf("prompting: %q", truncate(prompt, 200))
	promptResp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: newSess.SessionId,
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

// driverClient implements acp.Client. In full_access mode jcode never calls
// RequestPermission, and jcode uses its own LocalExecutor for file/terminal
// ops (not the client fs methods), so these are safe fallbacks.
type driverClient struct {
	workspace string
	emitter   *Emitter
}

var _ acp.Client = (*driverClient)(nil)

// SessionUpdate is on jcode's hot path. It logs to stderr for local debugging
// and hands the notification to the mapper, which queues run events on the
// non-blocking emitter (never blocks the agent loop).
func (c *driverClient) SessionUpdate(_ context.Context, params acp.SessionNotification) error {
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

// RequestPermission should never fire in full_access mode. If it somehow does,
// auto-select the first "allow"-like option so an unattended run cannot hang.
func (c *driverClient) RequestPermission(_ context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	fmt.Fprintln(os.Stderr, "[warn] agent requested permission unexpectedly; auto-allowing")
	for _, opt := range params.Options {
		if opt.Kind == acp.PermissionOptionKindAllowOnce || opt.Kind == acp.PermissionOptionKindAllowAlways {
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
			}}, nil
		}
	}
	if len(params.Options) > 0 {
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{OptionId: params.Options[0].OptionId},
		}}, nil
	}
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
		Cancelled: &acp.RequestPermissionOutcomeCancelled{},
	}}, nil
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
