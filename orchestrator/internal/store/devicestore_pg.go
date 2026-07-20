package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

func (s *PGStore) ListDevicesForUser(ctx context.Context, userID string) ([]domain.Device, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE user_id=$1 AND revoked_at IS NULL
		 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list devices for user: %w", err)
	}
	defer rows.Close()
	var out []domain.Device
	for rows.Next() {
		var d domain.Device
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.Hostname, &d.JcodeVersion, &d.Pubkey,
			&d.KeyGen, &d.LastSeenAt, &d.CreatedAt, &d.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// --- device relay: sessions / events / commands (docs/17 §4) ------------------

func (s *PGStore) UpsertDeviceSession(ctx context.Context, ds *domain.DeviceSession) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO device_sessions (device_id, session_id, meta, status, updated_at)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (device_id, session_id) DO UPDATE
		 SET meta=EXCLUDED.meta, status=EXCLUDED.status, updated_at=EXCLUDED.updated_at`,
		ds.DeviceID, ds.SessionID, ds.Meta, ds.Status, ds.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert device session: %w", err)
	}
	return nil
}

func (s *PGStore) ListDeviceSessions(ctx context.Context, deviceID string) ([]domain.DeviceSession, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT device_id, session_id, meta, status, updated_at
		 FROM device_sessions WHERE device_id=$1 ORDER BY updated_at DESC, session_id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list device sessions: %w", err)
	}
	defer rows.Close()
	var out []domain.DeviceSession
	for rows.Next() {
		var ds domain.DeviceSession
		if err := rows.Scan(&ds.DeviceID, &ds.SessionID, &ds.Meta, &ds.Status, &ds.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan device session: %w", err)
		}
		out = append(out, ds)
	}
	return out, rows.Err()
}

func (s *PGStore) AppendDeviceEvents(ctx context.Context, deviceID, sessionID string, events []*domain.DeviceEvent) (*DeviceEventBatch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("append device events: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	res := &DeviceEventBatch{Accepted: []int64{}, Conflicted: []int64{}}
	for _, ev := range events {
		tag, err := tx.Exec(ctx,
			`INSERT INTO device_events (device_id, session_id, seq, kind, envelope, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (device_id, session_id, seq) DO NOTHING`,
			deviceID, sessionID, ev.Seq, ev.Kind, ev.Envelope, ev.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("append device event seq=%d: %w", ev.Seq, err)
		}
		if tag.RowsAffected() == 1 {
			res.Accepted = append(res.Accepted, ev.Seq)
		} else {
			res.Conflicted = append(res.Conflicted, ev.Seq)
		}
	}
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM device_events WHERE device_id=$1 AND session_id=$2`,
		deviceID, sessionID).Scan(&res.MaxSeq); err != nil {
		return nil, fmt.Errorf("append device events: max seq: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("append device events: commit: %w", err)
	}
	return res, nil
}

func (s *PGStore) ListDeviceEvents(ctx context.Context, deviceID, sessionID string, afterSeq int64, limit int) ([]domain.DeviceEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT device_id, session_id, seq, kind, envelope, created_at
		 FROM device_events
		 WHERE device_id=$1 AND session_id=$2 AND seq>$3
		 ORDER BY seq ASC LIMIT $4`, deviceID, sessionID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("list device events: %w", err)
	}
	defer rows.Close()
	var out []domain.DeviceEvent
	for rows.Next() {
		var ev domain.DeviceEvent
		if err := rows.Scan(&ev.DeviceID, &ev.SessionID, &ev.Seq, &ev.Kind, &ev.Envelope, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan device event: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *PGStore) MaxDeviceEventSeq(ctx context.Context, deviceID, sessionID string) (int64, error) {
	var max int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM device_events WHERE device_id=$1 AND session_id=$2`,
		deviceID, sessionID).Scan(&max); err != nil {
		return 0, fmt.Errorf("max device event seq: %w", err)
	}
	return max, nil
}

const deviceCommandCols = `id, device_id, kind, session_id, envelope, status, result, created_at, acked_at`

func (s *PGStore) CreateDeviceCommand(ctx context.Context, c *domain.DeviceCommand) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO device_commands (`+deviceCommandCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		c.ID, c.DeviceID, c.Kind, c.SessionID, c.Envelope, c.Status, c.Result, c.CreatedAt, c.AckedAt)
	if err != nil {
		return fmt.Errorf("insert device command: %w", err)
	}
	return nil
}

func (s *PGStore) DeliverPendingDeviceCommands(ctx context.Context, deviceID string, limit int) ([]domain.DeviceCommand, error) {
	// Single-statement offer: the subselect picks the oldest pending ids, the
	// UPDATE flips them to delivered atomically (two concurrent polls can never
	// hand out the same command), RETURNING gives back the committed rows.
	rows, err := s.pool.Query(ctx,
		`UPDATE device_commands SET status='delivered'
		 WHERE id IN (
		     SELECT id FROM device_commands
		     WHERE device_id=$1 AND status='pending'
		     ORDER BY created_at, id LIMIT $2
		 )
		 RETURNING `+deviceCommandCols, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("deliver pending device commands: %w", err)
	}
	defer rows.Close()
	var out []domain.DeviceCommand
	for rows.Next() {
		var c domain.DeviceCommand
		if err := rows.Scan(&c.ID, &c.DeviceID, &c.Kind, &c.SessionID, &c.Envelope,
			&c.Status, &c.Result, &c.CreatedAt, &c.AckedAt); err != nil {
			return nil, fmt.Errorf("scan device command: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// RETURNING has no order guarantee; restore the created_at poll order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *PGStore) AckDeviceCommand(ctx context.Context, deviceID, commandID, status string, result []byte, at time.Time) error {
	// Only an unresolved command takes the ack; a duplicate ack is a no-op.
	tag, err := s.pool.Exec(ctx,
		`UPDATE device_commands SET status=$3, result=$4, acked_at=$5
		 WHERE id=$1 AND device_id=$2 AND status NOT IN ('acked','failed')`,
		commandID, deviceID, status, result, at)
	if err != nil {
		return fmt.Errorf("ack device command: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// Either the command does not exist for this device (ErrNotFound) or it is
	// already resolved (idempotent no-op) — distinguish with a cheap probe.
	var one int
	err = s.pool.QueryRow(ctx,
		`SELECT 1 FROM device_commands WHERE id=$1 AND device_id=$2`, commandID, deviceID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("ack device command: probe: %w", err)
	}
	return nil
}
