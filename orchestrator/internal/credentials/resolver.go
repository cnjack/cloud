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
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

// ErrNoCredential means neither a user identity nor a fallback PAT is available
// for the provider, so the run cannot be pushed/reviewed on anyone's behalf.
var ErrNoCredential = errors.New("no credential available for provider")

// ErrIntegrationCredential means a service is bound to an integration (D19 / F5)
// but its bot token could not be resolved (missing integration, no cipher, or a
// decryption failure). It is a FAIL-VISIBLE error: the resolver NEVER silently
// falls back to the triggering user's personal OAuth for an integration-bound
// service — an integration means "always act as the bot", so a broken bot
// credential must surface, not degrade to a different identity (CLAUDE.md red
// line #1).
var ErrIntegrationCredential = errors.New("integration credential unavailable")

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
	// depOnce throttles the GITEA_TOKEN-fallback deprecation notice to ONCE per
	// process (F5 review P3): the fallback fires on every reconcile tick / PR
	// poll, and a per-use Warn floods the logs without adding signal.
	depOnce sync.Once
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

// ResolveForService returns the token a RUN acts with, honouring the service's
// integration binding (D19 / F5):
//
//   - svc.IntegrationID set → ALWAYS the integration's BOT token, regardless of
//     who triggered the run. It NEVER consults the triggering user's personal
//     OAuth, and a broken bot credential is a FAIL-VISIBLE error
//     (ErrIntegrationCredential) — never a silent fall back to a different
//     identity (CLAUDE.md red line #1).
//   - svc.IntegrationID nil → the legacy path (Resolve below): the triggering
//     user's personal OAuth, falling back to the cluster GITEA_TOKEN.
//
// svc must be non-nil (every run-based caller has loaded it).
func (r *Resolver) ResolveForService(ctx context.Context, svc *domain.Service, userID *string) (Token, error) {
	if svc != nil && svc.IntegrationID != nil && *svc.IntegrationID != "" {
		return r.integrationToken(ctx, *svc.IntegrationID)
	}
	var prov domain.GitProvider
	if svc != nil {
		prov = svc.Provider
	}
	return r.Resolve(ctx, prov, userID)
}

// integrationToken decrypts and returns a service integration's bot token. Every
// failure path is fail-visible (ErrIntegrationCredential) — the caller must NOT
// degrade to a personal-OAuth path for an integration-bound service.
func (r *Resolver) integrationToken(ctx context.Context, integrationID string) (Token, error) {
	integ, err := r.st.GetIntegration(ctx, integrationID)
	if err != nil {
		return Token{}, fmt.Errorf("%w: load integration %s: %v", ErrIntegrationCredential, integrationID, err)
	}
	if r.cipher == nil {
		return Token{}, fmt.Errorf("%w: integration %q cannot be decrypted (AUTH_TOKEN_KEY not configured)", ErrIntegrationCredential, integ.Name)
	}
	value, err := r.cipher.DecryptString(integ.TokenEnc)
	if err != nil {
		r.log.Warn("credentials: decrypt integration token failed", "integration", integ.ID, "provider", integ.Provider)
		return Token{}, fmt.Errorf("%w: integration %q token decrypt failed", ErrIntegrationCredential, integ.Name)
	}
	return Token{Value: value, Scheme: patScheme(integ.Provider), Source: "integration:" + integ.Name}, nil
}

// patScheme is the REST auth scheme for a PAT on a provider: gitea authenticates a
// PAT with the "token" scheme; github/gitlab REST clients ignore the scheme (they
// hardcode Bearer), so "Bearer" is a harmless, honest label there.
func patScheme(prov domain.GitProvider) string {
	if prov == domain.ProviderGitea {
		return "token"
	}
	return "Bearer"
}

// Resolve returns the token to act with for a run on provider `prov` triggered by
// userID (nil for a service-principal / legacy run). It prefers the user's stored
// identity token (refreshing it first if expired) and falls back to the global
// gitea PAT. The returned token value is never logged by this package. This is the
// LEGACY path (no integration): ResolveForService routes here for unbound
// services, and non-run callers (repo picker, webhook receipt) call it directly.
func (r *Resolver) Resolve(ctx context.Context, prov domain.GitProvider, userID *string) (Token, error) {
	if userID != nil && *userID != "" && r.cipher != nil {
		if tok, ok := r.userToken(ctx, prov, *userID); ok {
			return tok, nil
		}
	}
	// Fallback: the global gitea PAT (only valid for the gitea host). This is a
	// deprecated legacy path (F5 / D19): new services should bind an integration.
	// The deprecation notice fires ONCE per process (P3) — the fallback is hit on
	// every tick otherwise.
	if prov == domain.ProviderGitea && r.giteaPAT != "" {
		r.depOnce.Do(func() {
			r.log.Warn("credentials: using deprecated cluster GITEA_TOKEN fallback; bind a project integration instead (D19)")
		})
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
