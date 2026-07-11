package kanbancfg

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// fakeReader is a controllable ConfigReader: it returns cfg (cloned), err, or
// ErrNotFound, and counts reads so cache/TTL behaviour can be asserted.
type fakeReader struct {
	cfg   *domain.KanbanConfig
	err   error
	reads int
}

func (f *fakeReader) GetClusterKanbanConfig(_ context.Context) (*domain.KanbanConfig, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	if f.cfg == nil {
		return nil, store.ErrNotFound
	}
	cp := *f.cfg
	return &cp, nil
}

func newTestCipher(t *testing.T) *auth.Cipher {
	t.Helper()
	c, err := auth.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return c
}

// Test 1: the DB > env > none resolution chain.
func TestResolverSource(t *testing.T) {
	ctx := context.Background()
	cipher := newTestCipher(t)

	// DB source: a row present wins over the env fallback.
	r := NewResolver(&fakeReader{cfg: &domain.KanbanConfig{BaseURL: "http://db"}}, cipher,
		&config.Config{JtypeBaseURL: "http://env", JtypeToken: "envtok"})
	eff, err := r.Effective(ctx)
	if err != nil || eff.Source != SourceDB || eff.BaseURL != "http://db" || !eff.Enabled() {
		t.Fatalf("db source = %+v err=%v", eff, err)
	}

	// env source: no DB row, JTYPE_BASE_URL set.
	r = NewResolver(&fakeReader{}, cipher, &config.Config{JtypeBaseURL: "http://env", JtypeToken: "envtok"})
	eff, err = r.Effective(ctx)
	if err != nil || eff.Source != SourceEnv || eff.BaseURL != "http://env" ||
		eff.ClusterToken != "envtok" || !eff.ClusterTokenSet {
		t.Fatalf("env source = %+v err=%v", eff, err)
	}

	// none: no DB row, no env base URL.
	r = NewResolver(&fakeReader{}, cipher, &config.Config{})
	eff, err = r.Effective(ctx)
	if err != nil || eff.Source != SourceNone || eff.Enabled() {
		t.Fatalf("none source = %+v err=%v", eff, err)
	}
}

