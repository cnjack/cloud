package main

// emitter.go — a non-blocking event pipeline that ships the agent's ACP activity
// (text, tool calls, tool results) to the orchestrator as run events.
//
// Design constraints (see cloud/docs/11-api.md §5.1 and the runner task):
//   - MUST NOT block the agent loop: SessionUpdate is on jcode's hot path, so
//     Emit() only does a non-blocking channel send. If the buffer is full it
//     drops the OLDEST queued event (the freshest activity matters most for a
//     live console) and records the drop; a single agent.text note is emitted
//     downstream so the console shows a gap marker.
//   - Batches events (flush every flushInterval OR when batchMax buffered) and
//     POSTs them to /internal/v1/runs/{id}/events with Bearer RUN_TOKEN.
//   - Idempotent + retryable: each event carries a monotonic client seq. The
//     orchestrator dedupes by (run_id, "runner", client_seq) and allocates the
//     authoritative global seq server-side, so re-sending a batch after a 5xx or
//     network error is safe and never collides with orchestrator-internal events.
//   - Retries on network errors / 5xx with capped exponential backoff; gives up a
//     batch after maxAttempts. maxAttempts alone bounds one batch, not the whole
//     shutdown drain (a full buffer can be ~100 batches) — so Close's final
//     drain is additionally bounded by an overall shutdownDeadline (default
//     10s) and abandons the rest of the queue after the first batch that fails
//     permanently, so a wedged/unreachable orchestrator can never stall
//     container shutdown for more than shutdownDeadline.
//
// The emitter is entirely best-effort: the agent run's success is judged by the
// diff, not by event delivery, so every failure here is logged and swallowed.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// Event types (mirror cloud/docs/11-api.md §4). Kept as local constants so the
// runner has no dependency on the orchestrator module.
const (
	eventAgentText       = "agent.text"
	eventAgentToolCall   = "agent.tool_call"
	eventAgentToolResult = "agent.tool_result"
	eventRunFailure      = "run.failure"
	// eventRunSession is emitted exactly once per ACP session establishment
	// (session/new OR session/load), carrying the ACP session id and whether it
	// was resumed. F9b (orchestrator ingest, not yet built) persists this so a
	// later warm-wake (D23 ①②, docs/02-decision-log.md / docs/14-cloud-v2-design.md
	// §4) knows which id to hand back to acpdrive's --resume.
	eventRunSession = "run.session"
	// eventAgentPermissionRequest/eventAgentPermissionResolved (F8a, D22's
	// permission half): the runner side of interactive approval-mode
	// permission forwarding. See EmitPermissionRequestSync /
	// EmitPermissionResolved below and permission.go's package doc comment
	// for the full contract (F8b, orchestrator ingest, not yet built,
	// consumes these).
	eventAgentPermissionRequest  = "agent.permission_request"
	eventAgentPermissionResolved = "agent.permission_resolved"
)

