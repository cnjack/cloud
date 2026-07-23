package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestPGDesktopConfigMeshRoundTrip(t *testing.T) {
	st, _ := pgTestStore(t)
	ctx := context.Background()
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("second migration pass: %v", err)
	}
	now := time.Now().UTC()
	userID := domain.NewID()
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO users (id,display_name,created_at) VALUES ($1,$2,$3)`,
		userID, "config-mesh", now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })

	first := &domain.Device{
		ID: domain.NewID(), UserID: userID, Name: "Desktop A",
		Pubkey: "pub-a", KeyGen: 1, CreatedAt: now,
	}
	second := &domain.Device{
		ID: domain.NewID(), UserID: userID, Name: "Desktop B",
		Pubkey: "pub-b", KeyGen: 1, CreatedAt: now,
	}
	if err := st.CreateDevice(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDevice(ctx, second); err != nil {
		t.Fatal(err)
	}

	if _, err := st.GetAccountSyncKey(ctx, userID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fresh sync key err=%v want ErrNotFound", err)
	}
	initialized, err := st.InitializeAccountSyncKey(
		ctx, userID, first.ID, 1, []byte(`{"wrap":"first"}`), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if initialized.Status != domain.AccountSyncKeyApproved || initialized.DeviceName != first.Name {
		t.Fatalf("initialized wrap=%+v", initialized)
	}
	if _, err := st.InitializeAccountSyncKey(
		ctx, userID, second.ID, 1, []byte(`{}`), now,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("second initialize err=%v want ErrConflict", err)
	}

	requested, err := st.RequestAccountSyncKey(ctx, userID, second.ID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if requested.Status != domain.AccountSyncKeyPending || requested.Pubkey != second.Pubkey {
		t.Fatalf("pending wrap=%+v", requested)
	}
	approved, err := st.RespondAccountSyncKeyRequest(
		ctx, userID, first.ID, second.ID, true, 1,
		[]byte(`{"wrap":"second"}`), now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != domain.AccountSyncKeyApproved || approved.ApprovedByDeviceID != first.ID {
		t.Fatalf("approved wrap=%+v", approved)
	}

	providerID := "zhipuai"
	saved, err := st.PutAccountProviderConfig(
		ctx, userID, providerID, 0, []byte(`{"encrypted":true}`), false, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.ProviderID != providerID {
		t.Fatalf("created provider=%+v", saved)
	}
	if _, err := st.PutAccountProviderConfig(
		ctx, userID, providerID, 0, []byte(`{"stale":true}`), false, now,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale provider write err=%v want ErrConflict", err)
	}
	deleted, err := st.PutAccountProviderConfig(
		ctx, userID, providerID, saved.Version, []byte(`{"deleted":true}`), true, now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Version != 2 || !deleted.Deleted {
		t.Fatalf("provider tombstone=%+v", deleted)
	}

	revoked, err := st.RevokeAccountSyncKeyWrap(
		ctx, userID, first.ID, second.ID, 1, now.Add(3*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != domain.AccountSyncKeyDenied || len(revoked.Wrap) != 0 {
		t.Fatalf("revoked wrap=%+v", revoked)
	}
	if _, err := st.GetAccountSyncKeyWrap(ctx, userID, second.ID); err != nil {
		t.Fatalf("revoked wrap remains queryable for status: %v", err)
	}
}
