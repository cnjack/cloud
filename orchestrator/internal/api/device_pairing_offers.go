package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// Pairing-offer endpoints (docs/17 §6.3 — M11 scan-to-pair). The desktop
// jcode mints a short-lived, single-use offer and renders {cloud, device,
// offer, secret} into a QR (jcode://pair?...); the mobile app scans it and
// claims the offer with its P-256 pubkey, which opens the ordinary CEK
// pairing (device_pairings row + pairing.request command carrying the
// offer_id so the device recognises its own offer). Only the SHA-256 hash of
// the secret is stored — the plaintext leaves the server exactly once, in the
// create response.

// pairingOfferWindow is how long an offer stays claimable.
func (s *Server) pairingOfferWindow() time.Duration {
	if s.devicePairingOfferWindow > 0 {
		return s.devicePairingOfferWindow
	}
	return domain.DevicePairingOfferWindow
}

// --- device endpoint (device token auth) --------------------------------------

// handleCreatePairingOffer mints an offer for the authenticated device
// (POST /internal/v1/device/pairing-offers). The plaintext secret is returned
// once; the server keeps only its hash. 201 {offer_id, secret, expires_at}.
func (s *Server) handleCreatePairingOffer(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	secret, err := auth.GenerateRunToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not mint the offer secret")
		return
	}
	now := time.Now().UTC()
	offer := &domain.DevicePairingOffer{
		ID:         domain.NewID(),
		DeviceID:   p.deviceID,
		SecretHash: auth.HashToken(secret),
		ExpiresAt:  now.Add(s.pairingOfferWindow()),
		CreatedAt:  now,
	}
	if err := s.st.CreateDevicePairingOffer(r.Context(), offer); err != nil {
		s.log.Error("create pairing offer", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create the pairing offer")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"offer_id":   offer.ID,
		"secret":     secret,
		"expires_at": offer.ExpiresAt,
	})
}

// --- client endpoint (session auth) --------------------------------------------

type claimPairingOfferReq struct {
	Secret string `json:"secret"`
	Label  string `json:"label"`
	Kty    string `json:"kty"`
	Pubkey string `json:"pubkey"`
}

// handleClaimPairingOffer turns a scanned offer into a CEK pairing
// (POST /api/v1/pairing-offers/{offer_id}/claim, docs/17 §6.3 — M11). The
// offer must exist (404), be unexpired (410), be unclaimed (409), and the
// presented secret must match (403). On success it creates the pending
// pairing row bound to the claiming user, queues the pairing.request command
// (payload carries offer_id so the device can auto-approve its own offer),
// and stamps the offer used. 201 {pairing_id, device_id}.
func (s *Server) handleClaimPairingOffer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.userID() == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "a user session is required to claim a pairing offer")
		return
	}
	var req claimPairingOfferReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Secret = strings.TrimSpace(req.Secret)
	req.Label = strings.TrimSpace(req.Label)
	req.Pubkey = strings.TrimSpace(req.Pubkey)
	if req.Secret == "" || req.Label == "" || req.Pubkey == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "secret, label and pubkey are required")
		return
	}
	if req.Kty != "P-256" {
		writeError(w, http.StatusBadRequest, "bad_request", "kty must be P-256")
		return
	}

	ctx := r.Context()
	offer, err := s.st.GetDevicePairingOffer(ctx, r.PathValue("offer_id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "pairing offer not found")
		return
	}
	if err != nil {
		s.log.Error("claim pairing offer: load", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the pairing offer")
		return
	}
	now := time.Now().UTC()
	if !now.Before(offer.ExpiresAt) {
		writeError(w, http.StatusGone, "offer_expired", "the pairing offer has expired — rescan a fresh QR code")
		return
	}
	if offer.ClaimedAt != nil {
		writeError(w, http.StatusConflict, "offer_claimed", "the pairing offer was already used — rescan a fresh QR code")
		return
	}
	if subtle.ConstantTimeCompare([]byte(auth.HashToken(req.Secret)), []byte(offer.SecretHash)) != 1 {
		writeError(w, http.StatusForbidden, "forbidden", "the pairing offer secret does not match")
		return
	}

	// Claim FIRST (conditional update): only the winner opens the pairing, so
	// a concurrent double-scan cannot create two pairings off one offer.
	if err := s.st.ClaimDevicePairingOffer(ctx, offer.ID, p.userID(), now); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "offer_claimed", "the pairing offer was already used — rescan a fresh QR code")
			return
		}
		s.log.Error("claim pairing offer: stamp", "offer", offer.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not claim the pairing offer")
		return
	}

	pairing := &domain.DevicePairing{
		ID:        domain.NewID(),
		DeviceID:  offer.DeviceID,
		Label:     req.Label,
		Pubkey:    req.Pubkey,
		Status:    domain.DevicePairingPending,
		CreatedAt: now,
	}
	if err := s.st.CreateDevicePairing(ctx, pairing); err != nil {
		s.log.Error("claim pairing offer: create pairing", "offer", offer.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create the pairing")
		return
	}
	payload, err := json.Marshal(map[string]any{
		"pairing_id": pairing.ID,
		"label":      pairing.Label,
		"kty":        req.Kty,
		"pubkey":     pairing.Pubkey,
		"offer_id":   offer.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not encode the command")
		return
	}
	cmd := &domain.DeviceCommand{
		ID:        domain.NewID(),
		DeviceID:  offer.DeviceID,
		Kind:      domain.DeviceCmdPairingRequest,
		Envelope:  payload,
		Status:    domain.DeviceCommandPending,
		CreatedAt: now,
	}
	if err := s.st.CreateDeviceCommand(ctx, cmd); err != nil {
		s.log.Error("claim pairing offer: enqueue pairing.request", "offer", offer.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not queue the pairing request")
		return
	}
	s.log.Info("pairing offer claimed", "offer", offer.ID, "device", offer.DeviceID, "pairing", pairing.ID, "user", p.userID())
	writeJSON(w, http.StatusCreated, map[string]any{"pairing_id": pairing.ID, "device_id": offer.DeviceID})
}
