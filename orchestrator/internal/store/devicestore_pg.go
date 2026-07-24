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

const deviceCols = `id, user_id, name, hostname, jcode_version, platform, pubkey, key_gen,
	capabilities, e2ee, fingerprint_hash, last_seen_at, created_at, revoked_at`

func scanDevice(row pgx.Row) (*domain.Device, error) {
	var d domain.Device
	err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.Hostname, &d.JcodeVersion, &d.Platform, &d.Pubkey,
		&d.KeyGen, &d.Capabilities, &d.E2EE, &fingerprintHashScanner{&d.FingerprintHash}, &d.LastSeenAt, &d.CreatedAt, &d.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan device: %w", err)
	}
	return &d, nil
}

// fingerprintHashScanner bridges the nullable fingerprint_hash column to the
// domain's plain string: NULL (pre-M16 rows) decodes as "".
type fingerprintHashScanner struct{ dst *string }

func (f fingerprintHashScanner) Scan(src any) error {
	if src == nil {
		*f.dst = ""
		return nil
	}
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("fingerprint_hash: unexpected type %T", src)
	}
	*f.dst = s
	return nil
}

// nullFingerprint maps the domain's empty string to SQL NULL: ” must never
// be stored, because the partial unique index (0036) treats every non-NULL
// value as a real fingerprint.
func nullFingerprint(hash string) any {
	if hash == "" {
		return nil
	}
	return hash
}

func (s *PGStore) CreateDevice(ctx context.Context, d *domain.Device) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO devices (`+deviceCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		d.ID, d.UserID, d.Name, d.Hostname, d.JcodeVersion, d.Platform, d.Pubkey,
		d.KeyGen, d.Capabilities, d.E2EE, nullFingerprint(d.FingerprintHash), d.LastSeenAt, d.CreatedAt, d.RevokedAt)
	if err != nil {
		// The 0036 partial unique index: another live device of this user
		// already claims the fingerprint — the M16 login-dedup race signal.
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
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
		`UPDATE devices SET name=$2, hostname=$3, jcode_version=$4, platform=$5, pubkey=$6, e2ee=$7, last_seen_at=$8, fingerprint_hash=$9
		 WHERE id=$1`,
		d.ID, d.Name, d.Hostname, d.JcodeVersion, d.Platform, d.Pubkey, d.E2EE, d.LastSeenAt, nullFingerprint(d.FingerprintHash))
	if err != nil {
		return fmt.Errorf("upsert device registration: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) UpdateDeviceCapabilities(ctx context.Context, id string, capabilities []byte) error {
	// Like the registration upsert, only the one mirrored column is writable;
	// a nil blob clears it (a connector that stops reporting capabilities).
	tag, err := s.pool.Exec(ctx,
		`UPDATE devices SET capabilities=$2 WHERE id=$1`, id, capabilities)
	if err != nil {
		return fmt.Errorf("update device capabilities: %w", err)
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
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.Hostname, &d.JcodeVersion, &d.Platform, &d.Pubkey,
			&d.KeyGen, &d.Capabilities, &d.E2EE, &fingerprintHashScanner{&d.FingerprintHash}, &d.LastSeenAt, &d.CreatedAt, &d.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FindDeviceByFingerprint returns the user's NON-REVOKED device carrying the
// given fingerprint hash (ErrNotFound when none) — the M16 login-dedup lookup.
func (s *PGStore) FindDeviceByFingerprint(ctx context.Context, userID, fingerprintHash string) (*domain.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`SELECT `+deviceCols+` FROM devices
		 WHERE user_id=$1 AND fingerprint_hash=$2 AND revoked_at IS NULL
		 ORDER BY created_at, id LIMIT 1`, userID, fingerprintHash))
}

// RevokeDevice soft-deletes a device (DELETE /api/v1/devices/{id}, M16): it
// stamps revoked_at, which (a) drops the row from ListDevicesForUser and the
// client API (readers treat it as gone), (b) frees its fingerprint for a
// future re-login (the partial unique index excludes revoked rows), and
// (c) kills every device token on the next lookup (GetDeviceTokenByHash joins
// devices and excludes revoked rows). device_events/device_sessions survive
// for audit (ON DELETE CASCADE only fires on a hard delete).
func (s *PGStore) RevokeDevice(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE devices SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, id, at)
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- device relay: sessions / events / commands (docs/17 §4) ------------------

func (s *PGStore) UpsertDeviceSession(ctx context.Context, ds *domain.DeviceSession) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO device_sessions (device_id, session_id, meta, status, last_activity_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (device_id, session_id) DO UPDATE
		 SET meta=EXCLUDED.meta, status=EXCLUDED.status, last_activity_at=EXCLUDED.last_activity_at,
		     updated_at=EXCLUDED.updated_at`,
		ds.DeviceID, ds.SessionID, ds.Meta, ds.Status, ds.LastActivityAt, ds.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert device session: %w", err)
	}
	return nil
}

