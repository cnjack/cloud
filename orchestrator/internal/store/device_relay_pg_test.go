package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// TestPGDeviceRelay runs the shared device-relay suite against a real Postgres,
// covering the pgx paths the memory store cannot: the bytea meta/envelope
// round-trips, ON CONFLICT idempotency, the single-statement deliver offer,
// and the conditional ack. Requires JCLOUD_PG_DSN.
func TestPGDeviceRelay(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed device relay test")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// A user to own the device (devices.user_id FK); cleaned up afterwards
	// (devices cascade with the user).
	u := &domain.User{ID: domain.NewID(), DisplayName: "Relay User", CreatedAt: time.Now().UTC()}
	id := &domain.UserIdentity{
		ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: "relay-uid-" + u.ID,
		Username: "relay-" + u.ID, AccessTokenEnc: []byte("enc"), CreatedAt: time.Now().UTC(),
	}
	if _, err := st.CreateUserWithIdentity(ctx, u, id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u.ID)
	})

	// A second user for the cross-user fingerprint-isolation check.
	u2 := &domain.User{ID: domain.NewID(), DisplayName: "Relay User 2", CreatedAt: time.Now().UTC()}
	id2 := &domain.UserIdentity{
		ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: "relay-uid-" + u2.ID,
		Username: "relay-" + u2.ID, AccessTokenEnc: []byte("enc"), CreatedAt: time.Now().UTC(),
	}
	if _, err := st.CreateUserWithIdentity(ctx, u2, id2); err != nil {
		t.Fatalf("create user 2: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u2.ID)
	})

	d := &domain.Device{
		ID: domain.NewID(), UserID: u.ID, Name: "relay-box", KeyGen: 1,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateDevice(ctx, d); err != nil {
		t.Fatalf("create device: %v", err)
	}

	t.Run("capabilities", func(t *testing.T) { testDeviceCapabilities(t, st, d.ID) })
	t.Run("sessions", func(t *testing.T) { testDeviceRelaySessions(t, st, d.ID) })
	t.Run("events", func(t *testing.T) { testDeviceRelayEvents(t, st, d.ID) })
	t.Run("eventsBeforeUpsert", func(t *testing.T) { testDeviceRelayEventsBeforeSessionUpsert(t, st, d.ID) })
	t.Run("commands", func(t *testing.T) { testDeviceRelayCommands(t, st, d) })
	t.Run("listForUser", func(t *testing.T) { testDeviceRelayListForUser(t, st, d.ID, u.ID) })
	t.Run("fingerprintAndRevoke", func(t *testing.T) { testDeviceFingerprintAndRevoke(t, st, u.ID, u2.ID) })
	t.Run("pairingOffers", func(t *testing.T) { testDevicePairingOffers(t, st, d.ID, u.ID) })
}
