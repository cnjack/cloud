package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// --- pairings: client create + status, device list + respond, expiry ----------

// createPairing POSTs a pairing as the owner and returns its id.
func createPairing(t *testing.T, fx deviceFixture, deviceID, owner, label string) string {
	t.Helper()
	resp := do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/pairings", owner,
		map[string]any{"label": label, "kty": "P-256", "pubkey": "b64-spki"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pairing: status=%d want 201", resp.StatusCode)
	}
	var v struct {
		PairingID string `json:"pairing_id"`
		Status    string `json:"status"`
	}
	decode(t, resp, &v)
	if v.PairingID == "" || v.Status != domain.DevicePairingPending {
		t.Fatalf("create pairing view = %+v want id + pending", v)
	}
	return v.PairingID
}

// pairingStatus reads the client-side pairing view.
func pairingStatus(t *testing.T, fx deviceFixture, deviceID, owner, pid string) pairingStatusView {
	t.Helper()
	resp := do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID+"/pairings/"+pid, owner, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get pairing: status=%d want 200", resp.StatusCode)
	}
	var v pairingStatusView
	decode(t, resp, &v)
	return v
}

func TestDevicePairingFlow(t *testing.T) {
	fx := setupDevice(t)
	token, deviceID, owner := onlineDevice(t, fx)

	pid := createPairing(t, fx, deviceID, owner, "console-web")

	// The pairing.request command is queued for the device with the contract
	// payload (pairing_id/label/kty/pubkey, session_id null).
	resp := do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/poll?wait=1s", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll: status=%d want 200", resp.StatusCode)
	}
	var pv struct {
		Commands []deviceCommandView `json:"commands"`
	}
	decode(t, resp, &pv)
	if len(pv.Commands) != 1 || pv.Commands[0].Kind != domain.DeviceCmdPairingRequest || pv.Commands[0].SessionID != nil {
		t.Fatalf("poll commands = %+v want one pairing.request with null session_id", pv.Commands)
	}
	var payload map[string]any
	if err := json.Unmarshal(pv.Commands[0].Payload, &payload); err != nil {
		t.Fatalf("pairing.request payload: %v", err)
	}
	if payload["pairing_id"] != pid || payload["label"] != "console-web" ||
		payload["kty"] != "P-256" || payload["pubkey"] != "b64-spki" {
		t.Fatalf("pairing.request payload = %v", payload)
	}

	// The device lists its pending pairings: id/label/created_at only.
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/pairings?status=pending", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list pairings: status=%d want 200", resp.StatusCode)
	}
	var lv struct {
		Pairings []devicePairingView `json:"pairings"`
	}
	decode(t, resp, &lv)
	if len(lv.Pairings) != 1 || lv.Pairings[0].ID != pid || lv.Pairings[0].Label != "console-web" || lv.Pairings[0].CreatedAt.IsZero() {
		t.Fatalf("pending pairings = %+v", lv.Pairings)
	}

	// The device fetches the single pairing (incl. the requester pubkey) —
	// `jcode cloud approve` wraps the CEK for exactly this key.
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/pairings/"+pid, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get own pairing: status=%d want 200", resp.StatusCode)
	}
	var gv struct {
		ID     string `json:"id"`
		Label  string `json:"label"`
		Pubkey string `json:"pubkey"`
		Status string `json:"status"`
	}
	decode(t, resp, &gv)
	if gv.ID != pid || gv.Label != "console-web" || gv.Pubkey != "b64-spki" || gv.Status != domain.DevicePairingPending {
		t.Fatalf("own pairing = %+v", gv)
	}
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/pairings/"+domain.NewID(), token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get unknown pairing: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// While pending, the client status read shows pending without a wrap.
	if v := pairingStatus(t, fx, deviceID, owner, pid); v.Status != domain.DevicePairingPending || v.Wrap != nil {
		t.Fatalf("pending view = %+v", v)
	}

	// Approve without a wrap → 400.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/pairings/"+pid+"/respond", token,
		map[string]any{"approve": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("approve without wrap: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Approve with the wrapped CEK blob.
	wrap := map[string]any{"ephemeral_pubkey": "b64-eph", "nonce": "b64-n", "ct": "b64-ct"}
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/pairings/"+pid+"/respond", token,
		map[string]any{"approve": true, "wrap": wrap})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("respond approve: status=%d want 200", resp.StatusCode)
	}
	var rv struct {
		Status string `json:"status"`
	}
	decode(t, resp, &rv)
	if rv.Status != domain.DevicePairingApproved {
		t.Fatalf("respond view = %+v want approved", rv)
	}

	// The client now reads approved + the wrap blob verbatim.
	v := pairingStatus(t, fx, deviceID, owner, pid)
	if v.Status != domain.DevicePairingApproved {
		t.Fatalf("approved view = %+v", v)
	}
	var gotWrap map[string]any
	if err := json.Unmarshal(v.Wrap, &gotWrap); err != nil || gotWrap["ephemeral_pubkey"] != "b64-eph" || gotWrap["ct"] != "b64-ct" {
		t.Fatalf("wrap = %s want verbatim", v.Wrap)
	}

	// Idempotent: re-responding (approve or deny) is a 200 no-op reporting the
	// stored status.
	for _, body := range []map[string]any{
		{"approve": true, "wrap": wrap},
		{"approve": false},
	} {
		resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/pairings/"+pid+"/respond", token, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("duplicate respond %v: status=%d want 200", body, resp.StatusCode)
		}
		decode(t, resp, &rv)
		if rv.Status != domain.DevicePairingApproved {
			t.Fatalf("duplicate respond %v: status=%q want approved (unchanged)", body, rv.Status)
		}
	}
}

