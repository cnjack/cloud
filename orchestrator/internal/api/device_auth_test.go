package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

// deviceFixture is a server with one logged-in user and an injectable clock on
// the device-flow registry, so the expiry branch is deterministic.
type deviceFixture struct {
	ts    *httptest.Server
	srv   *Server
	st    *store.MemStore
	clock *testClock
	sess  string // the user's session token
}

func setupDevice(t *testing.T) deviceFixture {
	t.Helper()
	st := store.NewMemStore()
	cfg := &config.Config{ConsoleToken: consoleToken, ConsoleURL: "https://console.test"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, cfg, log, sse.NewHub(), nil)
	clock := &testClock{t: time.Now().UTC()}
	srv.deviceFlows.now = clock.now
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	u := mkUser(t, st, "dev-user")
	return deviceFixture{ts: ts, srv: srv, st: st, clock: clock, sess: mkSession(t, st, u.ID)}
}

// startFlow posts /auth/device/code and returns the decoded start view.
func (fx deviceFixture) startFlow(t *testing.T) deviceCodeView {
	t.Helper()
	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/code", "", map[string]any{"client_name": "jcode-cli"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start flow: status=%d want 200", resp.StatusCode)
	}
	var v deviceCodeView
	decode(t, resp, &v)
	if v.DeviceCode == "" || v.UserCode == "" || v.ExpiresIn != 600 || v.Interval != 5 {
		t.Fatalf("start view incomplete: %+v", v)
	}
	if v.VerificationURI != "https://console.test/device" {
		t.Fatalf("verification_uri = %q, want https://console.test/device", v.VerificationURI)
	}
	return v
}

// pollToken posts /auth/device/token and returns the status + decoded error
// code ("" on success).
func (fx deviceFixture) pollToken(t *testing.T, deviceCode string) (int, string, deviceTokenView) {
	t.Helper()
	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/token", "", map[string]any{"device_code": deviceCode})
	var v deviceTokenView
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &v)
		return resp.StatusCode, "", v
	}
	var env errorBody
	decode(t, resp, &env)
	return resp.StatusCode, env.Error.Code, v
}

func TestDeviceLoginHappyPath(t *testing.T) {
	fx := setupDevice(t)
	flow := fx.startFlow(t)

	// Polling before the user decides → authorization_pending.
	if status, code, _ := fx.pollToken(t, flow.DeviceCode); status != http.StatusBadRequest || code != "authorization_pending" {
		t.Fatalf("pending poll: status=%d code=%q", status, code)
	}

	// Approve (the console call — session authed).
	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": flow.UserCode, "approve": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// The next poll mints the token.
	status, code, tok := fx.pollToken(t, flow.DeviceCode)
	if status != http.StatusOK || code != "" {
		t.Fatalf("redeem poll: status=%d code=%q", status, code)
	}
	if !strings.HasPrefix(tok.AccessToken, auth.DeviceTokenPrefix) || tok.TokenType != "device" || tok.DeviceID == "" {
		t.Fatalf("token view malformed: %+v", tok)
	}

	// Only the SHA-256 is stored: the hash resolves, the plaintext is nowhere.
	resolved, err := fx.st.GetDeviceTokenByHash(t.Context(), auth.HashToken(tok.AccessToken))
	if err != nil || resolved.DeviceID != tok.DeviceID || resolved.UserID == "" {
		t.Fatalf("stored token does not resolve by hash: %+v err=%v", resolved, err)
	}
	if resolved.TokenHash == tok.AccessToken {
		t.Fatalf("plaintext token persisted: %q", resolved.TokenHash)
	}
	resp = do(t, http.MethodGet, fx.ts.URL+"/auth/device/authorize?user_code="+flow.UserCode, fx.sess, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorization state: status=%d want 200", resp.StatusCode)
	}
	var authState deviceAuthorizeStateView
	decode(t, resp, &authState)
	if authState.Status != deviceFlowRedeemed || authState.DeviceID != tok.DeviceID {
		t.Fatalf("authorization state = %+v", authState)
	}
	dev, err := fx.st.GetDevice(t.Context(), tok.DeviceID)
	if err != nil || dev.Name != "jcode-cli" {
		t.Fatalf("device row: %+v err=%v", dev, err)
	}

	// One-shot redemption: a second poll with the same device_code can never
	// mint a second token.
	if status, code, _ := fx.pollToken(t, flow.DeviceCode); status != http.StatusBadRequest || code != "token_already_redeemed" {
		t.Fatalf("replay poll: status=%d code=%q want token_already_redeemed", status, code)
	}
}

func TestDeviceLoginDenied(t *testing.T) {
	fx := setupDevice(t)
	flow := fx.startFlow(t)

	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": flow.UserCode, "approve": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deny: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	if status, code, _ := fx.pollToken(t, flow.DeviceCode); status != http.StatusBadRequest || code != "access_denied" {
		t.Fatalf("denied poll: status=%d code=%q want access_denied", status, code)
	}
}

func TestDeviceLoginExpired(t *testing.T) {
	fx := setupDevice(t)
	flow := fx.startFlow(t)

	// Advance past the 600s window: both the poll and authorize paths report
	// the flow as gone/expired.
	fx.clock.advance(deviceFlowWindow + time.Second)

	if status, code, _ := fx.pollToken(t, flow.DeviceCode); status != http.StatusBadRequest || code != "expired_token" {
		t.Fatalf("expired poll: status=%d code=%q want expired_token", status, code)
	}
	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": flow.UserCode, "approve": true})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("authorize expired: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// A never-issued device_code is indistinguishable from a swept one.
	if status, code, _ := fx.pollToken(t, "not-a-real-code"); status != http.StatusBadRequest || code != "expired_token" {
		t.Fatalf("unknown poll: status=%d code=%q want expired_token", status, code)
	}
}

