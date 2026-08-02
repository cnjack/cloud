package store

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

const artifactShareMaxUploadGenerations = 3

func cloneArtifactShare(in domain.ArtifactShare) domain.ArtifactShare {
	out := in
	if in.EncryptedMetadata != nil {
		metadata := *in.EncryptedMetadata
		out.EncryptedMetadata = &metadata
	}
	return out
}

func artifactShareActiveAt(share domain.ArtifactShare, at time.Time) bool {
	if share.RevokedAt != nil || share.State == domain.ArtifactShareRevoked {
		return false
	}
	if share.State == domain.ArtifactShareComplete {
		return share.ExpiresAt.After(at)
	}
	return share.IntentExpiresAt.After(at)
}

func (m *MemStore) CreateArtifactShare(_ context.Context, share *domain.ArtifactShare, maxCount int, maxBytes int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.artifactShares[share.ID]; exists {
		return ErrAlreadyExists
	}
	var count int
	var bytes int64
	for _, existing := range m.artifactShares {
		if existing.UserID == share.UserID && artifactShareActiveAt(existing, share.CreatedAt) {
			count++
			bytes += existing.CiphertextSize
		}
	}
	if count >= maxCount || bytes+share.CiphertextSize > maxBytes {
		return ErrArtifactShareQuotaExceeded
	}
	if share.State == "" {
		share.State = domain.ArtifactSharePending
	}
	m.artifactShares[share.ID] = cloneArtifactShare(*share)
	return nil
}

func (m *MemStore) ClaimArtifactShareUpload(_ context.Context, id, userID, claimID string, at, leaseExpiresAt time.Time) (*domain.ArtifactShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	share, ok := m.artifactShares[id]
	if !ok {
		return nil, ErrNotFound
	}
	claimable := share.State == domain.ArtifactSharePending ||
		(share.State == domain.ArtifactShareUploading && share.UploadLeaseExpiresAt != nil && !share.UploadLeaseExpiresAt.After(at))
	if share.UserID != userID || !claimable || !share.IntentExpiresAt.After(at) || share.UploadGeneration >= artifactShareMaxUploadGenerations {
		return nil, ErrConflict
	}
	share.State = domain.ArtifactShareUploading
	share.UploadGeneration++
	share.UploadClaimID = claimID
	share.ObjectKey = "artifact-shares/" + share.ID + "/" + strconv.Itoa(share.UploadGeneration)
	share.UploadClaimedAt = timePtr(at)
	share.UploadLeaseExpiresAt = timePtr(leaseExpiresAt)
	m.artifactShares[id] = share
	out := cloneArtifactShare(share)
	return &out, nil
}

func (m *MemStore) MarkArtifactShareUploaded(_ context.Context, id, userID, claimID string, generation int, digest string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	share, ok := m.artifactShares[id]
	if !ok {
		return ErrNotFound
	}
	if share.UserID != userID || share.State != domain.ArtifactShareUploading || share.UploadClaimID != claimID || share.UploadGeneration != generation || share.UploadLeaseExpiresAt == nil || !share.UploadLeaseExpiresAt.After(at) || !share.IntentExpiresAt.After(at) {
		return ErrConflict
	}
	share.State = domain.ArtifactShareUploaded
	share.CiphertextSHA256 = digest
	share.UploadedAt = timePtr(at)
	m.artifactShares[id] = share
	return nil
}

func (m *MemStore) ReleaseArtifactShareUpload(_ context.Context, id, userID, claimID string, generation int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	share, ok := m.artifactShares[id]
	if !ok || share.UserID != userID || share.State != domain.ArtifactShareUploading || share.UploadClaimID != claimID || share.UploadGeneration != generation {
		return false
	}
	share.State = domain.ArtifactSharePending
	share.UploadClaimID = ""
	share.UploadClaimedAt = nil
	share.UploadLeaseExpiresAt = nil
	m.artifactShares[id] = share
	return true
}

func (m *MemStore) CompleteArtifactShare(_ context.Context, id, userID, digest string, metadata domain.EncryptedArtifactMetadata, at time.Time) (*domain.ArtifactShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	share, ok := m.artifactShares[id]
	if !ok {
		return nil, ErrNotFound
	}
	if share.UserID != userID {
		return nil, ErrNotFound
	}
	if share.State == domain.ArtifactShareComplete {
		if share.CiphertextSHA256 == digest && share.EncryptedMetadata != nil && *share.EncryptedMetadata == metadata {
			out := cloneArtifactShare(share)
			return &out, nil
		}
		return nil, ErrConflict
	}
	if share.State != domain.ArtifactShareUploaded || share.CiphertextSHA256 != digest || !share.IntentExpiresAt.After(at) {
		return nil, ErrConflict
	}
	share.State = domain.ArtifactShareComplete
	share.EncryptedMetadata = &metadata
	share.CompletedAt = timePtr(at)
	m.artifactShares[id] = share
	out := cloneArtifactShare(share)
	return &out, nil
}

func (m *MemStore) ListArtifactSharesForUser(_ context.Context, userID, artifactID string) ([]domain.ArtifactShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.ArtifactShare, 0)
	for _, share := range m.artifactShares {
		if share.UserID == userID && (artifactID == "" || share.ArtifactID == artifactID) {
			out = append(out, cloneArtifactShare(share))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (m *MemStore) GetPublicArtifactShare(_ context.Context, id string, at time.Time) (*domain.ArtifactShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	share, ok := m.artifactShares[id]
	if !ok || share.State != domain.ArtifactShareComplete || share.RevokedAt != nil || !share.ExpiresAt.After(at) {
		return nil, ErrNotFound
	}
	out := cloneArtifactShare(share)
	return &out, nil
}

func (m *MemStore) RevokeArtifactShare(_ context.Context, id, userID string, at time.Time) (*domain.ArtifactShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	share, ok := m.artifactShares[id]
	if !ok || share.UserID != userID {
		return nil, ErrNotFound
	}
	if share.RevokedAt == nil {
		share.State = domain.ArtifactShareRevoked
		share.RevokedAt = timePtr(at)
		m.artifactShares[id] = share
	}
	out := cloneArtifactShare(share)
	return &out, nil
}

func (m *MemStore) ListArtifactSharesForGC(_ context.Context, before time.Time, limit int) ([]domain.ArtifactShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]domain.ArtifactShare, 0, limit)
	for _, share := range m.artifactShares {
		if share.ObjectDeletedAt != nil {
			continue
		}
		expiredIntent := share.State != domain.ArtifactShareComplete && !share.IntentExpiresAt.After(before)
		expiredShare := share.State == domain.ArtifactShareComplete && !share.ExpiresAt.After(before)
		if share.RevokedAt != nil || expiredIntent || expiredShare {
			out = append(out, cloneArtifactShare(share))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) MarkArtifactShareObjectsDeleted(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	share, ok := m.artifactShares[id]
	if !ok {
		return ErrNotFound
	}
	if share.ObjectDeletedAt == nil {
		share.ObjectDeletedAt = timePtr(at)
		m.artifactShares[id] = share
	}
	return nil
}

func timePtr(t time.Time) *time.Time { return &t }
