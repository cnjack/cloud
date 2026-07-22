package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// Device relay CLIENT endpoints (docs/17 §4.3) — the console/mobile view of
// the caller's OWN devices, authenticated by the user session. Ownership is
// enforced per request (authorizeDevice): another user's device is a 403, an
// unknown one a 404 — the same semantics as authorizeProject.

// defaultDeviceHeartbeatTTL applies when the config carries no explicit value
// (tests build Config directly); production config.Load defaults it to 90s.
const defaultDeviceHeartbeatTTL = 90 * time.Second

func (s *Server) deviceTTL() time.Duration {
	if s.cfg.DeviceHeartbeatTTL > 0 {
		return s.cfg.DeviceHeartbeatTTL
	}
	return defaultDeviceHeartbeatTTL
}

// deviceOnlineAt reports whether the device reads online at `at`: a heartbeat
// (or register) within the TTL (docs/17 §4.1).
func deviceOnlineAt(d *domain.Device, ttl time.Duration, at time.Time) bool {
	return d != nil && d.LastSeenAt != nil && at.Sub(*d.LastSeenAt) <= ttl
}

func (s *Server) deviceOnline(d *domain.Device) bool {
	return deviceOnlineAt(d, s.deviceTTL(), time.Now().UTC())
}

// authorizeDevice loads the device and enforces that it belongs to the request
// principal's user. On failure it writes the error (404 unknown OR revoked —
// a soft-deleted device (M16) reads as gone on the whole client surface, 403
// not the owner — a principal without a user, e.g. the service principal, is
// always 403) and returns nil; the caller must stop.
func (s *Server) authorizeDevice(w http.ResponseWriter, r *http.Request, deviceID string) *domain.Device {
	d, err := s.st.GetDevice(r.Context(), deviceID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "device not found")
		return nil
	}
	if err != nil {
		s.log.Error("load device", "device", deviceID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the device")
		return nil
	}
	if d.RevokedAt != nil {
		writeError(w, http.StatusNotFound, "not_found", "device not found")
		return nil
	}
	if uid := principalFrom(r.Context()).userID(); uid == "" || d.UserID != uid {
		writeError(w, http.StatusForbidden, "forbidden", "this device belongs to another user")
		return nil
	}
	return d
}

