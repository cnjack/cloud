package api

// permissions.go — F8b: the orchestrator half of D22's permission-approval
// design. The runner half (F8a, runner/acpdrive/permission.go) forwards each
// jcode permission request of a permission_mode=approval session as an
// agent.permission_request event (ingest hook: applyPermissionEvents in
// events.go) and long-polls the decision endpoint below; the console answers
// through the external permission-response endpoint.
//
// Load-bearing contract detail (F8a, see acpdrive/permission.go): the decision
// endpoint answers **204 for an UNKNOWN request_id** — 404/410 are reserved for
// requests that once existed and have since expired (resolved, or the run is
// finalizing/terminal). acpdrive only polls after its request event was
// acknowledged, but a 404-for-unknown would still race any server-side ingest
// asynchrony and instantly convert a pending approval into a timeout-deny.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// handlePermissionDecision is the runner's decision long-poll:
// GET /internal/v1/runs/{id}/permissions/{request_id}/decision (RUN_TOKEN).
//
//	410 — the run is finalizing/terminal, or the request is already resolved
//	      (the runner treats it exactly like its client-side timeout: deny-safe);
//	204 — pending: undecided, INCLUDING an unknown request_id (hard constraint);
//	200 — {"option_id": "..."} the user's decision.
//
// Deliberately NOT a held long-poll server-side (unlike next-prompt): the
// runner polls with its own ~250ms floor and its whole wait is bounded by
// PERMISSION_TIMEOUT_SECONDS, so a cheap immediate answer keeps this endpoint
// trivially correct under the middleware's per-request run re-read.
func (s *Server) handlePermissionDecision(w http.ResponseWriter, r *http.Request, runID string) {
	run := runFromToken(r.Context())
	if run == nil { // defensive: the runToken middleware always stashes it
		var err error
		if run, err = s.st.GetRun(r.Context(), runID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not load run")
			return
		}
	}
	// A finalizing/terminal run can never be decided — tell the runner to stop
	// polling NOW (it answers jcode with the deny-safe outcome) instead of
	// letting it burn its whole permission budget on 204s.
	if run.Status.Terminal() || run.SessionFinalizing {
		writeError(w, http.StatusGone, "permission_expired", "the run is finishing — the request can no longer be decided")
		return
	}
	perm, err := s.st.GetRunPermission(r.Context(), runID, r.PathValue("request_id"))
	if errors.Is(err, store.ErrNotFound) {
		// HARD CONSTRAINT (F8a): unknown request_id = pending, never 404.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load permission request")
		return
	}
	if perm.Resolved() {
		// The runner already recorded the final outcome (e.g. its client-side
		// timeout raced a late decision): the request is spent.
		writeError(w, http.StatusGone, "permission_expired", "the request was already resolved")
		return
	}
	if perm.Decided() {
		writeJSON(w, http.StatusOK, map[string]string{"option_id": *perm.DecidedOptionID})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// permissionResponseReq is the POST /runs/{id}/permission-response body.
type permissionResponseReq struct {
	RequestID string `json:"request_id"`
	OptionID  string `json:"option_id"`
}

// handlePermissionResponse records the user's answer to a pending permission
// request (member+; viewers are read-only). Validation, in order:
//   - 400 — missing request_id/option_id;
//   - 404 — no such request on this run;
//   - 409 permission_already_resolved — already decided (someone else answered)
//     or already resolved (the runner timed it out);
//   - 400 invalid_option — the option was not one this request offered
//     (mirrors acpdrive's own defensive optionOffered check);
//   - 200 — the committed row. The runner's next decision poll picks it up; the
//     timeline's resolved state follows via the agent.permission_resolved event.
func (s *Server) handlePermissionResponse(w http.ResponseWriter, r *http.Request) {
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
	var req permissionResponseReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.OptionID = strings.TrimSpace(req.OptionID)
	if req.RequestID == "" || req.OptionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "request_id and option_id are required")
		return
	}
	perm, err := s.st.GetRunPermission(r.Context(), run.ID, req.RequestID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "permission request not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load permission request")
		return
	}
	if perm.Decided() || perm.Resolved() {
		writeError(w, http.StatusConflict, "permission_already_resolved",
			"this permission request has already been answered or has expired")
		return
	}
	if !perm.OptionOffered(req.OptionID) {
		writeError(w, http.StatusBadRequest, "invalid_option",
			"option_id is not one of the options this request offered")
		return
	}
	committed, won, err := s.st.DecideRunPermission(r.Context(), run.ID, req.RequestID, req.OptionID, prin.userID(), time.Now().UTC())
	if err != nil {
		s.log.Error("decide permission", "run", run.ID, "request", req.RequestID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not record the decision")
		return
	}
	if !won {
		// Lost the race with another answer / the runner's resolve — same typed
		// conflict as the pre-check (the pre-check only exists for a nicer path).
		writeError(w, http.StatusConflict, "permission_already_resolved",
			"this permission request has already been answered or has expired")
		return
	}
	writeJSON(w, http.StatusOK, committed)
}

