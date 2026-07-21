package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// --- pairing offers: device mint + client claim (docs/17 §6.3 — M11) ----------

// mintOffer posts /internal/v1/device/pairing-offers as the device and returns
// the decoded offer view.
func mintOffer(t *testing.T, fx deviceFixture, deviceToken string) (offerID, secret string) {
	t.Helper()
	resp := do(t, http.MethodPost, fx.ts.URL+"/internal/v1/device/pairing-offers", deviceToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mint offer: status=%d want 201", resp.StatusCode)
	}
	var v struct {
		OfferID   string    `json:"offer_id"`
		Secret    string    `json:"secret"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	decode(t, resp, &v)
	if v.OfferID == "" || v.Secret == "" || v.ExpiresAt.IsZero() {
		t.Fatalf("offer view incomplete: %+v", v)
	}
	return v.OfferID, v.Secret
}

func claimOffer(t *testing.T, fx deviceFixture, offerID, session, secret string) *http.Response {
	t.Helper()
	return do(t, http.MethodPost, fx.ts.URL+"/api/v1/pairing-offers/"+offerID+"/claim", session,
		map[string]any{"secret": secret, "label": "jcode-mobile", "kty": "P-256", "pubkey": "b64-spki"})
}

func TestPairingOfferLifecycle(t *testing.T) {
	fx := setupDevice(t)
	deviceToken, deviceID, owner := onlineDevice(t, fx)

	// A user session (not a device token) is required to claim.
	resp := claimOffer(t, fx, "any", deviceToken, "x")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("claim with device token: status=%d want 400", resp.StatusCode)
	}
	resp.Body.Close()

	offerID, secret := mintOffer(t, fx, deviceToken)

	// Wrong secret → 403, offer stays claimable.
	resp = claimOffer(t, fx, offerID, owner, "wrong-secret")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("claim wrong secret: status=%d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Happy path: claim creates the pending pairing + queues pairing.request
	// carrying the offer_id; the response names both ids.
	resp = claimOffer(t, fx, offerID, owner, secret)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claim: status=%d want 201", resp.StatusCode)
	}
	var cv struct {
		PairingID string `json:"pairing_id"`
		DeviceID  string `json:"device_id"`
	}
	decode(t, resp, &cv)
	if cv.PairingID == "" || cv.DeviceID != deviceID {
		t.Fatalf("claim view = %+v want pairing_id + device_id", cv)
	}

	resp = do(t, http.MethodGet, fx.ts.URL+"/internal/v1/device/poll?wait=1s", deviceToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll: status=%d want 200", resp.StatusCode)
	}
	var pv struct {
		Commands []deviceCommandView `json:"commands"`
	}
	decode(t, resp, &pv)
	if len(pv.Commands) != 1 || pv.Commands[0].Kind != domain.DeviceCmdPairingRequest {
		t.Fatalf("poll commands = %+v want one pairing.request", pv.Commands)
	}
	var payload map[string]any
	if err := json.Unmarshal(pv.Commands[0].Payload, &payload); err != nil {
		t.Fatalf("pairing.request payload: %v", err)
	}
	if payload["pairing_id"] != cv.PairingID || payload["offer_id"] != offerID ||
		payload["label"] != "jcode-mobile" || payload["kty"] != "P-256" || payload["pubkey"] != "b64-spki" {
		t.Fatalf("pairing.request payload = %v", payload)
	}

	// The client polls the new pairing as usual — pending.
	if v := pairingStatus(t, fx, deviceID, owner, cv.PairingID); v.Status != domain.DevicePairingPending {
		t.Fatalf("pairing status=%q want pending", v.Status)
	}

	// Second claim of the same offer → 409 (single-use), even with the right
	// secret.
	resp = claimOffer(t, fx, offerID, owner, secret)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-claim: status=%d want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown offer → 404.
	resp = claimOffer(t, fx, "no-such-offer", owner, "x")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("claim unknown offer: status=%d want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPairingOfferExpired(t *testing.T) {
	fx := setupDevice(t)
	fx.srv.devicePairingOfferWindow = time.Millisecond // mint already-stale offers
	deviceToken, _, owner := onlineDevice(t, fx)

	offerID, secret := mintOffer(t, fx, deviceToken)
	time.Sleep(5 * time.Millisecond)

	resp := claimOffer(t, fx, offerID, owner, secret)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("claim expired offer: status=%d want 410", resp.StatusCode)
	}
	var env errorBody
	decode(t, resp, &env)
	if env.Error.Code != "offer_expired" {
		t.Fatalf("expired claim error = %+v want offer_expired", env)
	}

	// The expiry check precedes the secret check (a 410 never confirms a secret).
	resp = claimOffer(t, fx, offerID, owner, "wrong-secret")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("claim expired offer, wrong secret: status=%d want 410", resp.StatusCode)
	}
	resp.Body.Close()
}
