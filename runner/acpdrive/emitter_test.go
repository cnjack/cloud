package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// captureServer records ingested event batches and lets a test script status
// codes (e.g. to force retries).
type captureServer struct {
	mu      sync.Mutex
	batches [][]event
	calls   int32
	// statusFor returns the HTTP status for call n (1-based); default 200.
	statusFor func(n int32) int
}

func (c *captureServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&c.calls, 1)
		status := http.StatusOK
		if c.statusFor != nil {
			status = c.statusFor(n)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var req struct {
			Events []event `json:"events"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		c.mu.Lock()
		c.batches = append(c.batches, req.Events)
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted":` + itoa(len(req.Events)) + `}`))
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func (c *captureServer) allEvents() []event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []event
	for _, b := range c.batches {
		out = append(out, b...)
	}
	return out
}

func newTestEmitter(t *testing.T, url string, cfg EmitterConfig) *Emitter {
	t.Helper()
	cfg.BaseURL = url
	cfg.RunID = "run-test"
	cfg.Token = "tok"
	e := NewEmitter(cfg)
	if e == nil {
		t.Fatal("emitter is nil")
	}
	return e
}

func TestEmitterBatchesAndAuth(t *testing.T) {
	cs := &captureServer{}
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		cs.handler().ServeHTTP(w, r)
	}))
	defer ts.Close()

	e := newTestEmitter(t, ts.URL, EmitterConfig{FlushInterval: 20 * time.Millisecond, BatchMax: 100})
	for i := 0; i < 5; i++ {
		e.EmitText("chunk")
	}
	e.Close() // flushes remaining

	evs := cs.allEvents()
	if len(evs) != 5 {
		t.Fatalf("delivered %d events want 5", len(evs))
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth header = %q want 'Bearer tok'", gotAuth)
	}
	// Client seq must be monotonic from 1 (idempotency key).
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d seq=%d want %d", i, ev.Seq, i+1)
		}
	}
}

func TestEmitterFlushesOnBatchMax(t *testing.T) {
	cs := &captureServer{}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()

	// Long flush interval so ONLY batchMax can trigger the first flush.
	e := newTestEmitter(t, ts.URL, EmitterConfig{FlushInterval: 10 * time.Second, BatchMax: 3})
	for i := 0; i < 3; i++ {
		e.EmitText("x")
	}
	// Wait for the size-triggered flush (not the timer).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(cs.allEvents()) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Close()
	if n := len(cs.allEvents()); n != 3 {
		t.Fatalf("delivered %d want 3 (batchMax flush)", n)
	}
}

func TestEmitterRetriesOn5xx(t *testing.T) {
	cs := &captureServer{
		// First two calls 503, third succeeds.
		statusFor: func(n int32) int {
			if n < 3 {
				return http.StatusServiceUnavailable
			}
			return http.StatusOK
		},
	}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()

	e := newTestEmitter(t, ts.URL, EmitterConfig{FlushInterval: 10 * time.Millisecond, MaxAttempts: 5})
	e.EmitText("hello")
	e.Close()

	if n := atomic.LoadInt32(&cs.calls); n < 3 {
		t.Fatalf("calls=%d want >=3 (retried through 2x503)", n)
	}
	if evs := cs.allEvents(); len(evs) != 1 || evs[0].Payload["text"] != "hello" {
		t.Fatalf("events after retry = %+v want one 'hello'", evs)
	}
}

func TestEmitterDropsOldestUnderBackpressure(t *testing.T) {
	cs := &captureServer{}
	// Server blocks until released so the loop can't drain; buffer fills => drops.
	release := make(chan struct{})
	var blockedOnce sync.Once
	blocked := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blockedOnce.Do(func() { close(blocked) })
		<-release
		cs.handler().ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Tiny buffer so drops are forced deterministically.
	e := newTestEmitter(t, ts.URL, EmitterConfig{
		BufferSize: 4, FlushInterval: 5 * time.Millisecond, BatchMax: 1000, MaxAttempts: 1,
	})
	// Prime one event so the loop enters send() and blocks on the server.
	e.Emit(eventAgentText, map[string]any{"i": -1})
	<-blocked
	// Now the loop is stuck; these pile into the size-4 buffer and overflow,
	// dropping the oldest each time.
	for i := 0; i < 50; i++ {
		e.Emit(eventAgentText, map[string]any{"i": i})
	}
	e.mu.Lock()
	dropped := e.dropped
	e.mu.Unlock()
	if dropped == 0 {
		t.Fatal("expected some events to be dropped under backpressure")
	}
	close(release) // let the server drain
	e.Close()

	// A drop note ("dropped N event(s)") must have been surfaced to the console.
	var sawNote bool
	for _, ev := range cs.allEvents() {
		if d, ok := ev.Payload["dropped"]; ok {
			_ = d
			sawNote = true
		}
	}
	if !sawNote {
		t.Fatal("expected a drop-note event to be delivered after backpressure")
	}
}

