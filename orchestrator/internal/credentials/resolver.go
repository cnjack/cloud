// Package credentials resolves the git-provider token the M3 control plane acts
// with on behalf of a run: it pushes the branch and opens the draft PR / posts
// the review as the TRIGGERING USER (their stored OAuth token), falling back to
// the global gitea PAT for legacy / service-principal runs that have no user
// (blueprint §3). Tokens are decrypted here and NEVER logged — callers receive a
// Source label for logging instead.
package credentials

import (
	"bytes"
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

// ErrPluginCredentialUnavailable means an installation which a run snapshotted
// cannot currently issue a short-lived runtime credential.  It is deliberately
// distinct from ErrNoCredential: a snapshot is an explicit promise made at run
// launch, and callers must surface a reconnect/action-required state rather
// than silently falling back to a user's identity or a cluster secret.
var ErrPluginCredentialUnavailable = errors.New("plugin runtime credential unavailable")

// Token is a resolved credential. Value is the secret (never log it); Scheme is
// how a REST client authenticates ("token" for a PAT, "Bearer" for OAuth);
// Source is a redaction-safe label for logs.
type Token struct {
	Value  string
	Scheme string
	Source string
}

// PluginCredential is the intentionally small secret-bearing response for a
// runner sidecar. It never contains refresh tokens, provider app private keys,
// or the cluster master key. BaseURL is needed only to write CLI/MCP config.
type PluginCredential struct {
	Provider    domain.ProviderKind `json:"provider"`
	BaseURL     string              `json:"base_url"`
	AccessToken string              `json:"access_token"`
	Scheme      string              `json:"scheme"`
	ExpiresAt   *time.Time          `json:"expires_at,omitempty"`
}

// PluginCredentialIssuer is the seam between the run-token endpoint and
// provider-specific credential minting. The default resolver decrypts a
// current installation access token. A GitHub App issuer can later mint an
// installation token here without coupling HTTP handlers to private keys.
type PluginCredentialIssuer interface {
	IssueRunPluginCredential(context.Context, *domain.PluginInstallation, *domain.ProviderConfig) (PluginCredential, error)
	// IssueRunPluginSnapshotCredential uses only immutable launch-time material.
	// It is exclusively for a durable run's internal credential endpoint; unlike
	// the current-installation method it must never read or update a newer grant
	// after an administrator changes a Provider or reconnects the Plugin.
	IssueRunPluginSnapshotCredential(context.Context, *domain.RunPluginSnapshot) (PluginCredential, error)
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

// IssueRunPluginCredential returns only an installation's current access
// credential. OAuth refresh happens here in the control plane; refresh tokens
// remain encrypted at rest and are never returned to the task sidecar.
func (r *Resolver) IssueRunPluginCredential(ctx context.Context, in *domain.PluginInstallation, cfg *domain.ProviderConfig) (PluginCredential, error) {
	return r.issueRunPluginCredential(ctx, in, cfg, true)
}

// IssueRunPluginSnapshotCredential deliberately reconstructs the issuer inputs
// from a frozen run snapshot.  A config change, Plugin disablement, or reconnect
// then affects new tasks only; it cannot cause a durable task's old grant to be
// sent to the new Provider URL. Refreshes remain control-plane-only and rotate
// only the same frozen grant version, never a later reconnect's version.
func (r *Resolver) IssueRunPluginSnapshotCredential(ctx context.Context, snapshot *domain.RunPluginSnapshot) (PluginCredential, error) {
	if snapshot == nil || !snapshot.HasFrozenRuntimeMaterial() {
		return PluginCredential{}, fmt.Errorf("%w: run snapshot is incomplete", ErrPluginCredentialUnavailable)
	}
	in := snapshot.FrozenInstallation()
	credential, err := r.issueRunPluginCredential(ctx, in, snapshot.FrozenProviderConfig(), false)
	if err != nil {
		return PluginCredential{}, err
	}
	// Refresh-token rotation is mutable *inside the same frozen grant version*.
	// The Store's guarded update synchronizes a live Installation only when it
	// still references this version, so a reconnect's newer version is never
	// overwritten by an old run's refresh.
	if !bytes.Equal(in.AccessTokenEnc, snapshot.AccessTokenEnc) ||
		!bytes.Equal(in.RefreshTokenEnc, snapshot.RefreshTokenEnc) ||
		!samePluginCredentialExpiry(in.TokenExpiresAt, snapshot.TokenExpiresAt) {
		if r.st == nil {
			return PluginCredential{}, fmt.Errorf("%w: snapshot credential store is unavailable", ErrPluginCredentialUnavailable)
		}
		if err := r.st.RotatePluginCredentialVersion(ctx, &domain.PluginCredentialVersion{
			ID: snapshot.CredentialVersionID, InstallationID: snapshot.InstallationID, Provider: snapshot.Provider,
			AccessTokenEnc: in.AccessTokenEnc, RefreshTokenEnc: in.RefreshTokenEnc, TokenExpiresAt: in.TokenExpiresAt,
		}); err != nil {
			return PluginCredential{}, fmt.Errorf("%w: persist rotated snapshot credential", ErrPluginCredentialUnavailable)
		}
	}
	return credential, nil
}

func samePluginCredentialExpiry(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (r *Resolver) issueRunPluginCredential(ctx context.Context, in *domain.PluginInstallation, cfg *domain.ProviderConfig, persistRefresh bool) (PluginCredential, error) {
	if r == nil || r.cipher == nil || in == nil || cfg == nil {
		return PluginCredential{}, ErrPluginCredentialUnavailable
	}
	if !domain.ValidProviderKind(in.Provider) || in.Provider != cfg.Provider {
		return PluginCredential{}, fmt.Errorf("%w: %s requires reconnect or an installation-token issuer", ErrPluginCredentialUnavailable, in.Provider)
	}
	if !cfg.PluginEnabled || in.ConfigRevision != cfg.ConfigRevision {
		return PluginCredential{}, fmt.Errorf("%w: %s provider configuration changed or the Plugin capability is disabled", ErrPluginCredentialUnavailable, in.Provider)
	}
	// GitHub uses App installation tokens at runtime, never a user OAuth grant.
	// The App key is decrypted only long enough to mint a short-lived token in
	// this control-plane process; neither the key nor a refresh token reaches
	// the response or the runner pod.
	if in.Provider == domain.PluginGitHub && len(in.AccessTokenEnc) == 0 {
		privateKey, err := r.cipher.DecryptString(cfg.AppPrivateKeyEnc)
		if err != nil || privateKey == "" || in.GitHubInstallID == "" {
			return PluginCredential{}, fmt.Errorf("%w: GitHub App configuration or installation id is missing", ErrPluginCredentialUnavailable)
		}
		issuer, err := provider.NewGitHubAppIssuer(cfg.AppID, []byte(privateKey))
		if err != nil {
			return PluginCredential{}, fmt.Errorf("%w: GitHub App configuration is invalid", ErrPluginCredentialUnavailable)
		}
		issued, err := issuer.IssueInstallationToken(ctx, in.GitHubInstallID)
		if err != nil {
			return PluginCredential{}, fmt.Errorf("%w: GitHub installation token could not be minted", ErrPluginCredentialUnavailable)
		}
		return PluginCredential{Provider: in.Provider, BaseURL: cfg.BaseURL, AccessToken: issued.Token, Scheme: "Bearer", ExpiresAt: &issued.ExpiresAt}, nil
	}
	if len(in.AccessTokenEnc) == 0 {
		return PluginCredential{}, fmt.Errorf("%w: %s access token is missing", ErrPluginCredentialUnavailable, in.Provider)
	}
	if in.TokenExpiresAt != nil && !r.now().Add(2*time.Minute).Before(*in.TokenExpiresAt) {
		if err := r.refreshPluginCredential(ctx, in, cfg); err != nil {
			if persistRefresh {
				in.Status = domain.PluginStatusActionRequired
				in.LastHealthError = "OAuth access expired and refresh failed; reconnect this Plugin"
				_ = r.st.UpdatePluginInstallation(ctx, in)
			}
			return PluginCredential{}, fmt.Errorf("%w: %s OAuth refresh failed", ErrPluginCredentialUnavailable, in.Provider)
		}
		if persistRefresh {
			// Refresh-token rotation stays within the currently referenced
			// immutable grant version. Appending a new version here would strand
			// an already-started run on the provider's now-invalid old refresh
			// token; only reconnect/identity replacement appends a new version.
			if r.st == nil || in.CredentialVersionID == "" {
				return PluginCredential{}, fmt.Errorf("%w: current plugin credential version is unavailable", ErrPluginCredentialUnavailable)
			}
			if err := r.st.RotatePluginCredentialVersion(ctx, &domain.PluginCredentialVersion{
				ID: in.CredentialVersionID, InstallationID: in.ID, Provider: in.Provider,
				AccessTokenEnc: in.AccessTokenEnc, RefreshTokenEnc: in.RefreshTokenEnc, TokenExpiresAt: in.TokenExpiresAt,
			}); err != nil {
				return PluginCredential{}, fmt.Errorf("%w: persist refreshed %s credential", ErrPluginCredentialUnavailable, in.Provider)
			}
		}
	}
	access, err := r.cipher.DecryptString(in.AccessTokenEnc)
	if err != nil || access == "" {
		return PluginCredential{}, fmt.Errorf("%w: %s token cannot be decrypted", ErrPluginCredentialUnavailable, in.Provider)
	}
	scheme := "Bearer"
	return PluginCredential{
		Provider: in.Provider, BaseURL: cfg.BaseURL, AccessToken: access,
		Scheme: scheme, ExpiresAt: in.TokenExpiresAt,
	}, nil
}

// refreshPluginCredential refreshes the supplied credential in memory. Callers
// persist its encrypted result only into the same grant-version identity; they
// never write a later Provider configuration or reconnect credential row.
func (r *Resolver) refreshPluginCredential(ctx context.Context, in *domain.PluginInstallation, cfg *domain.ProviderConfig) error {
	if len(in.RefreshTokenEnc) == 0 || len(cfg.ClientSecretEnc) == 0 {
		return errors.New("refresh credential is unavailable")
	}
	refreshToken, err := r.cipher.DecryptString(in.RefreshTokenEnc)
	if err != nil || refreshToken == "" {
		return errors.New("refresh credential cannot be decrypted")
	}
	clientSecret, err := r.cipher.DecryptString(cfg.ClientSecretEnc)
	if err != nil || clientSecret == "" {
		return errors.New("provider OAuth client secret cannot be decrypted")
	}
	oauthConfig := provider.OAuthConfig{
		ClientID: cfg.ClientID, ClientSecret: clientSecret,
		ExternalURL: cfg.BaseURL, InternalURL: cfg.BaseURL,
	}
	var oauthProvider provider.OAuthProvider
	switch in.Provider {
	case domain.PluginGitLab:
		oauthProvider = provider.NewGitLabOAuth(oauthConfig)
	case domain.PluginGitea:
		oauthProvider = provider.NewGiteaOAuth(oauthConfig)
	default:
		return errors.New("provider does not support refresh")
	}
	refreshed, err := oauthProvider.Refresh(ctx, refreshToken)
	if err != nil || refreshed == nil || refreshed.AccessToken == "" {
		return errors.New("provider rejected refresh")
	}
	accessEnc, err := r.cipher.EncryptString(refreshed.AccessToken)
	if err != nil {
		return err
	}
	refreshEnc := append([]byte(nil), in.RefreshTokenEnc...)
	if refreshed.RefreshToken != "" {
		refreshEnc, err = r.cipher.EncryptString(refreshed.RefreshToken)
		if err != nil {
			return err
		}
	}
	in.AccessTokenEnc = accessEnc
	in.RefreshTokenEnc = refreshEnc
	if refreshed.Expiry.IsZero() {
		in.TokenExpiresAt = nil
	} else {
		expiry := refreshed.Expiry.UTC()
		in.TokenExpiresAt = &expiry
	}
	in.LastHealthError = ""
	now := r.now()
	in.LastHealthyAt = &now
	return nil
}

var _ PluginCredentialIssuer = (*Resolver)(nil)

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
	if svc != nil {
		if binding, err := r.st.GetServiceRepositoryBinding(ctx, svc.ID); err == nil {
			installation, loadErr := r.st.GetPluginInstallation(ctx, binding.InstallationID)
			if loadErr != nil {
				return Token{}, fmt.Errorf("%w: load project plugin installation", ErrIntegrationCredential)
			}
			cfg, cfgErr := r.st.GetProviderConfig(ctx, installation.Provider)
			if cfgErr != nil {
				return Token{}, fmt.Errorf("%w: load project plugin provider configuration", ErrIntegrationCredential)
			}
			credential, issueErr := r.IssueRunPluginCredential(ctx, installation, cfg)
			if issueErr != nil {
				return Token{}, fmt.Errorf("%w: project plugin credential unavailable", ErrIntegrationCredential)
			}
			return Token{Value: credential.AccessToken, Scheme: credential.Scheme, Source: "plugin:" + installation.ID}, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return Token{}, fmt.Errorf("%w: load service repository binding", ErrIntegrationCredential)
		}
	}
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
		return Token{}, fmt.Errorf("%w: integration %q cannot be decrypted (JCLOUD_MASTER_KEY not configured)", ErrIntegrationCredential, integ.Name)
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

// ResolveUserOAuth resolves only a named human user's OAuth grant. It is used by
// repository-administration actions such as webhook registration, where using a
// service integration or the legacy cluster PAT would blur who authorized the
// provider-side mutation. Unlike Resolve, it has no fallback path.
func (r *Resolver) ResolveUserOAuth(ctx context.Context, prov domain.GitProvider, userID string) (Token, error) {
	if r == nil || r.cipher == nil || userID == "" || !domain.ValidProvider(prov) {
		return Token{}, fmt.Errorf("%w: %s (user OAuth is not connected)", ErrNoCredential, prov)
	}
	if tok, ok := r.userToken(ctx, prov, userID); ok {
		return tok, nil
	}
	return Token{}, fmt.Errorf("%w: %s (user OAuth is not connected)", ErrNoCredential, prov)
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
