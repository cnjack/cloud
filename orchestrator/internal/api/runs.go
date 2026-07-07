package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/store"
)

type createRunReq struct {
	Prompt string `json:"prompt"`
}

// handleCreateServiceRun is the run-creation endpoint: POST /services/{id}/runs.
// Runs are always dispatched against a specific service; the former project-level
// POST /projects/{id}/runs (which resolved a 'default' service) was removed with
// the simple-mode shim.
func (s *Server) handleCreateServiceRun(w http.ResponseWriter, r *http.Request) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	// Triggering a run requires member on the service's project.
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleMember) {
		return
	}
	s.createRunForService(w, r, svc)
}

// createRunForService is the shared body of the two run-creation endpoints. The
// authorization + project/service existence checks are done by the callers.
func (s *Server) createRunForService(w http.ResponseWriter, r *http.Request, svc *domain.Service) {
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
	// Fail-visible gate: refuse to queue a run the runner could not actually
	// execute because no LLM is configured (CLAUDE.md red line #1).
	if !s.modelConfigured(w, r) {
		return
	}
	// triggered_by is the current user (nil for the service principal).
	run := newQueuedRun(svc.ProjectID, svc.ID, req.Prompt, nil, principalFrom(r.Context()).userIDPtr())
	if err := s.st.CreateRun(r.Context(), run); err != nil {
		s.log.Error("create run", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create run")
		return
	}
	// Emit the initial run.status(queued) event so the stream has a first frame.
	s.emitStatus(r.Context(), run)
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) handleListServiceRuns(w http.ResponseWriter, r *http.Request) {
	serviceID := r.PathValue("id")
	svc, err := s.st.GetService(r.Context(), serviceID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleViewer) {
		return
	}
	runs, err := s.st.ListRunsByService(r.Context(), serviceID, queryInt(r, "limit", 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list runs")
		return
	}
	if runs == nil {
		runs = []domain.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// newQueuedRun constructs a fresh queued run. retriedFrom links a retry;
// triggeredBy records the user who created it (nil for a service principal).
func newQueuedRun(projectID, serviceID, prompt string, retriedFrom, triggeredBy *string) *domain.Run {
	return &domain.Run{
		ID:                domain.NewID(),
		ProjectID:         projectID,
		ServiceID:         serviceID,
		Prompt:            prompt,
		Status:            domain.StatusQueued,
		Kind:              domain.RunKindAgent,
		Phase:             "Queued",
		RetriedFrom:       retriedFrom,
		TriggeredByUserID: triggeredBy,
		Attempt:           1,
		CreatedAt:         time.Now().UTC(),
	}
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	// Works for both /projects/{id}/runs and /runs. PathValue("id") is "" for
	// the latter (all runs the principal may see).
	projectID := r.PathValue("id")
	limit := queryInt(r, "limit", 100)
	prin := principalFrom(r.Context())

	if projectID != "" {
		if !s.authorizeProject(r.Context(), w, prin, projectID, domain.RoleViewer) {
			return
		}
		runs, err := s.st.ListRuns(r.Context(), projectID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not list runs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": nonNilRuns(runs)})
		return
	}

	// Global list. Cluster-admins see everything; a regular user sees only runs
	// in the projects they are a member of.
	if prin.isClusterAdmin() {
		runs, err := s.st.ListRuns(r.Context(), "", limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not list runs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": nonNilRuns(runs)})
		return
	}
	runs, err := s.listRunsForUser(r, prin.userID(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": nonNilRuns(runs)})
}

// listRunsForUser aggregates runs across the projects a user is a member of,
// newest-first, capped at limit.
func (s *Server) listRunsForUser(r *http.Request, userID string, limit int) ([]domain.Run, error) {
	projects, err := s.st.ListProjectsForUser(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	var all []domain.Run
	for i := range projects {
		runs, err := s.st.ListRuns(r.Context(), projects[i].ID, limit)
		if err != nil {
			return nil, err
		}
		all = append(all, runs...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func nonNilRuns(runs []domain.Run) []domain.Run {
	if runs == nil {
		return []domain.Run{}
	}
	return runs
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
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), run.ProjectID, domain.RoleViewer) {
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
	// Cancelling a run requires member on its project.
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), run.ProjectID, domain.RoleMember) {
		return
	}
	if run.Status.Terminal() {
		writeError(w, http.StatusConflict, "conflict", "run is already in a terminal state")
		return
	}

	// CAS to canceled FIRST, atomically, then act on the COMMITTED row. Doing the
	// status change first closes the race with the reconciler's Job creation: if
	// the reconciler committed queued->scheduling concurrently, CancelRun's
	// re-read sees the committed job name (it never wipes it), so we delete the
	// right Job below. If the reconciler's ScheduleRun instead lost the CAS race
	// and its Job is orphaned, the reconciler's terminal-with-job cleanup reaps
	// it. Either way no Job is left running unreferenced.
	now := time.Now().UTC()
	committed, err := s.st.CancelRun(r.Context(), run.ID, "CanceledByOperator", now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		if errors.Is(err, store.ErrInvalidTransition) {
			writeError(w, http.StatusConflict, "conflict", "run cannot be canceled from its current state")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not cancel run")
		return
	}

	// Delete the Job named in the COMMITTED row (best-effort). If it fails or the
	// name is still empty here but a reconciler later attaches one, the
	// reconciler's terminal-with-job cleanup path deletes it. We leave the job
	// name on the row so that cleanup can find and reap it.
	if s.launcher != nil && committed.K8sJobName != "" {
		if err := s.launcher.DeleteJob(r.Context(), committed.K8sJobName); err != nil {
			s.log.Warn("cancel: delete job", "run", committed.ID, "err", err)
		}
	}
	s.emitStatus(r.Context(), committed)
	writeJSON(w, http.StatusOK, committed)
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
	// Retrying a run requires member on its project.
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), orig.ProjectID, domain.RoleMember) {
		return
	}
	// Retry creates a NEW run linked via retried_from (documented Symphony
	// divergence). Only terminal runs may be retried.
	if !orig.Status.Terminal() {
		writeError(w, http.StatusConflict, "conflict", "only a finished run can be retried")
		return
	}
	// Fail-visible gate: a retry is a fresh run — refuse it if no LLM is set.
	if !s.modelConfigured(w, r) {
		return
	}
	origID := orig.ID
	retry := newQueuedRun(orig.ProjectID, orig.ServiceID, orig.Prompt, &origID, principalFrom(r.Context()).userIDPtr())
	retry.Attempt = orig.Attempt + 1
	// A retry must preserve the run's IDENTITY, not just its prompt: retrying a
	// review run without copying Kind + PR association degenerates it into an
	// agent run that writes code and opens a junk PR (found live in M6 — the
	// retried review played the write_file scenario and opened PR #3).
	retry.Kind = orig.Kind
	retry.PRHeadBranch = orig.PRHeadBranch
	retry.PRBaseBranch = orig.PRBaseBranch
	if err := s.st.CreateRun(r.Context(), retry); err != nil {
		s.log.Error("retry run", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create retry run")
		return
	}
	s.emitStatus(r.Context(), retry)
	writeJSON(w, http.StatusCreated, retry)
}

// emitStatus appends and publishes an initial/updated run.status event from the
// API side (mirrors the reconciler's emitter for API-driven transitions). The
// store allocates the global seq atomically so this never races runner ingest.
func (s *Server) emitStatus(ctx context.Context, run *domain.Run) {
	payload := map[string]any{"status": string(run.Status), "phase": run.Phase}
	if run.Status == domain.StatusFailed {
		payload["failure_reason"] = string(run.FailureReason)
		payload["failure_message"] = run.FailureMessage
	}
	if run.PRURL != "" {
		payload["pr_url"] = run.PRURL
		payload["pr_number"] = run.PRNumber
	}
	ev, err := s.st.AppendInternalEvent(ctx, run.ID, domain.EventRunStatus, payload)
	if err != nil {
		s.log.Error("emit status", "run", run.ID, "err", err)
		return
	}
	if s.hub != nil {
		s.hub.Publish(run.ID, ev)
	}
}

// modelConfigured is the fail-visible gate shared by every run-creating handler
// (create / retry / review). It resolves the effective model config and, when
// nothing is configured, writes a typed 409 model_not_configured and returns
// false so the caller stops WITHOUT queuing a run that could never execute
// (CLAUDE.md red line #1). A resolve error is a 500 (also stops the caller).
func (s *Server) modelConfigured(w http.ResponseWriter, r *http.Request) bool {
	resolved, err := s.models.Resolve(r.Context())
	if err != nil {
		s.log.Error("resolve model config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve model configuration")
		return false
	}
	if !resolved.Configured() {
		writeError(w, http.StatusConflict, "model_not_configured", modelcfg.NotConfiguredMessage(""))
		return false
	}
	return true
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
