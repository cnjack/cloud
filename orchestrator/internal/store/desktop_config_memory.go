package store

import (
	"context"
	"sort"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func syncWrapKey(userID, deviceID string) string { return userID + "|" + deviceID }
func providerConfigKey(userID, providerID string) string {
	return userID + "|" + providerID
}

func cloneSyncWrap(v domain.AccountSyncKeyWrap) domain.AccountSyncKeyWrap {
	v.Wrap = append([]byte(nil), v.Wrap...)
	return v
}

func cloneProviderConfig(v domain.AccountProviderConfig) domain.AccountProviderConfig {
	v.Envelope = append([]byte(nil), v.Envelope...)
	return v
}

func (m *MemStore) GetAccountSyncKey(_ context.Context, userID string) (*domain.AccountSyncKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.accountSyncKeys[userID]
	if !ok {
		return nil, ErrNotFound
	}
	return &v, nil
}

func (m *MemStore) InitializeAccountSyncKey(_ context.Context, userID, deviceID string, keyGen int, wrap []byte, at time.Time) (*domain.AccountSyncKeyWrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.accountSyncKeys[userID]; exists {
		return nil, ErrConflict
	}
	d, ok := m.devices[deviceID]
	if !ok || d.UserID != userID || d.Pubkey == "" {
		return nil, ErrNotFound
	}
	now := at.UTC()
	m.accountSyncKeys[userID] = domain.AccountSyncKey{
		UserID: userID, KeyGen: keyGen, CreatedAt: now, UpdatedAt: now,
	}
	resolved := now
	v := domain.AccountSyncKeyWrap{
		UserID: userID, DeviceID: deviceID, DeviceName: d.Name, Pubkey: d.Pubkey,
		KeyGen: keyGen, Status: domain.AccountSyncKeyApproved,
		Wrap: append([]byte(nil), wrap...), ApprovedByDeviceID: deviceID,
		CreatedAt: now, ResolvedAt: &resolved,
	}
	m.accountSyncWraps[syncWrapKey(userID, deviceID)] = v
	out := cloneSyncWrap(v)
	return &out, nil
}

func (m *MemStore) GetAccountSyncKeyWrap(_ context.Context, userID, deviceID string) (*domain.AccountSyncKeyWrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.accountSyncWraps[syncWrapKey(userID, deviceID)]
	if !ok {
		return nil, ErrNotFound
	}
	if d, ok := m.devices[deviceID]; ok {
		v.DeviceName, v.Pubkey = d.Name, d.Pubkey
	}
	out := cloneSyncWrap(v)
	return &out, nil
}

func (m *MemStore) RequestAccountSyncKey(_ context.Context, userID, deviceID string, at time.Time) (*domain.AccountSyncKeyWrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.accountSyncKeys[userID]
	if !ok {
		return nil, ErrNotFound
	}
	d, ok := m.devices[deviceID]
	if !ok || d.UserID != userID || d.Pubkey == "" {
		return nil, ErrNotFound
	}
	mapKey := syncWrapKey(userID, deviceID)
	if current, exists := m.accountSyncWraps[mapKey]; exists && current.Status == domain.AccountSyncKeyApproved && current.KeyGen == key.KeyGen {
		current.DeviceName, current.Pubkey = d.Name, d.Pubkey
		out := cloneSyncWrap(current)
		return &out, nil
	}
	v := domain.AccountSyncKeyWrap{
		UserID: userID, DeviceID: deviceID, DeviceName: d.Name, Pubkey: d.Pubkey,
		KeyGen: key.KeyGen, Status: domain.AccountSyncKeyPending, CreatedAt: at.UTC(),
	}
	m.accountSyncWraps[mapKey] = v
	out := cloneSyncWrap(v)
	return &out, nil
}

func (m *MemStore) ListAccountSyncKeyWraps(_ context.Context, userID string, status domain.AccountSyncKeyStatus) ([]domain.AccountSyncKeyWrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.AccountSyncKeyWrap
	for _, v := range m.accountSyncWraps {
		if v.UserID != userID || (status != "" && v.Status != status) {
			continue
		}
		if d, ok := m.devices[v.DeviceID]; ok {
			v.DeviceName, v.Pubkey = d.Name, d.Pubkey
		}
		out = append(out, cloneSyncWrap(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) RespondAccountSyncKeyRequest(_ context.Context, userID, approverDeviceID, targetDeviceID string, approve bool, keyGen int, wrap []byte, at time.Time) (*domain.AccountSyncKeyWrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.accountSyncKeys[userID]
	if !ok || key.KeyGen != keyGen {
		return nil, ErrConflict
	}
	approver, ok := m.accountSyncWraps[syncWrapKey(userID, approverDeviceID)]
	if !ok || approver.Status != domain.AccountSyncKeyApproved || approver.KeyGen != keyGen {
		return nil, ErrConflict
	}
	mapKey := syncWrapKey(userID, targetDeviceID)
	target, ok := m.accountSyncWraps[mapKey]
	if !ok {
		return nil, ErrNotFound
	}
	if target.Status != domain.AccountSyncKeyPending || target.KeyGen != keyGen {
		return nil, ErrConflict
	}
	now := at.UTC()
	target.ResolvedAt = &now
	target.ApprovedByDeviceID = approverDeviceID
	if approve {
		target.Status = domain.AccountSyncKeyApproved
		target.Wrap = append([]byte(nil), wrap...)
	} else {
		target.Status = domain.AccountSyncKeyDenied
		target.Wrap = nil
	}
	m.accountSyncWraps[mapKey] = target
	if d, ok := m.devices[targetDeviceID]; ok {
		target.DeviceName, target.Pubkey = d.Name, d.Pubkey
	}
	out := cloneSyncWrap(target)
	return &out, nil
}

func (m *MemStore) RevokeAccountSyncKeyWrap(_ context.Context, userID, approverDeviceID, targetDeviceID string, keyGen int, at time.Time) (*domain.AccountSyncKeyWrap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.accountSyncKeys[userID]
	if !ok || key.KeyGen != keyGen {
		return nil, ErrConflict
	}
	approver, ok := m.accountSyncWraps[syncWrapKey(userID, approverDeviceID)]
	if !ok || approver.Status != domain.AccountSyncKeyApproved || approver.KeyGen != keyGen {
		return nil, ErrConflict
	}
	mapKey := syncWrapKey(userID, targetDeviceID)
	target, ok := m.accountSyncWraps[mapKey]
	if !ok {
		return nil, ErrNotFound
	}
	if target.Status != domain.AccountSyncKeyApproved || target.KeyGen != keyGen {
		return nil, ErrConflict
	}
	now := at.UTC()
	target.Status = domain.AccountSyncKeyDenied
	target.Wrap = nil
	target.ApprovedByDeviceID = approverDeviceID
	target.ResolvedAt = &now
	m.accountSyncWraps[mapKey] = target
	if d, ok := m.devices[targetDeviceID]; ok {
		target.DeviceName, target.Pubkey = d.Name, d.Pubkey
	}
	out := cloneSyncWrap(target)
	return &out, nil
}

func (m *MemStore) ListAccountProviderConfigs(_ context.Context, userID string) ([]domain.AccountProviderConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.AccountProviderConfig
	for _, v := range m.accountProviders {
		if v.UserID == userID {
			out = append(out, cloneProviderConfig(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ProviderID < out[j].ProviderID
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out, nil
}

func (m *MemStore) GetAccountProviderConfig(_ context.Context, userID, providerID string) (*domain.AccountProviderConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.accountProviders[providerConfigKey(userID, providerID)]
	if !ok {
		return nil, ErrNotFound
	}
	out := cloneProviderConfig(v)
	return &out, nil
}

func (m *MemStore) PutAccountProviderConfig(_ context.Context, userID, providerID string, baseVersion int64, envelope []byte, deleted bool, at time.Time) (*domain.AccountProviderConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapKey := providerConfigKey(userID, providerID)
	current, exists := m.accountProviders[mapKey]
	if (!exists && baseVersion != 0) || (exists && current.Version != baseVersion) {
		return nil, ErrConflict
	}
	v := domain.AccountProviderConfig{
		UserID: userID, ProviderID: providerID, Version: baseVersion + 1,
		Envelope: append([]byte(nil), envelope...), Deleted: deleted, UpdatedAt: at.UTC(),
	}
	m.accountProviders[mapKey] = v
	out := cloneProviderConfig(v)
	return &out, nil
}
