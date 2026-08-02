package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cnjack/jcloud/internal/domain"
)

const artifactShareCols = `id,user_id,COALESCE(device_id,''),artifact_id,revision,protocol,state,
	COALESCE(object_key,''),upload_generation,COALESCE(upload_claim_id,''),ciphertext_size,COALESCE(ciphertext_sha256,''),
	encrypted_metadata,intent_expires_at,upload_claimed_at,upload_lease_expires_at,expires_at,uploaded_at,completed_at,
	revoked_at,object_deleted_at,created_at`

func scanArtifactShare(row pgx.Row) (*domain.ArtifactShare, error) {
	var share domain.ArtifactShare
	var metadata []byte
	err := row.Scan(
		&share.ID, &share.UserID, &share.DeviceID, &share.ArtifactID, &share.Revision, &share.Protocol, &share.State,
		&share.ObjectKey, &share.UploadGeneration, &share.UploadClaimID, &share.CiphertextSize, &share.CiphertextSHA256,
		&metadata, &share.IntentExpiresAt, &share.UploadClaimedAt, &share.UploadLeaseExpiresAt, &share.ExpiresAt,
		&share.UploadedAt, &share.CompletedAt, &share.RevokedAt, &share.ObjectDeletedAt, &share.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan artifact share: %w", err)
	}
	if len(metadata) > 0 {
		var envelope domain.EncryptedArtifactMetadata
		if err := json.Unmarshal(metadata, &envelope); err != nil {
			return nil, fmt.Errorf("scan artifact share metadata: %w", err)
		}
		share.EncryptedMetadata = &envelope
	}
	return &share, nil
}

func (s *PGStore) CreateArtifactShare(ctx context.Context, share *domain.ArtifactShare, maxCount int, maxBytes int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create artifact share: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('artifact-share:' || $1, 0))`, share.UserID); err != nil {
		return fmt.Errorf("create artifact share: quota lock: %w", err)
	}
	var count int
	var bytes int64
	if err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(ciphertext_size),0)
		FROM device_artifact_shares
		WHERE user_id=$1 AND revoked_at IS NULL AND
		((state='complete' AND expires_at>$2) OR (state<>'complete' AND intent_expires_at>$2))`,
		share.UserID, share.CreatedAt).Scan(&count, &bytes); err != nil {
		return fmt.Errorf("create artifact share: quota read: %w", err)
	}
	if count >= maxCount || bytes+share.CiphertextSize > maxBytes {
		return ErrArtifactShareQuotaExceeded
	}
	_, err = tx.Exec(ctx, `INSERT INTO device_artifact_shares(
		id,user_id,device_id,artifact_id,revision,protocol,state,ciphertext_size,intent_expires_at,expires_at,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, share.ID, share.UserID, nullStr(share.DeviceID),
		share.ArtifactID, share.Revision, share.Protocol, share.State, share.CiphertextSize,
		share.IntentExpiresAt, share.ExpiresAt, share.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create artifact share: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create artifact share: commit: %w", err)
	}
	return nil
}

func (s *PGStore) ClaimArtifactShareUpload(ctx context.Context, id, userID, claimID string, at, leaseExpiresAt time.Time) (*domain.ArtifactShare, error) {
	return scanArtifactShare(s.pool.QueryRow(ctx, `UPDATE device_artifact_shares SET
		state='uploading', upload_generation=upload_generation+1, upload_claim_id=$3,
		object_key='artifact-shares/' || id || '/' || (upload_generation+1)::text,
		upload_claimed_at=$4, upload_lease_expires_at=$5
		WHERE id=$1 AND user_id=$2 AND intent_expires_at>$4 AND upload_generation<3 AND
		(state='pending' OR (state='uploading' AND upload_lease_expires_at<=$4))
		RETURNING `+artifactShareCols, id, userID, claimID, at, leaseExpiresAt))
}

func (s *PGStore) MarkArtifactShareUploaded(ctx context.Context, id, userID, claimID string, generation int, digest string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE device_artifact_shares SET state='uploaded',ciphertext_sha256=$5,uploaded_at=$6
		WHERE id=$1 AND user_id=$2 AND state='uploading' AND upload_claim_id=$3 AND upload_generation=$4
		AND upload_lease_expires_at>$6 AND intent_expires_at>$6`, id, userID, claimID, generation, digest, at)
	if err != nil {
		return fmt.Errorf("mark artifact share uploaded: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s *PGStore) ReleaseArtifactShareUpload(ctx context.Context, id, userID, claimID string, generation int) bool {
	tag, err := s.pool.Exec(ctx, `UPDATE device_artifact_shares SET state='pending',upload_claim_id=NULL,
		upload_claimed_at=NULL,upload_lease_expires_at=NULL
		WHERE id=$1 AND user_id=$2 AND state='uploading' AND upload_claim_id=$3 AND upload_generation=$4`,
		id, userID, claimID, generation)
	return err == nil && tag.RowsAffected() == 1
}

func (s *PGStore) CompleteArtifactShare(ctx context.Context, id, userID, digest string, metadata domain.EncryptedArtifactMetadata, at time.Time) (*domain.ArtifactShare, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("complete artifact share metadata: %w", err)
	}
	share, err := scanArtifactShare(s.pool.QueryRow(ctx, `UPDATE device_artifact_shares SET
		state='complete',encrypted_metadata=$4,completed_at=$5
		WHERE id=$1 AND user_id=$2 AND state='uploaded' AND ciphertext_sha256=$3 AND intent_expires_at>$5
		RETURNING `+artifactShareCols, id, userID, digest, encoded, at))
	if err == nil {
		return share, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	existing, err := s.getArtifactShareForUser(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if existing.State == domain.ArtifactShareComplete && existing.CiphertextSHA256 == digest && existing.EncryptedMetadata != nil && *existing.EncryptedMetadata == metadata {
		return existing, nil
	}
	return nil, ErrConflict
}

func (s *PGStore) getArtifactShareForUser(ctx context.Context, id, userID string) (*domain.ArtifactShare, error) {
	return scanArtifactShare(s.pool.QueryRow(ctx, `SELECT `+artifactShareCols+` FROM device_artifact_shares WHERE id=$1 AND user_id=$2`, id, userID))
}

func (s *PGStore) ListArtifactSharesForUser(ctx context.Context, userID, artifactID string) ([]domain.ArtifactShare, error) {
	query := `SELECT ` + artifactShareCols + ` FROM device_artifact_shares WHERE user_id=$1`
	args := []any{userID}
	if artifactID != "" {
		query += ` AND artifact_id=$2`
		args = append(args, artifactID)
	}
	query += ` ORDER BY created_at DESC,id`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list artifact shares: %w", err)
	}
	defer rows.Close()
	var out []domain.ArtifactShare
	for rows.Next() {
		share, err := scanArtifactShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *share)
	}
	return out, rows.Err()
}

