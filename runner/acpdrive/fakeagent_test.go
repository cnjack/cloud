package main

// fakeagent_test.go — a scriptable ACP agent used ONLY to test acpdrive's own
// client-side session loop (session.go) end to end, without a real jcode
// binary. It re-execs the compiled test binary itself as a subprocess (the
// standard Go "TestMain helper process" pattern, e.g. used by net/http and
// os/exec's own tests) and speaks real ACP JSON-RPC over stdio, so runSession
// exercises its actual exec.Command + stdin/stdout wiring, not a mock.
//
// Controlled entirely via env vars (inherited from the test process through
// runSession's cmd.Env = os.Environ()):
//
//	ACPDRIVE_TEST_FAKE_AGENT=1   switches TestMain into "be the fake agent" mode
//	FAKE_AGENT_LOG=<path>        NDJSON call log the test asserts against
//	FAKE_AGENT_SESSION_ID=<id>   sessionId returned by session/new (default sess_fake)
//	FAKE_AGENT_STOP_REASONS=a,b,c  comma-separated stop reasons for successive
//	                              Prompt calls; the last entry repeats once
//	                              exhausted; default "end_turn"
//	FAKE_AGENT_LOAD_SESSION_ERR=msg  if set, session/load returns a JSON-RPC
//	                              error with this message instead of succeeding
//	                              (F9a resume-failure fail-visible test)
//	FAKE_AGENT_REPLAY_TEXTS=a,b   comma-separated agent_message_chunk texts the
//	                              fake REPLAYS via session/update DURING
//	                              session/load, before returning — mimicking
//	                              jcode's ACP-spec transcript replay (F9a:
//	                              acpdrive must DROP these, never re-emit them)
//	FAKE_AGENT_LIVE_TEXT=txt      if set, each Prompt call sends one LIVE
//	                              session/update agent_message_chunk
//	                              "txt:<turn>" before returning, so tests can
//	                              assert real-turn streaming still flows after
//	                              the replay gate opens
//	FAKE_AGENT_SET_SESSION_MODE_ERR=msg  if set, SetSessionMode returns a
//	                              JSON-RPC error with this message instead of
//	                              succeeding (F8a fail-visible-on-failed-
//	                              set_mode test)
//	FAKE_AGENT_REQUEST_PERMISSION=1  each Prompt call issues ONE
//	                              session/request_permission back to the
//	                              client before returning (F8a end-to-end
//	                              forwarding test) — see
//	                              FAKE_AGENT_PERMISSION_* below and the
//	                              "request_permission_result" log entry this
//	                              produces (method/turn/outcome/option_id)
//	FAKE_AGENT_PERMISSION_TOOL_CALL_ID=id   default "tc_perm_1"
//	FAKE_AGENT_PERMISSION_TITLE=txt         default "Run rm -rf /tmp/x"
//	FAKE_AGENT_PERMISSION_OPTIONS=id:kind:name,...  comma-separated options,
//	                              each "optionId:kind:name" (colon-separated);
//	                              default two options,
//	                              "opt_allow:allow_once:Allow once" and
//	                              "opt_reject:reject_once:Reject once"; the
//	                              special value "none" means ZERO options
//	                              (the F8a P2 degenerate case — an empty,
//	                              non-nil list, which the ACP request
//	                              validation accepts)

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestMain intercepts re-exec'd fake-agent invocations before the normal
// `go test` machinery (flag parsing, -test.run, …) ever runs. This must be
// the ONLY TestMain in the package.
func TestMain(m *testing.M) {
	if os.Getenv("ACPDRIVE_TEST_FAKE_AGENT") == "1" {
		runFakeAgentProcess()
		return
	}
	os.Exit(m.Run())
}

