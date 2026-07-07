package modelcfg

import (
	"context"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
)

// resolverTTL is the safety-net freshness bound for a cached resolution. The
// primary consistency mechanism is Invalidate() (called by the PUT/DELETE
// handlers after a successful write, and both the API and the reconciler share
// ONE Resolver instance, wired in main.go). The TTL only bounds staleness for
// writers this process didn't see — e.g. a second orchestrator replica.
const resolverTTL = 3 * time.Second

// Resolver caches the effective model config so the hot paths (every run
// mutation, every reconcile tick, every webhook mention) don't pay a DB read +
// AES decryption each time. Successful resolutions are cached for resolverTTL;
// errors are NEVER cached (a transient DB blip must not stick). Safe for
// concurrent use.
type Resolver struct {
	st     ConfigReader
	cipher *auth.Cipher
	cfg    *config.Config
	now    func() time.Time // injectable clock for tests

	mu       sync.Mutex
	cached   Resolved
	cachedAt time.Time
	has      bool
}

// NewResolver builds a Resolver over the given store/cipher/config. cipher may
// be nil (no AUTH_TOKEN_KEY): a DB row without a key still resolves; one WITH a
// key surfaces the decryption error.
func NewResolver(st ConfigReader, cipher *auth.Cipher, cfg *config.Config) *Resolver {
	return &Resolver{st: st, cipher: cipher, cfg: cfg, now: time.Now}
}

// Resolve returns the effective model config, serving a cached value while it
// is fresh. Concurrent callers past an expired cache may resolve in parallel
// (no lock is held across the DB read); the duplicate work is harmless.
func (r *Resolver) Resolve(ctx context.Context) (Resolved, error) {
	r.mu.Lock()
	if r.has && r.now().Sub(r.cachedAt) < resolverTTL {
		v := r.cached
		r.mu.Unlock()
		return v, nil
	}
	r.mu.Unlock()

	v, err := Resolve(ctx, r.st, r.cipher, r.cfg)
	if err != nil {
		return Resolved{}, err // never cache an error
	}
	r.mu.Lock()
	r.cached, r.cachedAt, r.has = v, r.now(), true
	r.mu.Unlock()
	return v, nil
}

// Invalidate drops the cached value so the next Resolve re-reads the store.
// Called after every successful PUT/DELETE of the model config.
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	r.has = false
	r.mu.Unlock()
}
