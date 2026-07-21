package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// Device self-service endpoints (docs/17-jcode-device-relay §4.1), reached by a
// local jcode with its device token ("jcd_" Bearer — principal.go resolves it
// to a device principal). These are the ONLY endpoints that accept a device
// principal; requireDevice enforces that.

// heartbeatIntervalSec is the cadence the CLI should heartbeat on; a device
// whose last_seen_at is older than ~3x this (90s, docs/17 §4.1) reads offline.
const heartbeatIntervalSec = 30

type deviceRegisterReq struct {
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	JcodeVersion string `json:"jcode_version"`
	Platform     string `json:"platform"`
	Pubkey       string `json:"pubkey"`
	// E2EE is the connector's actual encryption state (M13): true only when
	// the CEK cipher is active and cloud.e2ee did not disable it. It drives
	// the downlink pairing gate (docs/17 §6.7).
	E2EE bool `json:"e2ee"`
	// Fingerprint is the sha256 hex of the machine fingerprint (M16). The
	// device row normally already carries it from the token poll; register
	// backfills rows minted without one (pre-M16 issuance, or a token minted
	// before the CLI learned its fingerprint) so the NEXT login dedups.
	Fingerprint string `json:"fingerprint"`
}

type deviceRegisterView struct {
	DeviceID          string `json:"device_id"`
	ServerTime        string `json:"server_time"`
	HeartbeatInterval int    `json:"heartbeat_interval"`
}

// handleDeviceRegister upserts the device's registration payload (docs/17
// §4.1): hostname/version/pubkey, plus a display-name override. The row itself
// was created at token issuance — a missing one means the device was deleted
// under a live token (404). pubkey is required: it is the device's E2EE
// identity key (docs/17 §6.1) and every login generates one before calling
// register. The response carries the server clock so the CLI can sanity-check
// its own, and the heartbeat cadence to use.
func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	var req deviceRegisterReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Pubkey) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "pubkey is required")
		return
	}
	fingerprint, ok := normalizeFingerprintHash(req.Fingerprint)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "fingerprint must be a sha256 hex string (64 lowercase hex chars)")
		return
	}
	d := s.loadDeviceForPrincipal(w, r, p.deviceID)
	if d == nil {
		return
	}
	// Fingerprint backfill (M16): only onto a row that has none, and only when
	// no OTHER device of this user already claims the hash — a second machine
	// must never steal the first one's dedup key.
	if fingerprint != "" && d.FingerprintHash == "" {
		_, err := s.st.FindDeviceByFingerprint(r.Context(), d.UserID, fingerprint)
		switch {
		case errors.Is(err, store.ErrNotFound):
			d.FingerprintHash = fingerprint
		case err == nil:
			// Another live device holds this fingerprint: leave the row as-is.
		default:
			s.log.Error("device register: fingerprint lookup", "device", d.ID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not register the device")
			return
		}
	}
	now := time.Now().UTC()
	wasOnline := deviceOnlineAt(d, s.deviceTTL(), now)
	// A blank name keeps the issuance-time default (the CLI's client_name).
	if name := strings.TrimSpace(req.Name); name != "" {
		d.Name = name
	}
	d.Hostname = strings.TrimSpace(req.Hostname)
	d.JcodeVersion = strings.TrimSpace(req.JcodeVersion)
	// Platform is trimmed and length-capped but otherwise unchecked: unknown
	// values pass through so a future connector flavor needs no server change.
	d.Platform = strings.TrimSpace(req.Platform)
	if len(d.Platform) > 32 {
		d.Platform = d.Platform[:32]
	}
	d.Pubkey = strings.TrimSpace(req.Pubkey)
	// e2ee is a bool the connector computes from its live cipher state; absent
	// (old connector) decodes as false — the plaintext grey path, no gate.
	d.E2EE = req.E2EE
	d.LastSeenAt = &now
	if err := s.st.UpsertDeviceRegistration(r.Context(), d); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		s.log.Error("device register", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not register the device")
		return
	}
	// Announce the offline→online edge to live client streams (best-effort; the
	// stream itself derives the signal from last_seen_at on connect).
	if !wasOnline {
		s.publishDeviceEvent(d.ID, sse.DeviceEventStatus, map[string]any{"online": true})
	}
	writeJSON(w, http.StatusOK, deviceRegisterView{
		DeviceID:          d.ID,
		ServerTime:        now.Format(time.RFC3339),
		HeartbeatInterval: heartbeatIntervalSec,
	})
}

// handleDeviceHeartbeat stamps last_seen_at (docs/17 §4.1) and answers 204.
// The online signal itself is derived by readers (last_seen_at within 90s);
// nothing is stored beyond the timestamp.
func (s *Server) handleDeviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	now := time.Now().UTC()
	// Load first so an offline→online edge can be announced; a missing row
	// means the device was deleted under a live token (404).
	d := s.loadDeviceForPrincipal(w, r, p.deviceID)
	if d == nil {
		return
	}
	wasOnline := deviceOnlineAt(d, s.deviceTTL(), now)
	if err := s.st.TouchDeviceLastSeen(r.Context(), p.deviceID, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		s.log.Error("device heartbeat", "device", p.deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not record the heartbeat")
		return
	}
	if !wasOnline {
		s.publishDeviceEvent(d.ID, sse.DeviceEventStatus, map[string]any{"online": true})
	}
	w.WriteHeader(http.StatusNoContent)
}
