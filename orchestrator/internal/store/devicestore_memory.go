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
	// Mirror the PG partial unique index (0036): at most one non-revoked
	// device per (user, fingerprint) — the login dedup invariant.
	if d.FingerprintHash != "" {
		if _, err := m.findDeviceByFingerprintLocked(d.UserID, d.FingerprintHash); err == nil {
			return ErrAlreadyExists
		}
	}
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
	existing.Platform = d.Platform
	existing.Pubkey = d.Pubkey
	existing.E2EE = d.E2EE
	existing.LastSeenAt = d.LastSeenAt
	existing.FingerprintHash = d.FingerprintHash
	m.devices[d.ID] = existing
	return nil
}

func (m *MemStore) UpdateDeviceCapabilities(_ context.Context, id string, capabilities []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return ErrNotFound
	}
	d.Capabilities = capabilities
	m.devices[id] = d
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

// findDeviceByFingerprintLocked returns the user's non-revoked device with the
// given fingerprint hash (ErrNotFound when none). Caller holds m.mu.
func (m *MemStore) findDeviceByFingerprintLocked(userID, fingerprintHash string) (*domain.Device, error) {
	var best *domain.Device
	for _, d := range m.devices {
		if d.UserID != userID || d.FingerprintHash != fingerprintHash || d.RevokedAt != nil {
			continue
		}
		cp := d
		if best == nil || cp.CreatedAt.Before(best.CreatedAt) ||
			(cp.CreatedAt.Equal(best.CreatedAt) && cp.ID < best.ID) {
			best = &cp
		}
	}
	if best == nil {
		return nil, ErrNotFound
	}
	return best, nil
}

func (m *MemStore) FindDeviceByFingerprint(_ context.Context, userID, fingerprintHash string) (*domain.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.findDeviceByFingerprintLocked(userID, fingerprintHash)
}

func (m *MemStore) RevokeDevice(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok || d.RevokedAt != nil {
		return ErrNotFound
	}
	at = at.UTC()
	d.RevokedAt = &at
	m.devices[id] = d
	return nil
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
		left, right := out[i].LastActivityAt, out[j].LastActivityAt
		switch {
		case left == nil && right == nil:
			return out[i].SessionID < out[j].SessionID
		case left == nil:
			return false
		case right == nil:
			return true
		case left.Equal(*right):
			return out[i].SessionID < out[j].SessionID
		default:
			return left.After(*right)
		}
	})
	return out, nil
}

func (m *MemStore) DeleteDeviceSession(_ context.Context, deviceID, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := deviceSessionKey(deviceID, sessionID)
	delete(m.deviceSessions, key)
	delete(m.deviceEvents, key)
	return nil
}

func (m *MemStore) DeleteDeviceSessionsExcept(_ context.Context, deviceID string, keepSessionIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep := make(map[string]struct{}, len(keepSessionIDs))
	for _, sessionID := range keepSessionIDs {
		keep[sessionID] = struct{}{}
	}
	for key, ds := range m.deviceSessions {
		if ds.DeviceID != deviceID {
			continue
		}
		if _, ok := keep[ds.SessionID]; ok {
			continue
		}
		delete(m.deviceSessions, key)
		delete(m.deviceEvents, key)
	}
	return nil
}

