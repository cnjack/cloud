package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

type createRunReq struct {
	Prompt string `json:"prompt"`
}

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := s.st.GetProject(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load project")
		return
	}

	var req createRunReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt is required")
		return
	}

	run := newQueuedRun(projectID, req.Prompt, nil)
	if err := s.st.CreateRun(r.Context(), run); err != nil {
		s.log.Error("create run", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create run")
		return
	}
	// Emit the initial run.status(queued) event so the stream has a first frame.
	s.emitStatus(r.Context(), run)
	writeJSON(w, http.StatusCreated, run)
}

// newQueuedRun constructs a fresh queued run. retriedFrom links a retry.
func newQueuedRun(projectID, prompt string, retriedFrom *string) *domain.Run {
	return &domain.Run{
		ID:          domain.NewID(),
		ProjectID:   projectID,
		Prompt:      prompt,
		Status:      domain.StatusQueued,
		Phase:       "Queued",
		RetriedFrom: retriedFrom,
		Attempt:     1,
		CreatedAt:   time.Now().UTC(),
	}
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	// Works for both /projects/{id}/runs and /runs. PathValue("id") is "" for
	// the latter, which the store treats as "all projects".
	projectID := r.PathValue("id")
	limit := queryInt(r, "limit", 100)
	runs, err := s.st.ListRuns(r.Context(), projectID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list runs")
		return
	}
	if runs == nil {
		runs = []domain.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get run")
		return
	}
	if run.Status.Terminal() {
		writeError(w, http.StatusConflict, "conflict", "run is already in a terminal state")
		return
	}

	// Delete the Job first (best-effort) so no orphan keeps running, then mark
	// canceled. Order matters: if we crash after delete but before the status
	// write, the next reconcile sees a scheduling/running run with a missing Job
	// and fails it — acceptable, and the operator already intended to stop it.
	if s.launcher != nil && run.K8sJobName != "" {
		if err := s.launcher.DeleteJob(r.Context(), run.K8sJobName); err != nil {
			s.log.Warn("cancel: delete job", "run", run.ID, "err", err)
		}
	}
	now := time.Now().UTC()
	run.Status = domain.StatusCanceled
	run.Phase = "CanceledByOperator"
	run.FinishedAt = &now
	if err := s.st.UpdateRun(r.Context(), run); err != nil {
		if errors.Is(err, store.ErrInvalidTransition) {
			writeError(w, http.StatusConflict, "conflict", "run cannot be canceled from its current state")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not cancel run")
		return
	}
	s.emitStatus(r.Context(), run)
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleRetryRun(w http.ResponseWriter, r *http.Request) {
	orig, err := s.st.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get run")
		return
	}
	// Retry creates a NEW run linked via retried_from (documented Symphony
	// divergence). Only terminal runs may be retried.
	if !orig.Status.Terminal() {
		writeError(w, http.StatusConflict, "conflict", "only a finished run can be retried")
		return
	}
	origID := orig.ID
	retry := newQueuedRun(orig.ProjectID, orig.Prompt, &origID)
	retry.Attempt = orig.Attempt + 1
	if err := s.st.CreateRun(r.Context(), retry); err != nil {
		s.log.Error("retry run", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create retry run")
		return
	}
	s.emitStatus(r.Context(), retry)
	writeJSON(w, http.StatusCreated, retry)
}

// emitStatus appends and publishes an initial/updated run.status event from the
// API side (mirrors the reconciler's emitter for API-driven transitions).
func (s *Server) emitStatus(ctx context.Context, run *domain.Run) {
	payload := map[string]any{"status": string(run.Status), "phase": run.Phase}
	if run.Status == domain.StatusFailed {
		payload["failure_reason"] = string(run.FailureReason)
		payload["failure_message"] = run.FailureMessage
	}
	seq, err := s.st.NextEventSeq(ctx, run.ID)
	if err != nil {
		s.log.Error("emit status: next seq", "run", run.ID, "err", err)
		return
	}
	if _, err := s.st.AppendEvents(ctx, run.ID, []store.EventInput{{Seq: seq, Type: domain.EventRunStatus, Payload: payload}}); err != nil {
		s.log.Error("emit status: append", "run", run.ID, "err", err)
		return
	}
	if s.hub != nil {
		s.hub.Publish(run.ID, domain.RunEvent{
			RunID: run.ID, Seq: seq, TS: time.Now().UTC(),
			Type: domain.EventRunStatus, Payload: payload,
		})
	}
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