func TestDevicePairingDenyAndAuthz(t *testing.T) {
	fx := setupDevice(t)
	token, deviceID, owner := onlineDevice(t, fx)
	stranger := mkSession(t, fx.st, mkUser(t, fx.st, "pair-stranger").ID)

	pid := createPairing(t, fx, deviceID, owner, "phone")

	// Deny.
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/pairings/"+pid+"/respond", token,
		map[string]any{"approve": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deny: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if v := pairingStatus(t, fx, deviceID, owner, pid); v.Status != domain.DevicePairingDenied || v.Wrap != nil {
		t.Fatalf("denied view = %+v", v)
	}

	// A stranger cannot create/read pairings; an unknown pairing is 404; the
	// device token is not a client credential (403 — no owning user).
	resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/pairings", stranger,
		map[string]any{"label": "x", "kty": "P-256", "pubkey": "y"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger create: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID+"/pairings/"+pid, stranger, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger read: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID+"/pairings/nope", owner, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown pairing: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/pairings", token,
		map[string]any{"label": "x", "kty": "P-256", "pubkey": "y"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("device token on client endpoint: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Device-side endpoints reject non-device principals (401) and another
	// device's pairing ids are 404 for THIS device.
	for _, tok := range []string{"", owner, consoleToken} {
		resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/pairings", tok, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("list pairings token=%q: status=%d want 401", tok, resp.StatusCode)
		}
		resp.Body.Close()
		resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/pairings/"+pid+"/respond", tok,
			map[string]any{"approve": false})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("respond token=%q: status=%d want 401", tok, resp.StatusCode)
		}
		resp.Body.Close()
	}
	token2, _ := fx.redeemFlow(t)
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/pairings/"+pid+"/respond", token2,
		map[string]any{"approve": false})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other device respond: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Validation: bad kty / missing fields → 400.
	for _, body := range []map[string]any{
		{"label": "x", "kty": "X25519", "pubkey": "y"},
		{"label": "x", "kty": "P-256"},
		{"kty": "P-256", "pubkey": "y"},
	} {
		resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/pairings", owner, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("create %v: status=%d want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestDevicePairingOfflineQueues(t *testing.T) {
	fx := setupDevice(t)
	// No heartbeat: the device is OFFLINE. Unlike chat commands, a pairing
	// request still queues — the device decides on its next poll (docs/17 §6.3).
	_, deviceID := fx.redeemFlow(t)
	owner := mustUser(t, fx, deviceID)
	pid := createPairing(t, fx, deviceID, owner, "console-web")
	if v := pairingStatus(t, fx, deviceID, owner, pid); v.Status != domain.DevicePairingPending {
		t.Fatalf("offline pairing view = %+v want pending", v)
	}
}

func TestDevicePairingExpiry(t *testing.T) {
	fx := setupDevice(t)
	// A zero-second window makes every pending pairing instantly stale.
	fx.srv.devicePairingWindow = time.Nanosecond
	token, deviceID, owner := onlineDevice(t, fx)
	time.Sleep(time.Millisecond)

	pid := createPairing(t, fx, deviceID, owner, "console-web")

	// The client read reports expired and settles the row.
	if v := pairingStatus(t, fx, deviceID, owner, pid); v.Status != domain.DevicePairingExpired || v.Wrap != nil {
		t.Fatalf("expired view = %+v", v)
	}

	// The device can no longer approve it: 409 pairing_expired.
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/pairings/"+pid+"/respond", token,
		map[string]any{"approve": true, "wrap": map[string]any{"ct": "x"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("respond expired: status=%d want 409", resp.StatusCode)
	}
	var env errorBody
	decode(t, resp, &env)
	if env.Error.Code != "pairing_expired" {
		t.Fatalf("respond expired: code=%q want pairing_expired", env.Error.Code)
	}

	// The settled row no longer lists as pending for the device.
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/pairings?status=pending", token, nil)
	var lv struct {
		Pairings []devicePairingView `json:"pairings"`
	}
	decode(t, resp, &lv)
	if len(lv.Pairings) != 0 {
		t.Fatalf("pending after expiry = %+v want empty", lv.Pairings)
	}
}

// --- token self-revocation ------------------------------------------------------

func TestDeviceRevoke(t *testing.T) {
	fx := setupDevice(t)
	token, _ := fx.redeemFlow(t)

	// The token works before revocation.
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/heartbeat", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat pre-revoke: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// Revoke: 204; non-device principals get 401.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/revoke", fx.sess, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoke with session: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/revoke", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// Effective immediately: the next call with the revoked token is a 401 —
	// which makes a retried revoke idempotent by construction.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/heartbeat", token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("heartbeat post-revoke: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/revoke", token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("re-revoke: status=%d want 401 (idempotent)", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- E2EE command envelopes (docs/17 §6.2) ---------------------------------------

// lastCommandPayload drains the device's queue and returns the single
// delivered command's raw payload.
func lastCommandPayload(t *testing.T, fx deviceFixture, deviceID string) (domain.DeviceCommand, map[string]any) {
	t.Helper()
	cmds, err := fx.st.DeliverPendingDeviceCommands(t.Context(), deviceID, 64)
	if err != nil || len(cmds) != 1 {
		t.Fatalf("pending = %d err=%v want 1", len(cmds), err)
	}
	var payload map[string]any
	if err := json.Unmarshal(cmds[0].Envelope, &payload); err != nil {
		t.Fatalf("command payload not JSON: %v", err)
	}
	return cmds[0], payload
}

var testEnvelope = map[string]any{
	"enc":     "aes-256-gcm",
	"key_gen": 1,
	"nonce":   "AAECAwQFBgcICQoL",
	"ct":      "Y2lwaGVydGV4dA==",
}

func TestDeviceSendMessageEnvelope(t *testing.T) {
	fx := setupDevice(t)
	_, deviceID, owner := onlineDevice(t, fx)

	// Envelope form: the payload is the envelope verbatim (enc marker intact).
	resp := do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/s1/messages", owner,
		map[string]any{"envelope": testEnvelope})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send envelope: status=%d want 202", resp.StatusCode)
	}
	resp.Body.Close()
	cmd, payload := lastCommandPayload(t, fx, deviceID)
	if cmd.Kind != domain.DeviceCmdChatSend || cmd.SessionID == nil || *cmd.SessionID != "s1" {
		t.Fatalf("command = %+v want chat.send on s1", cmd)
	}
	if payload["enc"] != "aes-256-gcm" || payload["ct"] != "Y2lwaGVydGV4dA==" {
		t.Fatalf("envelope payload = %v want verbatim", payload)
	}

	// Plaintext form is unchanged (channel pinned by the server).
	resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/s1/messages", owner,
		map[string]any{"text": "hi", "mode": "plan"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send plaintext: status=%d want 202", resp.StatusCode)
	}
	resp.Body.Close()
	_, payload = lastCommandPayload(t, fx, deviceID)
	if payload["text"] != "hi" || payload["channel"] != "console" || payload["mode"] != "plan" {
		t.Fatalf("plaintext payload = %v", payload)
	}

	// Validation: text AND envelope → 400; envelope without a string enc → 400.
	for _, body := range []map[string]any{
		{"text": "hi", "envelope": testEnvelope},
		{"envelope": map[string]any{"nonce": "x", "ct": "y"}},
		{"envelope": "not-an-object"},
	} {
		resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/s1/messages", owner, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("send %v: status=%d want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestDeviceStopAndApprovalEnvelope(t *testing.T) {
	fx := setupDevice(t)
	_, deviceID, owner := onlineDevice(t, fx)

	// chat.stop with an envelope payload.
	resp := do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/s1/stop", owner,
		map[string]any{"envelope": testEnvelope})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop envelope: status=%d want 202", resp.StatusCode)
	}
	resp.Body.Close()
	cmd, payload := lastCommandPayload(t, fx, deviceID)
	if cmd.Kind != domain.DeviceCmdChatStop || payload["enc"] != "aes-256-gcm" {
		t.Fatalf("stop command = %v %v", cmd.Kind, payload)
	}

	// chat.stop with no body still enqueues the empty plaintext payload.
	resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/s1/stop", owner, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop empty: status=%d want 202", resp.StatusCode)
	}
	resp.Body.Close()
	cmd, payload = lastCommandPayload(t, fx, deviceID)
	if cmd.Kind != domain.DeviceCmdChatStop || len(payload) != 0 {
		t.Fatalf("stop empty payload = %v want {}", payload)
	}

	// approval.respond with an envelope payload.
	resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/s1/approval", owner,
		map[string]any{"envelope": testEnvelope})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("approval envelope: status=%d want 202", resp.StatusCode)
	}
	resp.Body.Close()
	cmd, payload = lastCommandPayload(t, fx, deviceID)
	if cmd.Kind != domain.DeviceCmdApprovalRespond || payload["enc"] != "aes-256-gcm" {
		t.Fatalf("approval command = %v %v", cmd.Kind, payload)
	}

	// Validation: approval_id AND envelope → 400.
	resp = do(t, http.MethodPost, fx.ts.URL+"/api/v1/devices/"+deviceID+"/sessions/s1/approval", owner,
		map[string]any{"approval_id": "a1", "decision": "allow", "envelope": testEnvelope})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("approval both: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
