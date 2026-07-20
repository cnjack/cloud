package api

import (
	"encoding/json"
	"errors"
	"fmt"
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
// principal's user. On failure it writes the error (404 unknown, 403 not the
// owner — a principal without a user, e.g. the service principal, is always
// 403) and returns nil; the caller must stop.
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
	if uid := principalFrom(r.Context()).userID(); uid == "" || d.UserID != uid {
		writeError(w, http.StatusForbidden, "forbidden", "this device belongs to another user")
		return nil
	}
	return d
}

type deviceView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Hostname     string     `json:"hostname,omitempty"`
	JcodeVersion string     `json:"jcode_version,omitempty"`
	Pubkey       string     `json:"pubkey,omitempty"`
	KeyGen       int        `json:"key_gen"`
	Online       bool       `json:"online"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (s *Server) toDeviceView(d *domain.Device) deviceView {
	return deviceView{
		ID:           d.ID,
		Name:         d.Name,
		Hostname:     d.Hostname,
		JcodeVersion: d.JcodeVersion,
		Pubkey:       d.Pubkey,
		KeyGen:       d.KeyGen,
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

// enqueueDeviceCommand is the shared tail of the command entry points: it
// rejects offline devices (409 device_offline — a queued command would sit
// unobserved; fail visibly instead) and enqueues the command row the
// connector's poll delivers.
func (s *Server) enqueueDeviceCommand(w http.ResponseWriter, r *http.Request, d *domain.Device, kind string, sessionID *string, payload map[string]any) {
	if !s.deviceOnline(d) {
		writeError(w, http.StatusConflict, "device_offline", "the device is offline — the command would not be delivered")
		return
	}
	env, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not encode the command")
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

type deviceSendMessageReq struct {
	Text string `json:"text"`
	Mode string `json:"mode,omitempty"`
}

// handleDeviceSendMessage enqueues a chat.send command (docs/17 §4.3). sid
// "new" starts a NEW session — the command's session_id is null and the
// connector allocates the local id (mirrored back via the sessions upsert);
// the response's session_id is then null too. channel is pinned to "console"
// (this is the console/mobile entry point).
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
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "text is required")
		return
	}
	var sessionID *string
	if sid := r.PathValue("sid"); sid != "new" {
		sessionID = &sid
	}
	payload := map[string]any{"text": req.Text, "channel": "console"}
	if req.Mode != "" {
		payload["mode"] = req.Mode
	}
	s.enqueueDeviceCommand(w, r, d, domain.DeviceCmdChatSend, sessionID, payload)
}

// handleDeviceStopSession enqueues a chat.stop command for a running session
// (docs/17 §4.3). The payload is empty; the target session rides the command's
// outer session_id field.
func (s *Server) handleDeviceStopSession(w http.ResponseWriter, r *http.Request) {
	d := s.authorizeDevice(w, r, r.PathValue("id"))
	if d == nil {
		return
	}
	sid := r.PathValue("sid")
	s.enqueueDeviceCommand(w, r, d, domain.DeviceCmdChatStop, &sid, map[string]any{})
}

type deviceApprovalReq struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
}

// handleDeviceApproval enqueues an approval.respond command (docs/17 §4.3):
// the user's answer to a permission request the device raised.
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
	if strings.TrimSpace(req.ApprovalID) == "" || strings.TrimSpace(req.Decision) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "approval_id and decision are required")
		return
	}
	sid := r.PathValue("sid")
	s.enqueueDeviceCommand(w, r, d, domain.DeviceCmdApprovalRespond, &sid, map[string]any{
		"approval_id": req.ApprovalID,
		"decision":    req.Decision,
	})
}
