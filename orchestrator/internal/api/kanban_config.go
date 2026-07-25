package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// kanbanConfigView is the GET/PUT /api/v1/system/kanban response (D27, slimmed
// by D36). The cluster config is now JUST the jtype base URL — an
// infrastructure-level fact with NO secret material, so the view no longer
// carries any token fields. It reflects BOTH the DB OVERRIDE (base_url — what
// the console edit form binds) and the EFFECTIVE resolution (source +
// effective_* — the actual runtime state after DB > env resolution), so the
// console can render the form and an honest "DB (console) / env / off" badge
// from one payload. Credentials are entirely per-link (D25): the poller,
// writeback, board embed and connect flow all authorise with a link's own
// token, never a cluster-level one.
type kanbanConfigView struct {
	BaseURL          string `json:"base_url"`           // the DB override's base_url ("" when no DB row)
	Source           string `json:"source"`             // effective source: "db" | "env" | "none"
	Reason           string `json:"reason,omitempty"`   // why broken/disabled (empty when healthy)
	EffectiveEnabled bool   `json:"effective_enabled"`  // the integration is effectively on
	EffectiveBaseURL string `json:"effective_base_url"` // the base URL clients actually use
	PollInterval     string `json:"poll_interval"`      // JTYPE_POLL_INTERVAL (env-only, informational)
}

// kanbanConfigView builds the response from the DB override row + the effective
// resolution. A real store error reading the override is returned (→ 500); a
// resolver error is captured into Reason, not propagated, so the admin still
// sees the override they need to fix and an honest disabled state.
func (s *Server) buildKanbanConfigView(ctx context.Context) (kanbanConfigView, error) {
	v := kanbanConfigView{Source: "none", PollInterval: s.cfg.JtypePollInterval.String()}

	// DB override (base_url). An absent row simply leaves it empty.
	row, err := s.st.GetClusterKanbanConfig(ctx)
	switch {
	case err == nil:
		v.BaseURL = row.BaseURL
	case errors.Is(err, store.ErrNotFound):
		// no override — effective resolution falls back to env/none
	default:
		return kanbanConfigView{}, err
	}

	// Effective resolution (DB > env).
	eff, eerr := s.kanban.Effective(ctx)
	if eerr != nil {
		v.Reason = "kanban configuration unavailable — see orchestrator logs"
		s.log.Warn("kanban config: effective resolution failed", "err", eerr)
		return v, nil
	}
	v.Source = string(eff.Source)
	v.EffectiveEnabled = eff.Enabled()
	v.EffectiveBaseURL = eff.BaseURL
	return v, nil
}

// handleGetKanbanConfig returns the effective + override cluster kanban config
// (cluster-admin only, D27).
func (s *Server) handleGetKanbanConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	view, err := s.buildKanbanConfigView(r.Context())
	if err != nil {
		s.log.Error("get kanban config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read kanban config")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// updateKanbanConfigReq is the PUT /api/v1/system/kanban body (D36): base_url
// is required and is the ONLY field — there is no cluster-level token any more.
type updateKanbanConfigReq struct {
	BaseURL string `json:"base_url"`
}

// handlePutKanbanConfig upserts the cluster kanban base URL (cluster-admin
// only, D27), then Invalidate()s the shared resolver so the change activates
// WITHOUT a restart (fail-visible: a stored base URL that didn't take effect
// would be a silent no-op).
func (s *Server) handlePutKanbanConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	var req updateKanbanConfigReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	base := strings.TrimSpace(req.BaseURL)
	if msg, ok := validateBaseURL(base); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	cfg := &domain.KanbanConfig{
		BaseURL:   base,
		UpdatedBy: principalFrom(r.Context()).userID(),
	}
	if err := s.st.UpsertClusterKanbanConfig(r.Context(), cfg); err != nil {
		s.log.Error("upsert kanban config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not save kanban config")
		return
	}
	// Activate without a restart: the poller/writeback/board-validation resolve
	// through this same cache on their next call (D27).
	s.kanban.Invalidate()

	view, err := s.buildKanbanConfigView(r.Context())
	if err != nil {
		s.log.Error("build kanban config view after put", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "saved but could not read back kanban config")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleDeleteKanbanConfig removes the cluster kanban override (cluster-admin
// only, D27) and Invalidate()s the resolver, so the effective config falls back
// to the JTYPE_BASE_URL env (or off). Idempotent. Returns the new effective state.
func (s *Server) handleDeleteKanbanConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireClusterAdmin(w, r) {
		return
	}
	if err := s.st.DeleteClusterKanbanConfig(r.Context()); err != nil {
		s.log.Error("delete kanban config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not clear kanban config")
		return
	}
	s.kanban.Invalidate()

	view, err := s.buildKanbanConfigView(r.Context())
	if err != nil {
		s.log.Error("build kanban config view after delete", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "cleared but could not read back kanban config")
		return
	}
	writeJSON(w, http.StatusOK, view)
}
