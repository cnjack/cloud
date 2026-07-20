package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
)

// --- uplink: sessions / events / ephemeral ------------------------------------

func TestDeviceRelayUplinkAuth(t *testing.T) {
	fx := setupDevice(t)

	paths := []struct{ method, path string }{
		{http.MethodPost, "/internal/v1/device/sessions"},
		{http.MethodPost, "/internal/v1/device/sessions/s1/events"},
		{http.MethodPost, "/internal/v1/device/sessions/s1/ephemeral"},
		{http.MethodGet, "/internal/v1/device/poll"},
		{http.MethodPost, "/internal/v1/device/commands/c1/ack"},
	}
	for _, tc := range paths {
		for _, tok := range []string{"", "jcd_deadbeef", fx.sess, consoleToken} {
			resp := do(t, tc.method, fx.ts.URL+tc.path, tok, map[string]any{})
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s token=%q: status=%d want 401", tc.method, tc.path, tok, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}
}

func TestDeviceSessionsUpsert(t *testing.T) {
	fx := setupDevice(t)
	token, _ := fx.redeemFlow(t)

	// Fresh sessions upsert with last_seq 0 (no durable events yet).
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions", token, map[string]any{
		"sessions": []map[string]any{
			{"session_id": "s1", "status": "running", "meta": map[string]any{"title": "first"}},
			{"session_id": "s2", "status": "idle"},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upsert: status=%d want 200", resp.StatusCode)
	}
	var v struct {
		Sessions []deviceSessionUpsertView `json:"sessions"`
	}
	decode(t, resp, &v)
	if len(v.Sessions) != 2 || v.Sessions[0].SessionID != "s1" || v.Sessions[0].LastSeq != 0 || v.Sessions[1].LastSeq != 0 {
		t.Fatalf("upsert view = %+v want 2 sessions with last_seq 0", v)
	}

	// The meta blob is stored verbatim (asserted end-to-end via the client
	// sessions endpoint in TestClientDevicesAuthz).

	// After events land, a re-upsert reports the new last_seq (the connector's
	// reconnect cursor).
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions/s1/events", token, map[string]any{
		"events": []map[string]any{
			{"seq": 1, "kind": "user", "payload": map[string]any{"text": "hi"}},
			{"seq": 2, "kind": "assistant", "payload": map[string]any{"text": "yo"}},
		},
	})
	resp.Body.Close()
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions", token, map[string]any{
		"sessions": []map[string]any{{"session_id": "s1", "status": "idle", "meta": map[string]any{"title": "renamed"}}},
	})
	decode(t, resp, &v)
	if len(v.Sessions) != 1 || v.Sessions[0].LastSeq != 2 {
		t.Fatalf("re-upsert view = %+v want last_seq 2", v)
	}

	// Validation: bad status / missing session_id / unknown field → 400.
	for _, body := range []map[string]any{
		{"sessions": []map[string]any{{"session_id": "s1", "status": "busy"}}},
		{"sessions": []map[string]any{{"status": "idle"}}},
		{"sessions": []map[string]any{}, "surprise": 1},
	} {
		resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions", token, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("upsert %v: status=%d want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// deviceIDOf resolves the device a token belongs to (test helper).
func deviceIDOf(t *testing.T, fx deviceFixture, token string) string {
	t.Helper()
	dt, err := fx.st.GetDeviceTokenByHash(t.Context(), auth.HashToken(token))
	if err != nil {
		t.Fatalf("resolve device token: %v", err)
	}
	return dt.DeviceID
}

func TestDeviceEventsIdempotent(t *testing.T) {
	fx := setupDevice(t)
	token, deviceID := fx.redeemFlow(t)

	post := func(events []map[string]any) deviceEventsView {
		t.Helper()
		resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions/s1/events", token,
			map[string]any{"events": events})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("events: status=%d want 200", resp.StatusCode)
		}
		var v deviceEventsView
		decode(t, resp, &v)
		return v
	}

	v := post([]map[string]any{
		{"seq": 1, "kind": "user", "payload": map[string]any{"text": "a"}},
		{"seq": 2, "kind": "assistant", "payload": map[string]any{"text": "b"}},
		{"seq": 3, "kind": "tool_call", "payload": map[string]any{"name": "bash"}},
	})
	if len(v.Accepted) != 3 || len(v.Conflicted) != 0 || v.MaxSeq != 3 {
		t.Fatalf("first batch = %+v want 3 accepted / max 3", v)
	}

	// Idempotent replay: the overlapping seqs are skipped, never errors.
	v = post([]map[string]any{
		{"seq": 2, "kind": "assistant", "payload": map[string]any{"text": "b"}},
		{"seq": 3, "kind": "tool_call", "payload": map[string]any{"name": "bash"}},
		{"seq": 4, "kind": "tool_result", "payload": map[string]any{"out": "ok"}},
	})
	if len(v.Accepted) != 1 || v.Accepted[0] != 4 || len(v.Conflicted) != 2 || v.MaxSeq != 4 {
		t.Fatalf("replay batch = %+v want accepted [4] / conflicted [2,3] / max 4", v)
	}

	// The durable log replays ascending through the client endpoint.
	u := mustUser(t, fx, deviceID)
	resp := do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/s1/events?after_seq=1&limit=2", u, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("client events: status=%d want 200", resp.StatusCode)
	}
	var lv struct {
		Events []deviceEventView `json:"events"`
	}
	decode(t, resp, &lv)
	if len(lv.Events) != 2 || lv.Events[0].Seq != 2 || lv.Events[1].Seq != 3 {
		t.Fatalf("client events = %+v want seqs 2,3", lv.Events)
	}
	// Payload round-trips verbatim.
	var payload map[string]any
	if err := json.Unmarshal(lv.Events[0].Payload, &payload); err != nil || payload["text"] != "b" {
		t.Fatalf("payload = %s want verbatim", lv.Events[0].Payload)
	}

	// Validation: seq<=0 / missing kind → 400.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions/s1/events", token,
		map[string]any{"events": []map[string]any{{"seq": 0, "kind": "user"}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("seq 0: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// mustUser returns a session token for the user owning deviceID.
func mustUser(t *testing.T, fx deviceFixture, deviceID string) string {
	t.Helper()
	d, err := fx.st.GetDevice(t.Context(), deviceID)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	return mkSession(t, fx.st, d.UserID)
}

func TestDeviceEphemeral(t *testing.T) {
	fx := setupDevice(t)
	token, _ := fx.redeemFlow(t)

	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions/s1/ephemeral", token,
		map[string]any{"kind": "assistant_delta", "payload": map[string]any{"delta": "hel"}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ephemeral: status=%d want 202", resp.StatusCode)
	}
	resp.Body.Close()

	// Nothing was persisted (ephemeral never touches the durable log).
	if max, err := fx.st.MaxDeviceEventSeq(t.Context(), deviceIDOf(t, fx, token), "s1"); err != nil || max != 0 {
		t.Fatalf("ephemeral persisted: max=%d err=%v want 0", max, err)
	}

	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions/s1/ephemeral", token,
		map[string]any{"payload": map[string]any{}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ephemeral without kind: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- downlink: poll + ack -----------------------------------------------------

// onlineDevice drives a login + heartbeat so the device reads online, and
// returns (deviceToken, deviceID, ownerSession).
func onlineDevice(t *testing.T, fx deviceFixture) (string, string, string) {
	t.Helper()
	token, deviceID := fx.redeemFlow(t)
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/heartbeat", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
	return token, deviceID, mustUser(t, fx, deviceID)
}

func TestDevicePollAndAck(t *testing.T) {
	fx := setupDevice(t)
	// Fast long-poll timings for the test.
	fx.srv.devicePollMaxHold = 500 * time.Millisecond
	fx.srv.devicePollTick = 10 * time.Millisecond
	token, deviceID, owner := onlineDevice(t, fx)

	// Enqueue a command as the owner (chat.send to a new session).
	resp := do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/new/messages", owner,
		map[string]any{"text": "hello jcode", "mode": "code"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send message: status=%d want 202", resp.StatusCode)
	}
	var accepted deviceCommandAcceptedView
	decode(t, resp, &accepted)
	if accepted.CommandID == "" || accepted.SessionID != nil {
		t.Fatalf("accepted view = %+v want command_id + null session_id", accepted)
	}

	// Poll delivers it immediately (no hold).
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/poll?wait=1s", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll: status=%d want 200", resp.StatusCode)
	}
	var pv struct {
		Commands []deviceCommandView `json:"commands"`
	}
	decode(t, resp, &pv)
	if len(pv.Commands) != 1 {
		t.Fatalf("poll commands = %+v want 1", pv.Commands)
	}
	cmd := pv.Commands[0]
	if cmd.ID != accepted.CommandID || cmd.Kind != domain.DeviceCmdChatSend || cmd.SessionID != nil {
		t.Fatalf("command = %+v want chat.send with null session_id", cmd)
	}
	var payload map[string]any
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		t.Fatalf("command payload: %v", err)
	}
	if payload["text"] != "hello jcode" || payload["channel"] != "console" || payload["mode"] != "code" {
		t.Fatalf("command payload = %v want text/channel:console/mode", payload)
	}

	// A command is delivered ONCE: the next poll holds and answers 204.
	start := time.Now()
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/poll?wait=100ms", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("empty poll: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Fatalf("empty poll returned after %v, want a ~100ms hold", elapsed)
	}

	// Ack ok → 200; a duplicate ack is an idempotent 200; unknown id → 404.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/commands/"+cmd.ID+"/ack", token,
		map[string]any{"status": "ok", "result": map[string]any{"session_id": "local-1"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/commands/"+cmd.ID+"/ack", token,
		map[string]any{"status": "ok"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duplicate ack: status=%d want 200 (idempotent)", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/commands/nope/ack", token,
		map[string]any{"status": "ok"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ack unknown: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/commands/"+cmd.ID+"/ack", token,
		map[string]any{"status": "maybe"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ack bad status: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Malformed wait → 400.
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/poll?wait=soon", token, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad wait: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- client endpoints -----------------------------------------------------------

func TestClientDevicesAuthz(t *testing.T) {
	fx := setupDevice(t)
	token, deviceID, owner := onlineDevice(t, fx)
	_ = token
	stranger := mkSession(t, fx.st, mkUser(t, fx.st, "stranger").ID)

	// List: the owner's device shows online; the stranger's list is empty.
	resp := do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices", owner, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status=%d want 200", resp.StatusCode)
	}
	var lv struct {
		Devices []deviceView `json:"devices"`
	}
	decode(t, resp, &lv)
	if len(lv.Devices) != 1 || lv.Devices[0].ID != deviceID || !lv.Devices[0].Online {
		t.Fatalf("list = %+v want the one online device", lv.Devices)
	}
	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices", stranger, nil)
	decode(t, resp, &lv)
	if len(lv.Devices) != 0 {
		t.Fatalf("stranger list = %+v want empty", lv.Devices)
	}

	// Detail: owner 200, stranger 403, unknown 404, unauthenticated 401, the
	// CONSOLE_TOKEN (no user) 403.
	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID, owner, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status=%d want 200", resp.StatusCode)
	}
	var dv deviceView
	decode(t, resp, &dv)
	if dv.ID != deviceID || !dv.Online {
		t.Fatalf("detail = %+v", dv)
	}
	for _, tc := range []struct {
		token, id string
		want      int
	}{
		{stranger, deviceID, http.StatusForbidden},
		{owner, "no-such-device", http.StatusNotFound},
		{"", deviceID, http.StatusUnauthorized},
		{consoleToken, deviceID, http.StatusForbidden},
		{consoleToken, "/api/v1/devices", http.StatusBadRequest}, // list: no user
	} {
		id := tc.id
		if !strings.HasPrefix(id, "/") {
			id = "/api/v1/devices/" + id
		}
		resp = do(t, http.MethodGet, fx.ts.URL+id, tc.token, nil)
		if resp.StatusCode != tc.want {
			t.Fatalf("GET %s token=%q: status=%d want %d", id, tc.token, resp.StatusCode, tc.want)
		}
		resp.Body.Close()
	}

	// Sessions index: meta verbatim, stranger 403.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions", token, map[string]any{
		"sessions": []map[string]any{{"session_id": "s1", "status": "running", "meta": map[string]any{"title": "t"}}},
	})
	resp.Body.Close()
	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions", owner, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sessions: status=%d want 200", resp.StatusCode)
	}
	var sv struct {
		Sessions []deviceSessionView `json:"sessions"`
	}
	decode(t, resp, &sv)
	if len(sv.Sessions) != 1 || sv.Sessions[0].SessionID != "s1" || sv.Sessions[0].Status != "running" {
		t.Fatalf("sessions = %+v", sv.Sessions)
	}
	var meta map[string]any
	if err := json.Unmarshal(sv.Sessions[0].Meta, &meta); err != nil || meta["title"] != "t" {
		t.Fatalf("meta = %s want verbatim", sv.Sessions[0].Meta)
	}
	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions", stranger, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger sessions: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestClientCommandsOffline409(t *testing.T) {
	fx := setupDevice(t)
	// redeem WITHOUT heartbeat: the device has no last_seen_at → offline.
	_, deviceID := fx.redeemFlow(t)
	owner := mustUser(t, fx, deviceID)

	for _, tc := range []struct {
		path string
		body map[string]any
	}{
		{"/sessions/s1/messages", map[string]any{"text": "hi"}},
		{"/sessions/s1/stop", nil},
		{"/sessions/s1/approval", map[string]any{"approval_id": "a1", "decision": "allow"}},
	} {
		resp := do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+tc.path, owner, tc.body)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("POST %s offline: status=%d want 409", tc.path, resp.StatusCode)
		}
		var env errorBody
		decode(t, resp, &env)
		if env.Error.Code != "device_offline" {
			t.Fatalf("POST %s: code=%q want device_offline", tc.path, env.Error.Code)
		}
	}

	// Validation fires before the offline check? No — ownership + body first:
	// an empty text is a 400 even on an offline device... verify online path
	// enqueues the right commands instead.
	token, _, _ := onlineDevice(t, fx)
	_ = token
	owner = mustUser(t, fx, deviceIDOf(t, fx, token))

	resp := do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceIDOf(t, fx, token)+"/sessions/s1/stop", owner, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop: status=%d want 202", resp.StatusCode)
	}
	var av deviceCommandAcceptedView
	decode(t, resp, &av)
	if av.SessionID == nil || *av.SessionID != "s1" {
		t.Fatalf("stop view = %+v want session_id s1", av)
	}

	resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceIDOf(t, fx, token)+"/sessions/s1/approval", owner,
		map[string]any{"approval_id": "a1", "decision": "allow"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("approval: status=%d want 202", resp.StatusCode)
	}
	decode(t, resp, &av)

	// The enqueued rows carry the contract kinds/payloads.
	cmds, err := fx.st.DeliverPendingDeviceCommands(t.Context(), deviceIDOf(t, fx, token), 64)
	if err != nil || len(cmds) != 2 {
		t.Fatalf("pending = %v err=%v want 2", len(cmds), err)
	}
	if cmds[0].Kind != domain.DeviceCmdChatStop || cmds[1].Kind != domain.DeviceCmdApprovalRespond {
		t.Fatalf("kinds = %q,%q want chat.stop,approval.respond", cmds[0].Kind, cmds[1].Kind)
	}
	var payload map[string]any
	if err := json.Unmarshal(cmds[1].Envelope, &payload); err != nil ||
		payload["approval_id"] != "a1" || payload["decision"] != "allow" {
		t.Fatalf("approval payload = %s", cmds[1].Envelope)
	}
}