// Test 2: the cluster fallback token is SOURCE-COUPLED — a DB base URL with NO DB
// token must NOT borrow the env JTYPE_TOKEN (a PAT for one instance must never
// authenticate against another).
func TestResolverSourceIsolation(t *testing.T) {
	ctx := context.Background()
	r := NewResolver(&fakeReader{cfg: &domain.KanbanConfig{BaseURL: "http://db"}}, newTestCipher(t),
		&config.Config{JtypeBaseURL: "http://env", JtypeToken: "envtok"})
	eff, err := r.Effective(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Source != SourceDB || eff.ClusterToken != "" || eff.ClusterTokenSet {
		t.Fatalf("db source must not borrow the env token: %+v", eff)
	}
}

// Test 3: a DB token blob with no cipher (AUTH_TOKEN_KEY unset) is a surfaced
// ERROR — never a silent env fallback — and Factory reports off.
func TestResolverDBTokenNoCipher(t *testing.T) {
	ctx := context.Background()
	r := NewResolver(&fakeReader{cfg: &domain.KanbanConfig{BaseURL: "http://db", TokenEnc: []byte("blob")}}, nil,
		&config.Config{JtypeBaseURL: "http://env", JtypeToken: "envtok"})
	if _, err := r.Effective(ctx); err == nil {
		t.Fatal("want an error when a DB token exists but no cipher is configured")
	}
	if _, _, ok := r.Factory(ctx); ok {
		t.Fatal("Factory must report off (ok=false) on a resolver error")
	}
}

// Test 4a: a fresh resolution is cached for the TTL; Invalidate + TTL expiry both
// force a re-read.
func TestResolverTTLAndInvalidate(t *testing.T) {
	ctx := context.Background()
	fr := &fakeReader{cfg: &domain.KanbanConfig{BaseURL: "http://one"}}
	now := time.Now()
	r := NewResolver(fr, newTestCipher(t), &config.Config{})
	r.now = func() time.Time { return now }

	if eff, _ := r.Effective(ctx); eff.BaseURL != "http://one" || fr.reads != 1 {
		t.Fatalf("first resolve = %+v reads=%d", eff, fr.reads)
	}
	// Within TTL: served from cache even though the underlying row changed.
	fr.cfg.BaseURL = "http://two"
	if eff, _ := r.Effective(ctx); eff.BaseURL != "http://one" || fr.reads != 1 {
		t.Fatalf("cached resolve = %+v reads=%d (want cached http://one, 1 read)", eff, fr.reads)
	}
	// Past the TTL: re-reads.
	now = now.Add(resolverTTL + time.Millisecond)
	if eff, _ := r.Effective(ctx); eff.BaseURL != "http://two" || fr.reads != 2 {
		t.Fatalf("post-TTL resolve = %+v reads=%d", eff, fr.reads)
	}
	// Invalidate forces an immediate re-read even within the TTL.
	fr.cfg.BaseURL = "http://three"
	r.Invalidate()
	if eff, _ := r.Effective(ctx); eff.BaseURL != "http://three" || fr.reads != 3 {
		t.Fatalf("post-invalidate resolve = %+v reads=%d", eff, fr.reads)
	}
}

// Test 4b: errors are NEVER cached — a transient failure re-attempts on the next
// call rather than sticking a cached error.
func TestResolverErrorsNotCached(t *testing.T) {
	ctx := context.Background()
	fr := &fakeReader{err: errors.New("db down")}
	r := NewResolver(fr, newTestCipher(t), &config.Config{})

	if _, err := r.Effective(ctx); err == nil || fr.reads != 1 {
		t.Fatalf("first error resolve: err=%v reads=%d", err, fr.reads)
	}
	// Error was not cached: the next call re-reads (and errors again).
	if _, err := r.Effective(ctx); err == nil || fr.reads != 2 {
		t.Fatalf("errors must not be cached: err=%v reads=%d", err, fr.reads)
	}
	// Recovery: once the store is healthy the value resolves.
	fr.err = nil
	fr.cfg = &domain.KanbanConfig{BaseURL: "http://ok"}
	if eff, err := r.Effective(ctx); err != nil || eff.BaseURL != "http://ok" || fr.reads != 3 {
		t.Fatalf("recovery resolve = %+v err=%v reads=%d", eff, err, fr.reads)
	}
}

// Test 5: the factory pool is reused for an unchanged base URL, rebuilt when the
// base URL changes, and off => ok=false.
func TestResolverFactoryPool(t *testing.T) {
	ctx := context.Background()
	fr := &fakeReader{cfg: &domain.KanbanConfig{BaseURL: "http://one"}}
	r := NewResolver(fr, newTestCipher(t), &config.Config{})

	f1, _, ok := r.Factory(ctx)
	if !ok || f1 == nil || f1.BaseURL() != "http://one" {
		t.Fatalf("factory1 ok=%v base=%v", ok, f1)
	}
	f2, _, ok := r.Factory(ctx)
	if !ok || f2 != f1 {
		t.Fatal("factory pool must reuse the same *jtype.Factory for an unchanged base URL")
	}

	// Base URL change => rebuild (new pointer, new base).
	fr.cfg.BaseURL = "http://two"
	r.Invalidate()
	f3, _, ok := r.Factory(ctx)
	if !ok || f3 == f1 || f3.BaseURL() != "http://two" {
		t.Fatalf("factory must be rebuilt on a base-URL change: reused=%v base=%v", f3 == f1, f3.BaseURL())
	}

	// Off => (nil,"",false).
	fr.cfg = nil
	r.Invalidate()
	if f, tok, ok := r.Factory(ctx); ok || f != nil || tok != "" {
		t.Fatalf("off must be (nil,\"\",false), got f=%v tok=%q ok=%v", f, tok, ok)
	}
}