func (m *MemStore) AppendDeviceEvents(_ context.Context, deviceID, sessionID string, events []*domain.DeviceEvent) (*DeviceEventBatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := deviceSessionKey(deviceID, sessionID)
	// Mirror the PG store: an event batch auto-creates a minimal session row
	// (the connector uploads events ~10x more often than it mirrors sessions),
	// so ListDeviceSessions sees the session right away. A later
	// UpsertDeviceSession overwrites meta/status wholesale.
	if _, ok := m.deviceSessions[key]; !ok {
		m.deviceSessions[key] = domain.DeviceSession{
			DeviceID: deviceID, SessionID: sessionID,
			Status: domain.DeviceSessionRunning, UpdatedAt: time.Now().UTC(),
		}
	}
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

func (m *MemStore) GetDeviceCommand(_ context.Context, deviceID, commandID string) (*domain.DeviceCommand, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.deviceCommands[commandID]
	if !ok || c.DeviceID != deviceID {
		return nil, ErrNotFound
	}
	cp := c
	cp.Envelope = append([]byte(nil), c.Envelope...)
	cp.Result = append([]byte(nil), c.Result...)
	return &cp, nil
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

// --- device pairings (docs/17 §6.3) --------------------------------------------

func (m *MemStore) CreateDevicePairing(_ context.Context, p *domain.DevicePairing) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devicePairings[p.ID] = *p
	return nil
}

func (m *MemStore) GetDevicePairing(_ context.Context, deviceID, pairingID string) (*domain.DevicePairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.devicePairings[pairingID]
	if !ok || p.DeviceID != deviceID {
		return nil, ErrNotFound
	}
	cp := p
	return &cp, nil
}

func (m *MemStore) ListDevicePairings(_ context.Context, deviceID, status string) ([]domain.DevicePairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.DevicePairing
	for _, p := range m.devicePairings {
		if p.DeviceID == deviceID && (status == "" || p.Status == status) {
			out = append(out, p)
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

func (m *MemStore) ResolveDevicePairing(_ context.Context, deviceID, pairingID, status string, wrap []byte, expectedKeyGen int, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if expectedKeyGen > 0 {
		d, ok := m.devices[deviceID]
		if !ok {
			return ErrNotFound
		}
		if d.KeyGen != expectedKeyGen {
			return ErrConflict
		}
	}
	p, ok := m.devicePairings[pairingID]
	if !ok || p.DeviceID != deviceID {
		return ErrNotFound
	}
	if p.Status != domain.DevicePairingPending {
		// A duplicate response with the same outcome is idempotent. A competing
		// outcome lost the pending-state compare-and-swap and must be surfaced to
		// the caller instead of being reported as successful.
		if p.Status == status {
			return nil
		}
		return ErrConflict
	}
	at = at.UTC()
	p.Status = status
	p.Wrap = wrap
	p.ResolvedAt = &at
	m.devicePairings[pairingID] = p
	return nil
}

func (m *MemStore) RekeyDevicePairings(_ context.Context, deviceID, revokedPairingID string, keyGen int, wraps map[string][]byte, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	target, ok := m.devicePairings[revokedPairingID]
	if !ok || target.DeviceID != deviceID || target.Status != domain.DevicePairingApproved {
		return ErrNotFound
	}
	d, ok := m.devices[deviceID]
	if !ok || d.KeyGen != keyGen-1 {
		if ok {
			return ErrConflict
		}
		return ErrNotFound
	}
	// Validate the complete mutation before touching any row. This mirrors the
	// all-or-nothing PostgreSQL transaction and keeps the memory store useful as
	// a faithful concurrency/test double.
	validated := make(map[string]domain.DevicePairing, len(wraps))
	expected := make(map[string]struct{})
	for id, p := range m.devicePairings {
		if p.DeviceID == deviceID && p.Status == domain.DevicePairingApproved && id != revokedPairingID {
			expected[id] = struct{}{}
		}
	}
	if len(wraps) != len(expected) {
		return ErrConflict
	}
	for id := range expected {
		wrap, exists := wraps[id]
		if !exists || len(wrap) == 0 {
			return ErrConflict
		}
		p := m.devicePairings[id]
		validated[id] = p
	}
	at = at.UTC()
	target.Status = domain.DevicePairingRevoked
	target.Wrap = nil
	target.ResolvedAt = &at
	m.devicePairings[revokedPairingID] = target
	for id, wrap := range wraps {
		p := validated[id]
		p.Wrap = append([]byte(nil), wrap...)
		m.devicePairings[id] = p
	}
	d.KeyGen = keyGen
	m.devices[deviceID] = d
	return nil
}

func (m *MemStore) RevokeDeviceTokens(_ context.Context, deviceID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	at = at.UTC()
	for id, tok := range m.deviceTokens {
		if tok.DeviceID == deviceID && tok.RevokedAt == nil {
			tok.RevokedAt = &at
			m.deviceTokens[id] = tok
		}
	}
	return nil
}

// --- device pairing offers (docs/17 §6.3 — M11 scan-to-pair) ------------------

func (m *MemStore) CreateDevicePairingOffer(_ context.Context, o *domain.DevicePairingOffer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deviceOffers[o.ID] = *o
	return nil
}

func (m *MemStore) GetDevicePairingOffer(_ context.Context, offerID string) (*domain.DevicePairingOffer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.deviceOffers[offerID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := o
	return &cp, nil
}

func (m *MemStore) ClaimDevicePairingOffer(_ context.Context, offerID, userID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.deviceOffers[offerID]
	if !ok {
		return ErrNotFound
	}
	if o.ClaimedAt != nil {
		return ErrAlreadyExists
	}
	at = at.UTC()
	o.ClaimedBy = &userID
	o.ClaimedAt = &at
	m.deviceOffers[offerID] = o
	return nil
}
