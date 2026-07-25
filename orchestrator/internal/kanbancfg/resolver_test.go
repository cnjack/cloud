package kanbancfg

import (
	"context"
	"errors"
	"testing"
	"time"

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

// Test 1: the DB > env > none resolution chain (D36: base URL only — there is
// no cluster credential to resolve).
func TestResolverSource(t *testing.T) {
	ctx := context.Background()

	// DB source: a row present wins over the env fallback.
	r := NewResolver(&fakeReader{cfg: &domain.KanbanConfig{BaseURL: "http://db"}},
		&config.Config{JtypeBaseURL: "http://env"})
	eff, err := r.Effective(ctx)
	if err != nil || eff.Source != SourceDB || eff.BaseURL != "http://db" || !eff.Enabled() {
		t.Fatalf("db source = %+v err=%v", eff, err)
	}

	// env source: no DB row, JTYPE_BASE_URL set.
	r = NewResolver(&fakeReader{}, &config.Config{JtypeBaseURL: "http://env"})
	eff, err = r.Effective(ctx)
	if err != nil || eff.Source != SourceEnv || eff.BaseURL != "http://env" {
		t.Fatalf("env source = %+v err=%v", eff, err)
	}

	// none: no DB row, no env base URL.
	r = NewResolver(&fakeReader{}, &config.Config{})
	eff, err = r.Effective(ctx)
	if err != nil || eff.Source != SourceNone || eff.Enabled() {
		t.Fatalf("none source = %+v err=%v", eff, err)
	}
}

// Test 2: a store error propagates (never cached — see TestResolverErrorsNotCached)
// and Factory reports off.
func TestResolverStoreError(t *testing.T) {
	ctx := context.Background()
	r := NewResolver(&fakeReader{err: errors.New("db down")}, &config.Config{JtypeBaseURL: "http://env"})
	if _, err := r.Effective(ctx); err == nil {
		t.Fatal("want the store error to propagate")
	}
	if _, ok := r.Factory(ctx); ok {
		t.Fatal("Factory must report off (ok=false) on a resolver error")
	}
}

// Test 3a: a fresh resolution is cached for the TTL; Invalidate + TTL expiry both
// force a re-read.
func TestResolverTTLAndInvalidate(t *testing.T) {
	ctx := context.Background()
	fr := &fakeReader{cfg: &domain.KanbanConfig{BaseURL: "http://one"}}
	now := time.Now()
	r := NewResolver(fr, &config.Config{})
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

// Test 3b: errors are NEVER cached — a transient failure re-attempts on the next
// call rather than sticking a cached error.
func TestResolverErrorsNotCached(t *testing.T) {
	ctx := context.Background()
	fr := &fakeReader{err: errors.New("db down")}
	r := NewResolver(fr, &config.Config{})

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

// Test 4: the factory pool is reused for an unchanged base URL, rebuilt when the
// base URL changes, and off => ok=false.
func TestResolverFactoryPool(t *testing.T) {
	ctx := context.Background()
	fr := &fakeReader{cfg: &domain.KanbanConfig{BaseURL: "http://one"}}
	r := NewResolver(fr, &config.Config{})

	f1, ok := r.Factory(ctx)
	if !ok || f1 == nil || f1.BaseURL() != "http://one" {
		t.Fatalf("factory1 ok=%v base=%v", ok, f1)
	}
	f2, ok := r.Factory(ctx)
	if !ok || f2 != f1 {
		t.Fatal("factory pool must reuse the same *jtype.Factory for an unchanged base URL")
	}

	// Base URL change => rebuild (new pointer, new base).
	fr.cfg.BaseURL = "http://two"
	r.Invalidate()
	f3, ok := r.Factory(ctx)
	if !ok || f3 == f1 || f3.BaseURL() != "http://two" {
		t.Fatalf("factory must be rebuilt on a base-URL change: reused=%v base=%v", f3 == f1, f3.BaseURL())
	}

	// Off => (nil,false).
	fr.cfg = nil
	r.Invalidate()
	if f, ok := r.Factory(ctx); ok || f != nil {
		t.Fatalf("off must be (nil,false), got f=%v ok=%v", f, ok)
	}
}