// applyPermissionRequests is the ingest PRE-hook for agent.permission_request
// events: it upserts the run_permissions rows and runs BEFORE
// AppendRunnerEvents commits the batch. The ordering is load-bearing twice
// over:
//   - the ingest 2xx is what tells acpdrive it may start polling the decision
//     endpoint, so a request whose row was never written must fail the batch
//     (5xx) rather than be acked — else the approval burns out on 204s;
//   - upsert and append are NOT one transaction. If the append committed first
//     and the upsert then failed (500 → runner re-send), the retry would hit
//     the per-source seq dedupe (stored empty → nothing published) and live
//     SSE subscribers would NEVER see the pending card until a refresh.
//     Upserting FIRST means a failure leaves nothing else persisted: the
//     retry replays the full append+publish path and the gap closes. The
//     inverse double-write (upsert committed, append failed, batch re-sent)
//     is harmless — the upsert is insert-only idempotent and never resets a
//     decided/resolved row.
//
// Unlike the single-shot hooks it processes EVERY matching event in the batch
// (one turn can carry several permission requests). A run deleted mid-flight
// (ErrNotFound) is tolerated.
func (s *Server) applyPermissionRequests(ctx context.Context, runID string, events []ingestEvent) error {
	for _, e := range events {
		if e.Type != domain.EventPermissionRequest {
			continue
		}
		requestID, _ := e.Payload["request_id"].(string)
		if requestID == "" {
			continue // nothing actionable (malformed event)
		}
		toolCallID, _ := e.Payload["tool_call_id"].(string)
		title, _ := e.Payload["title"].(string)
		perm := &domain.RunPermission{
			RequestID:  requestID,
			RunID:      runID,
			ToolCallID: toolCallID,
			Title:      title,
			Options:    parsePermissionOptions(e.Payload["options"]),
			CreatedAt:  time.Now().UTC(),
		}
		if err := s.st.UpsertRunPermission(ctx, perm); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				s.log.Warn("ingest: permission request for a vanished run", "run", runID, "request", requestID)
				continue
			}
			return err
		}
	}
	return nil
}

// applyPermissionResolutions is the ingest POST-hook for
// agent.permission_resolved events (mirrors applyRunnerFailure's placement,
// after the durable append). Best-effort: nothing polls on a resolution, so a
// transient failure only loses the ledger's resolved_* stamp — the console
// still renders the outcome from the event stream itself, and the decision
// endpoint's degraded answer (204 until finalize) stays deny-safe.
func (s *Server) applyPermissionResolutions(ctx context.Context, runID string, events []ingestEvent) {
	for _, e := range events {
		if e.Type != domain.EventPermissionResolved {
			continue
		}
		requestID, _ := e.Payload["request_id"].(string)
		if requestID == "" {
			continue
		}
		optionID, _ := e.Payload["option_id"].(string)
		resolution, _ := e.Payload["resolution"].(string)
		if err := s.st.ResolveRunPermission(ctx, runID, requestID, optionID, resolution, time.Now().UTC()); err != nil {
			s.log.Warn("ingest: record permission resolution", "run", runID, "request", requestID, "err", err)
		}
	}
}

// parsePermissionOptions converts the event payload's options array (JSON-
// decoded []any of maps) into typed options. Entries without an option_id are
// dropped (they could never be decided); unknown keys are ignored.
func parsePermissionOptions(v any) []domain.PermissionOption {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]domain.PermissionOption, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var o domain.PermissionOption
		o.OptionID, _ = m["option_id"].(string)
		o.Name, _ = m["name"].(string)
		o.Kind, _ = m["kind"].(string)
		if o.OptionID == "" {
			continue
		}
		out = append(out, o)
	}
	return out
}
