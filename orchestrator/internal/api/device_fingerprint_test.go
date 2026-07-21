package api

import (
	"net/http"
	"strings"
	"testing"
)

// M16 — device fingerprint idempotency + device deletion (docs/17 §3/§4.3).

const testFingerprintA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testFingerprintB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// redeemFlowWithFingerprint drives a full login whose token poll carries the
// given fingerprint hash, returning the token view.
func (fx deviceFixture) redeemFlowWithFingerprint(t *testing.T, fingerprint string) deviceTokenView {
	t.Helper()
	flow := fx.startFlow(t)
	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": flow.UserCode, "approve": true})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize: status=%d want 200", resp.StatusCode)
	}
	poll := do(t, http.MethodPost, fx.ts.URL+"/auth/device/token", "",
		map[string]any{"device_code": flow.DeviceCode, "fingerprint": fingerprint})
	if poll.StatusCode != http.StatusOK {
		var env errorBody
		decode(t, poll, &env)
		t.Fatalf("redeem poll: status=%d code=%q", poll.StatusCode, env.Error.Code)
	}
	var v deviceTokenView
	decode(t, poll, &v)
	return v
}

// deviceOwnerID resolves the user owning deviceID (the fixture's seeded user).
func (fx deviceFixture) deviceOwnerID(t *testing.T, deviceID string) string {
	t.Helper()
	d, err := fx.st.GetDevice(t.Context(), deviceID)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	return d.UserID
}

func (fx deviceFixture) deviceCount(t *testing.T, userID string) int {
	t.Helper()
	devices, err := fx.st.ListDevicesForUser(t.Context(), userID)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	return len(devices)
}