// --- SSE stream -----------------------------------------------------------------

// readSSEFrame reads one event frame (terminated by a blank line) and returns
// the event type and data payload.
func readSSEFrame(t *testing.T, r *bufio.Reader) (string, string) {
	t.Helper()
	var event, data string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if event != "" || data != "" {
				return event, data
			}
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func TestDeviceStreamSSE(t *testing.T) {
	fx := setupDevice(t)
	token, deviceID := fx.redeemFlow(t)
	owner := mustUser(t, fx, deviceID)

	// The stream accepts ?access_token= (EventSource cannot set a header) and
	// starts with the current device.status (offline — no heartbeat yet).
	resp, err := http.Get(fx.ts.URL + "/api/v1/devices/" + deviceID + "/stream?access_token=" + owner)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: status=%d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("stream content-type = %q", ct)
	}
	r := bufio.NewReader(resp.Body)

	event, data := readSSEFrame(t, r)
	if event != "device.status" {
		t.Fatalf("first frame event = %q want device.status", event)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(data), &st); err != nil || st["online"] != false {
		t.Fatalf("first frame data = %s want {\"online\":false}", data)
	}

	// A register announces the offline→online edge.
	reg := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/register", token,
		map[string]any{"pubkey": "pk", "hostname": "box"})
	reg.Body.Close()
	event, data = readSSEFrame(t, r)
	if event != "device.status" {
		t.Fatalf("second frame event = %q want device.status", event)
	}
	if err := json.Unmarshal([]byte(data), &st); err != nil || st["online"] != true {
		t.Fatalf("second frame data = %s want {\"online\":true}", data)
	}

	// A durable event lands as session.event with its seq/kind/payload.
	ev := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions/s1/events", token, map[string]any{
		"events": []map[string]any{{"seq": 1, "kind": "user", "payload": map[string]any{"text": "hi"}}},
	})
	ev.Body.Close()
	event, data = readSSEFrame(t, r)
	if event != "session.event" {
		t.Fatalf("third frame event = %q want session.event", event)
	}
	var se map[string]any
	if err := json.Unmarshal([]byte(data), &se); err != nil {
		t.Fatalf("session.event data: %v", err)
	}
	if se["session_id"] != "s1" || se["seq"] != float64(1) || se["kind"] != "user" {
		t.Fatalf("session.event data = %s", data)
	}
	if p, _ := se["payload"].(map[string]any); p["text"] != "hi" {
		t.Fatalf("session.event payload = %v", se["payload"])
	}

	// An ephemeral event forwards as session.delta (still nothing persisted).
	eph := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/sessions/s1/ephemeral", token,
		map[string]any{"kind": "assistant_delta", "payload": map[string]any{"delta": "hel"}})
	eph.Body.Close()
	event, data = readSSEFrame(t, r)
	if event != "session.delta" {
		t.Fatalf("fourth frame event = %q want session.delta", event)
	}
	var sd map[string]any
	if err := json.Unmarshal([]byte(data), &sd); err != nil || sd["session_id"] != "s1" || sd["kind"] != "assistant_delta" {
		t.Fatalf("session.delta data = %s", data)
	}

	// A stranger cannot open the stream.
	bad := do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID+"/stream",
		mkSession(t, fx.st, mkUser(t, fx.st, "streamer-stranger").ID), nil)
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger stream: status=%d want 403", bad.StatusCode)
	}
	bad.Body.Close()
}
