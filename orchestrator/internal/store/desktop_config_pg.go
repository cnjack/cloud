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

func scanAccountSyncKey(row pgx.Row) (*domain.AccountSyncKey, error) {
	var v domain.AccountSyncKey
	if err := row.Scan(&v.UserID, &v.KeyGen, &v.CreatedAt, &v.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan account sync key: %w", err)
	}
	return &v, nil
}

func scanAccountSyncKeyWrap(row pgx.Row) (*domain.AccountSyncKeyWrap, error) {
	var v domain.AccountSyncKeyWrap
	if err := row.Scan(
		&v.UserID, &v.DeviceID, &v.DeviceName, &v.Pubkey, &v.KeyGen,
		&v.Status, &v.Wrap, &v.ApprovedByDeviceID, &v.CreatedAt, &v.ResolvedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan account sync key wrap: %w", err)
	}
	return &v, nil
}

const accountSyncWrapCols = `
	w.user_id, w.device_id, d.name, d.pubkey, w.key_gen, w.status, w.wrap,
	COALESCE(w.approved_by_device_id,''), w.created_at, w.resolved_at`

func (s *PGStore) GetAccountSyncKey(ctx context.Context, userID string) (*domain.AccountSyncKey, error) {
	return scanAccountSyncKey(s.pool.QueryRow(ctx,
		`SELECT user_id, key_gen, created_at, updated_at FROM account_sync_keys WHERE user_id=$1`, userID))
}

