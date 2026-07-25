package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/cnjack/jcloud/internal/domain"
)

// kanbanConfigStoreTests exercises the single-row cluster_kanban_config CRUD
// against ANY Store implementation, so memory and Postgres run the SAME
// assertions (the D27 memory/pg parity check). The store must start with no row.
// D36: the row is base-URL-only — no token columns remain (migration 0042).
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

	// 2. Roundtrip base_url / updated_by (+ updated_at stamped).
	if err := st.UpsertClusterKanbanConfig(ctx,
		&domain.KanbanConfig{BaseURL: "http://jtype.one", UpdatedBy: "admin-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetClusterKanbanConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "http://jtype.one" || got.UpdatedBy != "admin-1" {
		t.Fatalf("roundtrip = %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("updated_at should be stamped on upsert")
	}

	// 3. Singleton: a second upsert OVERWRITES the same row (never a second row).
	if err := st.UpsertClusterKanbanConfig(ctx,
		&domain.KanbanConfig{BaseURL: "http://jtype.two", UpdatedBy: "admin-2"}); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetClusterKanbanConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "http://jtype.two" || got.UpdatedBy != "admin-2" {
		t.Fatalf("overwrite = %+v", got)
	}

	// 4. Delete => ErrNotFound afterwards, and a repeat delete is idempotent.
	if err := st.DeleteClusterKanbanConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetClusterKanbanConfig(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete get = %v, want ErrNotFound", err)
	}
	if err := st.DeleteClusterKanbanConfig(ctx); err != nil {
		t.Fatalf("second delete = %v, want nil (idempotent)", err)
	}
}

// Memory half: the in-memory store satisfies the parity contract.
func TestMemKanbanConfig(t *testing.T) {
	kanbanConfigStoreTests(t, NewMemStore())
}

// PG half: real Postgres runs the identical assertions — and proves the 0042
// drop-columns migration leaves a schema the slimmed queries still work
// against. Requires JCLOUD_PG_DSN; the single-row table is cleared before/after
// so the test is isolated from prior runs.
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
