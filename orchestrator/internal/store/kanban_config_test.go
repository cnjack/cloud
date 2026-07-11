package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// kanbanConfigStoreTests exercises the single-row cluster_kanban_config CRUD
// against ANY Store implementation, so memory and Postgres run the SAME
// assertions (the D27 memory/pg parity check). The store must start with no row.
func kanbanConfigStoreTests(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()

	// 1. Empty => ErrNotFound; delete on empty is a clean idempotent no-op.
	if _, err := st.GetClusterKanbanConfig(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty get = %v, want ErrNotFound", err)
	}
	if err := st.DeleteClusterKanbanConfig(ctx); err != nil {
		t.Fatalf("delete on empty = %v, want nil (idempotent)", err)
	}

	// 2. Roundtrip base_url / token_enc / token_expires_at / updated_by
	// (+ updated_at stamped). token_expires_at is set by the D28 device flow.
	tok := []byte{0x01, 0x02, 0xff}
	exp := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := st.UpsertClusterKanbanConfig(ctx,
		&domain.KanbanConfig{BaseURL: "http://jtype.one", TokenEnc: tok, TokenExpiresAt: &exp, UpdatedBy: "admin-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetClusterKanbanConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "http://jtype.one" || got.UpdatedBy != "admin-1" || !bytes.Equal(got.TokenEnc, tok) {
		t.Fatalf("roundtrip = %+v", got)
	}
	if got.TokenExpiresAt == nil || !got.TokenExpiresAt.Equal(exp) {
		t.Fatalf("token_expires_at did not roundtrip: %v want %v", got.TokenExpiresAt, exp)
	}
	if !got.TokenSet() {
		t.Fatal("TokenSet() should be true when a token is stored")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("updated_at should be stamped on upsert")
	}

	// 3. Singleton: a second upsert OVERWRITES the same row (never a second row).
	// A nil TokenExpiresAt clears the expiry back to NULL (unknown / manual paste).
	if err := st.UpsertClusterKanbanConfig(ctx,
		&domain.KanbanConfig{BaseURL: "http://jtype.two", TokenEnc: nil, TokenExpiresAt: nil, UpdatedBy: "admin-2"}); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetClusterKanbanConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "http://jtype.two" || got.UpdatedBy != "admin-2" {
		t.Fatalf("overwrite = %+v", got)
	}
	if got.TokenExpiresAt != nil {
		t.Fatalf("nil expiry must clear token_expires_at to NULL, got %v", got.TokenExpiresAt)
	}

	// 4. A nil token clears the fallback: TokenSet() == false.
	if got.TokenSet() || len(got.TokenEnc) != 0 {
		t.Fatalf("nil token => TokenSet false, got %+v", got)
	}

	// 5. Delete => ErrNotFound afterwards, and a repeat delete is idempotent.
	if err := st.DeleteClusterKanbanConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetClusterKanbanConfig(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete get = %v, want ErrNotFound", err)
	}
	if err := st.DeleteClusterKanbanConfig(ctx); err != nil {
		t.Fatalf("second delete = %v, want nil (idempotent)", err)
	}

	// 6. SetClusterKanbanToken (D28) is CONDITIONAL on the stored base_url and
	// never writes base_url itself. Missing row => ErrNotFound.
	exp2 := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if err := st.SetClusterKanbanToken(ctx, "http://jtype.three", tok, &exp2, "flow-user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set token on empty = %v, want ErrNotFound", err)
	}
	if err := st.UpsertClusterKanbanConfig(ctx,
		&domain.KanbanConfig{BaseURL: "http://jtype.three", UpdatedBy: "admin-3"}); err != nil {
		t.Fatal(err)
	}
	// A STALE base_url (the admin re-pointed mid-flow) => ErrNotFound, row untouched.
	if err := st.SetClusterKanbanToken(ctx, "http://jtype.STALE", tok, &exp2, "flow-user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale-base set token = %v, want ErrNotFound", err)
	}
	got, err = st.GetClusterKanbanConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenSet() || got.TokenExpiresAt != nil || got.UpdatedBy != "admin-3" || got.BaseURL != "http://jtype.three" {
		t.Fatalf("stale-base set must not touch the row: %+v", got)
	}
	// A MATCHING base_url lands token + expiry + updated_by; base_url unchanged.
	if err := st.SetClusterKanbanToken(ctx, "http://jtype.three", tok, &exp2, "flow-user"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetClusterKanbanConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.TokenEnc, tok) || got.TokenExpiresAt == nil || !got.TokenExpiresAt.Equal(exp2) ||
		got.UpdatedBy != "flow-user" || got.BaseURL != "http://jtype.three" {
		t.Fatalf("conditional set roundtrip = %+v", got)
	}
}

// Test 6 (memory half): the in-memory store satisfies the parity contract.
func TestMemKanbanConfig(t *testing.T) {
	kanbanConfigStoreTests(t, NewMemStore())
}

// Test 6 (pg half): real Postgres runs the identical assertions. Requires
// JCLOUD_PG_DSN; the single-row table is cleared before/after so the test is
// isolated from prior runs.
func TestPGKanbanConfig(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed store test")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.DeleteClusterKanbanConfig(ctx); err != nil {
		t.Fatalf("pre-clean: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteClusterKanbanConfig(context.Background()) })
	kanbanConfigStoreTests(t, st)
}
