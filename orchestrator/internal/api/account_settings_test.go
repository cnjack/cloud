package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/store"
)

func settingsEnvelope(marker byte) map[string]any {
	return map[string]any{
		"enc": "aes-256-gcm", "key_gen": 1,
		"nonce": base64.StdEncoding.EncodeToString(make([]byte, 12)),
		"ct":    base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(marker), 16))),
	}
}

func TestAccountSettingsSessionAndDeviceShareEncryptedCASDocument(t *testing.T) {
	fx := setupDevice(t)
	deviceToken, _, owner := onlineDevice(t, fx)

	resp := do(t, http.MethodPut, fx.ts.URL+"/api/v1/account/settings", owner, map[string]any{
		"base_version": 0,
		"envelope":     settingsEnvelope('a'),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session put: status=%d", resp.StatusCode)
	}
	var created accountSettingsView
	decode(t, resp, &created)
	if created.Version != 1 || len(created.Envelope) == 0 {
		t.Fatalf("created settings = %+v", created)
	}

	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/account-settings", deviceToken, nil)
	var fromDevice accountSettingsView
	decode(t, resp, &fromDevice)
	if fromDevice.Version != 1 || string(fromDevice.Envelope) != string(created.Envelope) {
		t.Fatalf("device view = %+v, want version 1 same envelope", fromDevice)
	}

	resp = do(t, http.MethodPut, fx.ts.URL+"/internal/v1/device/account-settings", deviceToken, map[string]any{
		"base_version": 0,
		"envelope":     settingsEnvelope('b'),
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale put: status=%d want 409", resp.StatusCode)
	}
	var conflict struct {
		CurrentVersion int64 `json:"current_version"`
	}
	decode(t, resp, &conflict)
	if conflict.CurrentVersion != 1 {
		t.Fatalf("current_version=%d want 1", conflict.CurrentVersion)
	}
}

func TestAccountSettingsAreAccountIsolatedAndEnvelopeOnly(t *testing.T) {
	fx := setupDevice(t)
	_, _, owner := onlineDevice(t, fx)
	other := mkSession(t, fx.st, mkUser(t, fx.st, "settings-other").ID)

	resp := do(t, http.MethodPut, fx.ts.URL+"/api/v1/account/settings", owner, map[string]any{
		"base_version": 0,
		"envelope":     settingsEnvelope('z'),
	})
	var created accountSettingsView
	decode(t, resp, &created)
	if strings.Contains(string(created.Envelope), "zh-Hans") || strings.Contains(string(created.Envelope), "gpt") {
		t.Fatalf("plaintext leaked in envelope: %s", created.Envelope)
	}

	resp = do(t, http.MethodGet, fx.ts.URL+"/api/v1/account/settings", other, nil)
	var absent accountSettingsView
	decode(t, resp, &absent)
	if absent.Version != 0 || string(absent.Envelope) != "null" {
		t.Fatalf("other account saw settings: %+v", absent)
	}
}

func TestAccountSettingsRejectMalformedOrOversizedEnvelope(t *testing.T) {
	fx := setupDevice(t)
	_, _, owner := onlineDevice(t, fx)

	bad := settingsEnvelope('x')
	bad["plaintext_language"] = "zh-Hans"
	resp := do(t, http.MethodPut, fx.ts.URL+"/api/v1/account/settings", owner, map[string]any{
		"base_version": 0, "envelope": bad,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown envelope field: status=%d want 400", resp.StatusCode)
	}

	oversized, _ := json.Marshal(map[string]any{
		"base_version": 0,
		"envelope": map[string]any{
			"enc": "aes-256-gcm", "key_gen": 1,
			"nonce": base64.StdEncoding.EncodeToString(make([]byte, 12)),
			"ct":    strings.Repeat("A", maxAccountSettingsEnvelope),
		},
	})
	req, _ := http.NewRequest(http.MethodPut, fx.ts.URL+"/api/v1/account/settings", strings.NewReader(string(oversized)))
	req.Header.Set("Authorization", "Bearer "+owner)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized envelope: status=%d want 400", resp.StatusCode)
	}
}

func TestAccountSettingsMemStoreCopiesEnvelope(t *testing.T) {
	st := store.NewMemStore()
	saved, err := st.PutAccountSettings(context.Background(), "u", 0, []byte(`{"ct":"secret"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	saved.Envelope[0] = 'X'
	loaded, err := st.GetAccountSettings(context.Background(), "u")
	if err != nil || loaded.Envelope[0] != '{' {
		t.Fatalf("store did not copy envelope: loaded=%q err=%v", loaded.Envelope, err)
	}
}
