package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// Device relay UPLINK endpoints (docs/17 §4.1/§4.2) — reached by a local jcode
// connector with its device token. The connector mirrors its local sessions up
// (metadata + durable events + ephemeral deltas) and long-polls downlink
// commands here. Meta/payload blobs are OPAQUE to the server (plaintext JSON
// in the M3 relay phase, E2EE ciphertext from M5): they are stored and
// forwarded verbatim and never parsed.

// Device command long-poll timings (docs/17 §4.2). The hold MUST stay under
// the connector's own per-request timeout so a legitimate 204 is never a
// client-side timeout (same constraint as next-prompt, D22). The 500ms tick
// keeps delivery near-instant while staying restart-safe (a level-based store
// poll, no cross-process notifier).
const (
	defaultDevicePollMaxHold = 30 * time.Second
	defaultDevicePollWait    = 25 * time.Second
	defaultDevicePollTick    = 500 * time.Millisecond
	// devicePollMaxCommands bounds one poll response.
	devicePollMaxCommands = 64
)

func (s *Server) devicePollHold() time.Duration {
	if s.devicePollMaxHold > 0 {
		return s.devicePollMaxHold
	}
	return defaultDevicePollMaxHold
}

func (s *Server) devicePollInterval() time.Duration {
	if s.devicePollTick > 0 {
		return s.devicePollTick
	}
	return defaultDevicePollTick
}

// publishDeviceEvent fans an event out to the device's live client streams
// (best-effort: durable events are already in the store; ephemeral ones are
// lossy by design, docs/17 §4.4).
func (s *Server) publishDeviceEvent(deviceID, typ string, data map[string]any) {
	if s.deviceHub != nil {
		s.deviceHub.Publish(deviceID, sse.DeviceEvent{Type: typ, Data: data})
	}
}

// --- sessions upsert ----------------------------------------------------------

