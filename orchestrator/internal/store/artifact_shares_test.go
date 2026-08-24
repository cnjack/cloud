package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestMemArtifactShareLeaseTakeoverKeepsObjectAndDigestGenerationBound(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	share := &domain.ArtifactShare{
		ID: "share-1", UserID: "user-1", DeviceID: "device-1",
		ArtifactID: "artifact-1", Revision: 3, Protocol: domain.ArtifactShareProtocolV1,
		State: domain.ArtifactSharePending, CiphertextSize: 128,
		IntentExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(7 * 24 * time.Hour), CreatedAt: now,
	}
	if err := st.CreateArtifactShare(ctx, share, 100, 1<<30); err != nil {
		t.Fatal(err)
	}

	first, err := st.ClaimArtifactShareUpload(ctx, share.ID, share.UserID, "claim-1", now, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.UploadGeneration != 1 || !strings.HasSuffix(first.ObjectKey, "/1") {
		t.Fatalf("first claim=%+v", first)
	}
	if _, err := st.ClaimArtifactShareUpload(ctx, share.ID, share.UserID, "claim-early", now.Add(time.Minute), now.Add(6*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("active lease claim err=%v want conflict", err)
	}

	second, err := st.ClaimArtifactShareUpload(ctx, share.ID, share.UserID, "claim-2", now.Add(6*time.Minute), now.Add(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.UploadGeneration != 2 || second.ObjectKey == first.ObjectKey || !strings.HasSuffix(second.ObjectKey, "/2") {
		t.Fatalf("takeover=%+v first_key=%q", second, first.ObjectKey)
	}
	if err := st.MarkArtifactShareUploaded(ctx, share.ID, share.UserID, "claim-1", 1, "old-digest", now.Add(7*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("late old upload err=%v want conflict", err)
	}
	if err := st.MarkArtifactShareUploaded(ctx, share.ID, share.UserID, "claim-2", 2, "new-digest", now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}

	metadata := domain.EncryptedArtifactMetadata{Nonce: "nonce", Ciphertext: "ciphertext", PlaintextLength: 32}
	if _, err := st.CompleteArtifactShare(ctx, share.ID, share.UserID, "old-digest", metadata, now.Add(8*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong digest complete err=%v want conflict", err)
	}
	completed, err := st.CompleteArtifactShare(ctx, share.ID, share.UserID, "new-digest", metadata, now.Add(8*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != domain.ArtifactShareComplete || completed.ObjectKey != second.ObjectKey || completed.CiphertextSHA256 != "new-digest" {
		t.Fatalf("completed=%+v", completed)
	}
	public, err := st.GetPublicArtifactShare(ctx, share.ID, now.Add(9*time.Minute))
	if err != nil || public.State != domain.ArtifactShareComplete {
		t.Fatalf("public=%+v err=%v", public, err)
	}
}

func TestMemArtifactShareRevokeMakesPublicReadDisappearAndQueuesEveryGenerationForGC(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	share := &domain.ArtifactShare{
		ID: "share-revoke", UserID: "user-1", DeviceID: "device-1",
		ArtifactID: "artifact-1", Revision: 1, Protocol: domain.ArtifactShareProtocolV1,
		State: domain.ArtifactSharePending, CiphertextSize: 64,
		IntentExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now,
	}
	if err := st.CreateArtifactShare(ctx, share, 100, 1<<30); err != nil {
		t.Fatal(err)
	}
	first, err := st.ClaimArtifactShareUpload(ctx, share.ID, share.UserID, "claim-1", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.ClaimArtifactShareUpload(ctx, share.ID, share.UserID, "claim-2", now.Add(2*time.Minute), now.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkArtifactShareUploaded(ctx, share.ID, share.UserID, "claim-2", second.UploadGeneration, "digest", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	metadata := domain.EncryptedArtifactMetadata{Nonce: "nonce", Ciphertext: "ciphertext", PlaintextLength: 4}
	if _, err := st.CompleteArtifactShare(ctx, share.ID, share.UserID, "digest", metadata, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RevokeArtifactShare(ctx, share.ID, share.UserID, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RevokeArtifactShare(ctx, share.ID, share.UserID, now.Add(6*time.Minute)); err != nil {
		t.Fatalf("repeat revoke must be idempotent: %v", err)
	}
	if _, err := st.GetPublicArtifactShare(ctx, share.ID, now.Add(6*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked public read err=%v want not found", err)
	}
	candidates, err := st.ListArtifactSharesForGC(ctx, now.Add(6*time.Minute), 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("gc candidates=%+v err=%v", candidates, err)
	}
	keys := candidates[0].ObjectKeys()
	if len(keys) != 2 || keys[0] != first.ObjectKey || keys[1] != second.ObjectKey {
		t.Fatalf("generation keys=%v", keys)
	}
	if err := st.MarkArtifactShareObjectsDeleted(ctx, share.ID, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	candidates, err = st.ListArtifactSharesForGC(ctx, now.Add(8*time.Minute), 10)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("deleted candidates=%+v err=%v", candidates, err)
	}
}

func TestMemArtifactShareQuotaCountsOnlyActiveReservations(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	newShare := func(id string, size int64, intentExpiry time.Time) *domain.ArtifactShare {
		return &domain.ArtifactShare{
			ID: id, UserID: "quota-user", ArtifactID: "artifact-" + id, Revision: 1,
			Protocol: domain.ArtifactShareProtocolV1, State: domain.ArtifactSharePending,
			CiphertextSize: size, IntentExpiresAt: intentExpiry, ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now,
		}
	}
	if err := st.CreateArtifactShare(ctx, newShare("active", 60, now.Add(time.Hour)), 2, 100); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateArtifactShare(ctx, newShare("bytes-over", 41, now.Add(time.Hour)), 2, 100); !errors.Is(err, ErrArtifactShareQuotaExceeded) {
		t.Fatalf("bytes quota err=%v", err)
	}
	if _, err := st.RevokeArtifactShare(ctx, "active", "quota-user", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateArtifactShare(ctx, newShare("after-revoke", 100, now.Add(time.Hour)), 2, 100); err != nil {
		t.Fatalf("revoked reservation should release quota: %v", err)
	}
	if err := st.CreateArtifactShare(ctx, newShare("count-over", 1, now.Add(time.Hour)), 1, 1000); !errors.Is(err, ErrArtifactShareQuotaExceeded) {
		t.Fatalf("count quota err=%v", err)
	}
	expiredStore := NewMemStore()
	if err := expiredStore.CreateArtifactShare(ctx, newShare("already-expired", 1000, now.Add(-time.Minute)), 10, 2000); err != nil {
		t.Fatal(err)
	}
	if err := expiredStore.CreateArtifactShare(ctx, newShare("after-expiry", 100, now.Add(time.Hour)), 10, 100); err != nil {
		t.Fatalf("an expired intent must not reserve active bytes: %v", err)
	}
}

func TestMemArtifactShareUploadRetryIsBoundedToThreeGenerations(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	share := &domain.ArtifactShare{
		ID: "share-retries", UserID: "user-1", ArtifactID: "artifact-1", Revision: 1,
		Protocol: domain.ArtifactShareProtocolV1, State: domain.ArtifactSharePending, CiphertextSize: 64,
		IntentExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now,
	}
	if err := st.CreateArtifactShare(ctx, share, 100, 1<<30); err != nil {
		t.Fatal(err)
	}
	for generation := 1; generation <= 3; generation++ {
		at := now.Add(time.Duration(generation-1) * 2 * time.Minute)
		claimed, err := st.ClaimArtifactShareUpload(ctx, share.ID, share.UserID, "claim-"+string(rune('0'+generation)), at, at.Add(time.Minute))
		if err != nil || claimed.UploadGeneration != generation {
			t.Fatalf("generation=%d claimed=%+v err=%v", generation, claimed, err)
		}
	}
	if _, err := st.ClaimArtifactShareUpload(ctx, share.ID, share.UserID, "claim-4", now.Add(7*time.Minute), now.Add(8*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("fourth generation err=%v want conflict", err)
	}
}

func TestPGArtifactShareLifecycleMatchesMemoryStore(t *testing.T) {
	ctx := context.Background()
	st, _ := pgTestStore(t)
	now := time.Now().UTC()
	user := &domain.User{ID: domain.NewID(), DisplayName: "Artifact Share Test", CreatedAt: now}
	identity := &domain.UserIdentity{
		ID: domain.NewID(), Provider: domain.ProviderGitHub,
		ProviderUID: "artifact-share-" + user.ID, AccessTokenEnc: []byte("test-ciphertext"), CreatedAt: now,
	}
	if _, err := st.CreateUserWithIdentity(ctx, user, identity); err != nil {
		t.Fatal(err)
	}
	device := &domain.Device{ID: domain.NewID(), UserID: user.ID, Name: "test", KeyGen: 1, CreatedAt: now}
	if err := st.CreateDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM device_artifact_shares WHERE user_id=$1`, user.ID)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, user.ID)
	})

	share := &domain.ArtifactShare{
		ID: domain.NewID(), UserID: user.ID, DeviceID: device.ID,
		ArtifactID: "artifact-pg", Revision: 2, Protocol: domain.ArtifactShareProtocolV1,
		State: domain.ArtifactSharePending, CiphertextSize: 64,
		IntentExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now,
	}
	if err := st.CreateArtifactShare(ctx, share, 100, 1<<30); err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimArtifactShareUpload(ctx, share.ID, user.ID, "claim-pg", now, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkArtifactShareUploaded(ctx, share.ID, user.ID, "claim-pg", claim.UploadGeneration, "digest-pg", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	metadata := domain.EncryptedArtifactMetadata{Nonce: "nonce", Ciphertext: "ciphertext", PlaintextLength: 7}
	completed, err := st.CompleteArtifactShare(ctx, share.ID, user.ID, "digest-pg", metadata, now.Add(2*time.Minute))
	if err != nil || completed.State != domain.ArtifactShareComplete {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	public, err := st.GetPublicArtifactShare(ctx, share.ID, now.Add(3*time.Minute))
	if err != nil || public.EncryptedMetadata == nil || *public.EncryptedMetadata != metadata {
		t.Fatalf("public=%+v err=%v", public, err)
	}
}
