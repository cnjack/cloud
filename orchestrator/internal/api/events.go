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
	run, err := s.st.GetRun(r.Context(), runID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load run")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), run.ProjectID, domain.RoleViewer) {
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
	run, err := s.st.GetRun(r.Context(), runID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load run")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), run.ProjectID, domain.RoleViewer) {
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
	// Record permission REQUESTS (F8b) BEFORE the durable append. The two writes
	// are not one transaction, and this order is what closes the gap: if the
	// append committed first and the upsert then failed (500 → runner re-send),
	// the retry would hit the per-source seq dedupe (stored empty → nothing
	// published) and live SSE subscribers would never see the pending card until
	// a refresh. Upserting first means a failed batch persists nothing else, so
	// the retried batch replays the full append+publish path. A 500 is safely
	// retryable on both sides (seq dedupe + insert-only upsert); the 2xx below
	// is also what tells acpdrive it may start polling the decision endpoint,
	// so an event whose row was never written must never be acked.
	if err := s.applyPermissionRequests(r.Context(), runID, req.Events); err != nil {
		s.log.Error("ingest: record permission request", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not persist permission request")
		return
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

	// Record a runner-reported pushed branch (run.git) so the reconciler can open
	// the draft PR against it (ST-1).
	s.applyRunGit(r.Context(), runID, req.Events)

	// Record a runner-reported run outcome (run.result, e.g. no_changes) so the
	// run carries a first-class result even though its Job still exits 0 →
	// succeeded (D18). Status is untouched; the reconcile pass drives it.
	s.applyRunResult(r.Context(), runID, req.Events)

	// Record a runner-reported ACP session id (run.session) so a later resume can
	// reconstruct the session (F9b). First-writer-wins; never changes status.
	s.applyRunSession(r.Context(), runID, req.Events)

	// Record permission RESOLUTIONS (F8b) after the durable append, best-effort
	// like the other post-hooks — nothing polls on a resolution.
	s.applyPermissionResolutions(r.Context(), runID, req.Events)

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

// applyRunGit looks for a run.git event and, if present, records the pushed
// branch/commit on the run (first-writer-wins in the store). This is what lets
// the reconciler's PR pass find the run and open a draft PR against the branch
// (ST-1). It never changes status and is a no-op when the payload is empty.
func (s *Server) applyRunGit(ctx context.Context, runID string, events []ingestEvent) {
	for _, e := range events {
		if e.Type != domain.EventRunGit {
			continue
		}
		branch, _ := e.Payload["branch"].(string)
		commit, _ := e.Payload["commit_sha"].(string)
		if branch == "" {
			return // nothing actionable
		}
		if _, err := s.st.SetRunGit(ctx, runID, branch, commit); err != nil {
			s.log.Warn("ingest: record run git", "run", runID, "err", err)
		}
		return
	}
}

// applyRunResult looks for a run.result event and, if present, records the
// outcome on the run (runs.result) via SetRunResult. It is first-writer-wins in
// the store and NEVER changes status: the Job's exit code still drives the
// terminal status, so an empty-diff run (outcome no_changes) still reconciles to
// succeeded (D18). An unrecognised/absent outcome is ignored so we never store
// garbage.
func (s *Server) applyRunResult(ctx context.Context, runID string, events []ingestEvent) {
	for _, e := range events {
		if e.Type != domain.EventRunResult {
			continue
		}
		outcome, _ := e.Payload["outcome"].(string)
		rr := domain.RunResult(outcome)
		if !domain.ValidRunResult(rr) {
			return // unknown/empty outcome: ignore rather than persist it
		}
		if _, err := s.st.SetRunResult(ctx, runID, rr); err != nil {
			s.log.Warn("ingest: record run result", "run", runID, "err", err)
		}
		return
	}
}

// applyRunSession looks for a run.session event and, if present, records its
// acp_session_id on the run (SetRunACPSession) so a later resume can drive
// session/load against it (F9b / D23 ①②). First-writer-wins in the store and
// NEVER changes status. It fires for BOTH resumed=true and resumed=false: a
// resume run's id was already pre-filled at creation (the store write is then a
// no-op), and a fresh session run records the id the runner just established. An
// empty acp_session_id is ignored so a malformed event never clears a recorded id.
//
// Defense-in-depth (F9b audit): a RESUME run (ResumedFrom set) was dispatched
// with RESUME_SESSION_ID=<the pre-filled id>, and acpdrive --resume is
// contractually bound to session/load exactly that id — so a run.session
// reporting a DIFFERENT id means the runner broke that contract and the "resume"
// silently became a different conversation. Theoretically unreachable, but it
// must never be a silent first-writer-wins no-op: the expected id stays on the
// row (the store write is a no-op), and the anomaly is surfaced loudly — a Warn
// log AND a timeline-visible internal event (emitSessionMismatch).
func (s *Server) applyRunSession(ctx context.Context, runID string, events []ingestEvent) {
	for _, e := range events {
		if e.Type != domain.EventRunSession {
			continue
		}
		acpSessionID, _ := e.Payload["acp_session_id"].(string)
		if acpSessionID == "" {
			return // nothing actionable
		}
		committed, err := s.st.SetRunACPSession(ctx, runID, acpSessionID)
		if err != nil {
			s.log.Warn("ingest: record acp session", "run", runID, "err", err)
			return
		}
		if committed.ResumedFrom != nil && committed.AcpSessionID != "" && committed.AcpSessionID != acpSessionID {
			s.log.Warn("ingest: resumed run reported a DIFFERENT acp session than the one injected",
				"run", runID, "expected", committed.AcpSessionID, "actual", acpSessionID)
			s.emitSessionMismatch(ctx, runID, committed.AcpSessionID, acpSessionID)
		}
		return
	}
}

// emitSessionMismatch appends (and publishes) an internal run.session event
// flagging that a resume run's runner established a DIFFERENT ACP session than
// the one it was told to load (see applyRunSession). run.session is the closest
// existing event semantics (run.failure would be the wrong severity — the run
// itself keeps going); the extra expected_acp_session_id/warning keys ride in
// the payload, which clients must tolerate per the §4 taxonomy contract, and
// keep the anomaly inspectable from the timeline/events API rather than only a
// server log. Best-effort like the other emit helpers; a duplicate on a
// network-retried ingest batch is accepted (same trade-off as the D17
// writeback-comment precedent) — a conforming runner never reaches this path
// in the first place.
func (s *Server) emitSessionMismatch(ctx context.Context, runID, expected, actual string) {
	payload := map[string]any{
		"acp_session_id":          actual,
		"expected_acp_session_id": expected,
		"resumed":                 true,
		"warning":                 "acp_session_id_mismatch",
	}
	if ev, err := s.st.AppendInternalEvent(ctx, runID, domain.EventRunSession, payload); err != nil {
		s.log.Warn("emit session mismatch", "run", runID, "err", err)
	} else if s.hub != nil {
		s.hub.Publish(runID, ev)
	}
}
