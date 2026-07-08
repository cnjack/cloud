package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// Multi-turn session endpoints (D22; docs/14-cloud-v2-design.md §3).
//
// External (member+):
//   POST /api/v1/runs/{id}/messages  — enqueue a follow-up prompt for a session.
//   POST /api/v1/runs/{id}/finish    — wind the session down.
//
// Internal (RUN_TOKEN, driven by the runner's acpdrive loop):
//   POST /internal/v1/runs/{id}/turn-complete — a turn finished; park the run in
//        awaiting_input until the next message.
//   GET  /internal/v1/runs/{id}/next-prompt   — long-poll for the next message
//        (≤ hold seconds); 200 delivers it, 204 = none yet, 410 = finalized.

// defaultNextPromptHold is how long GET next-prompt holds the connection before
// answering 204. It MUST stay comfortably under the runner's own per-request
// timeout (acpdrive: 35s) so a legitimate 204 is never a client-side timeout.
// The F7a contract is "server holds ≤ ~25s".
const defaultNextPromptHold = 25 * time.Second

// defaultNextPromptPoll is the store-poll interval inside the hold. Delivery is
// therefore near-instant in the common case (a message posted while a poll is
// waiting is seen within one interval), while a self-healing level-based poll
// keeps the design restart-safe with no cross-process notifier.
const defaultNextPromptPoll = 500 * time.Millisecond

func (s *Server) npHold() time.Duration {
	if s.nextPromptHold > 0 {
		return s.nextPromptHold
	}
	return defaultNextPromptHold
}

func (s *Server) npPoll() time.Duration {
	if s.nextPromptPoll > 0 {
		return s.nextPromptPoll
	}
	return defaultNextPromptPoll
}

type sendMessageReq struct {
	Prompt string `json:"prompt"`
}

// handleSendMessage enqueues a follow-up prompt for a multi-turn session run
// (D22). The run must be a session and non-terminal, in {awaiting_input,
// running} — running means the message queues behind the in-flight turn and is
// picked up on the next next-prompt poll. Anything else is a typed 409
// run_not_awaiting (fail-visible: the user learns the run cannot take a message
// rather than silently dropping it). The message lands on the delivery queue AND
// as a user.message timeline event (rendered as a user bubble).
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get run")
		return
	}
	prin := principalFrom(r.Context())
	if !s.authorizeProject(r.Context(), w, prin, run.ProjectID, domain.RoleMember) {
		return
	}
	if !run.Session {
		writeError(w, http.StatusConflict, "run_not_awaiting", "this run is not a multi-turn session — start a run with session mode to send follow-up messages")
		return
	}
	// C2 (fail-visible): a finalizing session is winding down — next-prompt
	// answers 410, so a message accepted now would NEVER be processed. Reject it
	// loudly instead of queuing it into a void.
	if run.SessionFinalizing {
		writeError(w, http.StatusConflict, "run_finalizing",
			"the session is finishing — this message would not be processed")
		return
	}
	if run.Status != domain.StatusAwaitingInput && run.Status != domain.StatusRunning {
		writeError(w, http.StatusConflict, "run_not_awaiting",
			"the session is not accepting messages (status "+string(run.Status)+")")
		return
	}
	var req sendMessageReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt is required")
		return
	}
	msg, err := s.st.AppendRunMessage(r.Context(), run.ID, req.Prompt, prin.userID())
	if err != nil {
		s.log.Error("append run message", "run", run.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not enqueue message")
		return
	}
	// Timeline: the message shows immediately as a user bubble; the agent's reply
	// follows once the runner picks it up (next-prompt) and streams agent.text.
	s.emitUserMessage(r.Context(), run.ID, req.Prompt, principalDisplayName(prin))
	writeJSON(w, http.StatusCreated, msg)
}