func TestDeviceLoginFingerprintDedup(t *testing.T) {
	fx := setupDevice(t)

	// First login with fingerprint A: a fresh row, deduped=false.
	first := fx.redeemFlowWithFingerprint(t, testFingerprintA)
	if first.Deduped {
		t.Fatal("first login deduped=true, want false")
	}
	userID := fx.deviceOwnerID(t, first.DeviceID)
	if n := fx.deviceCount(t, userID); n != 1 {
		t.Fatalf("devices after first login = %d, want 1", n)
	}

	// Second login, SAME machine fingerprint: the existing row is reused — no
	// new devices row, same device_id, deduped=true, and a fresh WORKING token.
	second := fx.redeemFlowWithFingerprint(t, testFingerprintA)
	if !second.Deduped {
		t.Fatal("second login deduped=false, want true")
	}
	if second.DeviceID != first.DeviceID {
		t.Fatalf("deduped login device_id = %q, want reused %q", second.DeviceID, first.DeviceID)
	}
	if n := fx.deviceCount(t, userID); n != 1 {
		t.Fatalf("devices after deduped login = %d, want 1 (idempotent re-login)", n)
	}
	// The deduped login refreshed the reused row (name + last_seen).
	d, err := fx.st.GetDevice(t.Context(), first.DeviceID)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.FingerprintHash != testFingerprintA {
		t.Fatalf("fingerprint_hash = %q, want %q", d.FingerprintHash, testFingerprintA)
	}
	if d.LastSeenAt == nil {
		t.Fatal("deduped login did not stamp last_seen_at")
	}
	// Both tokens work (the re-login did not revoke the old one).
	for i, tok := range []string{first.AccessToken, second.AccessToken} {
		resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/heartbeat", tok, nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("token %d after dedup: heartbeat status=%d want 204", i, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// A DIFFERENT fingerprint mints a second device row.
	third := fx.redeemFlowWithFingerprint(t, testFingerprintB)
	if third.Deduped {
		t.Fatal("different-fingerprint login deduped=true, want false")
	}
	if third.DeviceID == first.DeviceID {
		t.Fatal("different fingerprint reused the device row")
	}
	if n := fx.deviceCount(t, userID); n != 2 {
		t.Fatalf("devices after different-fingerprint login = %d, want 2", n)
	}
}

func TestDeviceLoginFingerprintValidation(t *testing.T) {
	fx := setupDevice(t)
	flow := fx.startFlow(t)
	resp := do(t, http.MethodPost, fx.ts.URL+"/auth/device/authorize", fx.sess,
		map[string]any{"user_code": flow.UserCode, "approve": true})
	resp.Body.Close()

	for _, fp := range []string{"not-hex", strings.Repeat("ab", 31), strings.Repeat("g", 64)} {
		poll := do(t, http.MethodPost, fx.ts.URL+"/auth/device/token", "",
			map[string]any{"device_code": flow.DeviceCode, "fingerprint": fp})
		if poll.StatusCode != http.StatusBadRequest {
			t.Fatalf("fingerprint %q: status=%d want 400", fp, poll.StatusCode)
		}
		poll.Body.Close()
	}
	// Case/whitespace slop is tolerated (canonicalised to lowercase).
	poll := do(t, http.MethodPost, fx.ts.URL+"/auth/device/token", "",
		map[string]any{"device_code": flow.DeviceCode, "fingerprint": " " + strings.ToUpper(testFingerprintA) + " "})
	if poll.StatusCode != http.StatusOK {
		t.Fatalf("uppercase fingerprint: status=%d want 200", poll.StatusCode)
	}
	var v deviceTokenView
	decode(t, poll, &v)
	d, err := fx.st.GetDevice(t.Context(), v.DeviceID)
	if err != nil || d.FingerprintHash != testFingerprintA {
		t.Fatalf("fingerprint not canonicalised: %+v err=%v", d, err)
	}
}

// TestDeviceRegisterFingerprintBackfill: a device minted WITHOUT a fingerprint
// (pre-M16 issuance path) picks it up from the register uplink, so the NEXT
// login dedups onto it (注册幂等).
func TestDeviceRegisterFingerprintBackfill(t *testing.T) {
	fx := setupDevice(t)
	token, deviceID := fx.redeemFlow(t) // no fingerprint on the token poll
	userID := fx.deviceOwnerID(t, deviceID)

	d, err := fx.st.GetDevice(t.Context(), deviceID)
	if err != nil || d.FingerprintHash != "" {
		t.Fatalf("fresh device fingerprint = %q, want empty", d.FingerprintHash)
	}

	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/register", token,
		map[string]any{"pubkey": "pk", "fingerprint": testFingerprintA})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register with fingerprint: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	d, _ = fx.st.GetDevice(t.Context(), deviceID)
	if d.FingerprintHash != testFingerprintA {
		t.Fatalf("fingerprint not backfilled: %+v", d)
	}

	// The next login with that fingerprint dedups onto the backfilled row.
	v := fx.redeemFlowWithFingerprint(t, testFingerprintA)
	if !v.Deduped || v.DeviceID != deviceID {
		t.Fatalf("login after backfill: deduped=%v device=%q, want true/%q", v.Deduped, v.DeviceID, deviceID)
	}
	if n := fx.deviceCount(t, userID); n != 1 {
		t.Fatalf("devices after backfill dedup = %d, want 1", n)
	}
}

// TestDeviceRegisterFingerprintNoHijack: a register uplink reporting a
// fingerprint ALREADY held by another live device of the same user leaves the
// row fingerprint-free (no unique-index violation, no theft of the dedup key).
func TestDeviceRegisterFingerprintNoHijack(t *testing.T) {
	fx := setupDevice(t)
	vA := fx.redeemFlowWithFingerprint(t, testFingerprintA)
	tokenB, deviceIDB := fx.redeemFlow(t) // fingerprint-free second device

	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/register", tokenB,
		map[string]any{"pubkey": "pk", "fingerprint": testFingerprintA})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register with taken fingerprint: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
	d, err := fx.st.GetDevice(t.Context(), deviceIDB)
	if err != nil || d.FingerprintHash != "" {
		t.Fatalf("second device fingerprint = %q, want empty (hash already taken)", d.FingerprintHash)
	}
	a, err := fx.st.GetDevice(t.Context(), vA.DeviceID)
	if err != nil || a.FingerprintHash != testFingerprintA {
		t.Fatalf("first device fingerprint clobbered: %+v err=%v", a, err)
	}
}

