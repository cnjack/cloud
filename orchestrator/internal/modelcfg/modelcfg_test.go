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

// seedModel inserts a catalog model and returns its id.
func seedModel(t *testing.T, st *store.MemStore, name, base, model string, keyEnc []byte) string {
	t.Helper()
	m := &domain.Model{ID: domain.NewID(), Name: name, BaseURL: base, ModelName: model, APIKeyEnc: keyEnc, CreatedAt: time.Now()}
	if err := st.CreateModel(context.Background(), m); err != nil {
		t.Fatalf("seed model %q: %v", name, err)
	}
	return m.ID
}

// seedProject creates a project so grants can reference it.
func seedProject(t *testing.T, st *store.MemStore) string {
	t.Helper()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return p.ID
}

// --- materialisation (resolveModel) ------------------------------------------

func TestResolveEmptyModelNoEnvIsNone(t *testing.T) {
	st := store.NewMemStore()
	got, err := resolveModel(context.Background(), st, nil, &config.Config{}, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Configured() || got.Source != SourceNone {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestResolveEmptyModelFallsBackToEnv(t *testing.T) {
	st := store.NewMemStore()
	got, err := resolveModel(context.Background(), st, nil, envCfg(), "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceEnv || !got.Configured() {
		t.Fatalf("got %+v, want env", got)
	}
	if got.BaseURL != "http://env.model/v1" || got.ModelName != "env/model" || got.APIKey != "env-key" || !got.APIKeySet {
		t.Fatalf("env materialise mismatch: %+v", got)
	}
}

func TestResolveEnvPartialIsNone(t *testing.T) {
	st := store.NewMemStore()
	for name, cfg := range map[string]*config.Config{
		"only base_url":   {ModelBaseURL: "http://x/v1"},
		"only model_name": {ModelName: "a/b"},
	} {
		got, err := resolveModel(context.Background(), st, nil, cfg, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Configured() {
			t.Fatalf("%s: got %+v, want none", name, got)
		}
	}
}

func TestResolveEnvKeylessIsConfigured(t *testing.T) {
	st := store.NewMemStore()
	cfg := &config.Config{ModelBaseURL: "http://env/v1", ModelName: "env/m"}
	got, err := resolveModel(context.Background(), st, nil, cfg, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceEnv || got.APIKeySet || got.APIKey != "" {
		t.Fatalf("keyless env got %+v", got)
	}
}

func TestResolveCatalogModelDecrypts(t *testing.T) {
	st := store.NewMemStore()
	cipher := testCipher(t)
	enc, err := cipher.EncryptString("db-secret")
	if err != nil {
		t.Fatal(err)
	}
	id := seedModel(t, st, "gpt", "http://db/v1", "openai/gpt-4o", enc)
	// Env is ALSO set — a materialised catalog model must not care about env.
	got, err := resolveModel(context.Background(), st, cipher, envCfg(), id)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceCatalog || got.ModelID != id {
		t.Fatalf("got %+v want catalog/%s", got, id)
	}
	if got.BaseURL != "http://db/v1" || got.ModelName != "openai/gpt-4o" || got.APIKey != "db-secret" || !got.APIKeySet {
		t.Fatalf("catalog materialise mismatch: %+v", got)
	}
}

func TestResolveCatalogKeylessModel(t *testing.T) {
	st := store.NewMemStore()
	id := seedModel(t, st, "vllm", "http://vllm/v1", "local/llama", nil)
	got, err := resolveModel(context.Background(), st, testCipher(t), &config.Config{}, id)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceCatalog || got.APIKeySet {
		t.Fatalf("keyless catalog got %+v", got)
	}
}

// TestResolveDeletedModelIsNone exercises the REAL deletion path (not a dangling
// id): a model is created, a run stamps its id, the admin deletes the model, and
// materialising the (now-missing) id must fail-visible — never the env fallback.
func TestResolveDeletedModelIsNone(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	id := seedModel(t, st, "gpt", "http://db/v1", "openai/gpt-4o", nil)
	// Model resolves fine while it exists.
	if got, _ := resolveModel(ctx, st, testCipher(t), envCfg(), id); got.Source != SourceCatalog {
		t.Fatalf("pre-delete got %+v, want catalog", got)
	}
	// Admin deletes the model (in production the FK also NULLs runs.model_id).
	if err := st.DeleteModel(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, err := resolveModel(ctx, st, testCipher(t), envCfg(), id)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Configured() || got.Source != SourceNone {
		t.Fatalf("deleted model got %+v, want none (NOT env fallback)", got)
	}
}

// TestResolveNullModelNonEmptyCatalogIsNone is the C1 regression: a NULL model_id
// (an FK-nulled run whose model was deleted) with a NON-EMPTY catalog must resolve
// to source="none", NOT the MODEL_* env model — otherwise a deleted-model run
// silently revives the env fallback (mockllm fake output on the e2e rig).
func TestResolveNullModelNonEmptyCatalogIsNone(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedModel(t, st, "other", "http://o/v1", "p/o", nil) // catalog non-empty
	got, err := resolveModel(ctx, st, testCipher(t), envCfg(), "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Configured() || got.Source != SourceNone {
		t.Fatalf("NULL model_id + non-empty catalog got %+v, want none (env must NOT rescue it)", got)
	}
}

// TestResolveNullModelEmptyCatalogUsesEnv confirms the accepted env fallback still
// works when the catalog IS empty (local rig): NULL model_id → env model.
func TestResolveNullModelEmptyCatalogUsesEnv(t *testing.T) {
	st := store.NewMemStore()
	got, err := resolveModel(context.Background(), st, nil, envCfg(), "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != SourceEnv || got.ModelName != "env/model" {
		t.Fatalf("empty-catalog env fallback got %+v, want env", got)
	}
}

func TestResolveCatalogKeyWithoutCipherErrors(t *testing.T) {
	st := store.NewMemStore()
	cipher := testCipher(t)
	enc, _ := cipher.EncryptString("secret")
	id := seedModel(t, st, "gpt", "http://db/v1", "openai/gpt-4o", enc)
	if _, err := resolveModel(context.Background(), st, nil, &config.Config{}, id); err == nil {
		t.Fatal("expected an error decrypting a key with no cipher configured")
	}
}

// --- selection chain (selectModel) -------------------------------------------

func TestSelectRequestedGranted(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	id := seedModel(t, st, "a", "http://a/v1", "p/a", nil)
	if err := st.GrantModel(context.Background(), id, proj); err != nil {
		t.Fatal(err)
	}
	sel, out, err := selectModel(context.Background(), st, nil, proj, "", id)
	if err != nil || out != SelectOK || sel.ModelID != id || sel.ModelName != "p/a" {
		t.Fatalf("sel=%+v out=%v err=%v want %s/p-a/OK", sel, out, err, id)
	}
}

func TestSelectRequestedNotGranted(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	id := seedModel(t, st, "a", "http://a/v1", "p/a", nil) // NOT granted to proj
	_, out, err := selectModel(context.Background(), st, nil, proj, "", id)
	if err != nil || out != SelectNotGranted {
		t.Fatalf("out=%v err=%v want NotGranted", out, err)
	}
}

func TestSelectServiceDefault(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	a := seedModel(t, st, "a", "http://a/v1", "p/a", nil)
	b := seedModel(t, st, "b", "http://b/v1", "p/b", nil)
	for _, id := range []string{a, b} {
		if err := st.GrantModel(context.Background(), id, proj); err != nil {
			t.Fatal(err)
		}
	}
	// Two grants, but the service default disambiguates.
	sel, out, err := selectModel(context.Background(), st, nil, proj, b, "")
	if err != nil || out != SelectOK || sel.ModelID != b || sel.ModelName != "p/b" {
		t.Fatalf("sel=%+v out=%v want %s/p-b/OK", sel, out, b)
	}
}

func TestSelectStaleDefaultFallsThrough(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	a := seedModel(t, st, "a", "http://a/v1", "p/a", nil)
	stale := seedModel(t, st, "stale", "http://s/v1", "p/s", nil) // NOT granted
	if err := st.GrantModel(context.Background(), a, proj); err != nil {
		t.Fatal(err)
	}
	// default points at a model whose grant was revoked → skip it; sole remaining
	// grant (a) is used, not an error.
	sel, out, err := selectModel(context.Background(), st, nil, proj, stale, "")
	if err != nil || out != SelectOK || sel.ModelID != a {
		t.Fatalf("sel=%+v out=%v want %s/OK", sel, out, a)
	}
}

func TestSelectSoleGrant(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	a := seedModel(t, st, "a", "http://a/v1", "p/a", nil)
	if err := st.GrantModel(context.Background(), a, proj); err != nil {
		t.Fatal(err)
	}
	sel, out, err := selectModel(context.Background(), st, nil, proj, "", "")
	if err != nil || out != SelectOK || sel.ModelID != a || sel.ModelName != "p/a" {
		t.Fatalf("sel=%+v out=%v want %s/p-a/OK", sel, out, a)
	}
}

func TestSelectMultipleNoDefaultIsNotSelected(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	for _, n := range []string{"a", "b"} {
		id := seedModel(t, st, n, "http://"+n+"/v1", "p/"+n, nil)
		if err := st.GrantModel(context.Background(), id, proj); err != nil {
			t.Fatal(err)
		}
	}
	_, out, err := selectModel(context.Background(), st, nil, proj, "", "")
	if err != nil || out != SelectNotSelected {
		t.Fatalf("out=%v err=%v want NotSelected", out, err)
	}
}

func TestSelectZeroGrantsCatalogEmptyUsesEnv(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	// Env fallback: chosen id is "" (model_id NULL) but the env model NAME is
	// snapshotted for audit.
	sel, out, err := selectModel(context.Background(), st, envCfg(), proj, "", "")
	if err != nil || out != SelectOK || sel.ModelID != "" || sel.ModelName != "env/model" {
		t.Fatalf("sel=%+v out=%v want env fallback (empty id / env name / OK)", sel, out)
	}
}

func TestSelectZeroGrantsCatalogEmptyNoEnvIsNotConfigured(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	_, out, err := selectModel(context.Background(), st, &config.Config{}, proj, "", "")
	if err != nil || out != SelectNotConfigured {
		t.Fatalf("out=%v err=%v want NotConfigured", out, err)
	}
}

func TestSelectZeroGrantsCatalogNonEmptyIsNotConfigured(t *testing.T) {
	st := store.NewMemStore()
	proj := seedProject(t, st)
	// Catalog HAS a model but it is NOT granted to this project → model_not_configured,
	// NOT the env fallback (env only applies when the catalog is empty).
	seedModel(t, st, "a", "http://a/v1", "p/a", nil)
	_, out, err := selectModel(context.Background(), st, envCfg(), proj, "", "")
	if err != nil || out != SelectNotConfigured {
		t.Fatalf("out=%v err=%v want NotConfigured (env must NOT rescue a non-empty catalog)", out, err)
	}
}

// --- Resolver caching --------------------------------------------------------

func TestResolverCachesUntilInvalidate(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	cipher := testCipher(t)
	id := seedModel(t, st, "a", "http://a/v1", "p/a", nil)
	r := NewResolver(st, cipher, &config.Config{})

	v, err := r.ResolveModel(ctx, id)
	if err != nil || v.Source != SourceCatalog {
		t.Fatalf("initial resolve = %+v, %v", v, err)
	}
	// Delete the model without telling the resolver: still served from cache.
	if err := st.DeleteModel(ctx, id); err != nil {
		t.Fatal(err)
	}
	v, _ = r.ResolveModel(ctx, id)
	if v.Source != SourceCatalog {
		t.Fatalf("cached resolve = %+v; want stale catalog (cache serving)", v)
	}
	// Invalidate (what writes do) => the delete is visible.
	r.Invalidate()
	v, _ = r.ResolveModel(ctx, id)
	if v.Configured() {
		t.Fatalf("post-invalidate resolve = %+v; want none (model gone)", v)
	}
}

func TestResolverTTLExpiry(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	id := seedModel(t, st, "a", "http://a/v1", "p/a", nil)
	r := NewResolver(st, testCipher(t), &config.Config{})
	now := time.Now()
	r.now = func() time.Time { return now }

	if _, err := r.ResolveModel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteModel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.ResolveModel(ctx, id); v.Source != SourceCatalog {
		t.Fatalf("within TTL got %+v, want cached catalog", v)
	}
	now = now.Add(resolverTTL + time.Millisecond)
	if v, _ := r.ResolveModel(ctx, id); v.Configured() {
		t.Fatalf("past TTL got %+v, want none", v)
	}
}

type flakyReader struct {
	inner ConfigReader
	fail  bool
}

func (f *flakyReader) GetModel(ctx context.Context, id string) (*domain.Model, error) {
	if f.fail {
		return nil, errors.New("transient db error")
	}
	return f.inner.GetModel(ctx, id)
}
func (f *flakyReader) ListModelsForProject(ctx context.Context, p string) ([]domain.Model, error) {
	return f.inner.ListModelsForProject(ctx, p)
}
func (f *flakyReader) CountModels(ctx context.Context) (int, error) {
	return f.inner.CountModels(ctx)
}

func TestResolverDoesNotCacheErrors(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	id := seedModel(t, st, "a", "http://a/v1", "p/a", nil)
	fr := &flakyReader{inner: st, fail: true}
	r := NewResolver(fr, testCipher(t), &config.Config{})

	if _, err := r.ResolveModel(ctx, id); err == nil {
		t.Fatal("expected the transient error to surface")
	}
	fr.fail = false
	v, err := r.ResolveModel(ctx, id)
	if err != nil || v.Source != SourceCatalog {
		t.Fatalf("post-recovery resolve = %+v, %v; want catalog", v, err)
	}
}

func TestMessages(t *testing.T) {
	if m := NotConfiguredMessage(""); !strings.Contains(m, "no LLM is configured") || strings.Contains(m, "http") {
		t.Fatalf("not-configured base msg wrong: %q", m)
	}
	if m := NotConfiguredMessage("http://console/"); !strings.HasSuffix(m, ": http://console") {
		t.Fatalf("not-configured url variant wrong: %q", m)
	}
	if m := NotSelectedMessage(); !strings.Contains(m, "several models") {
		t.Fatalf("not-selected msg wrong: %q", m)
	}
	if m := NotGrantedMessage(); !strings.Contains(m, "not authorized") {
		t.Fatalf("not-granted msg wrong: %q", m)
	}
}