// handleDeleteDevice soft-deletes one of the caller's devices (M16): stamps
// devices.revoked_at and revokes every live device token, so the device's next
// request (heartbeat/poll) is a 401 and the row drops out of the device list.
// History (device_sessions/device_events) is RETAINED for audit — only the
// API surface goes away. A repeated DELETE hits the revoked row and reads 404.
func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	now := time.Now().UTC()
	if err := s.st.RevokeDevice(r.Context(), d.ID, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		s.log.Error("delete device", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete the device")
		return
	}
	if err := s.st.RevokeDeviceTokens(r.Context(), d.ID, now); err != nil {
		s.log.Error("delete device: revoke tokens", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke the device tokens")
		return
	}
	// Live streams flip to offline immediately instead of waiting out the
	// heartbeat TTL.
	s.publishDeviceEvent(d.ID, sse.DeviceEventStatus, map[string]any{"online": false})
	s.log.Info("device deleted", "device", d.ID, "user", d.UserID)
	w.WriteHeader(http.StatusNoContent)
}

type deviceView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname,omitempty"`
	JcodeVersion string `json:"jcode_version,omitempty"`
	Platform     string `json:"platform"`
	Pubkey       string `json:"pubkey,omitempty"`
	KeyGen       int    `json:"key_gen"`
	// Capabilities is the connector-reported compose mirror (M12), echoed
	// verbatim; omitted for devices that never reported any (old connectors —
	// clients hide the compose panel then).
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	// E2EE is the connector-reported encryption state (M13). Non-omitempty
	// for a stable client contract (like platform): false covers both
	// pre-M13 connectors and cloud.e2ee:false devices.
	E2EE       bool       `json:"e2ee"`
	Online     bool       `json:"online"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (s *Server) toDeviceView(d *domain.Device) deviceView {
	return deviceView{
		ID:           d.ID,
		Name:         d.Name,
		Hostname:     d.Hostname,
		JcodeVersion: d.JcodeVersion,
		Platform:     d.Platform,
		Pubkey:       d.Pubkey,
		KeyGen:       d.KeyGen,
		Capabilities: d.Capabilities,
		E2EE:         d.E2EE,
		Online:       s.deviceOnline(d),
		LastSeenAt:   d.LastSeenAt,
		CreatedAt:    d.CreatedAt,
	}
}

// handleListDevices returns the caller's devices with the derived online
// signal (docs/17 §4.3). A user session is required — the service principal
// owns no devices (same 400 as the device-authorize endpoint).
func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.userID() == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "a user session is required to list devices")
		return
	}
	devices, err := s.st.ListDevicesForUser(r.Context(), p.userID())
	if err != nil {
		s.log.Error("list devices", "user", p.userID(), "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list devices")
		return
	}
	views := make([]deviceView, 0, len(devices))
	for i := range devices {
		views = append(views, s.toDeviceView(&devices[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": views})
}

// handleGetDevice returns one of the caller's devices.
func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	writeJSON(w, http.StatusOK, s.toDeviceView(d))
}

type deviceSessionView struct {
	SessionID string          `json:"session_id"`
	Status    string          `json:"status"`
	Meta      json.RawMessage `json:"meta"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// handleListDeviceSessions returns a device's session index (docs/17 §4.3).
// Meta is returned VERBATIM (opaque: plaintext JSON in M3, ciphertext from
// M5); a session without meta reads as null.
func (s *Server) handleListDeviceSessions(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	sessions, err := s.st.ListDeviceSessions(r.Context(), d.ID)
	if err != nil {
		s.log.Error("list device sessions", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list sessions")
		return
	}
	views := make([]deviceSessionView, 0, len(sessions))
	for _, ds := range sessions {
		views = append(views, deviceSessionView{
			SessionID: ds.SessionID,
			Status:    ds.Status,
			Meta:      json.RawMessage(ds.Meta),
			UpdatedAt: ds.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

type deviceEventView struct {
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
	TS      time.Time       `json:"ts"`
}

// handleListDeviceSessionEvents replays a session's durable log with
// seq > after_seq, ascending (docs/17 §4.3) — the reconnect gap-filler for the
// stream's session.event notifications.
func (s *Server) handleListDeviceSessionEvents(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	after := int64(queryInt(r, "after_seq", 0))
	limit := queryInt(r, "limit", 1000)
	events, err := s.st.ListDeviceEvents(r.Context(), d.ID, r.PathValue("sid"), after, limit)
	if err != nil {
		s.log.Error("list device events", "device", d.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list events")
		return
	}
	views := make([]deviceEventView, 0, len(events))
	for _, ev := range events {
		views = append(views, deviceEventView{
			Seq:     ev.Seq,
			Kind:    ev.Kind,
			Payload: json.RawMessage(ev.Envelope),
			TS:      ev.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": views})
}

// handleDeviceStream is the device-level SSE stream (docs/17 §4.3): it sends
// the current device.status immediately, then live events — session.event for
// durable appends, session.delta for ephemeral forwards, device.status on
// online/offline transitions — with a 15s heartbeat. Offline detection is
// derived from last_seen_at, so the offline EDGE is detected by this loop's
// heartbeat tick (there is no background sweeper; with several streams open
// the transition may be announced once per stream — clients must tolerate a
// repeated device.status).
func (s *Server) handleDeviceStream(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	ch, unsub := s.deviceHub.Subscribe(d.ID)
	defer unsub()

	online := s.deviceOnline(d)
	if err := writeDeviceSSE(w, sse.DeviceEvent{
		Type: sse.DeviceEventStatus,
		Data: map[string]any{"online": online},
	}); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			writeSSEComment(w, "server shutting down")
			flusher.Flush()
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writeDeviceSSE(w, ev); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			// Re-derive the online signal: a heartbeat that stopped arriving is
			// only observable by checking last_seen_at against the TTL. On an
			// edge, publish through the hub so EVERY stream of this device
			// learns it (including this one, via the ch branch).
			if fresh, err := s.st.GetDevice(ctx, d.ID); err == nil {
				now := s.deviceOnline(fresh)
				if now != online {
					online = now
					s.publishDeviceEvent(d.ID, sse.DeviceEventStatus, map[string]any{"online": online})
					continue
				}
			}
			writeSSEComment(w, "heartbeat")
			flusher.Flush()
		}
	}
}

// writeDeviceSSE writes one device-stream frame: an `event:` line (the event
// type) plus a `data:` line with the JSON payload, terminated by a blank line.
// There is no `id:` line — durable seqs are per-session, so no single stream
// id space exists; reconnects fill gaps via the events API (after_seq).
func writeDeviceSSE(w http.ResponseWriter, ev sse.DeviceEvent) error {
	b, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
	return err
}

// --- downlink command entry points --------------------------------------------

type deviceCommandAcceptedView struct {
	CommandID string  `json:"command_id"`
	SessionID *string `json:"session_id"`
}

type deviceCommandStateView struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
}

// handleGetDeviceCommand lets an owning client observe the result of a
// request/response command. Result stays opaque: plaintext during gray rollout,
// an E2EE envelope otherwise.
func (s *Server) handleGetDeviceCommand(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	c, err := s.st.GetDeviceCommand(r.Context(), d.ID, r.PathValue("cid"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "command not found")
		return
	}
	if err != nil {
		s.log.Error("get device command", "device", d.ID, "command", r.PathValue("cid"), "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load the command")
		return
	}
	writeJSON(w, http.StatusOK, deviceCommandStateView{Status: c.Status, Result: json.RawMessage(c.Result)})
}

// enqueueDeviceCommand is the shared tail of the command entry points: it
// rejects offline devices (409 device_offline — a queued command would sit
// unobserved; fail visibly instead) and enqueues the command row the
// connector's poll delivers.
func (s *Server) enqueueDeviceCommand(w http.ResponseWriter, r *http.Request, d *domain.Device, kind string, sessionID *string, payload map[string]any) {
	env, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not encode the command")
		return
	}
	s.enqueueDeviceCommandRaw(w, r, d, kind, sessionID, env)
}

// enqueueDeviceCommandRaw is enqueueDeviceCommand for callers that already
// hold the payload bytes (an E2EE envelope stored verbatim, docs/17 §6.2).
func (s *Server) enqueueDeviceCommandRaw(w http.ResponseWriter, r *http.Request, d *domain.Device, kind string, sessionID *string, env []byte) {
	if !s.deviceOnline(d) {
		writeError(w, http.StatusConflict, "device_offline", "the device is offline — the command would not be delivered")
		return
	}
	c := &domain.DeviceCommand{
		ID:        domain.NewID(),
		DeviceID:  d.ID,
		Kind:      kind,
		SessionID: sessionID,
		Envelope:  env,
		Status:    domain.DeviceCommandPending,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateDeviceCommand(r.Context(), c); err != nil {
		s.log.Error("enqueue device command", "device", d.ID, "kind", kind, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not enqueue the command")
		return
	}
	writeJSON(w, http.StatusAccepted, deviceCommandAcceptedView{CommandID: c.ID, SessionID: sessionID})
}

// commandEnvelopePayload validates an E2EE command envelope (docs/17 §6.2 —
// an object with a string `enc` marker) and returns it verbatim as the command
// payload: the server routes it to the device without ever parsing beyond the
// marker. The device detects the ciphertext form by the same `enc` rule.
func commandEnvelopePayload(raw json.RawMessage) ([]byte, error) {
	var probe struct {
		Enc string `json:"enc"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Enc == "" {
		return nil, errors.New("envelope must be a JSON object with a string enc field")
	}
	return []byte(raw), nil
}

// rejectPlaintextDownlink enforces the M13 pairing gate (docs/17 §6.7): a
// device that registered e2ee=true accepts ONLY sealed {envelope} command
// payloads on the client command endpoints — a plaintext body is refused with
// 409 pairing_required (the device could not decrypt it, and a server-side
// plaintext path would defeat the E2EE guarantee). e2ee=false devices keep
// the M3/M4 plaintext behavior untouched. Returns true when it wrote the
// rejection — the caller must stop.
func rejectPlaintextDownlink(w http.ResponseWriter, d *domain.Device) bool {
	if !d.E2EE {
		return false
	}
	writeError(w, http.StatusConflict, "pairing_required",
		"this device has end-to-end encryption active — pair a client and send the encrypted envelope form")
	return true
}

// handleDeviceBrowseWorkspace queues a read-only directory listing on the
// desktop. The response is collected through GET .../commands/{cid}; keeping
// enqueue and result reads separate avoids holding an HTTP request open while
// the device's long-poll receives and executes the command.
func (s *Server) handleDeviceBrowseWorkspace(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	var req struct {
		Path     string          `json:"path,omitempty"`
		Envelope json.RawMessage `json:"envelope,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if len(req.Envelope) > 0 {
		if strings.TrimSpace(req.Path) != "" {
			writeError(w, http.StatusBadRequest, "bad_request", "send either path or envelope, not both")
			return
		}
		env, err := commandEnvelopePayload(req.Envelope)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		s.enqueueDeviceCommandRaw(w, r, d, domain.DeviceCmdWorkspaceBrowse, nil, env)
		return
	}
	if rejectPlaintextDownlink(w, d) {
		return
	}
	s.enqueueDeviceCommand(w, r, d, domain.DeviceCmdWorkspaceBrowse, nil, map[string]any{"path": req.Path})
}

type deviceSendMessageReq struct {
	Text     string          `json:"text"`
	Mode     string          `json:"mode,omitempty"`
	Envelope json.RawMessage `json:"envelope,omitempty"`
}

// handleDeviceSendMessage enqueues a chat.send command (docs/17 §4.3). sid
// "new" starts a NEW session — the command's session_id is null and the
// connector allocates the local id (mirrored back via the sessions upsert);
// the response's session_id is then null too. channel is pinned to "console"
// (this is the console/mobile entry point).
//
// The body is one of two shapes (docs/17 §6.2 gray rollout): {text, mode?} —
// the server builds the plaintext payload itself — or {envelope} — a client-
// side E2EE ciphertext of the same payload, stored verbatim and never parsed.
// Pairing gate (M13, docs/17 §6.7): an e2ee=true device accepts only the
// envelope shape; plaintext gets 409 pairing_required.
func (s *Server) handleDeviceSendMessage(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	var req deviceSendMessageReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	var sessionID *string
	if sid := r.PathValue("sid"); sid != "new" {
		sessionID = &sid
	}
	if len(req.Envelope) > 0 {
		if strings.TrimSpace(req.Text) != "" {
			writeError(w, http.StatusBadRequest, "bad_request", "send either text or envelope, not both")
			return
		}
		env, err := commandEnvelopePayload(req.Envelope)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		s.enqueueDeviceCommandRaw(w, r, d, domain.DeviceCmdChatSend, sessionID, env)
		return
	}
	if rejectPlaintextDownlink(w, d) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "text is required")
		return
	}
	payload := map[string]any{"text": req.Text, "channel": "console"}
	if req.Mode != "" {
		payload["mode"] = req.Mode
	}
	s.enqueueDeviceCommand(w, r, d, domain.DeviceCmdChatSend, sessionID, payload)
}

// handleDeviceStopSession enqueues a chat.stop command for a running session
// (docs/17 §4.3). The payload is normally empty ({}); an E2EE client MAY send
// {envelope} instead, which is stored verbatim like every other ciphertext.
// The target session rides the command's outer session_id field either way.
// On an e2ee=true device the envelope form is REQUIRED (M13 pairing gate —
// the empty/plaintext body gets 409 pairing_required).
func (s *Server) handleDeviceStopSession(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	sid := r.PathValue("sid")
	// The body is optional (an empty POST stays valid): a present envelope is
	// the ciphertext payload, no body means the plaintext {}.
	var req struct {
		Envelope json.RawMessage `json:"envelope,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if len(req.Envelope) > 0 {
		env, err := commandEnvelopePayload(req.Envelope)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		s.enqueueDeviceCommandRaw(w, r, d, domain.DeviceCmdChatStop, &sid, env)
		return
	}
	if rejectPlaintextDownlink(w, d) {
		return
	}
	s.enqueueDeviceCommand(w, r, d, domain.DeviceCmdChatStop, &sid, map[string]any{})
}

// handleDeleteDeviceSession asks the online desktop to delete the local
// session, then immediately removes the cloud mirror. If the local execution
// fails, the connector's next replace snapshot restores the mirror, keeping
// the desktop authoritative without leaving a stale row in the UI meanwhile.
func (s *Server) handleDeleteDeviceSession(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	if !s.deviceOnline(d) {
		writeError(w, http.StatusConflict, "device_offline", "the device is offline — the command would not be delivered")
		return
	}
	sid := r.PathValue("sid")
	var req struct {
		Envelope json.RawMessage `json:"envelope,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	var env []byte
	var err error
	if len(req.Envelope) > 0 {
		env, err = commandEnvelopePayload(req.Envelope)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	} else {
		if rejectPlaintextDownlink(w, d) {
			return
		}
		env = []byte(`{}`)
	}
	c := &domain.DeviceCommand{
		ID: domain.NewID(), DeviceID: d.ID, Kind: domain.DeviceCmdSessionDelete,
		SessionID: &sid, Envelope: env, Status: domain.DeviceCommandPending,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateDeviceCommand(r.Context(), c); err != nil {
		s.log.Error("enqueue device command", "device", d.ID, "kind", c.Kind, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not enqueue the command")
		return
	}
	if err := s.st.DeleteDeviceSession(r.Context(), d.ID, sid); err != nil {
		s.log.Error("delete device session mirror", "device", d.ID, "session", sid, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete the session mirror")
		return
	}
	writeJSON(w, http.StatusAccepted, deviceCommandAcceptedView{CommandID: c.ID, SessionID: &sid})
}

type deviceApprovalReq struct {
	ApprovalID string          `json:"approval_id"`
	Decision   string          `json:"decision"`
	Envelope   json.RawMessage `json:"envelope,omitempty"`
}

// handleDeviceApproval enqueues an approval.respond command (docs/17 §4.3):
// the user's answer to a permission request the device raised. The body is
// {approval_id, decision} in plaintext, or {envelope} — the E2EE ciphertext
// of that same payload, stored verbatim. On an e2ee=true device only the
// envelope form is accepted (M13 pairing gate, 409 pairing_required).
func (s *Server) handleDeviceApproval(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	var req deviceApprovalReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	sid := r.PathValue("sid")
	if len(req.Envelope) > 0 {
		if strings.TrimSpace(req.ApprovalID) != "" || strings.TrimSpace(req.Decision) != "" {
			writeError(w, http.StatusBadRequest, "bad_request", "send either approval_id/decision or envelope, not both")
			return
		}
		env, err := commandEnvelopePayload(req.Envelope)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		s.enqueueDeviceCommandRaw(w, r, d, domain.DeviceCmdApprovalRespond, &sid, env)
		return
	}
	if rejectPlaintextDownlink(w, d) {
		return
	}
	if strings.TrimSpace(req.ApprovalID) == "" || strings.TrimSpace(req.Decision) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "approval_id and decision are required")
		return
	}
	s.enqueueDeviceCommand(w, r, d, domain.DeviceCmdApprovalRespond, &sid, map[string]any{
		"approval_id": req.ApprovalID,
		"decision":    req.Decision,
	})
}