// handleFinishSession sets the finalize flag on a session run (D22): the next
// next-prompt poll answers 410, the runner exits gracefully, and the succeeded
// Job drives the run to succeeded. Idempotent — a finish on an already-terminal
// or already-finalizing run is a no-op returning the current row.
func (s *Server) handleFinishSession(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get run")
		return
	}
	prin := principalFrom(r.Context())
	if !s.authorizeProject(r.Context(), w, prin, run.ProjectID, domain.RoleMember) {
		return
	}
	if !run.Session {
		writeError(w, http.StatusConflict, "run_not_awaiting", "this run is not a multi-turn session")
		return
	}
	if run.Status.Terminal() {
		// Already done — finish is idempotent.
		writeJSON(w, http.StatusOK, run)
		return
	}
	committed, err := s.st.MarkSessionFinalizing(r.Context(), run.ID)
	if err != nil {
		s.log.Error("finish session", "run", run.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not finish session")
		return
	}
	// Only announce the first finish (avoid a duplicate row on a repeated click):
	// if the flag was already set the runner is already winding down.
	if !run.SessionFinalizing {
		s.emitSessionFinish(r.Context(), run.ID, "user", principalDisplayName(prin))
	}
	writeJSON(w, http.StatusOK, committed)
}

type turnCompleteReq struct {
	Turn       int    `json:"turn"`
	StopReason string `json:"stop_reason"`
}

