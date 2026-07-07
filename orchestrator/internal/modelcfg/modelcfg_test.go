package modelcfg

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

func testCipher(t *testing.T) *auth.Cipher {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	c, err := auth.NewCipher(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// envCfg is the MODEL_* env fallback with all three fields set (configured).
func envCfg() *config.Config {
	return &config.Config{
		ModelBaseURL: "http://env.model/v1",
		ModelName:    "env/model",
		ModelAPIKey:  "env-key",
	}
}

func TestResolveNoneWhenNothingConfigured(t *testing.T) {
	st := store.NewMemStore()
	got, err := Resolve(context.Background(), st, nil, &config.Config{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Configured() || got.Source != SourceNone {
		t.Fatalf("got %+v, want source=none / not configured", got)
	}
}

func TestResolveEnvWhenAllThreeSet(t *testing.T) {
	st := store.NewMemStore()
	got, err := Resolve(context.Background(), st, nil, envCfg())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceEnv || !got.Configured() {
		t.Fatalf("source=%q configured=%v, want env/true", got.Source, got.Configured())
	}
	if got.BaseURL != "http://env.model/v1" || got.ModelName != "env/model" || got.APIKey != "env-key" || !got.APIKeySet {
		t.Fatalf("env resolve mismatch: %+v", got)
	}
}

func TestResolveEnvPartialIsNotConfigured(t *testing.T) {
	st := store.NewMemStore()
	// Only one of base_url/model_name set — a partial env must NOT masquerade as
	// configured.
	for name, cfg := range map[string]*config.Config{
		"only base_url":   {ModelBaseURL: "http://env.model/v1"},
		"only model_name": {ModelName: "env/model"},
	} {
		got, err := Resolve(context.Background(), st, nil, cfg)
		if err != nil {
			t.Fatalf("%s: resolve: %v", name, err)
		}
		if got.Configured() || got.Source != SourceNone {
			t.Fatalf("%s: got %+v, want none", name, got)
		}
	}
}

// TestResolveEnvKeylessIsConfigured aligns the env path with the DB path's
// keyless semantics: base_url + model_name without an api key IS configured
// (keyless OpenAI-compatible endpoints), with api_key_set=false.
func TestResolveEnvKeylessIsConfigured(t *testing.T) {
	st := store.NewMemStore()
	cfg := &config.Config{ModelBaseURL: "http://env.model/v1", ModelName: "env/model"}
	got, err := Resolve(context.Background(), st, nil, cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceEnv || !got.Configured() {
		t.Fatalf("keyless env got %+v, want configured env", got)
	}
	if got.APIKey != "" || got.APIKeySet {
		t.Fatalf("keyless env should carry no key: key=%q set=%v", got.APIKey, got.APIKeySet)
	}
}

func TestResolveDBWinsOverEnvAndDecrypts(t *testing.T) {
	st := store.NewMemStore()
	cipher := testCipher(t)
	enc, err := cipher.EncryptString("db-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetModelConfig(context.Background(), &domain.ModelConfig{
		BaseURL: "http://db.model/v1", ModelName: "db/model", APIKeyEnc: enc, UpdatedBy: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	// Env is ALSO set; the DB row must win.
	got, err := Resolve(context.Background(), st, cipher, envCfg())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceDB {
		t.Fatalf("source=%q want db (DB must take precedence over env)", got.Source)
	}
	if got.BaseURL != "http://db.model/v1" || got.ModelName != "db/model" {
		t.Fatalf("db resolve mismatch: %+v", got)
	}
	if got.APIKey != "db-secret-key" || !got.APIKeySet {
		t.Fatalf("api key not decrypted: key=%q set=%v", got.APIKey, got.APIKeySet)
	}
}

func TestResolveDBEmptyKeyIsConfiguredWithoutKey(t *testing.T) {
	st := store.NewMemStore()
	cipher := testCipher(t)
	// No api_key_enc (keyless endpoint): configured, but api_key_set=false.
	if err := st.SetModelConfig(context.Background(), &domain.ModelConfig{
		BaseURL: "http://db.model/v1", ModelName: "db/model", APIKeyEnc: nil,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(context.Background(), st, cipher, &config.Config{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceDB || !got.Configured() {
		t.Fatalf("got %+v, want configured db", got)
	}
	if got.APIKey != "" || got.APIKeySet {
		t.Fatalf("keyless db config should have no key: key=%q set=%v", got.APIKey, got.APIKeySet)
	}
}

func TestResolveDBKeyWithoutCipherErrors(t *testing.T) {
	st := store.NewMemStore()
	cipher := testCipher(t)
	enc, _ := cipher.EncryptString("db-secret-key")
	if err := st.SetModelConfig(context.Background(), &domain.ModelConfig{
		BaseURL: "http://db.model/v1", ModelName: "db/model", APIKeyEnc: enc,
	}); err != nil {
		t.Fatal(err)
	}
	// cipher nil while a key ciphertext exists — must error, never act key-less.
	if _, err := Resolve(context.Background(), st, nil, &config.Config{}); err == nil {
		t.Fatal("expected an error decrypting a key with no cipher configured")
	}
}

func TestNotConfiguredMessage(t *testing.T) {
	base := NotConfiguredMessage("")
	if !strings.Contains(base, "not configured") || strings.Contains(base, "http") {
		t.Fatalf("base message wrong: %q", base)
	}
	withURL := NotConfiguredMessage("http://console.test/")
	if !strings.HasSuffix(withURL, ": http://console.test") {
		t.Fatalf("url variant should append the trimmed console URL: %q", withURL)
	}
}

// --- Resolver (caching) -------------------------------------------------------

func TestResolverCachesUntilInvalidate(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	r := NewResolver(st, nil, &config.Config{})

	// First resolve: none (and now cached).
	v, err := r.Resolve(ctx)
	if err != nil || v.Configured() {
		t.Fatalf("initial resolve = %+v, %v; want none", v, err)
	}

	// A DB write the resolver hasn't been told about: still served from cache.
	if err := st.SetModelConfig(ctx, &domain.ModelConfig{BaseURL: "http://x/v1", ModelName: "a/b"}); err != nil {
		t.Fatal(err)
	}
	v, err = r.Resolve(ctx)
	if err != nil || v.Configured() {
		t.Fatalf("cached resolve = %+v, %v; want the stale none (cache serving)", v, err)
	}

	// Invalidate (what PUT/DELETE do) => the write is visible immediately.
	r.Invalidate()
	v, err = r.Resolve(ctx)
	if err != nil || v.Source != SourceDB {
		t.Fatalf("post-invalidate resolve = %+v, %v; want db", v, err)
	}
}

func TestResolverTTLExpiry(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	r := NewResolver(st, nil, &config.Config{})
	now := time.Now()
	r.now = func() time.Time { return now }

	if _, err := r.Resolve(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.SetModelConfig(ctx, &domain.ModelConfig{BaseURL: "http://x/v1", ModelName: "a/b"}); err != nil {
		t.Fatal(err)
	}
	// Within TTL: still the cached none.
	if v, _ := r.Resolve(ctx); v.Configured() {
		t.Fatalf("within TTL got %+v, want cached none", v)
	}
	// Past TTL: re-resolves and sees the row.
	now = now.Add(resolverTTL + time.Millisecond)
	if v, _ := r.Resolve(ctx); v.Source != SourceDB {
		t.Fatalf("past TTL got %+v, want db", v)
	}
}

// flakyReader errors until unblocked — proves the Resolver never caches errors.
type flakyReader struct {
	inner ConfigReader
	fail  bool
}

func (f *flakyReader) GetModelConfig(ctx context.Context) (*domain.ModelConfig, error) {
	if f.fail {
		return nil, errors.New("transient db error")
	}
	return f.inner.GetModelConfig(ctx)
}

func TestResolverDoesNotCacheErrors(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	if err := st.SetModelConfig(ctx, &domain.ModelConfig{BaseURL: "http://x/v1", ModelName: "a/b"}); err != nil {
		t.Fatal(err)
	}
	fr := &flakyReader{inner: st, fail: true}
	r := NewResolver(fr, nil, &config.Config{})

	if _, err := r.Resolve(ctx); err == nil {
		t.Fatal("expected the transient error to surface")
	}
	// Recovery on the very next call — the error was not cached.
	fr.fail = false
	v, err := r.Resolve(ctx)
	if err != nil || v.Source != SourceDB {
		t.Fatalf("post-recovery resolve = %+v, %v; want db", v, err)
	}
}
