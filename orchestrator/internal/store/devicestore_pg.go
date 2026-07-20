package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cnjack/jcloud/internal/domain"
)

// --- devices (docs/17 — jcode device relay) -----------------------------------

const deviceCols = `id, user_id, name, hostname, jcode_version, pubkey, key_gen,
	last_seen_at, created_at, revoked_at`

func scanDevice(row pgx.Row) (*domain.Device, error) {
	var d domain.Device
	err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.Hostname, &d.JcodeVersion, &d.Pubkey,
		&d.KeyGen, &d.LastSeenAt, &d.CreatedAt, &d.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan device: %w", err)
	}
	return &d, nil
}

func (s *PGStore) CreateDevice(ctx context.Context, d *domain.Device) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO devices (`+deviceCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		d.ID, d.UserID, d.Name, d.Hostname, d.JcodeVersion, d.Pubkey,
		d.KeyGen, d.LastSeenAt, d.CreatedAt, d.RevokedAt)
	if err != nil {
		return fmt.Errorf("insert device: %w", err)
	}
	return nil
}

func (s *PGStore) GetDevice(ctx context.Context, id string) (*domain.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE id=$1`, id))
}

func (s *PGStore) UpsertDeviceRegistration(ctx context.Context, d *domain.Device) error {
	// Only the registration payload is writable: user_id/key_gen/created_at are
	// never touched, so a register call can neither re-own a device nor roll its
	// key generation. The row is created at token issuance, so RowsAffected==0
	// means the device was deleted under a live token.
	tag, err := s.pool.Exec(ctx,
		`UPDATE devices SET name=$2, hostname=$3, jcode_version=$4, pubkey=$5, last_seen_at=$6
		 WHERE id=$1`,
		d.ID, d.Name, d.Hostname, d.JcodeVersion, d.Pubkey, d.LastSeenAt)
	if err != nil {
		return fmt.Errorf("upsert device registration: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) TouchDeviceLastSeen(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE devices SET last_seen_at=$2 WHERE id=$1`, id, at)
	if err != nil {
		return fmt.Errorf("touch device last_seen_at: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) CreateDeviceToken(ctx context.Context, t *domain.DeviceToken) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO device_tokens (id, device_id, token_hash, created_at, revoked_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		t.ID, t.DeviceID, t.TokenHash, t.CreatedAt, t.RevokedAt)
	if err != nil {
		return fmt.Errorf("insert device token: %w", err)
	}
	return nil
}

func (s *PGStore) GetDeviceTokenByHash(ctx context.Context, tokenHash string) (*domain.DeviceToken, error) {
	// user_id joins from devices; revoked tokens AND tokens of revoked devices
	// both resolve to ErrNotFound so revocation is effective on the very next
	// lookup, no cache to invalidate.
	var t domain.DeviceToken
	err := s.pool.QueryRow(ctx,
		`SELECT t.id, t.device_id, d.user_id, t.token_hash, t.created_at, t.revoked_at
		 FROM device_tokens t JOIN devices d ON d.id = t.device_id
		 WHERE t.token_hash=$1 AND t.revoked_at IS NULL AND d.revoked_at IS NULL`, tokenHash).
		Scan(&t.ID, &t.DeviceID, &t.UserID, &t.TokenHash, &t.CreatedAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get device token by hash: %w", err)
	}
	return &t, nil
}
