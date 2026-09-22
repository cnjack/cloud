package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestPGPersonalModelScopeAndUserCascade(t *testing.T) {
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
	// Idempotence is part of the migration contract.
	if _, err := st.Pool().Exec(ctx, `ALTER TABLE model_providers ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(id) ON DELETE CASCADE`); err != nil {
		t.Fatal(err)
	}
	mk := func() string {
		u := &domain.User{ID: domain.NewID(), DisplayName: "Personal model fixture", CreatedAt: time.Now().UTC()}
		identity := &domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: domain.NewID(), Username: "personal-model-test", AccessTokenEnc: []byte("test-fixture"), CreatedAt: time.Now().UTC()}
		if _, err := st.CreateUserWithIdentity(ctx, u, identity); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, u.ID) })
		return u.ID
	}
	alice, bob := mk(), mk()
	project := &domain.Project{ID: domain.NewID(), Name: "Isolation fixture", CreatedAt: time.Now().UTC()}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	defer st.Pool().Exec(ctx, `DELETE FROM projects WHERE id=$1`, project.ID)
	provider := &domain.ModelProvider{ID: domain.NewID(), OwnerUserID: alice, Name: "Scoped provider", Kind: "openai", BaseURL: "https://model.example/v1", AuthType: domain.ModelProviderAuthAPIKey, APIKeyEnc: []byte("encrypted-test-fixture"), CatalogMode: domain.ModelProviderCatalogDisabled, CreatedAt: time.Now().UTC()}
	if err := st.CreateModelProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	twin := *provider
	twin.ID = domain.NewID()
	twin.OwnerUserID = bob
	if err := st.CreateModelProvider(ctx, &twin); err != nil {
		t.Fatalf("same name different account: %v", err)
	}
	fetched, err := st.GetModelProvider(ctx, provider.ID)
	if err != nil || fetched.OwnerUserID != alice {
		t.Fatalf("owner scan: %v", err)
	}
	model := &domain.Model{ID: domain.NewID(), ProviderID: provider.ID, Name: "Personal agent", ModelID: "agent", ModelName: "openai/agent", BaseURL: provider.BaseURL, APIKeyEnc: provider.APIKeyEnc, Capabilities: domain.ModelCapabilities{Tools: true}}
	if err := st.CreateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	own, err := st.ListModelsForAccount(ctx, alice)
	if err != nil || len(own) != 1 || own[0].ID != model.ID {
		t.Fatalf("own models=%v err=%v", len(own), err)
	}
	other, err := st.ListModelsForAccount(ctx, bob)
	if err != nil || len(other) != 0 {
		t.Fatalf("other models=%v err=%v", len(other), err)
	}
	if err := st.GrantModel(ctx, model.ID, project.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal project grant: %v", err)
	}
	if err := st.GrantModelToAccount(ctx, model.ID, bob, alice); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal account grant: %v", err)
	}
	shared, err := st.ListModelsForProject(ctx, project.ID)
	if err != nil || len(shared) != 0 {
		t.Fatal("personal model leaked into project")
	}
	model.Enabled = false
	if err := st.UpdateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	own, err = st.ListModelsForAccount(ctx, alice)
	if err != nil || len(own) != 0 {
		t.Fatal("disabled personal model remained available")
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetModelProvider(ctx, provider.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("provider orphan: %v", err)
	}
	if _, err := st.GetModel(ctx, model.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("model orphan: %v", err)
	}
}
