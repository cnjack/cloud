package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *PGStore) GetAccountProfile(ctx context.Context, userID string) (*domain.AccountProfile, error) {
	var settings domain.AccountProfile
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT display_name, preferences FROM users WHERE id=$1`, userID).Scan(&settings.DisplayName, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get account profile: %w", err)
	}
	if err := json.Unmarshal(raw, &settings.Preferences); err != nil {
		return nil, fmt.Errorf("decode account preferences: %w", err)
	}
	return &settings, nil
}

func (s *PGStore) UpdateAccountProfile(ctx context.Context, userID string, settings domain.AccountProfile) error {
	raw, err := json.Marshal(settings.Preferences)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE users SET display_name=$2, preferences=$3 WHERE id=$1`, userID, settings.DisplayName, raw)
	if err != nil {
		return fmt.Errorf("update account profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (m *MemStore) GetAccountProfile(_ context.Context, userID string) (*domain.AccountProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[userID]
	if !ok {
		return nil, ErrNotFound
	}
	return &domain.AccountProfile{DisplayName: user.DisplayName, Preferences: user.Preferences}, nil
}

func (m *MemStore) UpdateAccountProfile(_ context.Context, userID string, settings domain.AccountProfile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[userID]
	if !ok {
		return ErrNotFound
	}
	user.DisplayName, user.Preferences = settings.DisplayName, settings.Preferences
	m.users[userID] = user
	return nil
}
