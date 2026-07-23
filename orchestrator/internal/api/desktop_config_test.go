package api

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
)

func testX25519PublicKey(t *testing.T) string {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
}

func testX25519Wrap(t *testing.T, marker byte) map[string]any {
	t.Helper()
	return map[string]any{
		"ephemeral_pubkey": testX25519PublicKey(t),
		"nonce":            base64.StdEncoding.EncodeToString(make([]byte, 12)),
		"ct":               base64.StdEncoding.EncodeToString([]byte{marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker}),
	}
}

func registerConfigDevice(t *testing.T, fx deviceFixture, token, name string) {
	t.Helper()
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/register", token, map[string]any{
		"name": name, "hostname": name, "jcode_version": "test",
		"platform": "desktop", "pubkey": testX25519PublicKey(t), "e2ee": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register config device: status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDesktopConfigASKApprovalAndProviderCAS(t *testing.T) {
	fx := setupDevice(t)
	firstToken, firstID := fx.redeemFlow(t)
	registerConfigDevice(t, fx, firstToken, "first")

	// A browser session is authenticated but is deliberately not allowed to
	// receive ASK wraps or encrypted provider records.
	resp := do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/config-key", fx.sess, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session config-key status=%d want 401", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/config-key", firstToken, nil)
	var initial accountSyncKeyView
	decode(t, resp, &initial)
	if initial.State != "uninitialized" {
		t.Fatalf("initial key state=%+v", initial)
	}

	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/config-key/initialize", firstToken, map[string]any{
		"key_gen": 1, "wrap": testX25519Wrap(t, 'a'),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("initialize status=%d want 201", resp.StatusCode)
	}
	resp.Body.Close()

	secondToken, secondID := fx.redeemFlow(t)
	registerConfigDevice(t, fx, secondToken, "second")
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/config-key", secondToken, nil)
	var secondState accountSyncKeyView
	decode(t, resp, &secondState)
	if secondState.State != "request_required" || secondState.KeyGen != 1 {
		t.Fatalf("second key state=%+v", secondState)
	}

	resp = do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/config-key/request", secondToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("request status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// The unapproved Desktop cannot read the encrypted provider vault.
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/provider-configs", secondToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unapproved vault status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/config-key/requests", firstToken, nil)
	var pending struct {
		Requests []accountSyncKeyRequestView `json:"requests"`
	}
	decode(t, resp, &pending)
	if len(pending.Requests) != 1 || pending.Requests[0].DeviceID != secondID || pending.Requests[0].Pubkey == "" {
		t.Fatalf("pending requests=%+v", pending.Requests)
	}

	resp = do(t, http.MethodPost,
		fx.ts.URL+"/internal/v1/device/config-key/requests/"+secondID+"/respond",
		firstToken, map[string]any{"approve": true, "key_gen": 1, "wrap": testX25519Wrap(t, 'b')})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/config-key", secondToken, nil)
	decode(t, resp, &secondState)
	if secondState.State != "ready" || len(secondState.Wrap) == 0 {
		t.Fatalf("approved second state=%+v", secondState)
	}

	resp = do(t, http.MethodPut, fx.ts.URL+"/internal/v1/device/provider-configs/local-openai", secondToken, map[string]any{
		"base_version": 0, "envelope": settingsEnvelope('p'), "deleted": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider create status=%d want 200", resp.StatusCode)
	}
	var created accountProviderConfigView
	decode(t, resp, &created)
	if created.ProviderID != "local-openai" || created.Version != 1 || created.Deleted {
		t.Fatalf("created provider=%+v", created)
	}

	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/provider-configs", firstToken, nil)
	var list struct {
		Providers []accountProviderConfigView `json:"providers"`
	}
	decode(t, resp, &list)
	if len(list.Providers) != 1 || string(list.Providers[0].Envelope) != string(created.Envelope) {
		t.Fatalf("provider list=%+v", list.Providers)
	}

	resp = do(t, http.MethodPut, fx.ts.URL+"/internal/v1/device/provider-configs/local-openai", firstToken, map[string]any{
		"base_version": 0, "envelope": settingsEnvelope('q'), "deleted": true,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale provider update status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodDelete,
		fx.ts.URL+"/internal/v1/device/config-key/devices/"+secondID,
		firstToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke config device status=%d want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Revocation cannot erase an API key already downloaded to the device, but
	// it immediately blocks every future provider-vault read and write.
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/provider-configs", secondToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked vault status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/config-key", secondToken, nil)
	secondState = accountSyncKeyView{}
	decode(t, resp, &secondState)
	if secondState.State != "denied" || len(secondState.Wrap) != 0 {
		t.Fatalf("revoked second state=%+v", secondState)
	}

	// The initializing Desktop is the approver and cannot approve itself.
	resp = do(t, http.MethodPost,
		fx.ts.URL+"/internal/v1/device/config-key/requests/"+firstID+"/respond",
		firstToken, map[string]any{"approve": true, "key_gen": 1, "wrap": testX25519Wrap(t, 'x')})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("self approval status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDesktopConfigRejectsMalformedX25519Wrap(t *testing.T) {
	fx := setupDevice(t)
	token, _ := fx.redeemFlow(t)
	registerConfigDevice(t, fx, token, "bad-wrap")
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/config-key/initialize", token, map[string]any{
		"key_gen": 1,
		"wrap": map[string]any{
			"ephemeral_pubkey": base64.StdEncoding.EncodeToString(make([]byte, 31)),
			"nonce":            base64.StdEncoding.EncodeToString(make([]byte, 12)),
			"ct":               base64.StdEncoding.EncodeToString(make([]byte, 16)),
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed X25519 wrap status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