func (s *PGStore) ListDeviceSessions(ctx context.Context, deviceID string) ([]domain.DeviceSession, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT device_id, session_id, meta, status, last_activity_at, updated_at
		 FROM device_sessions WHERE device_id=$1
		 ORDER BY last_activity_at DESC NULLS LAST, session_id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list device sessions: %w", err)
	}
	defer rows.Close()
	var out []domain.DeviceSession
	for rows.Next() {
		var ds domain.DeviceSession
		if err := rows.Scan(&ds.DeviceID, &ds.SessionID, &ds.Meta, &ds.Status, &ds.LastActivityAt, &ds.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan device session: %w", err)
		}
		out = append(out, ds)
	}
	return out, rows.Err()
}

func (s *PGStore) DeleteDeviceSession(ctx context.Context, deviceID, sessionID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM device_sessions WHERE device_id=$1 AND session_id=$2`, deviceID, sessionID)
	if err != nil {
		return fmt.Errorf("delete device session: %w", err)
	}
	return nil
}

func (s *PGStore) DeleteDeviceSessionsExcept(ctx context.Context, deviceID string, keepSessionIDs []string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM device_sessions WHERE device_id=$1 AND NOT (session_id = ANY($2::text[]))`,
		deviceID, keepSessionIDs)
	if err != nil {
		return fmt.Errorf("delete stale device sessions: %w", err)
	}
	return nil
}

