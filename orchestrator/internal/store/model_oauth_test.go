package store

import (
	"context"
	"fmt"
	"github.com/cnjack/jcloud/internal/domain"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestPGOAuthLockRestartAndAccountCascade(t *testing.T) {
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
	if err = Migrate(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	u := &domain.User{ID: domain.NewID(), DisplayName: "OAuth fixture", CreatedAt: time.Now()}
	identity := &domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: domain.NewID(), Username: "oauth-test", AccessTokenEnc: []byte("fixture"), CreatedAt: time.Now()}
	if _, err = st.CreateUserWithIdentity(ctx, u, identity); err != nil {
		t.Fatal(err)
	}
	defer st.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, u.ID)
	p := &domain.ModelProvider{ID: domain.NewID(), OwnerUserID: u.ID, Name: "OAuth fixture", Kind: "openai-codex", BaseURL: "https://chatgpt.com/backend-api/codex", AuthType: domain.ModelProviderAuthOAuth, CatalogMode: domain.ModelProviderCatalogAuto}
	if err = st.CreateModelProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	model := &domain.Model{ID: domain.NewID(), ProviderID: p.ID, Name: "OAuth agent", ModelID: "fixture", ModelName: "openai-codex/fixture", BaseURL: p.BaseURL, Source: "custom", Enabled: true}
	if err = st.CreateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	model.Enabled = false
	if err = st.UpdateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	preserved, err := st.GetModelProvider(ctx, p.ID)
	if err != nil || preserved.AuthType != domain.ModelProviderAuthOAuth {
		t.Fatal("editing model downgraded OAuth")
	}
	second, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := st
			if i%2 == 1 {
				db = second
			}
			if err := db.WithModelOAuth(ctx, p.ID, func(b []byte) ([]byte, error) {
				n, _ := strconv.Atoi(string(b))
				time.Sleep(time.Millisecond)
				return []byte(strconv.Itoa(n + 1)), nil
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if err = second.WithModelOAuth(ctx, p.ID, func(b []byte) ([]byte, error) {
		if string(b) != "12" {
			return nil, fmt.Errorf("lost update %s", b)
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = st.Pool().QueryRow(ctx, `SELECT count(*) FROM model_provider_oauth WHERE provider_id=$1`, p.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("orphan OAuth state %d %v", n, err)
	}
}
