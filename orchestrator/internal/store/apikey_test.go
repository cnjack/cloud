package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
)

// newAPIKeyTestStore returns a MemStore seeded with one project so an api
// key's project_id FK resolves.
func newAPIKeyTestStore(t *testing.T) (*MemStore, *domain.Project) {
	t.Helper()
	m := NewMemStore()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "keys", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return m, p
}

// mkAPIKey mints a plaintext + the domain.APIKey row the way the API handler
// does: generate, hash for storage, never persist the plaintext.
func mkAPIKey(t *testing.T, projectID string) (*domain.APIKey, string) {
	t.Helper()
	plaintext, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	k := &domain.APIKey{
		ID:        domain.NewID(),
		ProjectID: projectID,
		Name:      "ci",
		KeyHash:   auth.HashToken(plaintext),
		Prefix:    auth.APIKeyDisplayPrefix(plaintext),
		CreatedAt: time.Now().UTC(),
	}
	return k, plaintext
}

func TestAPIKeyCRUD(t *testing.T) {
	m, p := newAPIKeyTestStore(t)
	ctx := context.Background()
	k, plaintext := mkAPIKey(t, p.ID)

	if err := m.CreateAPIKey(ctx, k); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := m.GetAPIKey(ctx, k.ID)
	if err != nil || got.Name != "ci" || got.ProjectID != p.ID {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if got.KeyHash == "" || got.KeyHash == plaintext {
		t.Fatalf("key_hash not stored correctly: %q", got.KeyHash)
	}

	// Resolve by hash — the principal-resolution path.
	byHash, err := m.GetAPIKeyByHash(ctx, auth.HashToken(plaintext))
	if err != nil || byHash.ID != k.ID {
		t.Fatalf("get by hash: %+v err=%v", byHash, err)
	}

	// A wrong hash (e.g. a mistyped/foreign key) never resolves.
	if _, err := m.GetAPIKeyByHash(ctx, auth.HashToken("jck_not-the-right-key")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get by wrong hash: err=%v want ErrNotFound", err)
	}

	// List by project.
	list, err := m.ListAPIKeysByProject(ctx, p.ID)
	if err != nil || len(list) != 1 || list[0].ID != k.ID {
		t.Fatalf("list by project: %+v err=%v", list, err)
	}

	// GetAPIKey on a missing id.
	if _, err := m.GetAPIKey(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: err=%v want ErrNotFound", err)
	}
}

// TestAPIKeyLastUsed covers the "hit -> best-effort last_used_at" contract:
// nil until first stamped, then reflects the given instant.
func TestAPIKeyLastUsed(t *testing.T) {
	m, p := newAPIKeyTestStore(t)
	ctx := context.Background()
	k, _ := mkAPIKey(t, p.ID)
	if err := m.CreateAPIKey(ctx, k); err != nil {
		t.Fatal(err)
	}

	got, _ := m.GetAPIKey(ctx, k.ID)
	if got.LastUsedAt != nil {
		t.Fatalf("last_used_at should start nil, got %v", got.LastUsedAt)
	}

	now := time.Now().UTC()
	if err := m.UpdateAPIKeyLastUsed(ctx, k.ID, now); err != nil {
		t.Fatalf("update last used: %v", err)
	}
	got, _ = m.GetAPIKey(ctx, k.ID)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Fatalf("last_used_at = %v want %v", got.LastUsedAt, now)
	}

	if err := m.UpdateAPIKeyLastUsed(ctx, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update last used on missing: err=%v want ErrNotFound", err)
	}
}

// TestAPIKeyRevoke: revocation is effective immediately (the very next
// GetAPIKeyByHash 404s) and is idempotent — revoking twice, or a missing id,
// is a no-op rather than an error (so a retried DELETE never surfaces as a
// failure).
func TestAPIKeyRevoke(t *testing.T) {
	m, p := newAPIKeyTestStore(t)
	ctx := context.Background()
	k, plaintext := mkAPIKey(t, p.ID)
	if err := m.CreateAPIKey(ctx, k); err != nil {
		t.Fatal(err)
	}

	// Resolvable before revocation.
	if _, err := m.GetAPIKeyByHash(ctx, auth.HashToken(plaintext)); err != nil {
		t.Fatalf("resolve before revoke: %v", err)
	}

	if err := m.RevokeAPIKey(ctx, k.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, _ := m.GetAPIKey(ctx, k.ID)
	if got.RevokedAt == nil {
		t.Fatal("revoked_at not stamped")
	}

	// Immediately unresolvable — same ErrNotFound as an unknown key, not a
	// distinct "revoked" signal (api/principal.go maps both to 401).
	if _, err := m.GetAPIKeyByHash(ctx, auth.HashToken(plaintext)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve after revoke: err=%v want ErrNotFound", err)
	}

	// Idempotent: revoking again is a no-op, not an error.
	if err := m.RevokeAPIKey(ctx, k.ID); err != nil {
		t.Fatalf("re-revoke: %v", err)
	}
	// Idempotent on a missing id too.
	if err := m.RevokeAPIKey(ctx, "missing"); err != nil {
		t.Fatalf("revoke missing: %v", err)
	}
}

// TestAPIKeyListScopedByProject: a project only ever sees its own keys.
func TestAPIKeyListScopedByProject(t *testing.T) {
	m, p1 := newAPIKeyTestStore(t)
	ctx := context.Background()
	p2 := &domain.Project{ID: domain.NewID(), Name: "other", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, p2); err != nil {
		t.Fatal(err)
	}
	k1, _ := mkAPIKey(t, p1.ID)
	k2, _ := mkAPIKey(t, p2.ID)
	if err := m.CreateAPIKey(ctx, k1); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateAPIKey(ctx, k2); err != nil {
		t.Fatal(err)
	}

	list1, _ := m.ListAPIKeysByProject(ctx, p1.ID)
	if len(list1) != 1 || list1[0].ID != k1.ID {
		t.Fatalf("project 1 list: %+v", list1)
	}
	list2, _ := m.ListAPIKeysByProject(ctx, p2.ID)
	if len(list2) != 1 || list2[0].ID != k2.ID {
		t.Fatalf("project 2 list: %+v", list2)
	}
}