// TestEmitterCloseBoundedWhenOrchestratorUnreachable proves the fix for the
// "Close() can block the runner container for tens of minutes" issue: with a
// full buffer and an orchestrator that never responds, Close must still
// return within roughly ShutdownDeadline, not maxAttempts*batches*HTTPTimeout
// (which for a full 1024-event buffer used to be on the order of 90 minutes).
func TestEmitterCloseBoundedWhenOrchestratorUnreachable(t *testing.T) {
	// A listener that accepts TCP connections but never writes an HTTP
	// response, so every request hangs until the client's HTTPTimeout fires.
	// This simulates an orchestrator that is unreachable/wedged (as opposed to
	// actively refusing, which would fail fast and not exercise the deadline).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Never respond; just hold the connection open until the client
			// gives up (its Timeout) or closes it.
			go func(c net.Conn) {
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						c.Close()
						return
					}
				}
			}(conn)
		}
	}()

	const shutdownDeadline = 500 * time.Millisecond
	e := newTestEmitter(t, "http://"+ln.Addr().String(), EmitterConfig{
		BufferSize: 1024,
		// FlushInterval/BatchMax deliberately large enough that filling the
		// buffer below can never trigger a normal-path (undeadlined) send
		// before Close() is called: this test isolates the shutdown drain
		// specifically (the thing the finding + fix are about), not the
		// pre-existing per-batch maxAttempts bound on the hot path.
		FlushInterval:    time.Hour,
		BatchMax:         100000,
		MaxAttempts:      5,
		HTTPTimeout:      50 * time.Millisecond,
		ShutdownDeadline: shutdownDeadline,
	})

	// Fill the buffer so the shutdown drain has many batches to (fail to) send.
	for i := 0; i < 1024; i++ {
		e.Emit(eventAgentText, map[string]any{"i": i})
	}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		e.Close()
		close(done)
	}()

	// Generous upper bound over shutdownDeadline: the drain loop's first batch
	// send isn't itself deadline-preemptible mid-request (only the retry
	// backoff between attempts is), so one attempt's full HTTPTimeout can run
	// past the deadline before it's next checked. That slop is O(HTTPTimeout),
	// not O(bufferSize/batchMax * maxAttempts * HTTPTimeout) — which is
	// exactly the bug this test proves is fixed (that used to be ~90 minutes
	// for a full 1024-event buffer).
	upperBound := shutdownDeadline + 5*time.Second
	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > upperBound {
			t.Fatalf("Close() took %s, want <= %s (shutdownDeadline=%s)", elapsed, upperBound, shutdownDeadline)
		}
		t.Logf("Close() returned in %s (shutdownDeadline=%s)", elapsed, shutdownDeadline)
	case <-time.After(upperBound):
		t.Fatalf("Close() did not return within %s (shutdownDeadline=%s) — shutdown is unbounded", upperBound, shutdownDeadline)
	}
}

func TestEmitterNilIsNoop(t *testing.T) {
	var e *Emitter // nil
	e.EmitText("nothing")
	e.Emit(eventAgentToolCall, map[string]any{"name": "x"})
	e.Close() // must not panic
}

// --- mapper tests ---

func TestMapAgentText(t *testing.T) {
	cs := &captureServer{}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()
	e := newTestEmitter(t, ts.URL, EmitterConfig{FlushInterval: 10 * time.Millisecond})

	mapSessionUpdate(e, acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("I will read the README\n"),
		},
	})
	e.Close()
	evs := cs.allEvents()
	if len(evs) != 1 || evs[0].Type != eventAgentText {
		t.Fatalf("events = %+v want one agent.text", evs)
	}
	if evs[0].Payload["text"] != "I will read the README\n" {
		t.Fatalf("text = %q (streamed chunk must be verbatim, trailing newline kept)", evs[0].Payload["text"])
	}
}