func (s *PGStore) InitializeAccountSyncKey(ctx context.Context, userID, deviceID string, keyGen int, wrap []byte, at time.Time) (*domain.AccountSyncKeyWrap, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize account sync key: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var pubkey string
	if err := tx.QueryRow(ctx,
		`SELECT pubkey FROM devices WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL FOR UPDATE`,
		deviceID, userID).Scan(&pubkey); errors.Is(err, pgx.ErrNoRows) || pubkey == "" {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("initialize account sync key: device: %w", err)
	}
	now := at.UTC()
	tag, err := tx.Exec(ctx,
		`INSERT INTO account_sync_keys (user_id,key_gen,created_at,updated_at)
		 VALUES ($1,$2,$3,$3) ON CONFLICT (user_id) DO NOTHING`,
		userID, keyGen, now)
	if err != nil {
		return nil, fmt.Errorf("initialize account sync key: key: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrConflict
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO account_sync_key_wraps
		 (user_id,device_id,key_gen,status,wrap,approved_by_device_id,created_at,resolved_at)
		 VALUES ($1,$2,$3,'approved',$4,$2,$5,$5)`,
		userID, deviceID, keyGen, wrap, now); err != nil {
		return nil, fmt.Errorf("initialize account sync key: wrap: %w", err)
	}
	v, err := scanAccountSyncKeyWrap(tx.QueryRow(ctx,
		`SELECT `+accountSyncWrapCols+`
		 FROM account_sync_key_wraps w JOIN devices d ON d.id=w.device_id
		 WHERE w.user_id=$1 AND w.device_id=$2`, userID, deviceID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("initialize account sync key: commit: %w", err)
	}
	return v, nil
}

func (s *PGStore) GetAccountSyncKeyWrap(ctx context.Context, userID, deviceID string) (*domain.AccountSyncKeyWrap, error) {
	return scanAccountSyncKeyWrap(s.pool.QueryRow(ctx,
		`SELECT `+accountSyncWrapCols+`
		 FROM account_sync_key_wraps w JOIN devices d ON d.id=w.device_id
		 WHERE w.user_id=$1 AND w.device_id=$2 AND d.revoked_at IS NULL`,
		userID, deviceID))
}

func (s *PGStore) RequestAccountSyncKey(ctx context.Context, userID, deviceID string, at time.Time) (*domain.AccountSyncKeyWrap, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("request account sync key: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var keyGen int
	if err := tx.QueryRow(ctx,
		`SELECT key_gen FROM account_sync_keys WHERE user_id=$1 FOR UPDATE`, userID).Scan(&keyGen); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("request account sync key: key: %w", err)
	}
	var pubkey string
	if err := tx.QueryRow(ctx,
		`SELECT pubkey FROM devices WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`,
		deviceID, userID).Scan(&pubkey); errors.Is(err, pgx.ErrNoRows) || pubkey == "" {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("request account sync key: device: %w", err)
	}
	now := at.UTC()
	_, err = tx.Exec(ctx,
		`INSERT INTO account_sync_key_wraps
		 (user_id,device_id,key_gen,status,wrap,approved_by_device_id,created_at,resolved_at)
		 VALUES ($1,$2,$3,'pending',NULL,NULL,$4,NULL)
		 ON CONFLICT (user_id,device_id) DO UPDATE SET
		   key_gen=EXCLUDED.key_gen, status='pending', wrap=NULL,
		   approved_by_device_id=NULL, created_at=EXCLUDED.created_at, resolved_at=NULL
		 WHERE account_sync_key_wraps.status <> 'approved'
		    OR account_sync_key_wraps.key_gen <> EXCLUDED.key_gen`,
		userID, deviceID, keyGen, now)
	if err != nil {
		return nil, fmt.Errorf("request account sync key: write: %w", err)
	}
	v, err := scanAccountSyncKeyWrap(tx.QueryRow(ctx,
		`SELECT `+accountSyncWrapCols+`
		 FROM account_sync_key_wraps w JOIN devices d ON d.id=w.device_id
		 WHERE w.user_id=$1 AND w.device_id=$2`, userID, deviceID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("request account sync key: commit: %w", err)
	}
	return v, nil
}

func (s *PGStore) ListAccountSyncKeyWraps(ctx context.Context, userID string, status domain.AccountSyncKeyStatus) ([]domain.AccountSyncKeyWrap, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+accountSyncWrapCols+`
		 FROM account_sync_key_wraps w JOIN devices d ON d.id=w.device_id
		 WHERE w.user_id=$1 AND ($2='' OR w.status=$2) AND d.revoked_at IS NULL
		 ORDER BY w.created_at`, userID, string(status))
	if err != nil {
		return nil, fmt.Errorf("list account sync key wraps: %w", err)
	}
	defer rows.Close()
	var out []domain.AccountSyncKeyWrap
	for rows.Next() {
		v, err := scanAccountSyncKeyWrap(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (s *PGStore) RespondAccountSyncKeyRequest(ctx context.Context, userID, approverDeviceID, targetDeviceID string, approve bool, keyGen int, wrap []byte, at time.Time) (*domain.AccountSyncKeyWrap, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("respond account sync key: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentGen int
	if err := tx.QueryRow(ctx,
		`SELECT key_gen FROM account_sync_keys WHERE user_id=$1 FOR UPDATE`, userID).Scan(&currentGen); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("respond account sync key: key: %w", err)
	}
	if currentGen != keyGen {
		return nil, ErrConflict
	}
	var approved bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(
		   SELECT 1 FROM account_sync_key_wraps
		   WHERE user_id=$1 AND device_id=$2 AND key_gen=$3 AND status='approved'
		 )`, userID, approverDeviceID, keyGen).Scan(&approved); err != nil {
		return nil, fmt.Errorf("respond account sync key: approver: %w", err)
	}
	if !approved {
		return nil, ErrConflict
	}
	status := domain.AccountSyncKeyDenied
	var storedWrap []byte
	if approve {
		status = domain.AccountSyncKeyApproved
		storedWrap = wrap
	}
	tag, err := tx.Exec(ctx,
		`UPDATE account_sync_key_wraps SET
		   status=$4, wrap=$5, approved_by_device_id=$3, resolved_at=$6
		 WHERE user_id=$1 AND device_id=$2 AND key_gen=$7 AND status='pending'`,
		userID, targetDeviceID, approverDeviceID, string(status), storedWrap, at.UTC(), keyGen)
	if err != nil {
		return nil, fmt.Errorf("respond account sync key: write: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrConflict
	}
	v, err := scanAccountSyncKeyWrap(tx.QueryRow(ctx,
		`SELECT `+accountSyncWrapCols+`
		 FROM account_sync_key_wraps w JOIN devices d ON d.id=w.device_id
		 WHERE w.user_id=$1 AND w.device_id=$2`, userID, targetDeviceID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("respond account sync key: commit: %w", err)
	}
	return v, nil
}

func (s *PGStore) RevokeAccountSyncKeyWrap(ctx context.Context, userID, approverDeviceID, targetDeviceID string, keyGen int, at time.Time) (*domain.AccountSyncKeyWrap, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("revoke account sync key: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentGen int
	if err := tx.QueryRow(ctx,
		`SELECT key_gen FROM account_sync_keys WHERE user_id=$1 FOR UPDATE`, userID).Scan(&currentGen); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("revoke account sync key: key: %w", err)
	}
	if currentGen != keyGen {
		return nil, ErrConflict
	}
	var approved bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(
		   SELECT 1 FROM account_sync_key_wraps
		   WHERE user_id=$1 AND device_id=$2 AND key_gen=$3 AND status='approved'
		 )`, userID, approverDeviceID, keyGen).Scan(&approved); err != nil {
		return nil, fmt.Errorf("revoke account sync key: approver: %w", err)
	}
	if !approved {
		return nil, ErrConflict
	}
	tag, err := tx.Exec(ctx,
		`UPDATE account_sync_key_wraps SET
		   status='denied', wrap=NULL, approved_by_device_id=$3, resolved_at=$4
		 WHERE user_id=$1 AND device_id=$2 AND key_gen=$5 AND status='approved'`,
		userID, targetDeviceID, approverDeviceID, at.UTC(), keyGen)
	if err != nil {
		return nil, fmt.Errorf("revoke account sync key: write: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrConflict
	}
	v, err := scanAccountSyncKeyWrap(tx.QueryRow(ctx,
		`SELECT `+accountSyncWrapCols+`
		 FROM account_sync_key_wraps w JOIN devices d ON d.id=w.device_id
		 WHERE w.user_id=$1 AND w.device_id=$2`, userID, targetDeviceID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("revoke account sync key: commit: %w", err)
	}
	return v, nil
}

func scanAccountProviderConfig(row pgx.Row) (*domain.AccountProviderConfig, error) {
	var v domain.AccountProviderConfig
	if err := row.Scan(&v.UserID, &v.ProviderID, &v.Version, &v.Envelope, &v.Deleted, &v.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan account provider config: %w", err)
	}
	return &v, nil
}

func (s *PGStore) ListAccountProviderConfigs(ctx context.Context, userID string) ([]domain.AccountProviderConfig, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id,provider_id,version,envelope,deleted,updated_at
		 FROM account_provider_configs WHERE user_id=$1 ORDER BY updated_at,provider_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list account provider configs: %w", err)
	}
	defer rows.Close()
	var out []domain.AccountProviderConfig
	for rows.Next() {
		v, err := scanAccountProviderConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (s *PGStore) GetAccountProviderConfig(ctx context.Context, userID, providerID string) (*domain.AccountProviderConfig, error) {
	return scanAccountProviderConfig(s.pool.QueryRow(ctx,
		`SELECT user_id,provider_id,version,envelope,deleted,updated_at
		 FROM account_provider_configs WHERE user_id=$1 AND provider_id=$2`,
		userID, providerID))
}

func (s *PGStore) PutAccountProviderConfig(ctx context.Context, userID, providerID string, baseVersion int64, envelope []byte, deleted bool, at time.Time) (*domain.AccountProviderConfig, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("put account provider config: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current int64
	err = tx.QueryRow(ctx,
		`SELECT version FROM account_provider_configs
		 WHERE user_id=$1 AND provider_id=$2 FOR UPDATE`, userID, providerID).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows) && baseVersion == 0:
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx,
			`INSERT INTO account_provider_configs
			 (user_id,provider_id,version,envelope,deleted,updated_at)
			 VALUES ($1,$2,1,$3,$4,$5) ON CONFLICT (user_id,provider_id) DO NOTHING`,
			userID, providerID, envelope, deleted, at.UTC())
		if err == nil && tag.RowsAffected() != 1 {
			return nil, ErrConflict
		}
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrConflict
	case err != nil:
		return nil, fmt.Errorf("put account provider config: lock: %w", err)
	case current != baseVersion:
		return nil, ErrConflict
	default:
		_, err = tx.Exec(ctx,
			`UPDATE account_provider_configs SET
			   version=$3,envelope=$4,deleted=$5,updated_at=$6
			 WHERE user_id=$1 AND provider_id=$2`,
			userID, providerID, baseVersion+1, envelope, deleted, at.UTC())
	}
	if err != nil {
		return nil, fmt.Errorf("put account provider config: write: %w", err)
	}
	v, err := scanAccountProviderConfig(tx.QueryRow(ctx,
		`SELECT user_id,provider_id,version,envelope,deleted,updated_at
		 FROM account_provider_configs WHERE user_id=$1 AND provider_id=$2`,
		userID, providerID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("put account provider config: commit: %w", err)
	}
	return v, nil
}