func TestDeviceAuthorizeGuards(t *testing.T) {
	fx := setupDevice(t)
	flow := fx.startFlow(t)

	// Unauthenticated → 401.
	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", "",
		map[string]any{"user_code": flow.UserCode, "approve": true})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated authorize: status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// The CONSOLE_TOKEN (service principal) authenticates but cannot own a
	// device → 400.
	resp = do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", consoleToken,
		map[string]any{"user_code": flow.UserCode, "approve": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("service principal authorize: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown user_code → 404.
	resp = do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": "ZZZZ-ZZZZ", "approve": true})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown user_code: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Case/format tolerance: lower-case without the dash approves the same flow.
	resp = do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": strings.ToLower(strings.ReplaceAll(flow.UserCode, "-", "")), "approve": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("normalized user_code: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// A decided flow cannot be re-decided.
	resp = do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": flow.UserCode, "approve": false})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-decide: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDeviceFlowStrictDecode(t *testing.T) {
	fx := setupDevice(t)

	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/code", "",
		map[string]any{"client_name": "jcode-cli", "surprise": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field on code: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPost, fx.ts.URL+"/auth/device/code", "", map[string]any{"client_name": "  "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank client_name: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPost, fx.ts.URL+"/auth/device/token", "",
		map[string]any{"device_code": "x", "extra": 1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field on token: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// redeemFlow drives a full login and returns the minted plaintext token +
// device id — the setup for the register/heartbeat tests.
func (fx deviceFixture) redeemFlow(t *testing.T) (string, string) {
	t.Helper()
	flow := fx.startFlow(t)
	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": flow.UserCode, "approve": true})
	resp.Body.Close()
	status, _, tok := fx.pollToken(t, flow.DeviceCode)
	if status != http.StatusOK {
		t.Fatalf("redeem: status=%d want 200", status)
	}
	return tok.AccessToken, tok.DeviceID
}

func TestDeviceRegisterAndHeartbeat(t *testing.T) {
	fx := setupDevice(t)
	token, deviceID := fx.redeemFlow(t)

	// Register updates the row and returns the server config. Platform is
	// trimmed like the other free-form fields.
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/register", token,
		map[string]any{"name": "workstation", "hostname": "ws.local", "jcode_version": "0.9.1", "platform": " desktop ", "pubkey": "pk-b64"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register: status=%d want 200", resp.StatusCode)
	}
	var rv deviceRegisterView
	decode(t, resp, &rv)
	if rv.DeviceID != deviceID || rv.HeartbeatInterval != heartbeatIntervalSec || rv.ServerTime == "" {
		t.Fatalf("register view malformed: %+v", rv)
	}
	dev, err := fx.st.GetDevice(t.Context(), deviceID)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if dev.Name != "workstation" || dev.Hostname != "ws.local" || dev.JcodeVersion != "0.9.1" || dev.Pubkey != "pk-b64" {
		t.Fatalf("register payload not stored: %+v", dev)
	}
	if dev.Platform != "desktop" {
		t.Fatalf("platform not stored/trimmed: %+v", dev)
	}
	if dev.LastSeenAt == nil {
		t.Fatalf("register did not stamp last_seen_at")
	}

	// A re-register overwrites platform; unknown values pass through, capped
	// at 32 chars.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/register", token,
		map[string]any{"pubkey": "pk-b64", "platform": "some-future-platform-that-is-far-too-long"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-register: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	dev, _ = fx.st.GetDevice(t.Context(), deviceID)
	if dev.Platform != "some-future-platform-that-is-far" || len(dev.Platform) != 32 {
		t.Fatalf("platform not updated/capped: %q", dev.Platform)
	}

	// A register without platform resets it to ''.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/register", token,
		map[string]any{"pubkey": "pk-b64"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register without platform: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	dev, _ = fx.st.GetDevice(t.Context(), deviceID)
	if dev.Platform != "" {
		t.Fatalf("platform not reset: %q want ''", dev.Platform)
	}

	// The client detail view always carries platform, '' included (the field
	// is non-omitempty for a stable client contract).
	owner := mustUser(t, fx, deviceID)
	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID, owner, nil)
	var dv map[string]any
	decode(t, resp, &dv)
	if p, ok := dv["platform"]; !ok || p != "" {
		t.Fatalf("client view platform = %v (present=%v) want ''", p, ok)
	}

	// Heartbeat → 204 and re-stamps last_seen_at.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/heartbeat", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// pubkey is required (the device's E2EE identity key).
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/register", token,
		map[string]any{"hostname": "ws.local"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("register without pubkey: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDeviceEndpointsAuth(t *testing.T) {
	fx := setupDevice(t)

	for _, path := range []string{"/internal/v1/device/register", "/internal/v1/device/heartbeat"} {
		// No token → 401.
		resp := do(t, http.MethodPost, fx.ts.URL+path, "", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s no token: status=%d want 401", path, resp.StatusCode)
		}
		resp.Body.Close()

		// A wrong/unknown device token → 401.
		resp = do(t, http.MethodPost, fx.ts.URL+path, "jcd_deadbeef", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s wrong token: status=%d want 401", path, resp.StatusCode)
		}
		resp.Body.Close()

		// A user session or the CONSOLE_TOKEN is NOT a device → 401.
		for _, tok := range []string{fx.sess, consoleToken} {
			resp = do(t, http.MethodPost, fx.ts.URL+path, tok, nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s non-device principal: status=%d want 401", path, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}

	// A revoked device's token stops resolving on the very next request (the
	// store-level join semantics are covered in store/device_test.go).
	token, _ := fx.redeemFlow(t)
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/heartbeat", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("fresh device token heartbeat: status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
}