// handleTurnComplete records that a session turn finished (D22): it CONSUMES the
// offered message that started this turn (phase 2 of the two-phase delivery —
// only now may the next queued message be offered) and parks the run in
// awaiting_input until the next message. RUN_TOKEN authed. Idempotent: a
// duplicate turn-complete (network retry) finds nothing left to consume and the
// awaiting transition is a no-op that preserves the idle epoch. A turn-complete
// on an already-terminal/canceled run is tolerated (200) — the runner will learn
// via next-prompt (410) — rather than failing the runner.
func (s *Server) handleTurnComplete(w http.ResponseWriter, r *http.Request, runID string) {
	var req turnCompleteReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	run := runFromToken(r.Context())
	if run == nil {
		if run, _ = s.st.GetRun(r.Context(), runID); run == nil {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
	}
	// A non-session run should never call this; tolerate it as a no-op rather
	// than 4xx-ing the runner.
	if !run.Session {
		writeJSON(w, http.StatusOK, map[string]any{"status": string(run.Status)})
		return
	}
	// Phase 2: the turn that the offered message started has completed. A 500
	// here is retryable for the runner (postTurnComplete backs off and retries),
	// so the consume is durably recorded before the loop may ask for more work.
	// No offered message (the first TASK_PROMPT turn) is a normal no-op.
	if _, err := s.st.ConsumeOfferedMessage(r.Context(), runID, time.Now().UTC()); err != nil {
		s.log.Error("turn-complete: consume offered message", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not record turn completion")
		return
	}
	// A turn-complete PROVES the pod is up and has already run a turn. If a very
	// fast first turn (mockllm can finish in ~1.3s) beat the reconciler's 3s tick
	// to MarkRunning, the run may still be queued/scheduling — parking it straight
	// to awaiting_input would hit ErrInvalidTransition and be dropped, hanging the
	// session silently until its TTL (CLAUDE.md red line #1). Heal it forward
	// along the real chain (queued→scheduling→running) FIRST so the park below is
	// a legal running→awaiting_input. healRunToRunning leaves a run that is already
	// running/awaiting_input/terminal untouched, so a duplicate turn-complete does
	// NOT reset the idle epoch.
	healed, _ := s.healRunToRunning(r.Context(), run)
	committed, err := s.st.SetRunAwaitingInput(r.Context(), runID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrInvalidTransition) {
			// The run reached a terminal/canceled state concurrently (a legitimate
			// outcome — a cancel or a dead pod races the turn) so there is nothing
			// to park; the runner's next-prompt poll gets a 410 and exits cleanly.
			// Warn (not Info): a run that is NOT terminal here would be a genuinely
			// stuck session, and that must be visible in the logs.
			s.log.Warn("turn-complete: run not parkable in awaiting_input (terminal/canceled concurrently?)",
				"run", runID, "status", string(healed.Status))
			writeJSON(w, http.StatusOK, map[string]any{"status": string(healed.Status)})
			return
		}
		s.log.Error("turn-complete", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not record turn completion")
		return
	}
	s.emitStatus(r.Context(), committed)
	writeJSON(w, http.StatusOK, map[string]any{"status": string(committed.Status), "turn": req.Turn})
}

// healRunToRunning walks a session run FORWARD along the legal transition chain
// queued→scheduling→running, healing the fast-turn race where a turn finished
// (or a first message arrived) before the reconciler observed the pod Running
// and called MarkRunning. It steps through the EXISTING store mutators — no
// shortcut edges (D22 state history stays real: a run genuinely passed through
// running), and each committed step is emitted so the heal is visible on the
// timeline.
//
// It STOPS at running and never pulls a run out of a later state: a run already
// running/awaiting_input/terminal/blocked is returned untouched (the switch's
// default), so callers may invoke it unconditionally — in particular a duplicate
// turn-complete on an awaiting_input run does not disturb its idle epoch.
//
// Concurrency: a reconciler tick may be marking the same run running at the same
// time. That is harmless — a running→running self-transition is a no-op, and if
// the reconciler (or a cancel) advances the run past this step first, the store
// mutator returns ErrInvalidTransition; we re-read the committed row, return it
// with the error, and stop. The caller's own SetRunAwaitingInput / ResumeRun is
// the final arbiter of the resulting state.
func (s *Server) healRunToRunning(ctx context.Context, run *domain.Run) (*domain.Run, error) {
	cur := run
	// queued→scheduling→running is at most two steps; the bound guards against an
	// unexpected store that never advances.
	for i := 0; i < 3 && (cur.Status == domain.StatusQueued || cur.Status == domain.StatusScheduling); i++ {
		var (
			committed *domain.Run
			err       error
		)
		switch cur.Status {
		case domain.StatusQueued:
			// Defensive: unreachable through the RUN_TOKEN-authed callers (a queued
			// run has no token_hash yet, so runToken 401s before we get here), but it
			// keeps a hand-driven queued run healable instead of stuck. Pass the run's
			// OWN job/token so ScheduleRun never clobbers a value another writer set.
			committed, err = s.st.ScheduleRun(ctx, cur.ID, cur.K8sJobName, cur.TokenHash, cur.Phase)
		case domain.StatusScheduling:
			committed, err = s.st.MarkRunning(ctx, cur.ID, "StreamingTurn", time.Now().UTC())
		}
		if err != nil {
			// A concurrent reconciler/cancel moved the run out from under us. Re-read
			// so the caller acts on the truth, and surface the error.
			if reloaded, gerr := s.st.GetRun(ctx, cur.ID); gerr == nil {
				return reloaded, err
			}
			return cur, err
		}
		cur = committed
		s.emitStatus(ctx, committed)
	}
	return cur, nil
}

// nextPromptResp is the 200 body of GET next-prompt (matches the F7a acpdrive
// client's nextPromptResponse: {message_id, prompt}).
type nextPromptResp struct {
	MessageID string `json:"message_id"`
	Prompt    string `json:"prompt"`
}

// handleNextPrompt is the runner's long-poll for the next session message (D22),
// phase 1 of the two-phase (offer/consume) delivery. It holds ≤ npHold(),
// answering:
//   - 410 as soon as the run is finalizing or terminal (session over → runner
//     exits cleanly) — finalize wins over any queued/offered message;
//   - 200 {message_id, prompt} when a message is deliverable. An already-offered
//     but not-yet-consumed message is re-sent VERBATIM (same id/prompt): the
//     runner only polls between turns, so a re-poll proves the previous response
//     was lost before it could start a turn — idempotent re-delivery, never a
//     double-prompt. Otherwise the oldest unoffered message is offered
//     (offered_at stamped) and the run resumed to running. Consumption happens
//     at the NEXT turn-complete.
//   - 204 if the hold elapses with nothing to deliver (the runner polls again).
func (s *Server) handleNextPrompt(w http.ResponseWriter, r *http.Request, runID string) {
	ctx := r.Context()
	deadline := time.Now().Add(s.npHold())
	poll := s.npPoll()
	for {
		run, err := s.st.GetRun(ctx, runID)
		if err != nil {
			s.log.Error("next-prompt: load run", "run", runID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not load run")
			return
		}
		// Finalize/terminal wins over any queued message: the session is ending.
		if run.SessionFinalizing || run.Status.Terminal() {
			writeError(w, http.StatusGone, "session_finalized", "the session has been finalized")
			return
		}
		msg, fresh, err := s.st.OfferNextMessage(ctx, runID, time.Now().UTC())
		if err == nil {
			// Ensure the run is (back to) running for the turn this message starts.
			// Two races to absorb:
			//  - the FIRST message may arrive while the run is still queued/scheduling
			//    (the reconciler has not marked it running yet, same fast-turn window
			//    as turn-complete) — heal it forward so the offer is not silently
			//    delivered onto a run left stuck below running;
			//  - the normal resume of an awaiting_input run, and the idempotent
			//    re-delivery of an already-running turn (running→running is a no-op).
			prev := run.Status
			healed, herr := s.healRunToRunning(ctx, run)
			if herr != nil {
				s.log.Warn("next-prompt: heal run to running", "run", runID, "err", herr)
			}
			if committed, rerr := s.st.ResumeRun(ctx, runID, "StreamingTurn"); rerr != nil {
				// A concurrent cancel/finalize could have moved it out of
				// awaiting_input; the offer is durable (and re-deliverable), log it.
				s.log.Warn("next-prompt: resume run", "run", runID, "err", rerr)
			} else if healed.Status == prev && (fresh || prev == domain.StatusAwaitingInput) {
				// Emit only when something actually changed AND heal did not already
				// emit it: a fresh offer, or a redelivery that had to heal
				// awaiting_input→running. A queued/scheduling heal already emitted its
				// own running step; a pure redelivery of a running turn stays silent.
				s.emitStatus(ctx, committed)
			}
			writeJSON(w, http.StatusOK, nextPromptResp{MessageID: msg.ID, Prompt: msg.Prompt})
			return
		}
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("next-prompt: offer message", "run", runID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not read message queue")
			return
		}
		// Nothing pending yet: hold and re-check, unless the hold elapsed or the
		// client (runner) hung up.
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !sleepCtx(ctx, poll) {
			// Client disconnected mid-hold: nothing to write.
			return
		}
	}
}

// sleepCtx sleeps for d, returning false early if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// principalDisplayName returns a human label for the message/finish author: the
// user's display name, or "" for the service principal (rendered generically).
func principalDisplayName(p *principal) string {
	if p != nil && p.user != nil {
		return p.user.DisplayName
	}
	return ""
}

// emitUserMessage appends a user.message timeline event (internal seq) and
// publishes it live. Best-effort: the message is already durably queued.
func (s *Server) emitUserMessage(ctx context.Context, runID, prompt, by string) {
	payload := map[string]any{"prompt": prompt}
	if by != "" {
		payload["by"] = by
	}
	if ev, err := s.st.AppendInternalEvent(ctx, runID, domain.EventUserMessage, payload); err != nil {
		s.log.Warn("emit user message", "run", runID, "err", err)
	} else if s.hub != nil {
		s.hub.Publish(runID, ev)
	}
}

// emitSessionFinish appends a session.finish timeline event (internal seq).
func (s *Server) emitSessionFinish(ctx context.Context, runID, reason, by string) {
	payload := map[string]any{"reason": reason}
	if by != "" {
		payload["by"] = by
	}
	if ev, err := s.st.AppendInternalEvent(ctx, runID, domain.EventSessionFinish, payload); err != nil {
		s.log.Warn("emit session finish", "run", runID, "err", err)
	} else if s.hub != nil {
		s.hub.Publish(runID, ev)
	}
}
