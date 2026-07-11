package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// kanbanConfigView is the GET/PUT /api/v1/system/kanban response (D27). It NEVER
// carries the plaintext cluster fallback token — only token_set. It reflects BOTH
// the DB OVERRIDE (base_url + token_set — what the console edit form binds) and
// the EFFECTIVE resolution (source + effective_* + cluster_token_set — the actual
// runtime state after DB > env resolution), so the console can render the form and
// an honest "DB (console) / env / off" badge from one payload. reason is set only
// when the config is broken (e.g. a DB fallback token stored but AUTH_TOKEN_KEY is
// unset) — surfaced rather than silently falling back to env (D14 fail-visible).
type kanbanConfigView struct {
	BaseURL          string `json:"base_url"`           // the DB override's base_url ("" when no DB row)
	TokenSet         bool   `json:"token_set"`          // the DB override carries a fallback token
	Source           string `json:"source"`             // effective source: "db" | "env" | "none"
	Reason           string `json:"reason,omitempty"`   // why broken/disabled (empty when healthy)
	EffectiveEnabled bool   `json:"effective_enabled"`  // the integration is effectively on
	EffectiveBaseURL string `json:"effective_base_url"` // the base URL clients actually use
	ClusterTokenSet  bool   `json:"cluster_token_set"`  // effective fallback token flag (per source)
	PollInterval     string `json:"poll_interval"`      // JTYPE_POLL_INTERVAL (env-only, informational)
}

// kanbanConfigView builds the response from the DB override row + the effective
// resolution. A real store error reading the override is returned (→ 500); a
// resolver error (broken config) is captured into Reason, not propagated, so the
// admin still sees the override they need to fix and an honest disabled state.
func (s *Server) buildKanbanConfigView(ctx context.Context) (kanbanConfigView, error) {
	v := kanbanConfigView{Source: "none", PollInterval: s.cfg.JtypePollInterval.String()}

	// DB override (base_url + token_set). An absent row simply leaves them empty.
	row, err := s.st.GetClusterKanbanConfig(ctx)
	switch {
	case err == nil:
		v.BaseURL = row.BaseURL
		v.TokenSet = row.TokenSet()
	case errors.Is(err, store.ErrNotFound):
		// no override — effective resolution falls back to env/none
	default:
		return kanbanConfigView{}, err
	}

	// Effective resolution (DB > env). A broken config (e.g. DB token + no cipher)
	// surfaces as Reason with the integration off — never a silent env fallback.
	eff, eerr := s.kanban.Effective(ctx)
	if eerr != nil {
		v.Reason = eerr.Error()
		return v, nil
	}
	v.Source = string(eff.Source)
	v.EffectiveEnabled = eff.Enabled()
	v.EffectiveBaseURL = eff.BaseURL
	v.ClusterTokenSet = eff.ClusterTokenSet
	return v, nil
}

// handleGetKanbanConfig returns the effective + override cluster kanban config
// (cluster-admin only, D27). Never returns the fallback token.
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

// updateKanbanConfigReq is the PUT /api/v1/system/kanban body. base_url is
// required. token is a pointer so "field absent" KEEPS the existing fallback
// token (a base_url-only edit), "" CLEARS it, and any other value SETS/ROTATES it
// — write-only (plaintext in, never echoed), mirroring the per-link token rotation.
type updateKanbanConfigReq struct {
	BaseURL string  `json:"base_url"`
	Token   *string `json:"token"`
}

// handlePutKanbanConfig upserts the cluster kanban config (cluster-admin only,
// D27), then Invalidate()s the shared resolver so the change activates WITHOUT a
// restart (fail-visible: a stored base URL that didn't take effect would be a
// silent no-op). A token supplied with no cipher configured is a typed 409
// cipher_not_configured (never stored in the clear), checked before any write.
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

	// Resolve the token blob. Absent => keep the current DB token; a value => seal
	// it (409 without a cipher, checked before the store write); "" => clear.
	var tokenEnc []byte
	if req.Token == nil {
		cur, err := s.st.GetClusterKanbanConfig(r.Context())
		switch {
		case err == nil:
			tokenEnc = cur.TokenEnc
		case errors.Is(err, store.ErrNotFound):
			// no existing row => nothing to keep
		default:
			s.log.Error("load kanban config for keep-token", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not read kanban config")
			return
		}
	} else {
		enc, ok := s.sealKanbanToken(w, strings.TrimSpace(*req.Token))
		if !ok {
			return // 409 cipher_not_configured (or 500) already written
		}
		tokenEnc = enc
	}

	cfg := &domain.KanbanConfig{
		BaseURL:   base,
		TokenEnc:  tokenEnc,
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
// to the JTYPE_* env (or off). Idempotent. Returns the new effective state.
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

// sealKanbanToken encrypts a cluster fallback token for storage. An empty token
// stores nil (clears the fallback). A non-empty token with no cipher configured
// (AUTH_TOKEN_KEY unset) is a typed 409 cipher_not_configured — never stored in
// the clear — mirroring encryptModelKey / the per-link token gate.
func (s *Server) sealKanbanToken(w http.ResponseWriter, token string) ([]byte, bool) {
	if token == "" {
		return nil, true
	}
	if s.cipher == nil {
		writeError(w, http.StatusConflict, "cipher_not_configured",
			"set AUTH_TOKEN_KEY on the orchestrator before storing a cluster jtype token")
		return nil, false
	}
	enc, err := s.cipher.EncryptString(token)
	if err != nil {
		s.log.Error("encrypt cluster kanban token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not encrypt the jtype token")
		return nil, false
	}
	return enc, true
}
