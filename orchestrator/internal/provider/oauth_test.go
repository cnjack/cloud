package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// oauthStub serves the token + user endpoints for one provider. It records the
// form values it received on the token call so the test can assert the exchange
// carried the right client credentials + redirect URI.
type oauthStub struct {
	tokenPath string
	userPath  string
	token     map[string]any
	user      map[string]any

	gotForm url.Values
	gotAuth string
}

func (s *oauthStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(s.tokenPath, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.token)
	})
	mux.HandleFunc(s.userPath, func(w http.ResponseWriter, r *http.Request) {
		s.gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.user)
	})
	return mux
}

func TestOAuthAuthorizeURL(t *testing.T) {
	p := NewGiteaOAuth(OAuthConfig{ClientID: "cid", ClientSecret: "sec", ExternalURL: "http://ext:3000", InternalURL: "http://int:3000"})
	got := p.AuthorizeURL("st4te", "http://localhost:8080/auth/callback/gitea")
	if !strings.HasPrefix(got, "http://ext:3000/login/oauth/authorize?") {
		t.Fatalf("authorize url host/path wrong: %s", got)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	if q.Get("client_id") != "cid" || q.Get("state") != "st4te" || q.Get("response_type") != "code" {
		t.Fatalf("authorize query wrong: %v", q)
	}
	if q.Get("redirect_uri") != "http://localhost:8080/auth/callback/gitea" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestGiteaOAuthExchangeAndUser(t *testing.T) {
	stub := &oauthStub{
		tokenPath: "/login/oauth/access_token",
		userPath:  "/api/v1/user",
		token:     map[string]any{"access_token": "gitea-at", "refresh_token": "gitea-rt", "expires_in": 3600, "token_type": "bearer"},
		user:      map[string]any{"id": 7, "login": "alice", "full_name": "Alice A", "avatar_url": "http://a/av.png"},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	p := NewGiteaOAuth(OAuthConfig{ClientID: "cid", ClientSecret: "sec", ExternalURL: srv.URL, InternalURL: srv.URL})
	ctx := context.Background()
	tok, err := p.Exchange(ctx, "the-code", "http://cb")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "gitea-at" || tok.RefreshToken != "gitea-rt" {
		t.Fatalf("token = %+v", tok)
	}
	if tok.Expiry.IsZero() {
		t.Fatal("expiry should be set from expires_in")
	}
	// The exchange must carry client creds, the code and the redirect URI.
	if stub.gotForm.Get("code") != "the-code" || stub.gotForm.Get("client_id") != "cid" ||
		stub.gotForm.Get("client_secret") != "sec" || stub.gotForm.Get("redirect_uri") != "http://cb" ||
		stub.gotForm.Get("grant_type") != "authorization_code" {
		t.Fatalf("token form wrong: %v", stub.gotForm)
	}

	u, err := p.FetchUser(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if u.ProviderUID != "7" || u.Username != "alice" || u.DisplayName != "Alice A" || u.AvatarURL != "http://a/av.png" {
		t.Fatalf("user = %+v", u)
	}
	if stub.gotAuth != "Bearer gitea-at" {
		t.Fatalf("user auth header = %q want 'Bearer gitea-at'", stub.gotAuth)
	}
}

func TestGitHubOAuthExchangeAndUser(t *testing.T) {
	stub := &oauthStub{
		tokenPath: "/login/oauth/access_token",
		userPath:  "/user",
		token:     map[string]any{"access_token": "gho_x", "token_type": "bearer", "scope": "read:user"},
		user:      map[string]any{"id": 42, "login": "octocat", "name": "The Octocat", "avatar_url": "http://gh/av"},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	// InternalURL=srv.URL (not github.com) so the user endpoint resolves to srv/user.
	p := NewGitHubOAuth(OAuthConfig{ClientID: "cid", ClientSecret: "sec", ExternalURL: srv.URL, InternalURL: srv.URL})
	ctx := context.Background()
	tok, err := p.Exchange(ctx, "c", "http://cb")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "gho_x" {
		t.Fatalf("token = %+v", tok)
	}
	if !tok.Expiry.IsZero() {
		t.Fatal("github token without expires_in should have zero expiry")
	}
	u, err := p.FetchUser(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if u.ProviderUID != "42" || u.Username != "octocat" || u.DisplayName != "The Octocat" {
		t.Fatalf("user = %+v", u)
	}
}

func TestGitHubDefaultAPIHost(t *testing.T) {
	// With the public github.com internal host, the user endpoint must resolve to
	// api.github.com (not github.com/user).
	p := NewGitHubOAuth(OAuthConfig{ClientID: "c", ClientSecret: "s"}).(*oauthClient)
	if p.userURL != "https://api.github.com/user" {
		t.Fatalf("github userURL = %q want https://api.github.com/user", p.userURL)
	}
	if p.authorizeURL != "https://github.com/login/oauth/authorize" {
		t.Fatalf("github authorizeURL = %q", p.authorizeURL)
	}
}

func TestGitLabOAuthExchangeAndUser(t *testing.T) {
	stub := &oauthStub{
		tokenPath: "/oauth/token",
		userPath:  "/api/v4/user",
		token:     map[string]any{"access_token": "glpat", "refresh_token": "glrt", "expires_in": 7200},
		user:      map[string]any{"id": 99, "username": "tanuki", "name": "Gitlab Tanuki", "avatar_url": "http://gl/av"},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	p := NewGitLabOAuth(OAuthConfig{ClientID: "cid", ClientSecret: "sec", ExternalURL: srv.URL, InternalURL: srv.URL})
	ctx := context.Background()
	tok, err := p.Exchange(ctx, "c", "http://cb")
	if err != nil {
		t.Fatal(err)
	}
	u, err := p.FetchUser(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if u.ProviderUID != "99" || u.Username != "tanuki" || u.DisplayName != "Gitlab Tanuki" {
		t.Fatalf("user = %+v", u)
	}
}

func TestOAuthExchangeErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "bad_verification_code", "error_description": "expired"})
	}))
	defer srv.Close()
	p := NewGiteaOAuth(OAuthConfig{ClientID: "c", ClientSecret: "s", ExternalURL: srv.URL, InternalURL: srv.URL})
	if _, err := p.Exchange(context.Background(), "c", "http://cb"); err == nil ||
		!strings.Contains(err.Error(), "bad_verification_code") {
		t.Fatalf("err = %v want bad_verification_code", err)
	}
}

func TestDisplayNameFallsBackToUsername(t *testing.T) {
	stub := &oauthStub{
		tokenPath: "/login/oauth/access_token",
		userPath:  "/api/v1/user",
		token:     map[string]any{"access_token": "at"},
		user:      map[string]any{"id": 1, "login": "bob", "full_name": ""},
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	p := NewGiteaOAuth(OAuthConfig{ClientID: "c", ClientSecret: "s", ExternalURL: srv.URL, InternalURL: srv.URL})
	tok, _ := p.Exchange(context.Background(), "c", "cb")
	u, err := p.FetchUser(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if u.DisplayName != "bob" {
		t.Fatalf("display name = %q want fallback to login 'bob'", u.DisplayName)
	}
}
