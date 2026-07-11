// Package kanbancfg resolves the EFFECTIVE cluster-level jtype kanban
// configuration — the jtype base URL + optional cluster fallback token the
// poller, the reconciler writeback pass, the create-link column validation and
// the console status all share (D27).
//
// Two sources, in precedence order (DB > env, mirroring modelcfg's catalog > env):
//
//   - DB: the single-row cluster_kanban_config a cluster admin sets from the
//     console. Present ⇒ Source=db, base URL + (decrypted) cluster fallback token
//     come from the row.
//   - env: the JTYPE_BASE_URL/JTYPE_TOKEN environment fallback (retained for
//     backward compatibility, D25). Applies ONLY when there is no DB row and
//     JTYPE_BASE_URL is non-empty.
//   - none: neither ⇒ the integration is OFF (a fail-visible clean no-op, never
//     a mock — CLAUDE.md red line #1).
//
// CRITICAL — the cluster fallback token is SOURCE-COUPLED to the base URL: a DB
// config never borrows the env JTYPE_TOKEN and an env config never borrows a DB
// token. A PAT minted for one jtype instance must not silently authenticate
// against another (D27).
//
// Cipher discipline (mirrors modelcfg.resolveModel): a DB row with a token_enc
// blob but no cipher (AUTH_TOKEN_KEY unset after a token was stored) is surfaced
// as an ERROR, never a silent fallback to the env token — the console then shows
// an honest "kanban disabled: <reason>" rather than acting on the wrong instance.
package kanbancfg

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/store"
)

// resolverTTL is the safety-net freshness bound for a cached resolution. The
// primary consistency mechanism is Invalidate() (called by the /system/kanban
// write handlers after a successful write; the API, poller and reconciler share
// ONE Resolver instance, wired in main.go). The TTL only bounds staleness for
// writers this process didn't see — e.g. a second orchestrator replica. Matches
// modelcfg's 3s.
const resolverTTL = 3 * time.Second

// Source labels where the effective config came from.
type Source string

const (
	// SourceDB: the single-row cluster_kanban_config (console-managed, D27).
	SourceDB Source = "db"
	// SourceEnv: the JTYPE_BASE_URL/JTYPE_TOKEN environment fallback (D25).
	SourceEnv Source = "env"
	// SourceNone: nothing usable — the integration is OFF (fail-visible no-op).
	SourceNone Source = "none"
)

// Effective is the materialised cluster kanban config. ClusterToken is the
// DECRYPTED cluster fallback token (empty when none); callers must NEVER
// serialise it to API clients — expose only ClusterTokenSet.
type Effective struct {
	Source          Source
	BaseURL         string
	ClusterToken    string
	ClusterTokenSet bool
}

// Enabled reports whether a usable base URL was resolved (the integration is on).
func (e Effective) Enabled() bool { return e.Source != SourceNone && e.BaseURL != "" }

// ConfigReader is the slice of the store the resolver needs (a store.Store or a
// test stub). It is the seam the resolver depends on.
type ConfigReader interface {
	GetClusterKanbanConfig(ctx context.Context) (*domain.KanbanConfig, error)
}

// Resolver resolves (and caches) the effective cluster kanban config so the hot
// paths (every poll tick, every writeback pass) don't pay a DB read + AES
// decryption each time. A successful resolution is cached for resolverTTL; errors
// are NEVER cached (a transient DB blip or a config error must not stick). It
// also pools ONE *jtype.Factory keyed by the resolved base URL, rebuilding only
// when that URL changes so the shared HTTP connection pool survives. Safe for
// concurrent use.
type Resolver struct {
	st     ConfigReader
	cipher *auth.Cipher
	cfg    *config.Config
	now    func() time.Time // injectable clock for tests

	mu       sync.Mutex
	cached   *Effective // last successful resolution (nil = cold/invalidated)
	cachedAt time.Time
	// factory pool: one client factory keyed by the resolved base URL. Rebuilt
	// only when factoryBase changes (a base-URL change), so an unchanged config
	// keeps its HTTP pool across ticks.
	factory     *jtype.Factory
	factoryBase string
}

