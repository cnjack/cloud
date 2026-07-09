package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/schedule"
	"github.com/cnjack/jcloud/internal/store"
)

// Schedules (F11 / D24) — service-level cron triggers. A schedule binds a
// standard 5-field cron expression + a prompt to a service; the schedule poller
// dispatches a headless agent run (origin=schedule) each time it comes due, with
// the service's default model (the D21/F4 chain). Listing is member+; create /
// update / delete are owner-managed. Every cron is validated fail-visibly at
// write time (invalid_cron / cron_too_frequent) so a bad expression is rejected
// before it is ever stored, never silently ignored at fire time.

// createScheduleReq is the POST /api/v1/services/{id}/schedules body. Enabled is a
// pointer so an omitted field defaults to true (a schedule you just created is
// meant to run) while an explicit false is honoured.
type createScheduleReq struct {
	CronExpr string `json:"cron_expr"`
	Prompt   string `json:"prompt"`
	Enabled  *bool  `json:"enabled"`
}

// patchScheduleReq is the PATCH /api/v1/schedules/{sid} body. Every field is a
// pointer: omitted = unchanged. cron_expr is re-validated when present.
type patchScheduleReq struct {
	CronExpr *string `json:"cron_expr"`
	Prompt   *string `json:"prompt"`
	Enabled  *bool   `json:"enabled"`
}

// handleListServiceSchedules lists a service's schedules (member+, F11). Ordered
// newest-first. last_error is echoed so the console can surface why a window was
// abandoned (fail-visible).
func (s *Server) handleListServiceSchedules(w http.ResponseWriter, r *http.Request) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleMember) {
		return
	}
	schedules, err := s.st.ListSchedulesByService(r.Context(), svc.ID)
	if err != nil {
		s.log.Error("list schedules", "service", svc.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list schedules")
		return
	}
	if schedules == nil {
		schedules = []domain.Schedule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": schedules})
}

// handleCreateServiceSchedule creates a schedule on a service (owner only, F11).
// The cron is validated (invalid_cron / cron_too_frequent are fail-visible 400s)
// and a non-empty prompt is required.
func (s *Server) handleCreateServiceSchedule(w http.ResponseWriter, r *http.Request) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load service")
		return
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleOwner) {
		return
	}
	var req createScheduleReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	cronExpr := strings.TrimSpace(req.CronExpr)
	prompt := strings.TrimSpace(req.Prompt)
	if cronExpr == "" || prompt == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "cron_expr and prompt are required")
		return
	}
	if err := schedule.ValidateCron(cronExpr); err != nil {
		code, msg := scheduleCronError(err)
		writeError(w, http.StatusBadRequest, code, msg)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	now := time.Now().UTC()
	sc := &domain.Schedule{
		ID:        domain.NewID(),
		ServiceID: svc.ID,
		CronExpr:  cronExpr,
		Prompt:    prompt,
		Enabled:   enabled,
		CreatedBy: principalFrom(r.Context()).userIDPtr(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.st.CreateSchedule(r.Context(), sc); err != nil {
		s.log.Error("create schedule", "service", svc.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create schedule")
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

// handleUpdateSchedule edits a schedule (owner only, F11). cron_expr / prompt /
// enabled are pointer fields (omitted = unchanged); a supplied cron is
// re-validated. last_error is poller-owned and untouched here; last_fired_at is
// reset to the edit instant when the cron CHANGES or the schedule is re-enabled
// (see the C1 comment below), otherwise untouched.
func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	sc, svc, ok := s.loadScheduleForWrite(w, r)
	if !ok {
		return
	}
	var req patchScheduleReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	// C1 — window reset on semantic edits: with the OLD last_fired_at baseline, a
	// NEW cron expression (or a re-enabled schedule) can make a boundary that
	// predates the edit look "due" and dispatch an immediate catch-up run. Reset
	// last_fired_at to the edit instant in those two cases (atomically, in
	// UpdateSchedule) — first fire computes from now; nothing is backfilled. Same
	// "no backfill" philosophy as restart.
	resetWindow := false
	if req.CronExpr != nil {
		cronExpr := strings.TrimSpace(*req.CronExpr)
		if cronExpr == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "cron_expr cannot be empty")
			return
		}
		if err := schedule.ValidateCron(cronExpr); err != nil {
			code, msg := scheduleCronError(err)
			writeError(w, http.StatusBadRequest, code, msg)
			return
		}
		if cronExpr != sc.CronExpr {
			resetWindow = true // new cadence starts from the edit instant
		}
		sc.CronExpr = cronExpr
	}
	if req.Prompt != nil {
		prompt := strings.TrimSpace(*req.Prompt)
		if prompt == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "prompt cannot be empty")
			return
		}
		sc.Prompt = prompt
	}
	if req.Enabled != nil {
		if *req.Enabled && !sc.Enabled {
			// Re-enable: a schedule disabled for days must not fire the moment it is
			// switched back on — its first fire computes from the re-enable instant.
			resetWindow = true
		}
		sc.Enabled = *req.Enabled
	}
	if err := s.st.UpdateSchedule(r.Context(), sc, resetWindow); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "schedule not found")
			return
		}
		s.log.Error("update schedule", "schedule", sc.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not update schedule")
		return
	}
	_ = svc // service loaded only for the RBAC check
	writeJSON(w, http.StatusOK, sc)
}

// handleDeleteSchedule removes a schedule (owner only, F11).
func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	sc, _, ok := s.loadScheduleForWrite(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteSchedule(r.Context(), sc.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "schedule not found")
			return
		}
		s.log.Error("delete schedule", "schedule", sc.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete schedule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": sc.ID})
}

// loadScheduleForWrite loads the schedule named by {sid}, its owning service, and
// authorizes the caller as OWNER of the service's project. It writes the error
// response and returns ok=false on any failure. Shared by the PATCH/DELETE
// handlers (both are owner-only mutations keyed off the bare schedule id).
func (s *Server) loadScheduleForWrite(w http.ResponseWriter, r *http.Request) (*domain.Schedule, *domain.Service, bool) {
	sc, err := s.st.GetSchedule(r.Context(), r.PathValue("sid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "schedule not found")
		return nil, nil, false
	}
	if err != nil {
		s.log.Error("load schedule", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load schedule")
		return nil, nil, false
	}
	svc, err := s.st.GetService(r.Context(), sc.ServiceID)
	if err != nil {
		// A schedule always has a service (FK cascade); a lookup failure is internal.
		s.log.Error("load schedule service", "schedule", sc.ID, "service", sc.ServiceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load schedule's service")
		return nil, nil, false
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), svc.ProjectID, domain.RoleOwner) {
		return nil, nil, false
	}
	return sc, svc, true
}

// scheduleCronError maps a schedule.ValidateCron error to the fail-visible API
// error code + message (both 400s).
func scheduleCronError(err error) (code, msg string) {
	if errors.Is(err, schedule.ErrCronTooFrequent) {
		return "cron_too_frequent",
			"cron fires too frequently: the minimum interval between scheduled runs is 5 minutes"
	}
	return "invalid_cron",
		"cron_expr must be a valid 5-field cron expression (minute hour day-of-month month day-of-week)"
}