// TestDeleteDevice covers the M16 DELETE /api/v1/devices/{id} contract:
// soft-delete + token revocation + permission semantics.
func TestDeleteDevice(t *testing.T) {
	fx := setupDevice(t)
	token, deviceID := fx.redeemFlow(t)
	userID := fx.deviceOwnerID(t, deviceID)

	// Sanity: the token works before deletion.
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/heartbeat", token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("pre-delete heartbeat: status=%d want 204", resp.StatusCode)
	}

	// Unknown device → 404; another user's device → 403.
	resp = do(t, http.MethodDelete, fx.ts.URL+"/api/v1/devices/nope", fx.sess, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown: status=%d want 404", resp.StatusCode)
	}
	other := mkUser(t, fx.st, "other-user")
	otherSess := mkSession(t, fx.st, other.ID)
	resp = do(t, http.MethodDelete, fx.ts.URL+"/api/v1/devices/"+deviceID, otherSess, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete someone else's device: status=%d want 403", resp.StatusCode)
	}
	// The service principal owns no devices → 403.
	resp = do(t, http.MethodDelete, fx.ts.URL+"/api/v1/devices/"+deviceID, consoleToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete as service principal: status=%d want 403", resp.StatusCode)
	}

	// The owner deletes: 204.
	resp = do(t, http.MethodDelete, fx.ts.URL+"/api/v1/devices/"+deviceID, fx.sess, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete own device: status=%d want 204", resp.StatusCode)
	}

	// The device token dies on the very next request.
	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/heartbeat", token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-delete heartbeat: status=%d want 401", resp.StatusCode)
	}
	// The client surface reads the device as gone: GET 404, list empty,
	// repeated DELETE 404.
	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/devices/"+deviceID, fx.sess, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted device: status=%d want 404", resp.StatusCode)
	}
	if n := fx.deviceCount(t, userID); n != 0 {
		t.Fatalf("deleted device still listed: %d, want 0", n)
	}
	resp = do(t, http.MethodDelete, fx.ts.URL+"/api/v1/devices/"+deviceID, fx.sess, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("re-delete: status=%d want 404", resp.StatusCode)
	}
	// History is retained (soft delete): the row still exists with revoked_at.
	d, err := fx.st.GetDevice(t.Context(), deviceID)
	if err != nil || d.RevokedAt == nil {
		t.Fatalf("deleted device row = %+v err=%v, want revoked_at set", d, err)
	}
}

// TestDeleteDeviceFreesFingerprint: after a delete, a re-login from the same
// machine mints a FRESH row (the revoked one no longer claims the fingerprint).
func TestDeleteDeviceFreesFingerprint(t *testing.T) {
	fx := setupDevice(t)
	first := fx.redeemFlowWithFingerprint(t, testFingerprintA)
	userID := fx.deviceOwnerID(t, first.DeviceID)

	resp := do(t, http.MethodDelete, fx.ts.URL+"/api/v1/devices/"+first.DeviceID, fx.sess, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status=%d want 204", resp.StatusCode)
	}

	second := fx.redeemFlowWithFingerprint(t, testFingerprintA)
	if second.Deduped {
		t.Fatal("login after delete deduped=true, want a fresh row")
	}
	if second.DeviceID == first.DeviceID {
		t.Fatal("login after delete reused the revoked row")
	}
	if n := fx.deviceCount(t, userID); n != 1 {
		t.Fatalf("devices after re-login = %d, want 1", n)
	}
}