// NewResolver builds a Resolver over the given store/cipher/config. cipher may be
// nil (no AUTH_TOKEN_KEY): a config WITHOUT a token still resolves; one WITH a DB
// token surfaces the decryption error (fail-visible), never a silent env fallback.
func NewResolver(st ConfigReader, cipher *auth.Cipher, cfg *config.Config) *Resolver {
	return &Resolver{st: st, cipher: cipher, cfg: cfg, now: time.Now}
}

// Effective returns the materialised cluster kanban config, serving a cached
// value while fresh. Concurrent callers past an expired entry may resolve in
// parallel (no lock is held across the DB read); the duplicate work is harmless.
func (r *Resolver) Effective(ctx context.Context) (Effective, error) {
	r.mu.Lock()
	if r.cached != nil && r.now().Sub(r.cachedAt) < resolverTTL {
		v := *r.cached
		r.mu.Unlock()
		return v, nil
	}
	r.mu.Unlock()

	v, err := r.resolve(ctx)
	if err != nil {
		return Effective{}, err // never cache an error
	}
	r.mu.Lock()
	r.cached = &v
	r.cachedAt = r.now()
	r.mu.Unlock()
	return v, nil
}

// resolve runs the DB > env > none chain once (uncached). See the package doc for
// the source-coupling + cipher-discipline invariants.
func (r *Resolver) resolve(ctx context.Context) (Effective, error) {
	row, err := r.st.GetClusterKanbanConfig(ctx)
	switch {
	case err == nil:
		// DB source. The cluster fallback token, if any, comes ONLY from this row
		// (source-coupled) — never the env JTYPE_TOKEN.
		eff := Effective{Source: SourceDB, BaseURL: row.BaseURL}
		if len(row.TokenEnc) > 0 {
			// cipher==nil ⇒ DecryptString returns ErrCipherNotConfigured, surfaced
			// as an error (never a silent env fallback). Mirrors modelcfg.
			tok, derr := r.cipher.DecryptString(row.TokenEnc)
			if derr != nil {
				return Effective{}, derr
			}
			eff.ClusterToken = tok
			eff.ClusterTokenSet = true
		}
		return eff, nil
	case errors.Is(err, store.ErrNotFound):
		// No DB row — fall through to the env fallback.
	default:
		return Effective{}, err
	}

	// Env fallback (D25): only when JTYPE_BASE_URL is set. The cluster token, if
	// any, comes ONLY from the env (source-coupled) — never a DB token.
	if r.cfg != nil && r.cfg.JtypeBaseURL != "" {
		return Effective{
			Source:          SourceEnv,
			BaseURL:         r.cfg.JtypeBaseURL,
			ClusterToken:    r.cfg.JtypeToken,
			ClusterTokenSet: r.cfg.JtypeToken != "",
		}, nil
	}
	return Effective{Source: SourceNone}, nil
}

// Factory returns the pooled *jtype.Factory for the resolved base URL, the
// effective cluster fallback token (source-coupled), and ok=false when the
// integration is OFF or the config errored (e.g. DB token + no cipher). The
// caller builds a token-bound client with f.Client(token). off ⇒ (nil,"",false)
// so the poller/writeback are a clean visible no-op. A base-URL change rebuilds
// the factory (new HTTP pool); an unchanged URL reuses it.
func (r *Resolver) Factory(ctx context.Context) (*jtype.Factory, string, bool) {
	eff, err := r.Effective(ctx)
	if err != nil || !eff.Enabled() {
		return nil, "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factory == nil || r.factoryBase != eff.BaseURL {
		r.factory = jtype.NewFactory(eff.BaseURL, 0)
		r.factoryBase = eff.BaseURL
	}
	if r.factory == nil { // defensive: NewFactory only returns nil for an empty URL
		return nil, "", false
	}
	return r.factory, eff.ClusterToken, true
}

// Invalidate drops the cached resolution so the next Effective/Factory re-reads
// the store. Called after every successful /system/kanban write (PUT/DELETE) so a
// console change activates WITHOUT a restart (the fail-visible red line: a stored
// base URL that didn't take effect would be a silent no-op). The factory pool is
// intentionally retained: the next Factory rebuilds it only if the base URL
// actually changed, preserving the HTTP pool otherwise.
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	r.cached = nil
	r.cachedAt = time.Time{}
	r.mu.Unlock()
}
