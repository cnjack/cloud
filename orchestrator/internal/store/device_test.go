package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
)

// mkDevice inserts a device row the way the device-login token endpoint does:
// name only — hostname/version/pubkey arrive later via register.
func mkDevice(t *testing.T, m *MemStore, userID string) *domain.Device {
	t.Helper()
	d := &domain.Device{
		ID:        domain.NewID(),
		UserID:    userID,
		Name:      "jack-macbook",
		KeyGen:    1,
		CreatedAt: time.Now().UTC(),
	}
	if err := m.CreateDevice(context.Background(), d); err != nil {
		t.Fatalf("create device: %v", err)
	}
	return d
}

func TestDeviceRegistrationLifecycle(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()
	d := mkDevice(t, m, "user-1")

	got, err := m.GetDevice(ctx, d.ID)
	if err != nil || got.Name != "jack-macbook" || got.UserID != "user-1" || got.Pubkey != "" || got.Platform != "" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if _, err := m.GetDevice(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: err=%v want ErrNotFound", err)
	}

	// Register: fills hostname/version/pubkey and stamps last_seen_at, and must
	// NOT touch user_id/key_gen (the store ignores them on the upsert).
	now := time.Now().UTC()
	reg := &domain.Device{
		ID:           d.ID,
		UserID:       "someone-else", // must be ignored
		Name:         "renamed",
		Hostname:     "macbook.local",
		JcodeVersion: "0.9.1",
		Platform:     "cli",
		Pubkey:       "pubkey-b64",
		KeyGen:       99, // must be ignored
		LastSeenAt:   &now,
	}
	if err := m.UpsertDeviceRegistration(ctx, reg); err != nil {
		t.Fatalf("upsert registration: %v", err)
	}
	got, err = m.GetDevice(ctx, d.ID)
	if err != nil {
		t.Fatalf("get after register: %v", err)
	}
	if got.UserID != "user-1" || got.KeyGen != 1 {
		t.Fatalf("upsert clobbered owner/key_gen: %+v", got)
	}
	if got.Hostname != "macbook.local" || got.JcodeVersion != "0.9.1" || got.Platform != "cli" || got.Pubkey != "pubkey-b64" {
		t.Fatalf("registration payload not stored: %+v", got)
	}
	if got.LastSeenAt == nil || !got.LastSeenAt.Equal(now) {
		t.Fatalf("last_seen_at not stamped: %+v", got.LastSeenAt)
	}

	// A re-register overwrites the payload, platform included.
	reg.Platform = "desktop"
	if err := m.UpsertDeviceRegistration(ctx, reg); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	got, _ = m.GetDevice(ctx, d.ID)
	if got.Platform != "desktop" {
		t.Fatalf("re-register did not update platform: %+v", got)
	}

	// Heartbeat re-stamps last_seen_at.
	later := now.Add(30 * time.Second)
	if err := m.TouchDeviceLastSeen(ctx, d.ID, later); err != nil {
		t.Fatalf("touch last seen: %v", err)
	}
	got, _ = m.GetDevice(ctx, d.ID)
	if got.LastSeenAt == nil || !got.LastSeenAt.Equal(later) {
		t.Fatalf("heartbeat not stamped: %+v", got.LastSeenAt)
	}

	// Upsert/heartbeat on a missing device is ErrNotFound (the row is created
	// at token issuance, so absent means deleted under a live token).
	if err := m.UpsertDeviceRegistration(ctx, &domain.Device{ID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upsert missing: err=%v want ErrNotFound", err)
	}
	if err := m.TouchDeviceLastSeen(ctx, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("touch missing: err=%v want ErrNotFound", err)
	}
}

func TestDeviceTokenByHash(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()
	d := mkDevice(t, m, "user-1")

	plaintext, err := auth.GenerateDeviceToken()
	if err != nil {
		t.Fatalf("generate device token: %v", err)
	}
	tok := &domain.DeviceToken{
		ID:        domain.NewID(),
		DeviceID:  d.ID,
		TokenHash: auth.HashToken(plaintext),
		CreatedAt: time.Now().UTC(),
	}
	if err := m.CreateDeviceToken(ctx, tok); err != nil {
		t.Fatalf("create device token: %v", err)
	}

	// The principal-resolution path: hash resolves, with the owner joined in.
	got, err := m.GetDeviceTokenByHash(ctx, auth.HashToken(plaintext))
	if err != nil || got.ID != tok.ID || got.DeviceID != d.ID || got.UserID != "user-1" {
		t.Fatalf("get by hash: %+v err=%v", got, err)
	}

	// The plaintext itself never resolves, and neither does an arbitrary hash.
	if _, err := m.GetDeviceTokenByHash(ctx, plaintext); !errors.Is(err, ErrNotFound) {
		t.Fatalf("plaintext lookup: err=%v want ErrNotFound", err)
	}
	if _, err := m.GetDeviceTokenByHash(ctx, auth.HashToken("jcd_wrong")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong hash: err=%v want ErrNotFound", err)
	}

	// A revoked token never resolves.
	rev := time.Now().UTC()
	revoked := &domain.DeviceToken{
		ID:        domain.NewID(),
		DeviceID:  d.ID,
		TokenHash: auth.HashToken("jcd_revoked"),
		CreatedAt: time.Now().UTC(),
		RevokedAt: &rev,
	}
	if err := m.CreateDeviceToken(ctx, revoked); err != nil {
		t.Fatalf("create revoked token: %v", err)
	}
	if _, err := m.GetDeviceTokenByHash(ctx, revoked.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token resolved: err=%v want ErrNotFound", err)
	}

	// A revoked device's tokens never resolve either.
	d2 := mkDevice(t, m, "user-1")
	tok2 := &domain.DeviceToken{
		ID:        domain.NewID(),
		DeviceID:  d2.ID,
		TokenHash: auth.HashToken("jcd_of-revoked-device"),
		CreatedAt: time.Now().UTC(),
	}
	if err := m.CreateDeviceToken(ctx, tok2); err != nil {
		t.Fatalf("create token for revoked device: %v", err)
	}
	m.mu.Lock()
	rd := m.devices[d2.ID]
	rd.RevokedAt = &rev
	m.devices[d2.ID] = rd
	m.mu.Unlock()
	if _, err := m.GetDeviceTokenByHash(ctx, tok2.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked device's token resolved: err=%v want ErrNotFound", err)
	}
}
