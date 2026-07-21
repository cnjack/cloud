package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// testDevicePairingOffers is the shared offer-lifecycle suite run against the
// memory store and (TestPGDeviceRelay/pairingOffers) real Postgres: create,
// read-back, single-use conditional claim, and the 404/409 distinction.
func testDevicePairingOffers(t *testing.T, st Store, deviceID, userID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	o := &domain.DevicePairingOffer{
		ID: domain.NewID(), DeviceID: deviceID, SecretHash: "hash-1",
		ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now,
	}
	if err := st.CreateDevicePairingOffer(ctx, o); err != nil {
		t.Fatalf("create offer: %v", err)
	}

	got, err := st.GetDevicePairingOffer(ctx, o.ID)
	if err != nil {
		t.Fatalf("get offer: %v", err)
	}
	if got.DeviceID != deviceID || got.SecretHash != "hash-1" || got.ClaimedAt != nil || got.ClaimedBy != nil {
		t.Fatalf("get: %+v want unclaimed offer", got)
	}
	if _, err := st.GetDevicePairingOffer(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: err=%v want ErrNotFound", err)
	}

	// First claim wins and stamps claimed_by/claimed_at.
	if err := st.ClaimDevicePairingOffer(ctx, o.ID, userID, now); err != nil {
		t.Fatalf("claim: %v", err)
	}
	got, err = st.GetDevicePairingOffer(ctx, o.ID)
	if err != nil {
		t.Fatalf("get claimed: %v", err)
	}
	if got.ClaimedAt == nil || got.ClaimedBy == nil || *got.ClaimedBy != userID {
		t.Fatalf("claimed offer = %+v want claimed_by=%s", got, userID)
	}

	// Second claim → ErrAlreadyExists (single-use), even by another user.
	if err := st.ClaimDevicePairingOffer(ctx, o.ID, "other-user", now); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("re-claim: err=%v want ErrAlreadyExists", err)
	}
	// Unknown offer → ErrNotFound (indistinguishable from foreign).
	if err := st.ClaimDevicePairingOffer(ctx, "missing", userID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim missing: err=%v want ErrNotFound", err)
	}
}

func TestDevicePairingOffers(t *testing.T) {
	m := NewMemStore()
	d := mkDevice(t, m, "user-1")
	testDevicePairingOffers(t, m, d.ID, "user-1")
}