// Regression for the run-detail markdown rendering bug: markdown structure
// lives in the newlines BETWEEN chunks. A model that streams line-aligned
// chunks ("# T\n", "\n", "- a\n", …) had every line ending eaten by the old
// per-chunk TrimRight, so the console's concatenated message flattened into
// one unreadable line (raw "##", "| --- |" visible). Chunks must pass through
// verbatim — including chunks that are nothing BUT a newline.
func TestMapAgentTextKeepsChunkNewlines(t *testing.T) {
	cs := &captureServer{}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()
	e := newTestEmitter(t, ts.URL, EmitterConfig{FlushInterval: 10 * time.Millisecond})

	chunks := []string{"# 报告\n", "\n", "- a\n", "- b\n", "\n", "| c | d |\n", "|---|---|\n"}
	for _, chunk := range chunks {
		mapSessionUpdate(e, acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(chunk)},
		})
	}
	e.Close()

	evs := cs.allEvents()
	if len(evs) != len(chunks) {
		t.Fatalf("events = %d, want %d (newline-only chunks must not be dropped)", len(evs), len(chunks))
	}
	var sb strings.Builder
	for _, ev := range evs {
		if ev.Type != eventAgentText {
			t.Fatalf("event type = %q, want agent.text", ev.Type)
		}
		text, _ := ev.Payload["text"].(string)
		sb.WriteString(text)
	}
	if got, want := sb.String(), strings.Join(chunks, ""); got != want {
		t.Fatalf("concatenated text = %q, want verbatim %q", got, want)
	}
}

func TestMapToolCallAndResult(t *testing.T) {
	cs := &captureServer{}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()
	e := newTestEmitter(t, ts.URL, EmitterConfig{FlushInterval: 10 * time.Millisecond, BatchMax: 100})

	// Tool call start.
	mapSessionUpdate(e, acp.SessionUpdate{
		ToolCall: &acp.SessionUpdateToolCall{
			ToolCallId: "tc_1",
			Title:      "Write HELLO.txt",
			Kind:       acp.ToolKindEdit,
			Status:     acp.ToolCallStatusPending,
			RawInput:   map[string]any{"file_path": "HELLO.txt", "content": "hi"},
		},
	})
	// Intermediate in_progress update: must NOT emit a result.
	inProg := acp.ToolCallStatusInProgress
	mapSessionUpdate(e, acp.SessionUpdate{
		ToolCallUpdate: &acp.SessionToolCallUpdate{ToolCallId: "tc_1", Status: &inProg},
	})
	// Terminal completed update: emit a result.
	done := acp.ToolCallStatusCompleted
	mapSessionUpdate(e, acp.SessionUpdate{
		ToolCallUpdate: &acp.SessionToolCallUpdate{
			ToolCallId: "tc_1", Status: &done, RawOutput: "wrote 2 bytes",
		},
	})
	e.Close()

	evs := cs.allEvents()
	if len(evs) != 2 {
		t.Fatalf("events = %d want 2 (call + result, no in_progress result)", len(evs))
	}
	call := evs[0]
	if call.Type != eventAgentToolCall {
		t.Fatalf("event0 type = %s want agent.tool_call", call.Type)
	}
	if call.Payload["name"] != "edit" || call.Payload["call_id"] != "tc_1" {
		t.Fatalf("tool_call payload = %+v", call.Payload)
	}
	if _, ok := call.Payload["args"].(map[string]any); !ok {
		t.Fatalf("tool_call args missing/not object: %+v", call.Payload["args"])
	}
	res := evs[1]
	if res.Type != eventAgentToolResult {
		t.Fatalf("event1 type = %s want agent.tool_result", res.Type)
	}
	if res.Payload["call_id"] != "tc_1" || res.Payload["is_error"] != false {
		t.Fatalf("tool_result payload = %+v", res.Payload)
	}
	if res.Payload["output"] != "wrote 2 bytes" {
		t.Fatalf("output = %q want 'wrote 2 bytes'", res.Payload["output"])
	}
}

func TestMapToolResultFailure(t *testing.T) {
	cs := &captureServer{}
	ts := httptest.NewServer(cs.handler())
	defer ts.Close()
	e := newTestEmitter(t, ts.URL, EmitterConfig{FlushInterval: 10 * time.Millisecond})

	failed := acp.ToolCallStatusFailed
	mapSessionUpdate(e, acp.SessionUpdate{
		ToolCallUpdate: &acp.SessionToolCallUpdate{
			ToolCallId: "tc_9", Status: &failed, RawOutput: "Tool execution failed: boom",
		},
	})
	e.Close()
	evs := cs.allEvents()
	if len(evs) != 1 || evs[0].Payload["is_error"] != true {
		t.Fatalf("expected one failed tool_result, got %+v", evs)
	}
}
