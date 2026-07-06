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
	run, err := s.st.GetRun(r.Context(), runID)
	if errors.Is(err, store.ErrNotFound) {
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
	// replay read and going live. Any live event with seq <= lastSent is
	// de-duplicated below.
	ch, unsub := s.hub.Subscribe(runID)
	defer unsub()

	// Replay durable backlog in pages.
	lastSent := after
	for {
		batch, err := s.st.ListEvents(ctx, runID, lastSent, 500)
		if err != nil {
			s.log.Error("stream: replay", "run", runID, "err", err)
			return
		}
		for _, ev := range batch {
			if err := writeSSE(w, ev); err != nil {
				return
			}
			lastSent = ev.Seq
		}
		flusher.Flush()
		if len(batch) < 500 {
			break
		}
	}

	// If the run was already terminal when we connected and we've replayed
	// everything, end the stream with a final marker so clients can close.
	if run.Status.Terminal() {
		fresh, err := s.st.GetRun(ctx, runID)
		if err == nil && fresh.Status.Terminal() {
			// Drain any late live events already queued, then finish.
			drainLive(w, flusher, ch, lastSent)
			writeSSEComment(w, "run terminal; stream complete")
			flusher.Flush()
			return
		}
	}

	// Go live. Heartbeat comments keep intermediaries from closing idle conns.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Seq <= lastSent {
				continue // already replayed
			}
			if err := writeSSE(w, ev); err != nil {
				return
			}
			lastSent = ev.Seq
			flusher.Flush()
			if isTerminalStatusEvent(ev) {
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

// drainLive flushes any buffered live events (with seq beyond lastSent) without
// blocking, used when a run is already terminal at connect time.
func drainLive(w http.ResponseWriter, flusher http.Flusher, ch <-chan domain.RunEvent, lastSent int64) {
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Seq > lastSent {
				if err := writeSSE(w, ev); err != nil {
					return
				}
				flusher.Flush()
			}
		default:
			return
		}
	}
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
// reason/message on the run so a subsequent Job-failure reconcile keeps it.
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
		run, err := s.st.GetRun(ctx, runID)
		if err != nil {
			return
		}
		// Only stamp the reason while the run is still non-terminal; the
		// reconciler will flip status to failed from cluster state. If the run
		// is already terminal, leave it.
		if run.Status.Terminal() {
			return
		}
		run.FailureReason = fr
		run.FailureMessage = msg
		// Persist as a no-op status write (same status) so fields save without
		// an illegal transition.
		if err := s.st.UpdateRun(ctx, run); err != nil {
			s.log.Warn("ingest: record failure reason", "run", runID, "err", err)
		}
		return
	}
}
