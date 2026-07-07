package credentials

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

func testCipher(t *testing.T) *auth.Cipher {
	t.Helper()
	key := make([]byte, 32)
	c, err := auth.NewCipher(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fakeOAuth is a minimal OAuthProvider whose Refresh returns a scripted token.
type fakeOAuth struct {
	refreshed  *provider.OAuthToken
	err        error
	sawRefresh string
}

func (f *fakeOAuth) ID() domain.GitProvider                        { return domain.ProviderGitea }
func (f *fakeOAuth) AuthorizeURL(state, redirectURI string) string { return "" }
func (f *fakeOAuth) Exchange(context.Context, string, string) (*provider.OAuthToken, error) {
	return nil, nil
}
func (f *fakeOAuth) FetchUser(context.Context, *provider.OAuthToken) (*provider.OAuthUser, error) {
	return nil, nil
}
func (f *fakeOAuth) Refresh(_ context.Context, refresh string) (*provider.OAuthToken, error) {
	f.sawRefresh = refresh
	if f.err != nil {
		return nil, f.err
	}
	return f.refreshed, nil
}

// TestResolvePATFallback: a user-less gitea run resolves the global PAT.
func TestResolvePATFallback(t *testing.T) {
	st := store.NewMemStore()
	r := NewResolver(st, nil, nil, "the-pat", nil)
	tok, err := r.Resolve(context.Background(), domain.ProviderGitea, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "the-pat" || tok.Scheme != "token" || tok.Source != "gitea_pat" {
		t.Fatalf("tok = %+v", tok)
	}
}

// TestResolveNoCredential: a user-less github run (no PAT for github) errors.
func TestResolveNoCredential(t *testing.T) {
	st := store.NewMemStore()
	r := NewResolver(st, nil, nil, "the-pat", nil)
	if _, err := r.Resolve(context.Background(), domain.ProviderGitHub, nil); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err=%v want ErrNoCredential", err)
	}
}

// TestResolveUserToken: a run triggered by a user resolves that user's stored
// (decrypted) OAuth token, preferring it over the PAT.
func TestResolveUserToken(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cipher := testCipher(t)
	enc, _ := cipher.EncryptString("user-access-tok")
	uid := domain.NewID()
	_, _ = st.CreateUserWithIdentity(ctx, &domain.User{ID: uid, DisplayName: "u"},
		&domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: "1",
			Username: "alice", AccessTokenEnc: enc, CreatedAt: time.Now()})

	r := NewResolver(st, cipher, nil, "the-pat", nil)
	tok, err := r.Resolve(ctx, domain.ProviderGitea, &uid)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "user-access-tok" || tok.Scheme != "Bearer" || tok.Source != "user_oauth:alice" {
		t.Fatalf("tok = %+v want the decrypted user token", tok)
	}
}

// TestResolveRefreshesExpired: an expired identity token is refreshed and the
// fresh token is returned and persisted.
func TestResolveRefreshesExpired(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cipher := testCipher(t)
	accessEnc, _ := cipher.EncryptString("stale-access")
	refreshEnc, _ := cipher.EncryptString("refresh-tok")
	past := time.Now().Add(-time.Hour)
	uid := domain.NewID()
	idID := domain.NewID()
	_, _ = st.CreateUserWithIdentity(ctx, &domain.User{ID: uid, DisplayName: "u"},
		&domain.UserIdentity{ID: idID, Provider: domain.ProviderGitea, ProviderUID: "1",
			Username: "bob", AccessTokenEnc: accessEnc, RefreshTokenEnc: refreshEnc,
			TokenExpiresAt: &past, CreatedAt: time.Now()})

	fake := &fakeOAuth{refreshed: &provider.OAuthToken{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", Expiry: time.Now().Add(time.Hour)}}
	oauth := map[domain.GitProvider]provider.OAuthProvider{domain.ProviderGitea: fake}
	r := NewResolver(st, cipher, oauth, "the-pat", nil)

	tok, err := r.Resolve(ctx, domain.ProviderGitea, &uid)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "fresh-access" {
		t.Fatalf("tok=%q want fresh-access (refreshed)", tok.Value)
	}
	if fake.sawRefresh != "refresh-tok" {
		t.Fatalf("refresh called with %q want refresh-tok", fake.sawRefresh)
	}
	// Persisted: the stored access token is now the refreshed one.
	id, _ := st.GetIdentityForUser(ctx, uid, domain.ProviderGitea)
	if dec, _ := cipher.DecryptString(id.AccessTokenEnc); dec != "fresh-access" {
		t.Fatalf("persisted access token=%q want fresh-access", dec)
	}
}

// TestAuthedURL covers the per-provider userinfo injection + passthroughs.
func TestAuthedURL(t *testing.T) {
	cases := []struct {
		tok  Token
		url  string
		prov domain.GitProvider
		want string
	}{
		{Token{Value: "tok"}, "http://gitea.svc/o/r.git", domain.ProviderGitea, "http://tok@gitea.svc/o/r.git"},
		{Token{Value: "tok"}, "https://github.com/o/r.git", domain.ProviderGitHub, "https://x-access-token:tok@github.com/o/r.git"},
		{Token{Value: "tok"}, "https://gitlab.com/o/r.git", domain.ProviderGitLab, "https://oauth2:tok@gitlab.com/o/r.git"},
		{Token{Value: "tok"}, "git://git/seed.git", domain.ProviderGitea, "git://git/seed.git"}, // non-http passthrough
		{Token{}, "http://gitea.svc/o/r.git", domain.ProviderGitea, "http://gitea.svc/o/r.git"}, // empty token → anonymous
	}
	for _, tc := range cases {
		if got := tc.tok.AuthedURL(tc.url, tc.prov); got != tc.want {
			t.Errorf("AuthedURL(%q,%s) = %q want %q", tc.url, tc.prov, got, tc.want)
		}
	}
}
