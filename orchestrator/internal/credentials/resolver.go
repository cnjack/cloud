// Package credentials resolves the git-provider token the M3 control plane acts
// with on behalf of a run: it pushes the branch and opens the draft PR / posts
// the review as the TRIGGERING USER (their stored OAuth token), falling back to
// the global gitea PAT for legacy / service-principal runs that have no user
// (blueprint §3). Tokens are decrypted here and NEVER logged — callers receive a
// Source label for logging instead.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// ErrNoCredential means neither a user identity nor a fallback PAT is available
// for the provider, so the run cannot be pushed/reviewed on anyone's behalf.
var ErrNoCredential = errors.New("no credential available for provider")

// Token is a resolved credential. Value is the secret (never log it); Scheme is
// how a REST client authenticates ("token" for a PAT, "Bearer" for OAuth);
// Source is a redaction-safe label for logs.
type Token struct {
	Value  string
	Scheme string
	Source string
}

// AuthedURL injects this token into an http(s) clone/push URL as userinfo, using
// the per-provider convention (github: x-access-token, gitlab: oauth2, gitea:
// the token as the username). A non-http URL (e.g. a raw git://) or an empty
// token is returned unchanged. The credential is placed in the URL passed to a
// git subprocess only — never logged (gitcli redacts). An empty-token receiver
// yields the bare URL so a public repo still clones anonymously.
func (t Token) AuthedURL(rawURL string, prov domain.GitProvider) string {
	if t.Value == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return rawURL
	}
	switch prov {
	case domain.ProviderGitHub:
		u.User = url.UserPassword("x-access-token", t.Value)
	case domain.ProviderGitLab:
		u.User = url.UserPassword("oauth2", t.Value)
	default: // gitea + anything else: token as the username
		u.User = url.User(t.Value)
	}
	return u.String()
}

// Resolver resolves per-run provider credentials.
type Resolver struct {
	st       store.Store
	cipher   *auth.Cipher
	oauth    map[domain.GitProvider]provider.OAuthProvider
	giteaPAT string
	log      *slog.Logger
	now      func() time.Time
}

// NewResolver builds a Resolver. cipher/oauth may be nil/empty (no OAuth
// configured), in which case only the gitea PAT fallback is available.
func NewResolver(st store.Store, cipher *auth.Cipher, oauth map[domain.GitProvider]provider.OAuthProvider, giteaPAT string, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{
		st:       st,
		cipher:   cipher,
		oauth:    oauth,
		giteaPAT: giteaPAT,
		log:      log,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Resolve returns the token to act with for a run on provider `prov` triggered by
// userID (nil for a service-principal / legacy run). It prefers the user's stored
// identity token (refreshing it first if expired) and falls back to the global
// gitea PAT. The returned token value is never logged by this package.
func (r *Resolver) Resolve(ctx context.Context, prov domain.GitProvider, userID *string) (Token, error) {
	if userID != nil && *userID != "" && r.cipher != nil {
		if tok, ok := r.userToken(ctx, prov, *userID); ok {
			return tok, nil
		}
	}
	// Fallback: the global gitea PAT (only valid for the gitea host).
	if prov == domain.ProviderGitea && r.giteaPAT != "" {
		return Token{Value: r.giteaPAT, Scheme: "token", Source: "gitea_pat"}, nil
	}
	return Token{}, fmt.Errorf("%w: %s (user=%v)", ErrNoCredential, prov, userID != nil)
}

// userToken tries to resolve the triggering user's stored token for prov,
// refreshing it when expired. Returns ok=false (and logs at debug) when the user
// has no identity on prov or the token cannot be decrypted, so the caller can
// fall back to the PAT.
func (r *Resolver) userToken(ctx context.Context, prov domain.GitProvider, userID string) (Token, bool) {
	id, err := r.st.GetIdentityForUser(ctx, userID, prov)
	if err != nil {
		return Token{}, false // no identity on this provider
	}
	access, err := r.cipher.DecryptString(id.AccessTokenEnc)
	if err != nil {
		r.log.Warn("credentials: decrypt access token failed; falling back", "provider", prov, "identity", id.ID)
		return Token{}, false
	}
	// Refresh when expired and a refresh path exists.
	if id.TokenExpiresAt != nil && r.now().After(*id.TokenExpiresAt) {
		if refreshed, ok := r.refresh(ctx, prov, id); ok {
			access = refreshed
		}
	}
	return Token{Value: access, Scheme: "Bearer", Source: "user_oauth:" + id.Username}, true
}

// refresh exchanges the stored refresh token for a fresh access token and
// persists the re-encrypted pair. Returns ok=false (keeping the old access
// token) when no refresh token / provider is available or the exchange fails.
func (r *Resolver) refresh(ctx context.Context, prov domain.GitProvider, id *domain.UserIdentity) (string, bool) {
	op := r.oauth[prov]
	if op == nil || len(id.RefreshTokenEnc) == 0 {
		return "", false
	}
	refreshTok, err := r.cipher.DecryptString(id.RefreshTokenEnc)
	if err != nil {
		return "", false
	}
	tok, err := op.Refresh(ctx, refreshTok)
	if err != nil {
		r.log.Warn("credentials: token refresh failed; using existing", "provider", prov, "identity", id.ID)
		return "", false
	}
	accessEnc, err := r.cipher.EncryptString(tok.AccessToken)
	if err != nil {
		return "", false
	}
	var refreshEnc []byte
	if tok.RefreshToken != "" {
		if enc, err := r.cipher.EncryptString(tok.RefreshToken); err == nil {
			refreshEnc = enc
		}
	}
	var expiresAt *time.Time
	if !tok.Expiry.IsZero() {
		e := tok.Expiry
		expiresAt = &e
	}
	if err := r.st.UpdateIdentityToken(ctx, id.ID, accessEnc, refreshEnc, expiresAt); err != nil {
		r.log.Warn("credentials: persist refreshed token failed", "provider", prov, "identity", id.ID)
		// The refreshed token is still usable this tick even if persistence failed.
	}
	return tok.AccessToken, true
}
