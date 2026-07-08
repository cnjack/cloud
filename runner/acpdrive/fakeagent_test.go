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

import (
	"context"
	"encoding/json"
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
	fa := &fakeAgent{
		sessionID:   sessionID,
		stopReasons: stopReasons,
		logPath:     os.Getenv("FAKE_AGENT_LOG"),
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
}

var _ acp.Agent = (*fakeAgent)(nil)

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

func (a *fakeAgent) Prompt(_ context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
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
func (a *fakeAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}
func (a *fakeAgent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}
