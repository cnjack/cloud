package store

import (
	"context"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// --- devices (docs/17 — jcode device relay) -----------------------------------

func (m *MemStore) CreateDevice(_ context.Context, d *domain.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[d.ID] = *d
	return nil
}

func (m *MemStore) GetDevice(_ context.Context, id string) (*domain.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := d
	return &cp, nil
}

func (m *MemStore) UpsertDeviceRegistration(_ context.Context, d *domain.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.devices[d.ID]
	if !ok {
		return ErrNotFound
	}
	// Only the registration payload is writable: user_id/key_gen/created_at
	// survive from the row created at token issuance.
	existing.Name = d.Name
	existing.Hostname = d.Hostname
	existing.JcodeVersion = d.JcodeVersion
	existing.Pubkey = d.Pubkey
	existing.LastSeenAt = d.LastSeenAt
	m.devices[d.ID] = existing
	return nil
}

func (m *MemStore) TouchDeviceLastSeen(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return ErrNotFound
	}
	at = at.UTC()
	d.LastSeenAt = &at
	m.devices[id] = d
	return nil
}

func (m *MemStore) CreateDeviceToken(_ context.Context, t *domain.DeviceToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deviceTokens[t.ID] = *t
	return nil
}

func (m *MemStore) GetDeviceTokenByHash(_ context.Context, tokenHash string) (*domain.DeviceToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	for _, tok := range m.deviceTokens {
		if tok.TokenHash != tokenHash || tok.RevokedAt != nil {
			continue
		}
		// Mirror the PG JOIN: a revoked device's tokens never resolve, and the
		// owning user's id rides along on the row.
		d, ok := m.devices[tok.DeviceID]
		if !ok || d.RevokedAt != nil {
			continue
		}
		cp := tok
		cp.UserID = d.UserID
		return &cp, nil
	}
	return nil, ErrNotFound
}