type deviceSessionsUpsertReq struct {
	Sessions []deviceSessionUpsert `json:"sessions"`
	// Replace marks Sessions as the connector's complete currently-synced
	// index. Older connectors omit it and retain the historical upsert-only
	// behaviour during rollout.
	Replace bool `json:"replace,omitempty"`
	// Capabilities is the connector's compose-capability mirror (M12):
	// {projects, models, efforts}. OPAQUE to the server — stored verbatim on
	// the devices row and echoed back to clients on GET /devices/{id}. Absent
	// leaves the stored value alone (old connectors); an explicit null clears
	// it.
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

type deviceSessionUpsert struct {
	SessionID      string          `json:"session_id"`
	Status         string          `json:"status"`
	Meta           json.RawMessage `json:"meta"`
	LastActivityAt *string         `json:"last_activity_at,omitempty"`
}

type deviceSessionUpsertView struct {
	SessionID string `json:"session_id"`
	LastSeq   int64  `json:"last_seq"`
}

// handleDeviceSessionsUpsert mirrors the device's session index up (docs/17
// §4.1): each entry is upserted keyed by (device_id, session_id) with meta
// stored verbatim. A top-level `capabilities` blob (M12) rides the same call:
// when present it replaces the devices-row mirror (an explicit null clears
// it); when absent the stored value is untouched. The response carries every
// session's current max durable seq so the connector can resume its numbering
// after a reconnect.
func (s *Server) handleDeviceSessionsUpsert(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	var req deviceSessionsUpsertReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Capabilities != nil {
		caps := []byte(req.Capabilities)
		if string(caps) == "null" {
			caps = nil
		}
		if err := s.st.UpdateDeviceCapabilities(r.Context(), p.deviceID, caps); err != nil {
			s.log.Error("device capabilities update", "device", p.deviceID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not update the device capabilities")
			return
		}
	}
	now := time.Now().UTC()
	views := make([]deviceSessionUpsertView, 0, len(req.Sessions))
	keepSessionIDs := make([]string, 0, len(req.Sessions))
	for _, in := range req.Sessions {
		sessionID := strings.TrimSpace(in.SessionID)
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "each session needs a session_id")
			return
		}
		if in.Status != domain.DeviceSessionIdle && in.Status != domain.DeviceSessionRunning {
			writeError(w, http.StatusBadRequest, "bad_request", "status must be idle or running")
			return
		}
		lastActivityAt, err := parseDeviceSessionActivity(in.LastActivityAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		ds := &domain.DeviceSession{
			DeviceID:       p.deviceID,
			SessionID:      sessionID,
			Status:         in.Status,
			Meta:           []byte(in.Meta),
			LastActivityAt: lastActivityAt,
			UpdatedAt:      now,
		}
		if err := s.st.UpsertDeviceSession(r.Context(), ds); err != nil {
			s.log.Error("device sessions upsert", "device", p.deviceID, "session", sessionID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not upsert the session")
			return
		}
		maxSeq, err := s.st.MaxDeviceEventSeq(r.Context(), p.deviceID, sessionID)
		if err != nil {
			s.log.Error("device sessions upsert: max seq", "device", p.deviceID, "session", sessionID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not read the session log")
			return
		}
		views = append(views, deviceSessionUpsertView{SessionID: sessionID, LastSeq: maxSeq})
		keepSessionIDs = append(keepSessionIDs, sessionID)
	}
	if req.Replace {
		if err := s.st.DeleteDeviceSessionsExcept(r.Context(), p.deviceID, keepSessionIDs); err != nil {
			s.log.Error("device sessions reconcile", "device", p.deviceID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not reconcile the session index")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

func parseDeviceSessionActivity(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	if !strings.HasSuffix(*raw, "Z") {
		return nil, errors.New("last_activity_at must be an RFC3339 UTC timestamp ending in Z")
	}
	at, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return nil, errors.New("last_activity_at must be an RFC3339 UTC timestamp")
	}
	return &at, nil
}

// --- durable events -----------------------------------------------------------

type deviceEventsReq struct {
	Events []deviceEventInput `json:"events"`
}

type deviceEventInput struct {
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type deviceEventsView struct {
	Accepted   []int64 `json:"accepted"`
	Conflicted []int64 `json:"conflicted"`
	MaxSeq     int64   `json:"max_seq"`
}

// handleDeviceSessionEvents appends a batch of durable events (docs/17 §4.1),
// idempotent by (device_id, session_id, seq): a replayed seq is skipped and
// reported as conflicted, never an error. Accepted events are fanned out to
// live client streams as session.event (best-effort; durability came first).
func (s *Server) handleDeviceSessionEvents(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	sid := r.PathValue("sid")
	var req deviceEventsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	now := time.Now().UTC()
	events := make([]*domain.DeviceEvent, 0, len(req.Events))
	for _, e := range req.Events {
		if e.Seq <= 0 || strings.TrimSpace(e.Kind) == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "each event needs seq>0 and a kind")
			return
		}
		events = append(events, &domain.DeviceEvent{
			DeviceID: p.deviceID, SessionID: sid,
			Seq: e.Seq, Kind: e.Kind, Envelope: []byte(e.Payload), CreatedAt: now,
		})
	}
	res, err := s.st.AppendDeviceEvents(r.Context(), p.deviceID, sid, events)
	if err != nil {
		s.log.Error("device events append", "device", p.deviceID, "session", sid, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not persist events")
		return
	}
	for _, ev := range events {
		if !seqIn(ev.Seq, res.Accepted) {
			continue
		}
		s.publishDeviceEvent(p.deviceID, sse.DeviceEventSessionEvent, map[string]any{
			"session_id": sid,
			"seq":        ev.Seq,
			"kind":       ev.Kind,
			"payload":    json.RawMessage(ev.Envelope),
		})
	}
	writeJSON(w, http.StatusOK, deviceEventsView{
		Accepted:   res.Accepted,
		Conflicted: res.Conflicted,
		MaxSeq:     res.MaxSeq,
	})
}

func seqIn(seq int64, list []int64) bool {
	for _, s := range list {
		if s == seq {
			return true
		}
	}
	return false
}

// --- ephemeral events ---------------------------------------------------------

type deviceEphemeralReq struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// handleDeviceSessionEphemeral forwards a real-time streaming event (docs/17
// §4.4): it is NEVER persisted — a client that missed it reconstructs from the
// durable log (after_seq) and the final complete message. 202 Accepted.
func (s *Server) handleDeviceSessionEphemeral(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	var req deviceEphemeralReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Kind) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "kind is required")
		return
	}
	s.publishDeviceEvent(p.deviceID, sse.DeviceEventSessionDelta, map[string]any{
		"session_id": r.PathValue("sid"),
		"kind":       req.Kind,
		"payload":    req.Payload,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// --- downlink: long poll + ack ------------------------------------------------

type deviceCommandView struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	SessionID *string         `json:"session_id"`
	Payload   json.RawMessage `json:"payload"`
}

func toDeviceCommandView(c *domain.DeviceCommand) deviceCommandView {
	return deviceCommandView{
		ID:        c.ID,
		Kind:      c.Kind,
		SessionID: c.SessionID,
		Payload:   json.RawMessage(c.Envelope),
	}
}

// handleDevicePoll is the connector's long-poll for downlink commands (docs/17
// §4.2): it answers 200 {commands:[...]} as soon as a pending command exists
// (marking it delivered), otherwise holds up to the requested wait (default
// 25s, capped at the server's 30s) and answers 204. Delivery is single-shot —
// a lost response is not re-offered; the ack resolves the command.
func (s *Server) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	wait := defaultDevicePollWait
	if v := r.URL.Query().Get("wait"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "wait must be a duration like 25s")
			return
		}
		wait = d
	}
	if max := s.devicePollHold(); wait > max {
		wait = max
	}

	ctx := r.Context()
	deadline := time.Now().Add(wait)
	for {
		cmds, err := s.st.DeliverPendingDeviceCommands(ctx, p.deviceID, devicePollMaxCommands)
		if err != nil {
			s.log.Error("device poll", "device", p.deviceID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not read the command queue")
			return
		}
		if len(cmds) > 0 {
			views := make([]deviceCommandView, 0, len(cmds))
			for i := range cmds {
				views = append(views, toDeviceCommandView(&cmds[i]))
			}
			writeJSON(w, http.StatusOK, map[string]any{"commands": views})
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !sleepCtx(ctx, s.devicePollInterval()) {
			// Client disconnected mid-hold: nothing to write.
			return
		}
	}
}

type deviceCommandAckReq struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
}

// handleDeviceCommandAck records a command's execution result (docs/17 §4.2):
// "ok" marks it acked, "error" failed; the opaque result blob and acked_at are
// stored. Idempotent — a duplicate ack is a no-op 200. A command of ANOTHER
// device is indistinguishable from an unknown one (404).
func (s *Server) handleDeviceCommandAck(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	var req deviceCommandAckReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	var status string
	switch req.Status {
	case "ok":
		status = domain.DeviceCommandAcked
	case "error":
		status = domain.DeviceCommandFailed
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "status must be ok or error")
		return
	}
	err := s.st.AckDeviceCommand(r.Context(), p.deviceID, r.PathValue("id"), status, []byte(req.Result), time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "command not found")
		return
	}
	if err != nil {
		s.log.Error("device command ack", "device", p.deviceID, "command", r.PathValue("id"), "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not record the ack")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}