func (s *PGStore) GetPublicArtifactShare(ctx context.Context, id string, at time.Time) (*domain.ArtifactShare, error) {
	return scanArtifactShare(s.pool.QueryRow(ctx, `SELECT `+artifactShareCols+` FROM device_artifact_shares
		WHERE id=$1 AND state='complete' AND revoked_at IS NULL AND expires_at>$2`, id, at))
}

func (s *PGStore) RevokeArtifactShare(ctx context.Context, id, userID string, at time.Time) (*domain.ArtifactShare, error) {
	return scanArtifactShare(s.pool.QueryRow(ctx, `UPDATE device_artifact_shares SET
		state='revoked',revoked_at=COALESCE(revoked_at,$3)
		WHERE id=$1 AND user_id=$2 RETURNING `+artifactShareCols, id, userID, at))
}

func (s *PGStore) ListArtifactSharesForGC(ctx context.Context, before time.Time, limit int) ([]domain.ArtifactShare, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+artifactShareCols+` FROM device_artifact_shares
		WHERE object_deleted_at IS NULL AND (revoked_at IS NOT NULL OR
		(state='complete' AND expires_at<=$1) OR (state<>'complete' AND intent_expires_at<=$1))
		ORDER BY created_at,id LIMIT $2`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list artifact shares for gc: %w", err)
	}
	defer rows.Close()
	var out []domain.ArtifactShare
	for rows.Next() {
		share, err := scanArtifactShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *share)
	}
	return out, rows.Err()
}

func (s *PGStore) MarkArtifactShareObjectsDeleted(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE device_artifact_shares SET object_deleted_at=COALESCE(object_deleted_at,$2) WHERE id=$1`, id, at)
	if err != nil {
		return fmt.Errorf("mark artifact share objects deleted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
