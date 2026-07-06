package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// handleListEvents returns durable events with seq > after_seq (default 0).
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, err := s.st.GetRun(r.Context(), runID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load run")
		return
	}
	after := int64(queryInt(r, "after_seq", 0))
	limit := queryInt(r, "limit", 1000)
	events, err := s.st.ListEvents(r.Context(), runID, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list events")
		return
	}
	if events == nil {
		events = []domain.RunEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleStream implements Server-Sent Events: it first replays durable events
// with seq > after_seq, then switches to the live hub. This "replay then live"
// order guarantees a reconnecting client misses nothing and sees no gaps.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	// Confirm the run exists (404 otherwise). Terminality is re-checked after
	// replay from a fresh read, so we do not rely on this snapshot's status.
	if _, err := s.st.GetRun(r.Context(), runID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load run")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	after := int64(queryInt(r, "after_seq", 0))

	// Subscribe BEFORE replaying so no event slips through the gap between the
	// replay read and going live.
	ch, unsub := s.hub.Subscribe(runID)
	defer unsub()

	// st tracks exactly which seqs have been delivered so the live phase can
	// distinguish a true duplicate (already sent) from an out-of-order publish we
	// have not sent yet, and can detect and backfill gaps from the durable log.
	st := &streamState{sent: map[int64]bool{}, lastSent: after}

	// Replay durable backlog in pages. If a terminal run.status appears in the
	// backlog, close immediately — the run finished during/before the replay
	// window and there is nothing more to stream.
	for {
		batch, err := s.st.ListEvents(ctx, runID, st.lastSent, 500)
		if err != nil {
			s.log.Error("stream: replay", "run", runID, "err", err)
			return
		}
		for _, ev := range batch {
			if err := st.deliver(w, ev); err != nil {
				return
			}
			if isTerminalStatusEvent(ev) {
				writeSSEComment(w, "run terminal; stream complete")
				flusher.Flush()
				return
			}
		}
		flusher.Flush()
		if len(batch) < 500 {
			break
		}
	}

	// The run may have gone terminal between our connect-time GetRun and the end
	// of replay (its terminal run.status could have committed after Subscribe but
	// been published before we subscribed, so it is neither replayed above nor
	// guaranteed on the live channel). Re-fetch: if terminal, backfill any events
	// we have not yet sent from the durable log and finish. This is the fix for
	// the "SSE never terminates when the run went terminal during replay" hang.
	if fresh, err := s.st.GetRun(ctx, runID); err == nil && fresh.Status.Terminal() {
		if err := st.backfill(ctx, w, flusher, s.st, runID); err != nil {
			return
		}
		writeSSEComment(w, "run terminal; stream complete")
		flusher.Flush()
		return
	}

	// Go live. Heartbeat comments keep intermediaries from closing idle conns.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			// The request context is derived from the server's BaseContext, which
			// the server cancels on graceful shutdown (as well as on client
			// disconnect). Write a best-effort final comment so a shutting-down
			// server does not cut the stream mid-frame; harmless if the client
			// already went away.
			writeSSEComment(w, "server shutting down")
			flusher.Flush()
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if st.sent[ev.Seq] {
				continue // genuine duplicate (replay/live overlap): already delivered
			}
			// A gap (ev.Seq beyond the next contiguous seq) means an earlier
			// event was published out of order or dropped by the hub buffer.
			// Backfill the missing seqs from the durable log before delivering
			// this one, so the client never sees a gap and never loses an event.
			if ev.Seq > st.lastSent+1 {
				if err := st.backfill(ctx, w, flusher, s.st, runID); err != nil {
					return
				}
			}
			// deliver is idempotent, so a backfill that already covered ev is a
			// no-op here.
			if err := st.deliver(w, ev); err != nil {
				return
			}
			flusher.Flush()
			if st.terminalSeen {
				writeSSEComment(w, "run terminal; stream complete")
				flusher.Flush()
				return
			}
		case <-heartbeat.C:
			writeSSEComment(w, "heartbeat")
			flusher.Flush()
		}
	}
}

// streamState tracks per-connection delivery so out-of-order live publishes and
// hub-buffer drops are recovered from the durable log rather than silently lost.
type streamState struct {
	sent         map[int64]bool // every seq written to the client
	lastSent     int64          // highest contiguous seq written (no gaps below it)
	terminalSeen bool           // a terminal run.status was written
}

// deliver writes one event and records it. It advances lastSent past any now-
// contiguous run of already-sent seqs so gap detection stays accurate even when
// events arrive out of order.
func (st *streamState) deliver(w http.ResponseWriter, ev domain.RunEvent) error {
	if st.sent[ev.Seq] {
		return nil
	}
	if err := writeSSE(w, ev); err != nil {
		return err
	}
	st.sent[ev.Seq] = true
	if isTerminalStatusEvent(ev) {
		st.terminalSeen = true
	}
	for st.sent[st.lastSent+1] {
		st.lastSent++
	}
	return nil
}

