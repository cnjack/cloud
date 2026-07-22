package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cnjack/jcloud/internal/domain"
)

func scanAccountSettings(row pgx.Row) (*domain.AccountSettings, error) {
	var settings domain.AccountSettings
	if err := row.Scan(&settings.UserID, &settings.Version, &settings.Envelope, &settings.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan account settings: %w", err)
	}
	return &settings, nil
}

func (s *PGStore) GetAccountSettings(ctx context.Context, userID string) (*domain.AccountSettings, error) {
	return scanAccountSettings(s.pool.QueryRow(ctx,
		`SELECT user_id, version, envelope, updated_at FROM account_settings WHERE user_id=$1`, userID))
}

func (s *PGStore) PutAccountSettings(ctx context.Context, userID string, baseVersion int64, envelope []byte, at time.Time) (*domain.AccountSettings, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("put account settings: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current int64
	err = tx.QueryRow(ctx, `SELECT version FROM account_settings WHERE user_id=$1 FOR UPDATE`, userID).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows) && baseVersion == 0:
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx,
			`INSERT INTO account_settings (user_id, version, envelope, updated_at) VALUES ($1,1,$2,$3)
			 ON CONFLICT (user_id) DO NOTHING`,
			userID, envelope, at.UTC())
		if err == nil && tag.RowsAffected() != 1 {
			return nil, ErrConflict
		}
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrConflict
	case err != nil:
		return nil, fmt.Errorf("put account settings: lock: %w", err)
	case current != baseVersion:
		return nil, ErrConflict
	default:
		_, err = tx.Exec(ctx,
			`UPDATE account_settings SET version=$2, envelope=$3, updated_at=$4 WHERE user_id=$1`,
			userID, baseVersion+1, envelope, at.UTC())
	}
	if err != nil {
		return nil, fmt.Errorf("put account settings: write: %w", err)
	}
	settings, err := scanAccountSettings(tx.QueryRow(ctx,
		`SELECT user_id, version, envelope, updated_at FROM account_settings WHERE user_id=$1`, userID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("put account settings: commit: %w", err)
	}
	return settings, nil
}
