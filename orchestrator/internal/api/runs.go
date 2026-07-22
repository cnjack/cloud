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
	// ModelID is the composer's optional model pick (D21). Empty => resolve via
	// the service default / the project's sole granted model. Must be in the
	// project's grant set (else 403 model_not_granted).
	ModelID string `json:"model_id"`
	// Session opts this run into multi-turn SESSION mode (D22): the run parks in
	// awaiting_input between turns and accepts follow-up messages. Only valid for
	// kind=agent runs (which every composer/service run is). Default false =
	// today's single-shot behaviour.
	Session bool `json:"session"`
	// PermissionMode (F8b): "" (default, full_access — the runner auto-approves)
	// or "approval" — the runner forwards each jcode permission request for
	// interactive user approval. Only valid together with session: true (a
	// headless single-shot has nobody watching to answer), else 400.
	PermissionMode string `json:"permission_mode"`
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
	if svc.DeletingAt != nil {
		writeError(w, http.StatusConflict, "service_deleting", "service is being deleted")
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
	// Permission mode (F8b): only "" / "approval", and approval only rides on a
	// session run — validated up front so an invalid combination never queues.
	req.PermissionMode = strings.TrimSpace(req.PermissionMode)
	switch req.PermissionMode {
	case "":
		// full_access — today's behaviour.
	case domain.PermissionModeApproval:
		if !req.Session {
			writeError(w, http.StatusBadRequest, "bad_request",
				`permission_mode "approval" requires session mode (set "session": true) — a single-shot run has nobody watching to approve`)
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "bad_request",
			`unknown permission_mode "`+req.PermissionMode+`" (valid: "" or "approval")`)
		return
	}
	// Fail-visible gate (CLAUDE.md red line #1): resolve which model this run uses
	// via the D21 chain (composer pick → service default → sole grant). An
	// unconfigured/ambiguous/unauthorized state is a typed error and NO run is
	// queued. The chosen id + name snapshot are stamped on the run so the
	// reconciler + proxy materialise the same model (and it stays auditable).
	modelID, modelName, ok := s.selectModelForRun(w, r, svc, req.ModelID, modelcfg.NotGrantedMessage())
	if !ok {
		return
	}
	// Dispatch-time integration host gate (D20 / F5 adjudication A): a tightened
	// cluster allowlist stops EXISTING integrations immediately — a 403, not a run
	// that quietly ignores policy.
	if !s.integrationDispatchAllowed(w, r, svc) {
		return
	}
	// triggered_by is the current user (nil for the service principal).
	run := newQueuedRun(svc.ProjectID, svc.ID, req.Prompt, nil, principalFrom(r.Context()).userIDPtr())
	run.ModelID = modelID
	run.ModelName = modelName
	// Session mode (D22) is opt-in and only meaningful for agent runs (which this
	// path always produces). Webhook/kanban/schedule triggers never set it.
	run.Session = req.Session
	// Permission mode (F8b) rides only on session runs (validated above).
	run.PermissionMode = req.PermissionMode
	if err := s.st.CreateRun(r.Context(), run); err != nil {
		if errors.Is(err, store.ErrServiceDeleting) {
			writeError(w, http.StatusConflict, "service_deleting", "service is being deleted")
			return
		}
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

// deref returns the pointed-to string, or "" for a nil pointer.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
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

	// Global list. Cluster-admins see everything; a project-scoped API key
	// (F12 / D24) sees only its bound project's runs (same boundary as
	// GET /projects/{id}/runs); a regular user sees only runs in the projects
	// they are a member of.
	if prin.isClusterAdmin() {
		runs, err := s.st.ListRuns(r.Context(), "", limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not list runs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": nonNilRuns(runs)})
		return
	}
	if prin.isAPIKey() {
		runs, err := s.st.ListRuns(r.Context(), prin.scopedProjectID, limit)
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
	// Fail-visible gate: a retry is a fresh dispatch — re-run the D21 resolution
	// chain, preserving the original run's model pick when it is still granted
	// (else it fails visibly / re-resolves via the service default).
	svc, err := s.st.GetService(r.Context(), orig.ServiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if svc.DeletingAt != nil {
		writeError(w, http.StatusConflict, "service_deleting", "service is being deleted")
		return
	}
	modelID, modelName, ok := s.selectModelForRun(w, r, svc, deref(orig.ModelID), modelcfg.NotGrantedReuseMessage())
	if !ok {
		return
	}
	// Dispatch-time integration host gate (D20 / F5 adjudication A): a retry is a
	// fresh dispatch, so a since-tightened allowlist blocks it too.
	if !s.integrationDispatchAllowed(w, r, svc) {
		return
	}
	origID := orig.ID
	retry := newQueuedRun(orig.ProjectID, orig.ServiceID, orig.Prompt, &origID, principalFrom(r.Context()).userIDPtr())
	retry.Attempt = orig.Attempt + 1
	retry.ModelID = modelID
	retry.ModelName = modelName
	// A retry must preserve the run's IDENTITY, not just its prompt: retrying a
	// review run without copying Kind + PR association degenerates it into an
	// agent run that writes code and opens a junk PR (found live in M6 — the
	// retried review played the write_file scenario and opened PR #3).
	retry.Kind = orig.Kind
	retry.PRHeadBranch = orig.PRHeadBranch
	retry.PRBaseBranch = orig.PRBaseBranch
	// Session-ness is part of that identity too (D22): retrying a session run
	// starts a fresh session (same prompt, new ACP session), not a single-shot.
	retry.Session = orig.Session
	// So is the permission mode (F8b): an approval session retries as an
	// approval session — silently degrading it to full_access would drop the
	// user's guardrail.
	retry.PermissionMode = orig.PermissionMode
	if err := s.st.CreateRun(r.Context(), retry); err != nil {
		s.log.Error("retry run", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create retry run")
		return
	}
	s.emitStatus(r.Context(), retry)
	writeJSON(w, http.StatusCreated, retry)
}

type resumeRunReq struct {
	Prompt string `json:"prompt"`
	// Nil preserves the original run's model. An explicit empty string re-runs
	// normal service-default resolution, while a model id is validated against
	// this project's grants.
	ModelID *string `json:"model_id"`
	// Nil preserves the original session guardrail. full_access is explicit so a
	// caller can relax an approval-mode run for this new, separately scheduled
	// resume run.
	PermissionMode *string `json:"permission_mode"`
}

// handleResumeRun continues a FINISHED session run in a NEW run that resumes the
// SAME ACP session (F9b / D23 ①②; docs/14-cloud-v2-design.md §4). It is the
// terminal-state twin of the (live-session) message box: the new run carries
// resumed_from + the original's acp_session_id, so the reconciler injects
// RESUME_SESSION_ID and the runner drives ACP session/load instead of
// session/new. member+ (same as run dispatch). Every precondition is a typed,
// fail-visible 409 (CLAUDE.md red line #1) so the console can explain exactly
// why a session cannot be resumed rather than queuing a run that could never run.
func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	orig, err := s.st.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not get run")
		return
	}
	// Resuming a run requires member on its project.
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), orig.ProjectID, domain.RoleMember) {
		return
	}
	// Precondition 1: only a SESSION run that has reached a terminal state can be
	// resumed. A non-session run has no ACP session to reload; a live session
	// takes follow-ups through the message box, not a fresh resume run — both are
	// the same typed code but distinct, readable messages.
	if !orig.Session {
		writeError(w, http.StatusConflict, "run_not_resumable",
			"this run is not a multi-turn session, so there is no session to resume")
		return
	}
	if !orig.Status.Terminal() {
		writeError(w, http.StatusConflict, "run_not_resumable",
			"the session is still active — use the message box to continue it instead of starting a new one")
		return
	}
	// Precondition 2: the original must have recorded its ACP session id (via a
	// run.session event). Without it there is nothing to session/load against.
	if orig.AcpSessionID == "" {
		writeError(w, http.StatusConflict, "session_not_recorded",
			"this session never recorded an agent session id, so it cannot be resumed")
		return
	}
	// Precondition 3: resume replays the session transcript from the persistent
	// workspace PVC (the runner's session/load reads it from $HOME/.jcode on the
	// PVC). Persistence is a CLUSTER switch (Feature C / D05) — with it off there
	// is no PVC, the transcript never survived, and a resume would fail-visibly at
	// session/load. Reject it up front with an actionable message instead.
	if !s.cfg.PersistentWorkspace {
		writeError(w, http.StatusConflict, "workspace_not_persistent",
			"resuming a session needs a persistent workspace (the transcript lives on the service's PVC), which is not enabled on this cluster")
		return
	}
	var req resumeRunReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt is required")
		return
	}
	permissionMode := orig.PermissionMode
	if req.PermissionMode != nil {
		switch strings.TrimSpace(*req.PermissionMode) {
		case "", "full_access":
			permissionMode = ""
		case domain.PermissionModeApproval:
			permissionMode = domain.PermissionModeApproval
		default:
			writeError(w, http.StatusBadRequest, "bad_request",
				`unknown permission_mode "`+strings.TrimSpace(*req.PermissionMode)+`" (valid: "full_access" or "approval")`)
			return
		}
	}
	svc, err := s.st.GetService(r.Context(), orig.ServiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if svc.DeletingAt != nil {
		writeError(w, http.StatusConflict, "service_deleting", "service is being deleted")
		return
	}
	// A resume is a fresh dispatch. Preserve the original model by default, or
	// validate the composer's explicit pick against the project grant set.
	requestedModel := deref(orig.ModelID)
	notGrantedMsg := modelcfg.NotGrantedReuseMessage()
	if req.ModelID != nil {
		requestedModel = strings.TrimSpace(*req.ModelID)
		notGrantedMsg = modelcfg.NotGrantedMessage()
	}
	modelID, modelName, ok := s.selectModelForRun(w, r, svc, requestedModel, notGrantedMsg)
	if !ok {
		return
	}
	// Dispatch-time integration host gate (D20 / F5 adjudication A), as on create.
	if !s.integrationDispatchAllowed(w, r, svc) {
		return
	}
	origID := orig.ID
	resume := newQueuedRun(orig.ProjectID, orig.ServiceID, req.Prompt, nil, principalFrom(r.Context()).userIDPtr())
	resume.ResumedFrom = &origID
	resume.ModelID = modelID
	resume.ModelName = modelName
	// A resume run is always a SESSION and carries the explicit new guardrail,
	// or preserves the original one when the composer left it unchanged.
	resume.Session = true
	resume.PermissionMode = permissionMode
	// Copy the original's ACP session id onto the new run NOW so the reconciler can
	// inject RESUME_SESSION_ID at Job-launch, BEFORE this run has emitted its own
	// run.session. The runner then re-emits the SAME id (resumed=true) and the
	// first-writer-wins ingest is a no-op — so injection needs no lookup of the
	// (possibly since-deleted) original run.
	resume.AcpSessionID = orig.AcpSessionID
	// The project's max_live_sessions cap is enforced naturally when the reconciler
	// tries to schedule this queued session run (F7b logic) — no extra check here.
	if err := s.st.CreateRun(r.Context(), resume); err != nil {
		s.log.Error("resume run", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create resume run")
		return
	}
	s.emitStatus(r.Context(), resume)
	writeJSON(w, http.StatusCreated, resume)
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

// selectModelForRun is the fail-visible model-resolution gate shared by every
// run-creating handler (create / retry / review). It runs the D21 chain for a run
// against svc with the supplied model id (may be ""), and returns the chosen
// model id pointer (nil => env fallback / empty catalog) + the provider/model
// NAME snapshot to stamp on the run. notGrantedMsg is written for the
// SelectNotGranted outcome so the composer path ("the model you selected…") and
// the retry/review reuse path ("the model this run used…") differ. On any
// unconfigured/ambiguous/unauthorized outcome it writes the typed error and
// returns ok=false so the caller stops WITHOUT queuing a run that could never
// execute (CLAUDE.md red line #1). A resolve error is a 500.
func (s *Server) selectModelForRun(w http.ResponseWriter, r *http.Request, svc *domain.Service, requested, notGrantedMsg string) (*string, string, bool) {
	def := deref(svc.DefaultModelID)
	sel, outcome, err := s.models.SelectModel(r.Context(), svc.ProjectID, def, strings.TrimSpace(requested))
	if err != nil {
		s.log.Error("select model", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve model configuration")
		return nil, "", false
	}
	switch outcome {
	case modelcfg.SelectOK:
		if sel.ModelID == "" {
			return nil, sel.ModelName, true // env fallback → model_id NULL, name snapshotted
		}
		id := sel.ModelID
		return &id, sel.ModelName, true
	case modelcfg.SelectNotGranted:
		writeError(w, http.StatusForbidden, "model_not_granted", notGrantedMsg)
	case modelcfg.SelectNotSelected:
		writeError(w, http.StatusConflict, "model_not_selected", modelcfg.NotSelectedMessage())
	default: // SelectNotConfigured
		writeError(w, http.StatusConflict, "model_not_configured", modelcfg.NotConfiguredMessage(""))
	}
	return nil, "", false
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