func runFakeAgentProcess() {
	stopReasons := []string{"end_turn"}
	if v := os.Getenv("FAKE_AGENT_STOP_REASONS"); v != "" {
		stopReasons = strings.Split(v, ",")
	}
	sessionID := acp.SessionId(envOr("FAKE_AGENT_SESSION_ID", "sess_fake"))
	var replayTexts []string
	if v := os.Getenv("FAKE_AGENT_REPLAY_TEXTS"); v != "" {
		replayTexts = strings.Split(v, ",")
	}
	fa := &fakeAgent{
		sessionID:            sessionID,
		stopReasons:          stopReasons,
		logPath:              os.Getenv("FAKE_AGENT_LOG"),
		loadSessionErr:       os.Getenv("FAKE_AGENT_LOAD_SESSION_ERR"),
		replayTexts:          replayTexts,
		liveText:             os.Getenv("FAKE_AGENT_LIVE_TEXT"),
		setSessionModeErr:    os.Getenv("FAKE_AGENT_SET_SESSION_MODE_ERR"),
		requestPermission:    os.Getenv("FAKE_AGENT_REQUEST_PERMISSION") == "1",
		permissionToolCallID: envOr("FAKE_AGENT_PERMISSION_TOOL_CALL_ID", "tc_perm_1"),
		permissionTitle:      envOr("FAKE_AGENT_PERMISSION_TITLE", "Run rm -rf /tmp/x"),
		permissionOptions:    parsePermissionOptions(os.Getenv("FAKE_AGENT_PERMISSION_OPTIONS")),
	}
	asc := acp.NewAgentSideConnection(fa, os.Stdout, os.Stdin)
	fa.conn = asc
	<-asc.Done()
	os.Exit(0)
}

// fakeAgent implements acp.Agent. Session lifecycle methods beyond
// Initialize/NewSession/Prompt are stubbed (mirrors the SDK's own
// example_agent_test.go); acpdrive's session loop never calls them.
type fakeAgent struct {
	conn        *acp.AgentSideConnection
	sessionID   acp.SessionId
	stopReasons []string
	logPath     string
	turn        int32 // atomic, incremented per Prompt call
	// loadSessionErr, if non-empty, makes LoadSession fail with this message
	// instead of succeeding (F9a resume-failure fail-visible test).
	loadSessionErr string
	// replayTexts, if non-empty, are sent as session/update
	// agent_message_chunk notifications DURING LoadSession, before it returns
	// — mimicking jcode's ACP-spec transcript replay.
	replayTexts []string
	// liveText, if non-empty, makes each Prompt send one live session/update
	// chunk "liveText:<turn>" before returning.
	liveText string
	// setSessionModeErr, if non-empty, makes SetSessionMode fail with this
	// message instead of succeeding (F8a fail-visible test).
	setSessionModeErr string
	// requestPermission, when true, makes each Prompt call issue one
	// session/request_permission back to the client before returning (F8a
	// end-to-end forwarding test).
	requestPermission    bool
	permissionToolCallID string
	permissionTitle      string
	permissionOptions    []acp.PermissionOption
}

// parsePermissionOptions decodes FAKE_AGENT_PERMISSION_OPTIONS
// ("id:kind:name,id:kind:name,...") into ACP permission options, defaulting
// to one allow + one reject option when unset — a realistic minimal set for
// the F8a forwarding tests. The special value "none" yields ZERO options as
// an empty-but-non-nil slice (RequestPermissionRequest validation rejects a
// nil Options), for the P2 degenerate-case tests.
func parsePermissionOptions(v string) []acp.PermissionOption {
	if v == "" {
		return []acp.PermissionOption{
			{OptionId: "opt_allow", Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow once"},
			{OptionId: "opt_reject", Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject once"},
		}
	}
	opts := []acp.PermissionOption{}
	if v == "none" {
		return opts
	}
	for _, entry := range strings.Split(v, ",") {
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 {
			continue
		}
		opts = append(opts, acp.PermissionOption{
			OptionId: acp.PermissionOptionId(parts[0]),
			Kind:     acp.PermissionOptionKind(parts[1]),
			Name:     parts[2],
		})
	}
	return opts
}

var _ acp.Agent = (*fakeAgent)(nil)

// fakeAgent also implements the optional acp.AgentLoader interface (F9a):
// acpdrive's session/load support is exercised end to end against this fake,
// exactly like NewSession/Prompt above, rather than mocked at the JSON-RPC
// transport level.
var _ acp.AgentLoader = (*fakeAgent)(nil)

func (a *fakeAgent) logEvent(v map[string]any) {
	if a.logPath == "" {
		return
	}
	f, err := os.OpenFile(a.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(v)
	_, _ = f.Write(append(b, '\n'))
}

func (a *fakeAgent) Initialize(_ context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.logEvent(map[string]any{"method": "initialize"})
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber}, nil
}

func (a *fakeAgent) NewSession(_ context.Context, _ acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.logEvent(map[string]any{"method": "session/new"})
	return acp.NewSessionResponse{SessionId: a.sessionID}, nil
}

// LoadSession (F9a): logs the call (session id + cwd) so tests can assert
// session/new was NOT also called, and fails with loadSessionErr when the
// test scripts a resume failure (FAKE_AGENT_LOAD_SESSION_ERR). On the success
// path it first REPLAYS replayTexts as session/update notifications — like
// jcode, which streams the loaded transcript back through session/update
// before answering session/load (ACP spec; the SDK's notification barrier
// delivers each one to the client handler before LoadSession returns).
func (a *fakeAgent) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.logEvent(map[string]any{
		"method":     "session/load",
		"session_id": string(p.SessionId),
		"cwd":        p.Cwd,
	})
	if a.loadSessionErr != "" {
		return acp.LoadSessionResponse{}, &acp.RequestError{Code: -32603, Message: a.loadSessionErr}
	}
	for _, txt := range a.replayTexts {
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: p.SessionId,
			Update: acp.SessionUpdate{
				AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(txt)},
			},
		})
	}
	return acp.LoadSessionResponse{}, nil
}