// backfill delivers every durable event with seq beyond what we've sent that we
// have not yet delivered, closing gaps left by out-of-order or dropped live
// publishes. It pages until caught up.
func (st *streamState) backfill(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, store eventLister, runID string) error {
	for {
		batch, err := store.ListEvents(ctx, runID, st.lastSent, 500)
		if err != nil {
			return err
		}
		for _, ev := range batch {
			if err := st.deliver(w, ev); err != nil {
				return err
			}
		}
		flusher.Flush()
		if len(batch) < 500 {
			return nil
		}
	}
}

// eventLister is the slice of the store the stream backfill needs.
type eventLister interface {
	ListEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]domain.RunEvent, error)
}

func isTerminalStatusEvent(ev domain.RunEvent) bool {
	if ev.Type != domain.EventRunStatus {
		return false
	}
	st, _ := ev.Payload["status"].(string)
	return domain.RunStatus(st).Terminal()
}

// sseFrame is the JSON data payload for each SSE frame (see cloud/docs/11-api.md).
type sseFrame struct {
	Seq     int64          `json:"seq"`
	TS      time.Time      `json:"ts"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// writeSSE writes one event frame: an `event:` line (the event type) plus a
// `data:` line with the JSON frame, terminated by a blank line.
func writeSSE(w http.ResponseWriter, ev domain.RunEvent) error {
	b, err := json.Marshal(sseFrame{Seq: ev.Seq, TS: ev.TS, Type: ev.Type, Payload: ev.Payload})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", ev.Type, ev.Seq, b)
	return err
}

func writeSSEComment(w http.ResponseWriter, msg string) {
	_, _ = fmt.Fprintf(w, ": %s\n\n", msg)
}

// --- runner ingest ----------------------------------------------------------

type ingestEventsReq struct {
	Events []ingestEvent `json:"events"`
}
type ingestEvent struct {
	Seq     int64          `json:"seq"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// handleIngestEvents accepts a batch of runner events, idempotent by (run_id,
// seq). It authenticates via the per-run token (runToken middleware) and fans
// accepted events out to live subscribers. A run.failure event refines the
// run's failure classification.
func (s *Server) handleIngestEvents(w http.ResponseWriter, r *http.Request, runID string) {
	var req ingestEventsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if len(req.Events) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": 0})
		return
	}
	inputs := make([]store.EventInput, 0, len(req.Events))
	for _, e := range req.Events {
		if e.Seq <= 0 || e.Type == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "each event needs seq>0 and type")
			return
		}
		inputs = append(inputs, store.EventInput{Seq: e.Seq, Type: e.Type, Payload: e.Payload})
	}
	// Runner ingest: the runner's seq is a per-source idempotency key; the store
	// allocates the authoritative global seq so runner events never collide with
	// internally-emitted ones (see cloud/docs/11-api.md §5.1). `stored` carries
	// the allocated seq for each newly-inserted event.
	stored, err := s.st.AppendRunnerEvents(r.Context(), runID, inputs)
	if err != nil {
		s.log.Error("ingest events", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not persist events")
		return
	}

	// Record a runner-reported failure reason so the reconciler's cluster-derived
	// classification does not overwrite the more specific one.
	s.applyRunnerFailure(r.Context(), runID, req.Events)

	// Fan out to live subscribers using the server-allocated seq (best-effort;
	// durability already done).
	if s.hub != nil {
		for _, ev := range stored {
			s.hub.Publish(runID, ev)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": len(stored)})
}

// applyRunnerFailure looks for a run.failure event and, if present, records the
// reason/message on the run so a subsequent Job-failure reconcile keeps it. It
// delegates to SetRunnerFailure, which re-reads the committed row, writes only
// the failure fields (first-writer-wins), and never touches status or any other
// column — so it cannot clobber a concurrent reconciler transition.
func (s *Server) applyRunnerFailure(ctx context.Context, runID string, events []ingestEvent) {
	for _, e := range events {
		if e.Type != domain.EventRunFailure {
			continue
		}
		reason, _ := e.Payload["reason"].(string)
		msg, _ := e.Payload["message"].(string)
		fr := domain.FailureReason(reason)
		if !domain.ValidFailureReason(fr) {
			fr = domain.FailureAgentError
		}
		if msg == "" {
			msg = "runner reported a failure"
		}
		if _, err := s.st.SetRunnerFailure(ctx, runID, fr, msg); err != nil {
			s.log.Warn("ingest: record failure reason", "run", runID, "err", err)
		}
		return
	}
}