func (s *PGStore) AppendDeviceEvents(ctx context.Context, deviceID, sessionID string, events []*domain.DeviceEvent) (*DeviceEventBatch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("append device events: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The connector batches events every ~200ms but only mirrors session rows
	// on its 2s sync tick, so the first batches of a fresh session can arrive
	// before any device_sessions row exists — and the device_events_session_fk
	// would reject them. Auto-create a minimal placeholder row in the same
	// transaction: meta stays NULL (bytea is nullable) and the connector's
	// regular upsert fills meta/status in when it lands (ON CONFLICT DO UPDATE
	// there only touches meta/status/updated_at).
	if _, err := tx.Exec(ctx,
		`INSERT INTO device_sessions (device_id, session_id, status, updated_at)
		 VALUES ($1,$2,$3,now())
		 ON CONFLICT (device_id, session_id) DO NOTHING`,
		deviceID, sessionID, domain.DeviceSessionRunning); err != nil {
		return nil, fmt.Errorf("append device events: ensure session row: %w", err)
	}

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

func (s *PGStore) GetDeviceCommand(ctx context.Context, deviceID, commandID string) (*domain.DeviceCommand, error) {
	var c domain.DeviceCommand
	err := s.pool.QueryRow(ctx,
		`SELECT `+deviceCommandCols+` FROM device_commands WHERE id=$1 AND device_id=$2`,
		commandID, deviceID,
	).Scan(&c.ID, &c.DeviceID, &c.Kind, &c.SessionID, &c.Envelope,
		&c.Status, &c.Result, &c.CreatedAt, &c.AckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get device command: %w", err)
	}
	return &c, nil
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

// --- device pairings (docs/17 §6.3) --------------------------------------------

const devicePairingCols = `id, device_id, requester_label, requester_pubkey, status, wrapped_cek, created_at, resolved_at`

func scanDevicePairing(row pgx.Row) (*domain.DevicePairing, error) {
	var p domain.DevicePairing
	err := row.Scan(&p.ID, &p.DeviceID, &p.Label, &p.Pubkey, &p.Status, &p.Wrap, &p.CreatedAt, &p.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan device pairing: %w", err)
	}
	return &p, nil
}

func (s *PGStore) CreateDevicePairing(ctx context.Context, p *domain.DevicePairing) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO device_pairings (`+devicePairingCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.ID, p.DeviceID, p.Label, p.Pubkey, p.Status, p.Wrap, p.CreatedAt, p.ResolvedAt)
	if err != nil {
		return fmt.Errorf("insert device pairing: %w", err)
	}
	return nil
}

func (s *PGStore) GetDevicePairing(ctx context.Context, deviceID, pairingID string) (*domain.DevicePairing, error) {
	return scanDevicePairing(s.pool.QueryRow(ctx,
		`SELECT `+devicePairingCols+` FROM device_pairings WHERE id=$1 AND device_id=$2`,
		pairingID, deviceID))
}

func (s *PGStore) ListDevicePairings(ctx context.Context, deviceID, status string) ([]domain.DevicePairing, error) {
	query := `SELECT ` + devicePairingCols + ` FROM device_pairings WHERE device_id=$1`
	args := []any{deviceID}
	if status != "" {
		query += ` AND status=$2`
		args = append(args, status)
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list device pairings: %w", err)
	}
	defer rows.Close()
	var out []domain.DevicePairing
	for rows.Next() {
		var p domain.DevicePairing
		if err := rows.Scan(&p.ID, &p.DeviceID, &p.Label, &p.Pubkey, &p.Status, &p.Wrap, &p.CreatedAt, &p.ResolvedAt); err != nil {
			return nil, fmt.Errorf("scan device pairing: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PGStore) ResolveDevicePairing(ctx context.Context, deviceID, pairingID, status string, wrap []byte, expectedKeyGen int, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("resolve device pairing: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if expectedKeyGen > 0 {
		var currentKeyGen int
		err = tx.QueryRow(ctx, `SELECT key_gen FROM devices WHERE id=$1 FOR UPDATE`, deviceID).Scan(&currentKeyGen)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("resolve device pairing: lock device: %w", err)
		}
		if currentKeyGen != expectedKeyGen {
			return ErrConflict
		}
	}
	// Only a pending pairing takes the resolution. A duplicate response with
	// the same outcome is idempotent; a competing outcome is a conflict.
	tag, err := tx.Exec(ctx,
		`UPDATE device_pairings SET status=$3, wrapped_cek=$4, resolved_at=$5
		 WHERE id=$1 AND device_id=$2 AND status='pending'`,
		pairingID, deviceID, status, wrap, at)
	if err != nil {
		return fmt.Errorf("resolve device pairing: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("resolve device pairing: commit: %w", err)
		}
		return nil
	}
	var currentStatus string
	err = tx.QueryRow(ctx,
		`SELECT status FROM device_pairings WHERE id=$1 AND device_id=$2`, pairingID, deviceID).Scan(&currentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve device pairing: probe: %w", err)
	}
	if currentStatus != status {
		return ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("resolve device pairing: commit no-op: %w", err)
	}
	return nil
}

func (s *PGStore) RekeyDevicePairings(ctx context.Context, deviceID, revokedPairingID string, keyGen int, wraps map[string][]byte, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rekey device pairings: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Claim the generation transition first. The compare-and-swap both locks the
	// device row and prevents two concurrent revocations from committing wraps
	// derived from the same old CEK.
	tag, err := tx.Exec(ctx, `UPDATE devices SET key_gen=$2 WHERE id=$1 AND key_gen=$3`, deviceID, keyGen, keyGen-1)
	if err != nil {
		return fmt.Errorf("rekey device pairings: update device: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	// Every approval takes the same device-row lock in ResolveDevicePairing.
	// Re-read the complete approved set while holding it so a response racing
	// this rekey cannot leave a newly approved client on generation N.
	rows, err := tx.Query(ctx,
		`SELECT id FROM device_pairings WHERE device_id=$1 AND status='approved' FOR UPDATE`, deviceID)
	if err != nil {
		return fmt.Errorf("rekey device pairings: lock approved set: %w", err)
	}
	expected := make(map[string]struct{})
	targetPresent := false
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("rekey device pairings: scan approved set: %w", err)
		}
		if id == revokedPairingID {
			targetPresent = true
		} else {
			expected[id] = struct{}{}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rekey device pairings: approved set: %w", err)
	}
	if !targetPresent {
		return ErrNotFound
	}
	if len(wraps) != len(expected) {
		return ErrConflict
	}
	for id := range expected {
		if len(wraps[id]) == 0 {
			return ErrConflict
		}
	}
	tag, err = tx.Exec(ctx,
		`UPDATE device_pairings SET status='revoked', wrapped_cek=NULL, resolved_at=$3
		 WHERE id=$1 AND device_id=$2 AND status='approved'`,
		revokedPairingID, deviceID, at)
	if err != nil {
		return fmt.Errorf("rekey device pairings: revoke: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	for id, wrap := range wraps {
		tag, err = tx.Exec(ctx,
			`UPDATE device_pairings SET wrapped_cek=$3
			 WHERE id=$1 AND device_id=$2 AND status='approved'`,
			id, deviceID, wrap)
		if err != nil {
			return fmt.Errorf("rekey device pairings: update wrap %s: %w", id, err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("rekey device pairings: commit: %w", err)
	}
	return nil
}

func (s *PGStore) RevokeDeviceTokens(ctx context.Context, deviceID string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE device_tokens SET revoked_at=$2 WHERE device_id=$1 AND revoked_at IS NULL`,
		deviceID, at)
	if err != nil {
		return fmt.Errorf("revoke device tokens: %w", err)
	}
	return nil
}

// --- device pairing offers (docs/17 §6.3 — M11 scan-to-pair) ------------------

const devicePairingOfferCols = `id, device_id, secret_hash, claimed_by, claimed_at, expires_at, created_at`

func scanDevicePairingOffer(row pgx.Row) (*domain.DevicePairingOffer, error) {
	var o domain.DevicePairingOffer
	err := row.Scan(&o.ID, &o.DeviceID, &o.SecretHash, &o.ClaimedBy, &o.ClaimedAt, &o.ExpiresAt, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan device pairing offer: %w", err)
	}
	return &o, nil
}

func (s *PGStore) CreateDevicePairingOffer(ctx context.Context, o *domain.DevicePairingOffer) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO device_pairing_offers (`+devicePairingOfferCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		o.ID, o.DeviceID, o.SecretHash, o.ClaimedBy, o.ClaimedAt, o.ExpiresAt, o.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert device pairing offer: %w", err)
	}
	return nil
}

func (s *PGStore) GetDevicePairingOffer(ctx context.Context, offerID string) (*domain.DevicePairingOffer, error) {
	return scanDevicePairingOffer(s.pool.QueryRow(ctx,
		`SELECT `+devicePairingOfferCols+` FROM device_pairing_offers WHERE id=$1`, offerID))
}

func (s *PGStore) ClaimDevicePairingOffer(ctx context.Context, offerID, userID string, at time.Time) error {
	// Only an unclaimed offer takes the stamp: the first claim wins, a second
	// (or concurrent) claim updates nothing and probes to tell 404 from 409.
	tag, err := s.pool.Exec(ctx,
		`UPDATE device_pairing_offers SET claimed_by=$2, claimed_at=$3
		 WHERE id=$1 AND claimed_at IS NULL`,
		offerID, userID, at)
	if err != nil {
		return fmt.Errorf("claim device pairing offer: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var claimedAt *time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT claimed_at FROM device_pairing_offers WHERE id=$1`, offerID).Scan(&claimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("claim device pairing offer: probe: %w", err)
	}
	return ErrAlreadyExists
}
