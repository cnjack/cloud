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
	KeyGen int             `json:"key_gen"`
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
	if err := s.st.ResolveDevicePairing(r.Context(), p.DeviceID, p.ID, domain.DevicePairingExpired, nil, 0, now); err != nil {
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
	view := pairingStatusView{Status: s.effectivePairingStatus(p, now), KeyGen: d.KeyGen}
	if p.Status == domain.DevicePairingApproved && len(p.Wrap) > 0 {
		view.Wrap = json.RawMessage(p.Wrap)
	}
	writeJSON(w, http.StatusOK, view)
}

// --- device endpoints (device token auth) ---------------------------------------

type devicePairingView struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Pubkey     string     `json:"pubkey"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

func pairingView(p *domain.DevicePairing, status string) devicePairingView {
	return devicePairingView{
		ID: p.ID, Label: p.Label, Pubkey: p.Pubkey, Status: status,
		CreatedAt: p.CreatedAt, ResolvedAt: p.ResolvedAt,
	}
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
			views = append(views, pairingView(&pairings[i], status))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": views})
}

func (s *Server) requireApprovedClientPairing(w http.ResponseWriter, r *http.Request, deviceID, pairingID string) *domain.DevicePairing {
	if strings.TrimSpace(pairingID) == "" {
		writeError(w, http.StatusForbidden, "pairing_approval_required", "an approved pairing identity is required")
		return nil
	}
	p, err := s.st.GetDevicePairing(r.Context(), deviceID, pairingID)
	if err != nil || p.Status != domain.DevicePairingApproved {
		writeError(w, http.StatusForbidden, "pairing_approval_required", "this client is not approved to review pairings")
		return nil
	}
	return p
}

// handleListClientDevicePairings lets an already-approved console/mobile
// client review pending requests. The approver id is a high-entropy capability
// minted during its own pairing and becomes invalid as soon as that row is
// revoked; the account session still supplies the ownership boundary.
func (s *Server) handleListClientDevicePairings(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	if s.requireApprovedClientPairing(w, r, d.ID, r.URL.Query().Get("approver_id")) == nil {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = domain.DevicePairingPending
	}
	now := time.Now().UTC()
	pairings, err := s.st.ListDevicePairings(r.Context(), d.ID, "")
	if err != nil {
		s.log.Error("list client device pairings", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list pairings")
		return
	}
	views := make([]devicePairingView, 0, len(pairings))
	for i := range pairings {
		s.settlePairingExpiry(r, &pairings[i], now)
		effective := s.effectivePairingStatus(&pairings[i], now)
		if effective == status {
			views = append(views, pairingView(&pairings[i], effective))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": views})
}

type respondClientDevicePairingReq struct {
	ApproverID string          `json:"approver_id"`
	Approve    bool            `json:"approve"`
	KeyGen     int             `json:"key_gen,omitempty"`
	Wrap       json.RawMessage `json:"wrap,omitempty"`
}

// handleRespondClientDevicePairing is the paired-client equivalent of the
// device response endpoint. CEK wrapping happens in the approved client; the
// orchestrator verifies the durable approver record and stores only the opaque
// wrap for the target.
func (s *Server) handleRespondClientDevicePairing(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	var req respondClientDevicePairingReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if s.requireApprovedClientPairing(w, r, d.ID, req.ApproverID) == nil {
		return
	}
	targetID := r.PathValue("pid")
	if targetID == req.ApproverID {
		writeError(w, http.StatusBadRequest, "bad_request", "a pairing cannot approve itself")
		return
	}
	if req.Approve && len(req.Wrap) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "approve requires the wrapped CEK (wrap)")
		return
	}
	if req.Approve && req.KeyGen < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "approve requires key_gen")
		return
	}
	target, err := s.st.GetDevicePairing(r.Context(), d.ID, targetID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "pairing not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load the pairing")
		return
	}
	now := time.Now().UTC()
	status := s.effectivePairingStatus(target, now)
	if status == domain.DevicePairingExpired {
		s.settlePairingExpiry(r, target, now)
		writeError(w, http.StatusConflict, "pairing_expired", "the pairing request has expired")
		return
	}
	if status == domain.DevicePairingPending {
		status = domain.DevicePairingDenied
		var wrap []byte
		if req.Approve {
			status = domain.DevicePairingApproved
			wrap = []byte(req.Wrap)
		}
		if err := s.st.ResolveDevicePairing(r.Context(), d.ID, target.ID, status, wrap, req.KeyGen, now); err != nil {
			if errors.Is(err, store.ErrConflict) {
				writeError(w, http.StatusConflict, "key_generation_conflict", "the content key changed; refresh and approve again")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "could not record the response")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
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
	d, err := s.st.GetDevice(r.Context(), p.deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load the device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         pairing.ID,
		"label":      pairing.Label,
		"pubkey":     pairing.Pubkey,
		"status":     s.effectivePairingStatus(pairing, now),
		"created_at": pairing.CreatedAt,
		"key_gen":    d.KeyGen,
	})
}

type respondDevicePairingReq struct {
	Approve bool            `json:"approve"`
	KeyGen  int             `json:"key_gen,omitempty"`
	Wrap    json.RawMessage `json:"wrap,omitempty"`
}

// handleRespondDevicePairing records the device's decision (docs/17 §6.3):
// approve requires the CEK generation and ECIES-wrapped CEK blob (stored
// verbatim for the client to fetch); deny needs neither. Repeating the same
// outcome is idempotent, while a competing outcome or stale generation is a
// conflict. A pairing of another device is 404; stale pending settles expired.
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
	if req.Approve && req.KeyGen < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "approve requires key_gen")
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
		if err := s.st.ResolveDevicePairing(r.Context(), p.deviceID, pairing.ID, status, wrap, req.KeyGen, now); err != nil {
			if errors.Is(err, store.ErrConflict) {
				writeError(w, http.StatusConflict, "key_generation_conflict", "the content key changed; refresh and approve again")
				return
			}
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

type devicePairingRekeyReq struct {
	RevokePairingID string `json:"revoke_pairing_id"`
	KeyGen          int    `json:"key_gen"`
	Wraps           []struct {
		PairingID string          `json:"pairing_id"`
		Wrap      json.RawMessage `json:"wrap"`
	} `json:"wraps"`
}

// handleRekeyDevicePairings commits a targeted client revoke. The desktop has
// already generated generation N+1 and wrapped it for every remaining approved
// client; validation requires that complete set so persistence never strands a
// non-revoked client with an old generation.
func (s *Server) handleRekeyDevicePairings(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	var req devicePairingRekeyReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	d, err := s.st.GetDevice(r.Context(), p.deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load the device")
		return
	}
	target, err := s.st.GetDevicePairing(r.Context(), d.ID, req.RevokePairingID)
	// A retry after the transaction committed but its HTTP response was lost is
	// successful and must not make desktop roll back to the compromised old CEK.
	if err == nil && target.Status == domain.DevicePairingRevoked && req.KeyGen == d.KeyGen {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "revoked_pairing_id": target.ID, "key_gen": req.KeyGen,
		})
		return
	}
	if req.KeyGen != d.KeyGen+1 {
		writeError(w, http.StatusConflict, "key_generation_conflict", "key_gen must advance exactly one generation")
		return
	}
	if err != nil || target.Status != domain.DevicePairingApproved {
		writeError(w, http.StatusNotFound, "not_found", "approved pairing not found")
		return
	}
	approved, err := s.st.ListDevicePairings(r.Context(), d.ID, domain.DevicePairingApproved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load approved pairings")
		return
	}
	expected := make(map[string]struct{}, len(approved)-1)
	for i := range approved {
		if approved[i].ID != target.ID {
			expected[approved[i].ID] = struct{}{}
		}
	}
	wraps := make(map[string][]byte, len(req.Wraps))
	for _, item := range req.Wraps {
		if _, ok := expected[item.PairingID]; !ok || len(item.Wrap) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "wraps must contain each remaining approved pairing exactly once")
			return
		}
		if _, duplicate := wraps[item.PairingID]; duplicate {
			writeError(w, http.StatusBadRequest, "bad_request", "duplicate pairing_id in wraps")
			return
		}
		wraps[item.PairingID] = append([]byte(nil), item.Wrap...)
	}
	if len(wraps) != len(expected) {
		writeError(w, http.StatusBadRequest, "bad_request", "a rekey wrap is required for every remaining approved pairing")
		return
	}
	now := time.Now().UTC()
	if err := s.st.RekeyDevicePairings(r.Context(), d.ID, target.ID, req.KeyGen, wraps, now); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "key_generation_conflict", "device key generation changed concurrently")
			return
		}
		s.log.Error("rekey device pairings", "device", d.ID, "pairing", target.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not commit pairing revocation")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "revoked_pairing_id": target.ID, "key_gen": req.KeyGen,
	})
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
