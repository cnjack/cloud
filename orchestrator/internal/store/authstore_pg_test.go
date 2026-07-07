package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// TestPGAuthStore exercises the M2 auth-store SQL paths that the memory store
// cannot validate: BYTEA token columns, the advisory-locked first-user decision,
// the identity UNIQUE(provider, provider_uid) conflict, session validity
// filtering, and the members join queries. Requires JCLOUD_PG_DSN.
func TestPGAuthStore(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed auth store test")
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

	// Isolate from any pre-existing rows so the "first user" assertion is
	// meaningful: run inside a savepoint-free clean slate by tracking + deleting
	// what we create. To make first-user deterministic we require an empty users
	// table; skip loudly if the DB already has users (a shared DB).
	if n, _ := st.CountUsers(ctx); n != 0 {
		t.Skipf("users table not empty (%d rows); run against a clean DB for the first-admin assertion", n)
	}

	mkID := func(prov domain.GitProvider, uid, name string) *domain.UserIdentity {
		return &domain.UserIdentity{
			ID: domain.NewID(), Provider: prov, ProviderUID: uid, Username: name,
			AccessTokenEnc: []byte("enc-" + uid), CreatedAt: time.Now().UTC(),
		}
	}

	var userIDs []string
	t.Cleanup(func() {
		for _, id := range userIDs {
			_, _ = st.Pool().Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id)
		}
	})

	// First user => cluster admin.
	u1 := &domain.User{ID: domain.NewID(), DisplayName: "Alice Admin", CreatedAt: time.Now().UTC()}
	id1 := mkID(domain.ProviderGitea, "uid-1", "alice")
	first, err := st.CreateUserWithIdentity(ctx, u1, id1)
	if err != nil {
		t.Fatal(err)
	}
	userIDs = append(userIDs, u1.ID)
	if !first || !u1.IsClusterAdmin {
		t.Fatalf("first user: first=%v admin=%v want true/true", first, u1.IsClusterAdmin)
	}

	// Second user => NOT admin.
	u2 := &domain.User{ID: domain.NewID(), DisplayName: "Bob", CreatedAt: time.Now().UTC()}
	id2 := mkID(domain.ProviderGitea, "uid-2", "bob")
	first2, err := st.CreateUserWithIdentity(ctx, u2, id2)
	if err != nil {
		t.Fatal(err)
	}
	userIDs = append(userIDs, u2.ID)
	if first2 {
		t.Fatal("second user should not be first")
	}

	// GetIdentity + GetUser.
	gotID, err := st.GetIdentity(ctx, domain.ProviderGitea, "uid-1")
	if err != nil || gotID.UserID != u1.ID || string(gotID.AccessTokenEnc) != "enc-uid-1" {
		t.Fatalf("GetIdentity = %+v, %v", gotID, err)
	}

	// UpdateIdentityToken round trips the BYTEA columns + expiry.
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := st.UpdateIdentityToken(ctx, id1.ID, []byte("new-access"), []byte("new-refresh"), &exp); err != nil {
		t.Fatal(err)
	}
	gotID, _ = st.GetIdentity(ctx, domain.ProviderGitea, "uid-1")
	if string(gotID.AccessTokenEnc) != "new-access" || string(gotID.RefreshTokenEnc) != "new-refresh" ||
		gotID.TokenExpiresAt == nil || !gotID.TokenExpiresAt.Equal(exp) {
		t.Fatalf("updated identity = %+v", gotID)
	}

	// AttachIdentity: a fresh identity for u1, then a conflict with u2's identity.
	if err := st.AttachIdentity(ctx, u1.ID, mkID(domain.ProviderGitHub, "gh-9", "alice-gh")); err != nil {
		t.Fatalf("attach new identity: %v", err)
	}
	if ids, _ := st.ListIdentities(ctx, u1.ID); len(ids) != 2 {
		t.Fatalf("u1 identities=%d want 2", len(ids))
	}
	if err := st.AttachIdentity(ctx, u1.ID, mkID(domain.ProviderGitea, "uid-2", "bob")); !errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("attach taken identity: err=%v want ErrIdentityTaken", err)
	}

	// Search + resolve by username.
	if us, _ := st.SearchUsers(ctx, "ali", 20); len(us) != 1 || us[0].ID != u1.ID {
		t.Fatalf("search 'ali' = %+v want [u1]", us)
	}
	if us, _ := st.SearchUsers(ctx, "bob", 20); len(us) != 1 || us[0].ID != u2.ID {
		t.Fatalf("search by username 'bob' = %+v want [u2]", us)
	}
	if u, err := st.GetUserByProviderUsername(ctx, domain.ProviderGitea, "bob"); err != nil || u.ID != u2.ID {
		t.Fatalf("GetUserByProviderUsername bob = %+v, %v", u, err)
	}

	// Sessions: valid, expired, revoked.
	now := time.Now().UTC()
	valid := &domain.Session{ID: domain.NewID(), UserID: u1.ID, TokenHash: "hash-valid", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	expired := &domain.Session{ID: domain.NewID(), UserID: u1.ID, TokenHash: "hash-expired", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	if err := st.CreateSession(ctx, valid); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if u, err := st.GetUserBySessionToken(ctx, "hash-valid"); err != nil || u.ID != u1.ID {
		t.Fatalf("valid session user = %+v, %v", u, err)
	}
	if _, err := st.GetUserBySessionToken(ctx, "hash-expired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session should be ErrNotFound, got %v", err)
	}
	if err := st.RevokeSession(ctx, "hash-valid"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUserBySessionToken(ctx, "hash-valid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked session should be ErrNotFound, got %v", err)
	}

	// Members: project owned by u1, with u2 as viewer.
	p := &domain.Project{ID: domain.NewID(), Name: "auth-proj", CreatedAt: now, OwnerUserID: u1.ID}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(context.Background(), p.ID) })
	// Owner_user_id round-trips.
	if gp, _ := st.GetProject(ctx, p.ID); gp.OwnerUserID != u1.ID {
		t.Fatalf("owner_user_id=%q want %q", gp.OwnerUserID, u1.ID)
	}
	if err := st.UpsertMember(ctx, &domain.ProjectMember{ProjectID: p.ID, UserID: u1.ID, Role: domain.RoleOwner, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMember(ctx, &domain.ProjectMember{ProjectID: p.ID, UserID: u2.ID, Role: domain.RoleViewer, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Upsert same member with a new role (idempotent update).
	if err := st.UpsertMember(ctx, &domain.ProjectMember{ProjectID: p.ID, UserID: u2.ID, Role: domain.RoleMember, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetMember(ctx, p.ID, u2.ID); m.Role != domain.RoleMember {
		t.Fatalf("u2 role=%q want member (upsert)", m.Role)
	}
	if members, _ := st.ListMembers(ctx, p.ID); len(members) != 2 {
		t.Fatalf("members=%d want 2", len(members))
	}
	if n, _ := st.CountProjectOwners(ctx, p.ID); n != 1 {
		t.Fatalf("owners=%d want 1", n)
	}
	// ListProjectsForUser reflects membership.
	if ps, _ := st.ListProjectsForUser(ctx, u2.ID); len(ps) != 1 || ps[0].ID != p.ID {
		t.Fatalf("u2 projects = %+v want [p]", ps)
	}
	if err := st.RemoveMember(ctx, p.ID, u2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetMember(ctx, p.ID, u2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed member should be ErrNotFound, got %v", err)
	}

	// Run triggered_by_user_id round-trips.
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: now}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	uid := u1.ID
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "x", Status: domain.StatusQueued, Attempt: 1, CreatedAt: now, TriggeredByUserID: &uid}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if gr, _ := st.GetRun(ctx, run.ID); gr.TriggeredByUserID == nil || *gr.TriggeredByUserID != u1.ID {
		t.Fatalf("run triggered_by=%v want %q", gr.TriggeredByUserID, u1.ID)
	}
}