// event is one queued run event. Seq is assigned by the emitter (monotonic from
// 1) and used only as the per-source idempotency key.
type event struct {
	Seq     int64          `json:"seq"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// Emitter buffers events and ships them to the orchestrator in the background.
// A nil *Emitter is a valid no-op (used when the orchestrator env is absent, so
// the runner still works standalone / in the pure-headless proof).
type Emitter struct {
	baseURL string
	runID   string
	token   string
	client  *http.Client

	flushInterval    time.Duration
	batchMax         int
	maxAttempts      int
	shutdownDeadline time.Duration

	mu      sync.Mutex
	seq     int64
	dropped int64

	ch   chan event
	done chan struct{}
	// flushReq carries Flush() requests to the loop: each request is an ack
	// channel the loop closes once everything queued so far has been sent.
	flushReq chan chan struct{}
	wg       sync.WaitGroup
}

// EmitterConfig configures a new emitter. Zero values fall back to defaults.
type EmitterConfig struct {
	BaseURL       string        // ORCH_BASE_URL
	RunID         string        // RUN_ID
	Token         string        // RUN_TOKEN
	BufferSize    int           // channel capacity (default 1024)
	FlushInterval time.Duration // default 500ms
	BatchMax      int           // flush when this many buffered (default 10)
	MaxAttempts   int           // per-batch send attempts (default 5)
	HTTPTimeout   time.Duration // per-request timeout (default 10s)

	// ShutdownDeadline bounds the TOTAL time Close's final drain may spend
	// trying to flush the queue (default 10s). It is a wall-clock budget for
	// the whole drain, not a per-batch timeout: with a full buffer and an
	// unreachable orchestrator, retrying every batch to exhaustion could
	// otherwise take ~55s per batch (maxAttempts * (HTTPTimeout + backoff)) *
	// up to ~100 batches, blocking container shutdown for tens of minutes.
	// Once the deadline elapses, or the first batch during shutdown fails
	// permanently (retries exhausted or a non-retryable status), the rest of
	// the queue is abandoned — event delivery is always best-effort and must
	// never hold up the run's exit.
	ShutdownDeadline time.Duration
}

// NewEmitter returns a started emitter, or nil if baseURL/runID/token are not all
// present (in which case Emit is a safe no-op and the runner behaves exactly as
// before this pipeline existed).
func NewEmitter(cfg EmitterConfig) *Emitter {
	if cfg.BaseURL == "" || cfg.RunID == "" || cfg.Token == "" {
		return nil
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1024
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 500 * time.Millisecond
	}
	if cfg.BatchMax <= 0 {
		cfg.BatchMax = 10
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	if cfg.ShutdownDeadline <= 0 {
		cfg.ShutdownDeadline = 10 * time.Second
	}
	e := &Emitter{
		baseURL:          cfg.BaseURL,
		runID:            cfg.RunID,
		token:            cfg.Token,
		client:           &http.Client{Timeout: cfg.HTTPTimeout},
		flushInterval:    cfg.FlushInterval,
		batchMax:         cfg.BatchMax,
		maxAttempts:      cfg.MaxAttempts,
		shutdownDeadline: cfg.ShutdownDeadline,
		ch:               make(chan event, cfg.BufferSize),
		done:             make(chan struct{}),
		flushReq:         make(chan chan struct{}),
	}
	e.wg.Add(1)
	go e.loop()
	return e
}

// NewEmitterFromEnv builds an emitter from ORCH_BASE_URL / RUN_ID / RUN_TOKEN.
func NewEmitterFromEnv() *Emitter {
	return NewEmitter(EmitterConfig{
		BaseURL: os.Getenv("ORCH_BASE_URL"),
		RunID:   os.Getenv("RUN_ID"),
		Token:   os.Getenv("RUN_TOKEN"),
	})
}

// Emit queues one event without blocking. If the buffer is full it drops the
// oldest queued event to make room for this (newer) one and bumps the drop
// counter, which is surfaced as a note event at the next flush.
func (e *Emitter) Emit(typ string, payload map[string]any) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.seq++
	ev := event{Seq: e.seq, Type: typ, Payload: payload}
	e.mu.Unlock()

	for {
		select {
		case e.ch <- ev:
			return
		default:
			// Buffer full: drop the oldest to prioritise fresh activity.
			select {
			case <-e.ch:
				e.mu.Lock()
				e.dropped++
				e.mu.Unlock()
			default:
				// Raced empty; retry the send.
			}
		}
	}
}

// EmitText is a convenience for agent.text.
func (e *Emitter) EmitText(text string) {
	if e == nil || text == "" {
		return
	}
	e.Emit(eventAgentText, map[string]any{"text": text})
}

// EmitSession is a convenience for run.session (F9a): emitted once right after
// an ACP session is established, whether by session/new (resumed=false) or
// session/load (resumed=true). Payload shape is the F9b ingest contract:
//
//	{"acp_session_id": "<id>", "resumed": bool}
func (e *Emitter) EmitSession(acpSessionID string, resumed bool) {
	if e == nil {
		return
	}
	e.Emit(eventRunSession, map[string]any{
		"acp_session_id": acpSessionID,
		"resumed":        resumed,
	})
}

// EmitPermissionRequestSync is F8a's runner-side half of D22's permission
// forwarding contract: emitted once per ACP RequestPermission call forwarded
// for interactive approval (RUN_PERMISSION_MODE=approval). Payload shape is
// the F8b ingest contract:
//
//	{"request_id","tool_call_id","title","options":[{"option_id","name","kind"}]}
//
// UNLIKE every other emit method, this one is SYNCHRONOUS and at-least-once:
// it bypasses the async batch queue and POSTs the event directly, retrying
// network/5xx failures with capped backoff until it is acknowledged (2xx) or
// ctx expires, and returns nil ONLY once the control plane has accepted it.
// The caller (forwardPermissionRequest, permission.go) MUST NOT start polling
// the decision endpoint before this returns nil: the decision endpoint
// answers 404 only for requests that once existed and expired, so polling
// for a request the server has never seen would misread "still in flight in
// an async batch" as "expired" and instantly timeout-deny a perfectly
// pending approval (the P1 race this method exists to close).
//
// Blocking here is fine: RequestPermission is the one client method that is
// SUPPOSED to block (each inbound ACP request runs in its own goroutine, see
// driverClient.RequestPermission in main.go), so the "never block the agent
// loop" rule that forces every other emit through the async queue does not
// apply on this path.
//
// Ordering note: because this jumps the queue, the permission_request may
// reach the server before earlier queued events (e.g. the agent.tool_call it
// refers to) have flushed. That is acceptable: the client Seq (still
// allocated from the same monotonic counter, so still a valid idempotency
// key) is not the display order — the server allocates the authoritative
// global seq at arrival — and F8b keys the approval UI on request_id, not on
// event adjacency.
func (e *Emitter) EmitPermissionRequestSync(ctx context.Context, requestID, toolCallID, title string, options []acp.PermissionOption) error {
	if e == nil {
		// No control plane wired: there is no one to ask for approval, so the
		// caller's timeout-deny path is the only safe answer.
		return fmt.Errorf("no event emitter configured (ORCH_BASE_URL/RUN_ID/RUN_TOKEN absent)")
	}
	opts := make([]map[string]any, 0, len(options))
	for _, o := range options {
		opts = append(opts, map[string]any{
			"option_id": string(o.OptionId),
			"name":      o.Name,
			"kind":      string(o.Kind),
		})
	}
	e.mu.Lock()
	e.seq++
	ev := event{Seq: e.seq, Type: eventAgentPermissionRequest, Payload: map[string]any{
		"request_id":   requestID,
		"tool_call_id": toolCallID,
		"title":        title,
		"options":      opts,
	}}
	e.mu.Unlock()

	body, err := json.Marshal(map[string]any{"events": []event{ev}})
	if err != nil {
		return fmt.Errorf("marshal permission_request event: %w", err)
	}
	url := fmt.Sprintf("%s/internal/v1/runs/%s/events", e.baseURL, e.runID)

	// Same retry philosophy as the control-plane client (network/5xx retried
	// with capped exponential backoff), but bounded by ctx rather than a
	// fixed attempt count: the caller's ctx already carries the permission
	// timeout, which is exactly the budget this delivery is allowed to spend.
	backoff := 200 * time.Millisecond
	for {
		ok, retryable := e.postCtx(ctx, url, body)
		if ok {
			return nil
		}
		if !retryable {
			return fmt.Errorf("permission_request event rejected by the control plane (non-retryable status)")
		}
		fmt.Fprintf(os.Stderr, "[emitter] permission_request sync POST failed; retrying in %s\n", backoff)
		if !sleepCtx(ctx, backoff) {
			return fmt.Errorf("permission_request event not delivered before the deadline: %w", ctx.Err())
		}
		backoff = nextBackoff(backoff, 5*time.Second)
	}
}

// EmitPermissionResolved pairs 1:1 with a prior EmitPermissionRequest for the
// same requestID: resolution is "user" (the orchestrator's decision endpoint
// returned a choice) or "timeout" (PERMISSION_TIMEOUT_SECONDS elapsed, or the
// decision endpoint reported the request expired/invalid — 404/410 — which is
// indistinguishable to the agent, see controlPlaneClient.waitForPermissionDecision).
// optionID is "" only in the degenerate case of a tool call offering zero
// permission options.
func (e *Emitter) EmitPermissionResolved(requestID, optionID, resolution string) {
	if e == nil {
		return
	}
	e.Emit(eventAgentPermissionResolved, map[string]any{
		"request_id": requestID,
		"option_id":  optionID,
		"resolution": resolution,
	})
}

// Close flushes remaining events and stops the background loop. It blocks
// until the queue drains, the first batch fails permanently during shutdown,
// or shutdownDeadline (default 10s) elapses — whichever comes first — so a
// wedged/unreachable orchestrator can never block the caller (and therefore
// container shutdown) for more than that budget.
// Flush blocks until every event queued so far has been POSTed (in order),
// then returns. Used by the session loop before reporting turn-complete: the
// orchestrator parks the run in awaiting_input on that POST, so any text
// still sitting in the batch buffer would be stored AFTER the status event —
// the console then renders the status row splitting the turn's final message.
// Flushing first keeps the stored seq order matching the order things happened.
// Best-effort like everything else here: it returns when the queue is drained
// or the emitter is closed mid-wait, whichever comes first.
func (e *Emitter) Flush() {
	if e == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case e.flushReq <- ack:
	case <-e.done:
		return
	}
	select {
	case <-ack:
	case <-e.done:
	}
}

func (e *Emitter) Close() {
	if e == nil {
		return
	}
	close(e.done)
	e.wg.Wait()
}

func (e *Emitter) loop() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()

	batch := make([]event, 0, e.batchMax)
	drain := func() {
		for len(batch) < e.batchMax {
			select {
			case ev := <-e.ch:
				batch = append(batch, ev)
			default:
				return
			}
		}
	}

	for {
		select {
		case ev := <-e.ch:
			batch = append(batch, ev)
			drain()
			if len(batch) >= e.batchMax {
				e.send(batch, time.Time{})
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				e.send(batch, time.Time{})
				batch = batch[:0]
			}
		case req := <-e.flushReq:
			// Flush(): push out everything queued so far, in order, then ack.
			drain()
			for len(batch) > 0 {
				e.send(batch, time.Time{})
				batch = batch[:0]
				drain()
			}
			close(req)
		case <-e.done:
			// Final drain: pull everything still queued and flush in batches,
			// but bounded by an overall deadline so a wedged/unreachable
			// orchestrator can never stall container shutdown. Abandon the
			// rest of the queue as soon as either the deadline elapses or one
			// batch fails permanently (retries exhausted, or a non-retryable
			// status) — if the orchestrator can't take one batch it won't
			// take the next hundred either, so there's no point paying the
			// per-batch retry cost for each of them.
			shutdownDeadline := time.Now().Add(e.shutdownDeadline)
			for {
				drain()
				if len(batch) == 0 {
					break
				}
				if time.Now().After(shutdownDeadline) {
					fmt.Fprintf(os.Stderr, "[emitter] shutdown deadline exceeded; abandoning remaining queued events\n")
					break
				}
				if ok := e.send(batch, shutdownDeadline); !ok {
					fmt.Fprintf(os.Stderr, "[emitter] batch failed during shutdown drain; abandoning remaining queued events\n")
					break
				}
				batch = batch[:0]
			}
			return
		}
	}
}

// send POSTs a batch with retry/backoff, returning whether it was ultimately
// delivered. A drop note (if any drops occurred) is prepended so the console
// can render a "N events dropped" marker. Best-effort: on permanent failure
// the batch is logged and abandoned (returns false), and the caller decides
// what to do next — the hot-path callers (batchMax/ticker flushes) ignore the
// result since event delivery must never block the agent loop, while the
// shutdown drain in loop() uses it to abandon the rest of the queue rather
// than retry every remaining batch against a dead orchestrator.
//
// deadline, if non-zero, additionally bounds the retry loop: once passed, send
// stops retrying and gives up immediately instead of sleeping through another
// backoff. This is what keeps Close()'s final drain within shutdownDeadline
// even when a single batch is mid-retry when the deadline arrives.
func (e *Emitter) send(batch []event, deadline time.Time) bool {
	if len(batch) == 0 {
		return true
	}
	// Snapshot & reset the drop counter, injecting a note event if needed.
	e.mu.Lock()
	dropped := e.dropped
	e.dropped = 0
	if dropped > 0 {
		e.seq++
		note := event{Seq: e.seq, Type: eventAgentText, Payload: map[string]any{
			"text":    fmt.Sprintf("[runner] dropped %d event(s) due to backpressure", dropped),
			"dropped": dropped,
		}}
		batch = append([]event{note}, batch...)
	}
	e.mu.Unlock()

	body, err := json.Marshal(map[string]any{"events": batch})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[emitter] marshal batch: %v\n", err)
		return false
	}
	url := fmt.Sprintf("%s/internal/v1/runs/%s/events", e.baseURL, e.runID)

	backoff := 200 * time.Millisecond
	for attempt := 1; attempt <= e.maxAttempts; attempt++ {
		ok, retryable := e.post(url, body)
		if ok {
			return true
		}
		if !retryable || attempt == e.maxAttempts {
			fmt.Fprintf(os.Stderr, "[emitter] giving up on batch of %d after %d attempt(s)\n", len(batch), attempt)
			return false
		}
		if !deadline.IsZero() && time.Now().Add(backoff).After(deadline) {
			fmt.Fprintf(os.Stderr, "[emitter] shutdown deadline would be exceeded by next retry; giving up on batch of %d after %d attempt(s)\n", len(batch), attempt)
			return false
		}
		time.Sleep(backoff)
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
	return false
}

// post sends one request. Returns (ok, retryable). 2xx => ok. 5xx / network =>
// retryable. 4xx (except 429) => permanent failure (won't succeed on retry).
func (e *Emitter) post(url string, body []byte) (ok, retryable bool) {
	return e.postCtx(context.Background(), url, body)
}

// postCtx is post bounded by BOTH the per-request HTTP timeout and the given
// ctx (used by EmitPermissionRequestSync so a request in flight when the
// permission timeout fires is cut short rather than running out its full
// HTTP timeout past the caller's deadline).
func (e *Emitter) postCtx(ctx context.Context, url string, body []byte) (ok, retryable bool) {
	reqCtx, cancel := context.WithTimeout(ctx, e.client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)

	resp, err := e.client.Do(req)
	if err != nil {
		return false, true // network error: retryable
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, true
	default:
		fmt.Fprintf(os.Stderr, "[emitter] non-retryable status %d posting events\n", resp.StatusCode)
		return false, false
	}
}
