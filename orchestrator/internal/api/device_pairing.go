package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// Device pairing endpoints (docs/17-jcode-device-relay §6.3) — the CEK
// distribution handshake. A client (console/mobile) POSTs a pairing with its
// P-256 public key; the orchestrator queues a pairing.request command for the
// device; the device approves (uploading the ECIES-wrapped CEK blob) or denies
// it; the client polls the pairing until it resolves and unwraps the CEK
// locally. Wrap is OPAQUE to the server (stored and returned verbatim).
//
// A pending pairing expires 10 minutes after creation (domain.DevicePairingWindow):
// reads lazily settle it as expired so the state is stable for every observer.

// pairingStatusView is the client-facing pairing state (GET
// /api/v1/devices/{id}/pairings/{pid}). Wrap rides along only once approved.
type pairingStatusView struct {
	Status string          `json:"status"`
	Wrap   json.RawMessage `json:"wrap,omitempty"`
}

// pairingWindow is how long a pending pairing waits for the on-device approval
// before it reads as expired (docs/17 §6.3).
func (s *Server) pairingWindow() time.Duration {
	if s.devicePairingWindow > 0 {
		return s.devicePairingWindow
	}
	return domain.DevicePairingWindow
}

// effectivePairingStatus maps a stale pending row to expired; everything else
// reads as stored.
func (s *Server) effectivePairingStatus(p *domain.DevicePairing, now time.Time) string {
	if p.Status == domain.DevicePairingPending && now.Sub(p.CreatedAt) > s.pairingWindow() {
		return domain.DevicePairingExpired
	}
	return p.Status
}

// settlePairingExpiry persists the pending → expired transition when the row
// reads stale (lazy expiry, no sweeper goroutine). Best-effort: a failed write
// still reports expired to the caller, and the next read retries the settle.
func (s *Server) settlePairingExpiry(r *http.Request, p *domain.DevicePairing, now time.Time) {
	if s.effectivePairingStatus(p, now) != domain.DevicePairingExpired {
		return
	}
	if err := s.st.ResolveDevicePairing(r.Context(), p.DeviceID, p.ID, domain.DevicePairingExpired, nil, now); err != nil {
		s.log.Error("settle pairing expiry", "pairing", p.ID, "err", err)
	}
}

// --- client endpoints (session auth, own devices) ------------------------------

type createDevicePairingReq struct {
	Label  string `json:"label"`
	Kty    string `json:"kty"`
	Pubkey string `json:"pubkey"`
}

