package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// OAuthProvider is the identity seam for a git host's OAuth2 login flow (M2). It
// is deliberately separate from Provider (the PR API seam) so a host's login and
// its PR operations can be wired independently.
//
// dual-URL: AuthorizeURL is built from the provider's EXTERNAL root (the
// browser-reachable host the user is redirected to); Exchange and FetchUser hit
// the INTERNAL root (server-to-server). Locally only gitea is exercised for real;
// github/gitlab are unit-tested against httptest and not integration-tested.
//
// M3 will extend this interface (or add a sibling) with the token-holding PR
// operations the runner-contract inversion needs — pushing a branch and opening
// the draft PR with the *triggering user's* token, and posting a review comment.
// Predeclared here so the shape is known:
//
//	// PushAndOpenPR(ctx, tok *OAuthToken, in CreateDraftPRInput) (*PR, error)
//	// CreateReviewComment(ctx, tok *OAuthToken, owner, repo string, pr int, body string) error
type OAuthProvider interface {
	// ID is the provider this client speaks to.
	ID() domain.GitProvider
	// AuthorizeURL builds the provider authorize URL (EXTERNAL host) for a login
	// redirect, carrying the CSRF state and the callback redirect URI.
	AuthorizeURL(state, redirectURI string) string
	// Exchange trades an authorization code for tokens (INTERNAL host). redirectURI
	// MUST equal the one used in AuthorizeURL (OAuth2 requires the match).
	Exchange(ctx context.Context, code, redirectURI string) (*OAuthToken, error)
	// FetchUser reads the authenticated user's profile (INTERNAL host / API).
	FetchUser(ctx context.Context, tok *OAuthToken) (*OAuthUser, error)
}

// OAuthToken is the result of a code exchange. RefreshToken/Expiry are empty/zero
// when the provider does not issue them.
type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time // zero = no expiry advertised
}

// OAuthUser is the normalized identity a provider returns for the logged-in user.
type OAuthUser struct {
	ProviderUID string // stable per-provider id (stringified)
	Username    string // login handle
	DisplayName string // human name (falls back to Username)
	AvatarURL   string
}

// oauthClient is the shared HTTP implementation. Per-provider constructors fill
// in the endpoint URLs and the profile field mapping; the flow (authorize URL,
// form-encoded code exchange, bearer profile fetch) is identical across the three
// hosts we support.
type oauthClient struct {
	id           domain.GitProvider
	clientID     string
	clientSecret string
	authorizeURL string // full URL on the EXTERNAL host
	tokenURL     string // full URL on the INTERNAL host
	userURL      string // full URL on the INTERNAL host / API
	scope        string
	http         *http.Client
	parseUser    func(map[string]any) *OAuthUser
}

func (c *oauthClient) ID() domain.GitProvider { return c.id }

func (c *oauthClient) AuthorizeURL(state, redirectURI string) string {
	q := url.Values{}
	q.Set("client_id", c.clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	if c.scope != "" {
		q.Set("scope", c.scope)
	}
	sep := "?"
	if strings.Contains(c.authorizeURL, "?") {
		sep = "&"
	}
	return c.authorizeURL + sep + q.Encode()
}

// tokenResp is the union of the three providers' token response shapes.
type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (c *oauthClient) Exchange(ctx context.Context, code, redirectURI string) (*OAuthToken, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub returns form-encoded by default; Accept: application/json forces JSON
	// for all three so one decoder handles them.
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s token exchange: %w", c.id, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s token exchange: status %d: %s", c.id, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("%s decode token: %w", c.id, err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("%s token exchange error: %s %s", c.id, tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("%s token exchange: no access_token in response", c.id)
	}
	tok := &OAuthToken{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if tr.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC()
	}
	return tok, nil
}

func (c *oauthClient) FetchUser(ctx context.Context, tok *OAuthToken) (*OAuthUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s fetch user: %w", c.id, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s fetch user: status %d: %s", c.id, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%s decode user: %w", c.id, err)
	}
	u := c.parseUser(raw)
	if u == nil || u.ProviderUID == "" {
		return nil, fmt.Errorf("%s fetch user: response missing id", c.id)
	}
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}
	return u, nil
}

