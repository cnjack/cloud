package store

import (
	"context"
	"errors"
	"github.com/cnjack/jcloud/internal/domain"
	"os"
	"testing"
	"time"
)

func TestPGAccountProfilePersistence(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN required")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	user := &domain.User{ID: domain.NewID(), DisplayName: "Profile fixture", CreatedAt: time.Now().UTC()}
	identity := &domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: domain.NewID(), Username: "profile-test", AccessTokenEnc: []byte("test-only-encrypted-fixture"), CreatedAt: time.Now().UTC()}
	if _, err := st.CreateUserWithIdentity(ctx, user, identity); err != nil {
		t.Fatal(err)
	}
	defer st.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, user.ID)
	want := domain.AccountProfile{DisplayName: "Saved name", Preferences: domain.AccountPreferences{Effort: "high", SendKey: "mod_enter", Theme: "dark"}}
	if err := st.UpdateAccountProfile(ctx, user.ID, want); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetAccountProfile(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *got != want {
		t.Fatalf("profile: %+v want %+v", got, want)
	}
	if err := reopened.UpdateAccountProfile(ctx, domain.NewID(), want); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
}