// handleCreateDevicePairing starts a pairing (docs/17 §6.3): it stores the
// pending row and queues a pairing.request command for the device. The command
// is queued even while the device is OFFLINE — unlike chat commands there is
// no freshness requirement; the device picks it up on its next poll and the
// client polls the pairing state in the meantime. 201 {pairing_id, status}.
func (s *Server) handleCreateDevicePairing(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	var req createDevicePairingReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	req.Pubkey = strings.TrimSpace(req.Pubkey)
	if req.Label == "" || req.Pubkey == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "label and pubkey are required")
		return
	}
	if req.Kty != "P-256" {
		writeError(w, http.StatusBadRequest, "bad_request", "kty must be P-256")
		return
	}

	now := time.Now().UTC()
	p := &domain.DevicePairing{
		ID:        domain.NewID(),
		DeviceID:  d.ID,
		Label:     req.Label,
		Pubkey:    req.Pubkey,
		Status:    domain.DevicePairingPending,
		CreatedAt: now,
	}
	if err := s.st.CreateDevicePairing(r.Context(), p); err != nil {
		s.log.Error("create device pairing", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create the pairing")
		return
	}

	payload, err := json.Marshal(map[string]any{
		"pairing_id": p.ID,
		"label":      p.Label,
		"kty":        req.Kty,
		"pubkey":     p.Pubkey,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not encode the command")
		return
	}
	cmd := &domain.DeviceCommand{
		ID:        domain.NewID(),
		DeviceID:  d.ID,
		Kind:      domain.DeviceCmdPairingRequest,
		Envelope:  payload,
		Status:    domain.DeviceCommandPending,
		CreatedAt: now,
	}
	if err := s.st.CreateDeviceCommand(r.Context(), cmd); err != nil {
		s.log.Error("enqueue pairing.request", "device", d.ID, "pairing", p.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not queue the pairing request")
		return
	}
	s.log.Info("device pairing requested", "device", d.ID, "pairing", p.ID, "label", p.Label)
	writeJSON(w, http.StatusCreated, map[string]any{"pairing_id": p.ID, "status": p.Status})
}

// handleGetDevicePairing reports the pairing state to the requesting client
// (docs/17 §6.3): {status}, plus the opaque wrap blob once approved — the
// client unwraps the CEK locally, the server never sees it in plaintext.
func (s *Server) handleGetDevicePairing(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	p, err := s.st.GetDevicePairing(r.Context(), d.ID, r.PathValue("pid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "pairing not found")
		return
	}
	if err != nil {
		s.log.Error("get device pairing", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the pairing")
		return
	}
	now := time.Now().UTC()
	s.settlePairingExpiry(r, p, now)
	view := pairingStatusView{Status: s.effectivePairingStatus(p, now)}
	if p.Status == domain.DevicePairingApproved && len(p.Wrap) > 0 {
		view.Wrap = json.RawMessage(p.Wrap)
	}
	writeJSON(w, http.StatusOK, view)
}

// --- device endpoints (device token auth) ---------------------------------------

type devicePairingView struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

// handleListDevicePairings is the device's view of its pairing requests
// (docs/17 §6.3), filtered by ?status= (default pending) so the connector can
// pick up requests awaiting its approval. Stale pendings are settled as
// expired before listing, so an approval prompt never outlives its window.
func (s *Server) handleListDevicePairings(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = domain.DevicePairingPending
	}
	now := time.Now().UTC()
	pairings, err := s.st.ListDevicePairings(r.Context(), p.deviceID, "")
	if err != nil {
		s.log.Error("list device pairings", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list pairings")
		return
	}
	views := make([]devicePairingView, 0, len(pairings))
	for i := range pairings {
		s.settlePairingExpiry(r, &pairings[i], now)
		if s.effectivePairingStatus(&pairings[i], now) == status {
			views = append(views, devicePairingView{
				ID:        pairings[i].ID,
				Label:     pairings[i].Label,
				CreatedAt: pairings[i].CreatedAt,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": views})
}

// handleGetOwnPairing returns one pairing to the device itself (docs/17 §6.3),
// including the requester pubkey — `jcode cloud approve <id>` needs it to wrap
// the CEK for the right key. The status reflects lazy expiry (a stale pending
// reads as expired and is settled).
func (s *Server) handleGetOwnPairing(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	pairing, err := s.st.GetDevicePairing(r.Context(), p.deviceID, r.PathValue("pid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "pairing not found")
		return
	}
	if err != nil {
		s.log.Error("get own pairing", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the pairing")
		return
	}
	now := time.Now().UTC()
	s.settlePairingExpiry(r, pairing, now)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         pairing.ID,
		"label":      pairing.Label,
		"pubkey":     pairing.Pubkey,
		"status":     s.effectivePairingStatus(pairing, now),
		"created_at": pairing.CreatedAt,
	})
}

type respondDevicePairingReq struct {
	Approve bool            `json:"approve"`
	Wrap    json.RawMessage `json:"wrap,omitempty"`
}

// handleRespondDevicePairing records the device's decision (docs/17 §6.3):
// approve requires the ECIES-wrapped CEK blob (stored verbatim for the client
// to fetch); deny needs nothing. Idempotent — re-responding to an already
// resolved pairing is a 200 no-op reporting the stored status; a pairing of
// another device is 404; a stale pending pairing settles as expired (409).
func (s *Server) handleRespondDevicePairing(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	var req respondDevicePairingReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Approve && len(req.Wrap) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "approve requires the wrapped CEK (wrap)")
		return
	}

	pairing, err := s.st.GetDevicePairing(r.Context(), p.deviceID, r.PathValue("pid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "pairing not found")
		return
	}
	if err != nil {
		s.log.Error("respond device pairing: load", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the pairing")
		return
	}

	now := time.Now().UTC()
	status := s.effectivePairingStatus(pairing, now)
	if status == domain.DevicePairingPending {
		if req.Approve {
			status = domain.DevicePairingApproved
		} else {
			status = domain.DevicePairingDenied
		}
		var wrap []byte
		if req.Approve {
			wrap = []byte(req.Wrap)
		}
		if err := s.st.ResolveDevicePairing(r.Context(), p.deviceID, pairing.ID, status, wrap, now); err != nil {
			s.log.Error("respond device pairing: resolve", "device", p.deviceID, "pairing", pairing.ID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not record the response")
			return
		}
		s.log.Info("device pairing resolved", "device", p.deviceID, "pairing", pairing.ID, "status", status)
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
		return
	}
	if status == domain.DevicePairingExpired {
		s.settlePairingExpiry(r, pairing, now)
		writeError(w, http.StatusConflict, "pairing_expired", "the pairing request has expired")
		return
	}
	// Already approved/denied: a duplicate respond is an idempotent no-op.
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// handleDeviceRevoke revokes the device's own token(s) (docs/17 §3.3 — jcode
// logout). Effective immediately: the hash lookup excludes revoked rows, so
// the very next request with this token is a 401 (which makes a retried
// revoke idempotent by construction). 204 No Content.
func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if err := s.st.RevokeDeviceTokens(r.Context(), p.deviceID, time.Now().UTC()); err != nil {
		s.log.Error("device revoke", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke the device token")
		return
	}
	s.log.Info("device token revoked", "device", p.deviceID)
	w.WriteHeader(http.StatusNoContent)
}
