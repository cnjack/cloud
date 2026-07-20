package store

import (
	"context"
	"sort"
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

func (m *MemStore) ListDevicesForUser(_ context.Context, userID string) ([]domain.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Device
	for _, d := range m.devices {
		if d.UserID == userID && d.RevokedAt == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// --- device relay: sessions / events / commands (docs/17 §4) ------------------

func deviceSessionKey(deviceID, sessionID string) string { return deviceID + "|" + sessionID }

func (m *MemStore) UpsertDeviceSession(_ context.Context, ds *domain.DeviceSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deviceSessions[deviceSessionKey(ds.DeviceID, ds.SessionID)] = *ds
	return nil
}

func (m *MemStore) ListDeviceSessions(_ context.Context, deviceID string) ([]domain.DeviceSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.DeviceSession
	for _, ds := range m.deviceSessions {
		if ds.DeviceID == deviceID {
			out = append(out, ds)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (m *MemStore) AppendDeviceEvents(_ context.Context, deviceID, sessionID string, events []*domain.DeviceEvent) (*DeviceEventBatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := deviceSessionKey(deviceID, sessionID)
	res := &DeviceEventBatch{Accepted: []int64{}, Conflicted: []int64{}}
	for _, ev := range events {
		if m.deviceEventSeqLocked(key, ev.Seq) {
			res.Conflicted = append(res.Conflicted, ev.Seq)
			continue
		}
		m.deviceEvents[key] = append(m.deviceEvents[key], *ev)
		res.Accepted = append(res.Accepted, ev.Seq)
	}
	sort.Slice(m.deviceEvents[key], func(i, j int) bool { return m.deviceEvents[key][i].Seq < m.deviceEvents[key][j].Seq })
	res.MaxSeq = m.maxDeviceEventSeqLocked(key)
	return res, nil
}

// deviceEventSeqLocked reports whether seq is already present in the log.
// Caller holds m.mu.
func (m *MemStore) deviceEventSeqLocked(key string, seq int64) bool {
	for _, ev := range m.deviceEvents[key] {
		if ev.Seq == seq {
			return true
		}
	}
	return false
}

// maxDeviceEventSeqLocked returns the log's max seq, or 0 when empty.
// Caller holds m.mu.
func (m *MemStore) maxDeviceEventSeqLocked(key string) int64 {
	evs := m.deviceEvents[key]
	if len(evs) == 0 {
		return 0
	}
	return evs[len(evs)-1].Seq
}

func (m *MemStore) ListDeviceEvents(_ context.Context, deviceID, sessionID string, afterSeq int64, limit int) ([]domain.DeviceEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.DeviceEvent
	for _, ev := range m.deviceEvents[deviceSessionKey(deviceID, sessionID)] {
		if ev.Seq <= afterSeq {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *MemStore) MaxDeviceEventSeq(_ context.Context, deviceID, sessionID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxDeviceEventSeqLocked(deviceSessionKey(deviceID, sessionID)), nil
}

func (m *MemStore) CreateDeviceCommand(_ context.Context, c *domain.DeviceCommand) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deviceCommands[c.ID] = *c
	return nil
}

func (m *MemStore) DeliverPendingDeviceCommands(_ context.Context, deviceID string, limit int) ([]domain.DeviceCommand, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pending []domain.DeviceCommand
	for _, c := range m.deviceCommands {
		if c.DeviceID == deviceID && c.Status == domain.DeviceCommandPending {
			pending = append(pending, c)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt.Equal(pending[j].CreatedAt) {
			return pending[i].ID < pending[j].ID
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	if len(pending) > limit {
		pending = pending[:limit]
	}
	for i := range pending {
		pending[i].Status = domain.DeviceCommandDelivered
		m.deviceCommands[pending[i].ID] = pending[i]
	}
	return pending, nil
}

func (m *MemStore) AckDeviceCommand(_ context.Context, deviceID, commandID, status string, result []byte, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.deviceCommands[commandID]
	if !ok || c.DeviceID != deviceID {
		return ErrNotFound
	}
	if c.Status == domain.DeviceCommandAcked || c.Status == domain.DeviceCommandFailed {
		// Already resolved: a duplicate ack is an idempotent no-op.
		return nil
	}
	at = at.UTC()
	c.Status = status
	c.Result = result
	c.AckedAt = &at
	m.deviceCommands[commandID] = c
	return nil
}