func (a *fakeAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	n := int(atomic.AddInt32(&a.turn, 1))
	text := ""
	if len(p.Prompt) > 0 && p.Prompt[0].Text != nil {
		text = p.Prompt[0].Text.Text
	}
	a.logEvent(map[string]any{
		"method":     "session/prompt",
		"turn":       n,
		"session_id": string(p.SessionId),
		"prompt":     text,
	})
	if a.liveText != "" {
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: p.SessionId,
			Update: acp.SessionUpdate{
				AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(fmt.Sprintf("%s:%d", a.liveText, n))},
			},
		})
	}
	// F8a: mimic jcode raising session/request_permission mid-turn. This is a
	// BLOCKING outbound (agent -> client) call from within this Prompt
	// handler goroutine — exactly what a real jcode approval-mode tool call
	// does — proving acpdrive's client-side handler can run the whole
	// emit/poll/resolve round trip without deadlocking session/prompt.
	if a.requestPermission {
		resp, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: p.SessionId,
			Options:   a.permissionOptions,
			ToolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId(a.permissionToolCallID),
				Title:      acp.Ptr(a.permissionTitle),
			},
		})
		outcome, optionID := "error", ""
		switch {
		case err != nil:
			outcome = "error:" + err.Error()
		case resp.Outcome.Selected != nil:
			outcome = "selected"
			optionID = string(resp.Outcome.Selected.OptionId)
		case resp.Outcome.Cancelled != nil:
			outcome = "cancelled"
		}
		a.logEvent(map[string]any{
			"method":    "request_permission_result",
			"turn":      n,
			"outcome":   outcome,
			"option_id": optionID,
		})
	}
	reason := a.stopReasons[len(a.stopReasons)-1]
	if n-1 < len(a.stopReasons) {
		reason = a.stopReasons[n-1]
	}
	return acp.PromptResponse{StopReason: acp.StopReason(reason)}, nil
}

// --- stubs (never exercised by acpdrive's session loop) ---

func (a *fakeAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}
func (a *fakeAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}
func (a *fakeAgent) Cancel(context.Context, acp.CancelNotification) error { return nil }
func (a *fakeAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}
func (a *fakeAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}
func (a *fakeAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

// SetSessionMode (F8a): logs the call (session id + mode id) so tests can
// assert approval mode was requested right after session establishment, and
// fails with setSessionModeErr when the test scripts a set_mode failure.
func (a *fakeAgent) SetSessionMode(_ context.Context, p acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	a.logEvent(map[string]any{
		"method":     "session/set_mode",
		"session_id": string(p.SessionId),
		"mode_id":    string(p.ModeId),
	})
	if a.setSessionModeErr != "" {
		return acp.SetSessionModeResponse{}, &acp.RequestError{Code: -32603, Message: a.setSessionModeErr}
	}
	return acp.SetSessionModeResponse{}, nil
}
func (a *fakeAgent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}