var _ OAuthProvider = (*oauthClient)(nil)

// OAuthConfig carries the per-provider OAuth configuration (env-derived). Empty
// ExternalURL/InternalURL take the provider's public defaults (github/gitlab).
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	ExternalURL  string // browser-reachable provider root
	InternalURL  string // server-to-server provider root
}

// jsonField reads a string-or-number field from a decoded JSON object, returning
// the stringified value ("" if absent). Provider ids arrive as JSON numbers.
func jsonField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// NewGiteaOAuth builds an OAuthProvider for a Gitea instance. Endpoints:
// authorize /login/oauth/authorize, token /login/oauth/access_token, user
// /api/v1/user.
func NewGiteaOAuth(cfg OAuthConfig) OAuthProvider {
	ext := trimBase(cfg.ExternalURL)
	in := trimBase(cfg.InternalURL)
	return &oauthClient{
		id:           domain.ProviderGitea,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		authorizeURL: ext + "/login/oauth/authorize",
		tokenURL:     in + "/login/oauth/access_token",
		userURL:      in + "/api/v1/user",
		http:         &http.Client{Timeout: 15 * time.Second},
		parseUser: func(m map[string]any) *OAuthUser {
			return &OAuthUser{
				ProviderUID: jsonField(m, "id"),
				Username:    jsonField(m, "login"),
				DisplayName: jsonField(m, "full_name"),
				AvatarURL:   jsonField(m, "avatar_url"),
			}
		},
	}
}

// NewGitHubOAuth builds an OAuthProvider for GitHub (or GHE). Endpoints:
// authorize /login/oauth/authorize, token /login/oauth/access_token on the login
// host; user /user on the API host (api.github.com for github.com, else the
// internal root — good enough for the httptest unit path).
func NewGitHubOAuth(cfg OAuthConfig) OAuthProvider {
	ext := trimBase(orDefault(cfg.ExternalURL, "https://github.com"))
	in := trimBase(orDefault(cfg.InternalURL, "https://github.com"))
	apiBase := in
	if host := hostOf(in); host == "github.com" || strings.HasSuffix(host, ".github.com") {
		apiBase = "https://api.github.com"
	}
	return &oauthClient{
		id:           domain.ProviderGitHub,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		authorizeURL: ext + "/login/oauth/authorize",
		tokenURL:     in + "/login/oauth/access_token",
		userURL:      apiBase + "/user",
		scope:        "read:user",
		http:         &http.Client{Timeout: 15 * time.Second},
		parseUser: func(m map[string]any) *OAuthUser {
			return &OAuthUser{
				ProviderUID: jsonField(m, "id"),
				Username:    jsonField(m, "login"),
				DisplayName: jsonField(m, "name"),
				AvatarURL:   jsonField(m, "avatar_url"),
			}
		},
	}
}

// NewGitLabOAuth builds an OAuthProvider for GitLab. Endpoints: authorize
// /oauth/authorize, token /oauth/token, user /api/v4/user.
func NewGitLabOAuth(cfg OAuthConfig) OAuthProvider {
	ext := trimBase(orDefault(cfg.ExternalURL, "https://gitlab.com"))
	in := trimBase(orDefault(cfg.InternalURL, "https://gitlab.com"))
	return &oauthClient{
		id:           domain.ProviderGitLab,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		authorizeURL: ext + "/oauth/authorize",
		tokenURL:     in + "/oauth/token",
		userURL:      in + "/api/v4/user",
		scope:        "read_user",
		http:         &http.Client{Timeout: 15 * time.Second},
		parseUser: func(m map[string]any) *OAuthUser {
			return &OAuthUser{
				ProviderUID: jsonField(m, "id"),
				Username:    jsonField(m, "username"),
				DisplayName: jsonField(m, "name"),
				AvatarURL:   jsonField(m, "avatar_url"),
			}
		},
	}
}

func trimBase(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
