package store

import (
	"context"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func (m *MemStore) GetAccountSettings(_ context.Context, userID string) (*domain.AccountSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings, ok := m.accountSettings[userID]
	if !ok {
		return nil, ErrNotFound
	}
	settings.Envelope = append([]byte(nil), settings.Envelope...)
	return &settings, nil
}

func (m *MemStore) PutAccountSettings(_ context.Context, userID string, baseVersion int64, envelope []byte, at time.Time) (*domain.AccountSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.accountSettings[userID]
	if (!exists && baseVersion != 0) || (exists && current.Version != baseVersion) {
		return nil, ErrConflict
	}
	next := domain.AccountSettings{
		UserID: userID, Version: baseVersion + 1,
		Envelope: append([]byte(nil), envelope...), UpdatedAt: at.UTC(),
	}
	m.accountSettings[userID] = next
	next.Envelope = append([]byte(nil), next.Envelope...)
	return &next, nil
}
