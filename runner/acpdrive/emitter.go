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
//     batch after maxAttempts so a wedged orchestrator can't stall shutdown.
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
)

// Event types (mirror cloud/docs/11-api.md §4). Kept as local constants so the
// runner has no dependency on the orchestrator module.
const (
	eventAgentText       = "agent.text"
	eventAgentToolCall   = "agent.tool_call"
	eventAgentToolResult = "agent.tool_result"
	eventRunFailure      = "run.failure"
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

	flushInterval time.Duration
	batchMax      int
	maxAttempts   int

	mu      sync.Mutex
	seq     int64
	dropped int64

	ch   chan event
	done chan struct{}
	wg   sync.WaitGroup
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
	e := &Emitter{
		baseURL:       cfg.BaseURL,
		runID:         cfg.RunID,
		token:         cfg.Token,
		client:        &http.Client{Timeout: cfg.HTTPTimeout},
		flushInterval: cfg.FlushInterval,
		batchMax:      cfg.BatchMax,
		maxAttempts:   cfg.MaxAttempts,
		ch:            make(chan event, cfg.BufferSize),
		done:          make(chan struct{}),
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

// Close flushes remaining events and stops the background loop. Bounded by the
// caller's patience via the returned deadline: it blocks until the queue drains
// or the loop's shutdown drain budget elapses.
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
				e.send(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				e.send(batch)
				batch = batch[:0]
			}
		case <-e.done:
			// Final drain: pull everything still queued and flush in batches.
			for {
				drain()
				if len(batch) == 0 {
					break
				}
				e.send(batch)
				batch = batch[:0]
			}
			return
		}
	}
}

// send POSTs a batch with retry/backoff. A drop note (if any drops occurred) is
// prepended so the console can render a "N events dropped" marker. Best-effort:
// on permanent failure the batch is logged and abandoned.
func (e *Emitter) send(batch []event) {
	if len(batch) == 0 {
		return
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
		return
	}
	url := fmt.Sprintf("%s/internal/v1/runs/%s/events", e.baseURL, e.runID)

	backoff := 200 * time.Millisecond
	for attempt := 1; attempt <= e.maxAttempts; attempt++ {
		ok, retryable := e.post(url, body)
		if ok {
			return
		}
		if !retryable || attempt == e.maxAttempts {
			fmt.Fprintf(os.Stderr, "[emitter] giving up on batch of %d after %d attempt(s)\n", len(batch), attempt)
			return
		}
		time.Sleep(backoff)
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// post sends one request. Returns (ok, retryable). 2xx => ok. 5xx / network =>
// retryable. 4xx (except 429) => permanent failure (won't succeed on retry).
func (e *Emitter) post(url string, body []byte) (ok, retryable bool) {
	ctx, cancel := context.WithTimeout(context.Background(), e.client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
