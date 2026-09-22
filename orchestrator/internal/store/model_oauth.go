package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// WithModelOAuth serializes credential rotation across replicas. The callback
// receives only ciphertext and must not call this store recursively. A nil next
// value leaves the row unchanged. External calls inside it must be time-bounded.
func (s *PGStore) WithModelOAuth(ctx context.Context, id string, fn func([]byte) ([]byte, error)) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the parent as well: first-use initialization and deletion serialize too.
	var exists string
	if err := tx.QueryRow(ctx, `SELECT id FROM model_providers WHERE id=$1 FOR UPDATE`, id).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT state_enc FROM model_provider_oauth WHERE provider_id=$1`, id).Scan(&raw); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	next, err := fn(bytes.Clone(raw))
	if err != nil {
		return err
	}
	if next != nil && !bytes.Equal(raw, next) {
		if _, err := tx.Exec(ctx, `INSERT INTO model_provider_oauth(provider_id,state_enc) VALUES($1,$2) ON CONFLICT(provider_id) DO UPDATE SET state_enc=excluded.state_enc,updated_at=now()`, id, next); err != nil {
			return fmt.Errorf("save model authorization: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (s *MemStore) WithModelOAuth(ctx context.Context, id string, fn func([]byte) ([]byte, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.modelProviders[id]; !ok {
		return ErrNotFound
	}
	next, err := fn(bytes.Clone(s.modelOAuth[id]))
	if err != nil {
		return err
	}
	if next != nil {
		if s.modelOAuth == nil {
			s.modelOAuth = map[string][]byte{}
		}
		s.modelOAuth[id] = bytes.Clone(next)
	}
	return nil
}
