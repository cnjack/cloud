package modelcfg

import (
	"context"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
)

// resolverTTL is the safety-net freshness bound for a cached materialisation. The
// primary consistency mechanism is Invalidate() (called by the model catalog
// write handlers after a successful write; the API and reconciler share ONE
// Resolver instance, wired in main.go). The TTL only bounds staleness for writers
// this process didn't see — e.g. a second orchestrator replica.
const resolverTTL = 3 * time.Second

// Resolver caches MATERIALISED model configs so the hot paths (every reconcile
// tick, every LLM proxy request) don't pay a DB read + AES decryption each time.
// The cache is keyed by model id ("" for the env fallback); successful
// resolutions are cached for resolverTTL, errors are NEVER cached (a transient DB
// blip must not stick). SELECTION (SelectModel) is not cached — it runs once per
// run create. Safe for concurrent use.
type Resolver struct {
	st     ConfigReader
	cipher *auth.Cipher
	cfg    *config.Config
	now    func() time.Time // injectable clock for tests

	mu     sync.Mutex
	cache  map[string]cacheEntry // keyed by model id ("" = env fallback)
}

type cacheEntry struct {
	v  Resolved
	at time.Time
}

// NewResolver builds a Resolver over the given store/cipher/config. cipher may be
// nil (no AUTH_TOKEN_KEY): a model without a key still resolves; one WITH a key
// surfaces the decryption error.
func NewResolver(st ConfigReader, cipher *auth.Cipher, cfg *config.Config) *Resolver {
	return &Resolver{st: st, cipher: cipher, cfg: cfg, now: time.Now, cache: map[string]cacheEntry{}}
}

// ResolveModel materialises a stamped run.model_id (or the env fallback when
// modelID == "") into the effective config, serving a cached value while fresh.
// Concurrent callers past an expired entry may resolve in parallel (no lock is
// held across the DB read); the duplicate work is harmless.
func (r *Resolver) ResolveModel(ctx context.Context, modelID string) (Resolved, error) {
	r.mu.Lock()
	if e, ok := r.cache[modelID]; ok && r.now().Sub(e.at) < resolverTTL {
		v := e.v
		r.mu.Unlock()
		return v, nil
	}
	r.mu.Unlock()

	v, err := resolveModel(ctx, r.st, r.cipher, r.cfg, modelID)
	if err != nil {
		return Resolved{}, err // never cache an error
	}
	r.mu.Lock()
	r.cache[modelID] = cacheEntry{v: v, at: r.now()}
	r.mu.Unlock()
	return v, nil
}

// SelectModel runs the resolution chain to CHOOSE a model for a new run against
// the given project/service. It is uncached (per-create) and reads grants live so
// a just-granted model is immediately usable. Returns the chosen Selection
// (ModelID "" => env fallback; ModelName is the audit snapshot) and a typed
// outcome.
func (r *Resolver) SelectModel(ctx context.Context, projectID, defaultModelID, requested string) (Selection, SelectOutcome, error) {
	return selectModel(ctx, r.st, r.cfg, projectID, defaultModelID, requested)
}

// Invalidate drops the whole materialisation cache so the next ResolveModel
// re-reads the store. Called after every successful catalog write (create /
// update / delete / grant / revoke).
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	r.cache = map[string]cacheEntry{}
	r.mu.Unlock()
}
