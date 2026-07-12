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

// Webhook setup must never borrow a project integration or the legacy cluster
// PAT. It is an explicit action performed under the member's own OAuth grant.
func TestResolveUserOAuthNeverFallsBackToPAT(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cipher := testCipher(t)
	userID := domain.NewID()
	_, _ = st.CreateUserWithIdentity(ctx, &domain.User{ID: userID, DisplayName: "github-only"},
		&domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitHub, ProviderUID: "42",
			Username: "octo", AccessTokenEnc: mustEncrypt(t, cipher, "github-oauth"), CreatedAt: time.Now()})

	r := NewResolver(st, cipher, nil, "legacy-gitea-pat", nil)
	if _, err := r.ResolveUserOAuth(ctx, domain.ProviderGitea, userID); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err=%v want ErrNoCredential (no PAT fallback)", err)
	}

	tok, err := r.ResolveUserOAuth(ctx, domain.ProviderGitHub, userID)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "github-oauth" || tok.Scheme != "Bearer" || tok.Source != "user_oauth:octo" {
		t.Fatalf("tok=%+v want the user's GitHub OAuth token", tok)
	}
}

func mustEncrypt(t *testing.T, cipher *auth.Cipher, value string) []byte {
	t.Helper()
	encoded, err := cipher.EncryptString(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
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

// mkIntegration seals token and stores an integration, returning it.
func mkIntegration(t *testing.T, st *store.MemStore, cipher *auth.Cipher, projectID, name string, prov domain.GitProvider, token string) *domain.Integration {
	t.Helper()
	enc, err := cipher.EncryptString(token)
	if err != nil {
		t.Fatal(err)
	}
	in := &domain.Integration{
		ID: domain.NewID(), ProjectID: projectID, Name: name, Provider: prov,
		Host: "git.example.com", CredType: domain.CredTypePAT, TokenEnc: enc,
		BotUsername: "bot", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.CreateIntegration(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	return in
}

// TestResolveForServiceIntegrationWins: a service bound to an integration ALWAYS
// resolves the integration bot token, ignoring the triggering user's personal
// OAuth (D19 / F5). This is the core of the priority matrix.
func TestResolveForServiceIntegrationWins(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cipher := testCipher(t)

	// A triggering user WITH their own stored OAuth token that must be ignored.
	userEnc, _ := cipher.EncryptString("personal-oauth")
	uid := domain.NewID()
	_, _ = st.CreateUserWithIdentity(ctx, &domain.User{ID: uid, DisplayName: "u"},
		&domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: "1",
			Username: "alice", AccessTokenEnc: userEnc, CreatedAt: time.Now()})

	in := mkIntegration(t, st, cipher, "proj1", "default", domain.ProviderGitea, "bot-pat")
	svc := &domain.Service{ID: domain.NewID(), ProjectID: "proj1", Provider: domain.ProviderGitea, IntegrationID: &in.ID}

	r := NewResolver(st, cipher, nil, "the-pat", nil)
	tok, err := r.ResolveForService(ctx, svc, &uid)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "bot-pat" {
		t.Fatalf("value=%q want bot-pat (integration wins over personal OAuth)", tok.Value)
	}
	if tok.Scheme != "token" || tok.Source != "integration:default" {
		t.Fatalf("tok=%+v want gitea token scheme + integration source", tok)
	}
}

// TestResolveForServiceIntegrationSchemeByProvider: github/gitlab integration
// tokens Bearer; gitea uses the token scheme.
func TestResolveForServiceIntegrationSchemeByProvider(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cipher := testCipher(t)
	for _, tc := range []struct {
		prov       domain.GitProvider
		wantScheme string
	}{
		{domain.ProviderGitea, "token"},
		{domain.ProviderGitHub, "Bearer"},
		{domain.ProviderGitLab, "Bearer"},
	} {
		in := mkIntegration(t, st, cipher, "p", string(tc.prov), tc.prov, "tok-"+string(tc.prov))
		svc := &domain.Service{ID: domain.NewID(), ProjectID: "p", Provider: tc.prov, IntegrationID: &in.ID}
		tok, err := NewResolver(st, cipher, nil, "", nil).ResolveForService(ctx, svc, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tok.Scheme != tc.wantScheme {
			t.Fatalf("%s scheme=%q want %q", tc.prov, tok.Scheme, tc.wantScheme)
		}
	}
}

// TestResolveForServiceUnboundFallsBack: a service with NO integration keeps the
// legacy path — the triggering user's personal OAuth, then the gitea PAT.
func TestResolveForServiceUnboundFallsBack(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cipher := testCipher(t)
	userEnc, _ := cipher.EncryptString("personal-oauth")
	uid := domain.NewID()
	_, _ = st.CreateUserWithIdentity(ctx, &domain.User{ID: uid, DisplayName: "u"},
		&domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: "1",
			Username: "alice", AccessTokenEnc: userEnc, CreatedAt: time.Now()})

	svc := &domain.Service{ID: domain.NewID(), ProjectID: "p", Provider: domain.ProviderGitea} // no integration
	r := NewResolver(st, cipher, nil, "the-pat", nil)

	// With a user: personal OAuth.
	tok, err := r.ResolveForService(ctx, svc, &uid)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "personal-oauth" || tok.Source != "user_oauth:alice" {
		t.Fatalf("tok=%+v want personal OAuth", tok)
	}
	// Without a user: gitea PAT fallback.
	tok, err = r.ResolveForService(ctx, svc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "the-pat" || tok.Source != "gitea_pat" {
		t.Fatalf("tok=%+v want gitea PAT fallback", tok)
	}
}

// TestResolveForServiceDecryptFailIsFailVisible: an integration whose token cannot
// be decrypted (no cipher) is a FAIL-VISIBLE error — NEVER a silent fall back to
// the triggering user's personal OAuth (CLAUDE.md red line #1).
func TestResolveForServiceDecryptFailIsFailVisible(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cipher := testCipher(t)

	// A user with a working personal token that must NOT be used as a fallback.
	userEnc, _ := cipher.EncryptString("personal-oauth")
	uid := domain.NewID()
	_, _ = st.CreateUserWithIdentity(ctx, &domain.User{ID: uid, DisplayName: "u"},
		&domain.UserIdentity{ID: domain.NewID(), Provider: domain.ProviderGitea, ProviderUID: "1",
			Username: "alice", AccessTokenEnc: userEnc, CreatedAt: time.Now()})

	in := mkIntegration(t, st, cipher, "p", "default", domain.ProviderGitea, "bot-pat")
	svc := &domain.Service{ID: domain.NewID(), ProjectID: "p", Provider: domain.ProviderGitea, IntegrationID: &in.ID}

	// Resolver built WITHOUT a cipher: the sealed integration token cannot be opened.
	r := NewResolver(st, nil, nil, "the-pat", nil)
	_, err := r.ResolveForService(ctx, svc, &uid)
	if !errors.Is(err, ErrIntegrationCredential) {
		t.Fatalf("err=%v want ErrIntegrationCredential (no silent personal-OAuth fallback)", err)
	}
}

// TestResolveForServiceMissingIntegrationIsFailVisible: a dangling integration id
// (integration deleted mid-flight) is a fail-visible error, not a fallback.
func TestResolveForServiceMissingIntegrationIsFailVisible(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	missing := "does-not-exist"
	svc := &domain.Service{ID: domain.NewID(), ProjectID: "p", Provider: domain.ProviderGitea, IntegrationID: &missing}
	r := NewResolver(st, testCipher(t), nil, "the-pat", nil)
	if _, err := r.ResolveForService(ctx, svc, nil); !errors.Is(err, ErrIntegrationCredential) {
		t.Fatalf("err=%v want ErrIntegrationCredential", err)
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
