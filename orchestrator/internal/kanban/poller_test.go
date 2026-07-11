package kanban

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/kanbancfg"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/store"
)

// envResolver builds a kanbancfg.Resolver over st that is ENABLED via the env
// source (JTYPE_BASE_URL set), with the given cluster fallback token — the
// default "configured" state for poller tests that don't exercise the DB path.
func envResolver(st kanbancfg.ConfigReader, baseURL, clusterToken string) *kanbancfg.Resolver {
	return kanbancfg.NewResolver(st, nil, &config.Config{JtypeBaseURL: baseURL, JtypeToken: clusterToken})
}

// testCipher builds a live AES-256-GCM cipher from an all-zero 32-byte key, for
// the DB-source token tests.
func testCipher(t *testing.T) *auth.Cipher {
	t.Helper()
	c, err := auth.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return c
}

// testLogger is a quiet slog logger for tests (discards output).
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeAPI is an in-memory jtype stand-in for poller tests.
type fakeAPI struct {
	mu       sync.Mutex
	docs     map[string]jtype.Doc // id -> list item
	contents map[string]string    // id -> content
	comments map[string][]string  // docID -> bodies
	getCalls int                  // number of GetDocument calls (cursor probe)
	listErr  error
	tokens   []string // PATs the token->client factory was asked to bind (F6)
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		docs:     map[string]jtype.Doc{},
		contents: map[string]string{},
		comments: map[string][]string{},
	}
}

func (f *fakeAPI) addCard(id, path, board, status, title, body string, clock int64) {
	content := "---\nboard: " + board + "\nstatus: " + status + "\ntitle: " + title + "\n---\n" + body + "\n"
	f.docs[id] = jtype.Doc{ID: id, Path: path, UpdatedClock: clock, Title: title}
	f.contents[id] = content
}

func (f *fakeAPI) ListDocuments(ctx context.Context, ws string) ([]jtype.Doc, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]jtype.Doc, 0, len(f.docs))
	for _, d := range f.docs {
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeAPI) GetDocument(ctx context.Context, ws, id string) (*jtype.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	return &jtype.Document{Path: f.docs[id].Path, Content: f.contents[id], ContentHash: "h", UpdatedClock: f.docs[id].UpdatedClock}, nil
}

func (f *fakeAPI) AddComment(ctx context.Context, ws, docID, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments[docID] = append(f.comments[docID], body)
	return nil
}

// modelStub returns a fixed selection outcome. When SelectOK it yields a fixed
// model id + name so the poller stamps runs.model_id/model_name.
type modelStub struct{ outcome modelcfg.SelectOutcome }

func (m modelStub) SelectModel(ctx context.Context, projectID, defaultModelID, requested string) (modelcfg.Selection, modelcfg.SelectOutcome, error) {
	if m.outcome == modelcfg.SelectOK {
		return modelcfg.Selection{ModelID: "model-x", ModelName: "prov/model-x"}, modelcfg.SelectOK, nil
	}
	return modelcfg.Selection{}, m.outcome, nil
}

// configuredStub / notConfiguredStub are the two common cases the harness uses.
func stubFor(configured bool) modelStub {
	if configured {
		return modelStub{outcome: modelcfg.SelectOK}
	}
	return modelStub{outcome: modelcfg.SelectNotConfigured}
}

func newPollerHarness(t *testing.T, configured bool) (*Poller, *store.MemStore, *fakeAPI, *domain.KanbanLink, *domain.Project, *domain.Service) {
	t.Helper()
	m := store.NewMemStore()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "kan", CreatedAt: time.Now()}
	_ = m.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = m.CreateService(ctx, svc)
	link := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = m.CreateKanbanLink(ctx, link)

	api := newFakeAPI()
	// The seeded link carries no per-link token, so it resolves via the effective
	// cluster fallback "cluster-tok" (env source); the factory records each
	// requested PAT and returns the one fake (ignoring the resolved factory).
	clientFor := func(_ *jtype.Factory, tok string) DocumentAPI { api.tokens = append(api.tokens, tok); return api }
	resolver := envResolver(m, "http://jtype.test", "cluster-tok")
	poller := New(m, resolver, clientFor, nil, stubFor(configured), testLogger(t), "http://console", time.Second)
	return poller, m, api, link, p, svc
}

func TestPollerDispatchesTriggerCard(t *testing.T) {
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("doc1", "cards/x.md", "b", "ai", "Add healthz", "put a banner in the footer", 5)

	poller.Tick(context.Background())

	runs, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs) != 1 {
		t.Fatalf("want 1 dispatched run, got %d", len(runs))
	}
	if runs[0].Origin != domain.RunOriginKanban {
		t.Fatalf("run origin = %q", runs[0].Origin)
	}
	if runs[0].Prompt != "Add healthz\n\nput a banner in the footer" {
		t.Fatalf("prompt = %q", runs[0].Prompt)
	}
	// D21: the run is stamped with the selected model id + name snapshot.
	if runs[0].ModelID == nil || *runs[0].ModelID != "model-x" || runs[0].ModelName != "prov/model-x" {
		t.Fatalf("run model = %v / %q, want model-x / prov/model-x", runs[0].ModelID, runs[0].ModelName)
	}
	// Claim now carries the run id.
	claim, _ := m.EnsureKanbanClaim(context.Background(), link.ID, "doc1", "cards/x.md")
	if claim.RunID != runs[0].ID {
		t.Fatalf("claim run_id = %q, want %q", claim.RunID, runs[0].ID)
	}
}

// A second tick must not re-dispatch (idempotent), and the cursor must suppress
// the redundant GetDocument fetch entirely.
func TestPollerIdempotentAndCursor(t *testing.T) {
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)

	poller.Tick(context.Background())
	first := api.getCalls
	runs1, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs1) != 1 {
		t.Fatalf("tick1 want 1 run, got %d", len(runs1))
	}

	poller.Tick(context.Background())
	runs2, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs2) != 1 {
		t.Fatalf("tick2 re-dispatched; want 1 run, got %d", len(runs2))
	}
	// Cursor filters doc1 (clock unchanged) so no GetDocument on tick2.
	if api.getCalls != first {
		t.Fatalf("cursor did not suppress fetch: getCalls went %d -> %d", first, api.getCalls)
	}
}

func TestPollerIgnoresNonTriggerColumn(t *testing.T) {
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("a", "cards/a.md", "b", "todo", "T", "x", 1) // wrong column

	poller.Tick(context.Background())
	runs, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs) != 0 {
		t.Fatalf("want 0 runs for non-trigger column, got %d", len(runs))
	}
}

func TestPollerIgnoresNonTriggerAndOtherBoard(t *testing.T) {
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("a", "cards/a.md", "b", "todo", "T", "x", 1)   // wrong column
	api.addCard("c", "cards/c.md", "other", "ai", "T", "x", 1) // wrong board

	poller.Tick(context.Background())
	runs, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs) != 0 {
		t.Fatalf("want 0 runs for non-matching cards, got %d", len(runs))
	}
}

// When the LLM is not configured: no run is created, run_id stays empty
// (recoverable), exactly one comment is posted, and a second tick doesn't
// re-comment.
func TestPollerNotConfiguredNoticeOnce(t *testing.T) {
	poller, m, api, link, _, _ := newPollerHarness(t, false)
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)

	poller.Tick(context.Background())
	runs, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs) != 0 {
		t.Fatalf("not-configured must not create a run, got %d", len(runs))
	}
	claim, _ := m.EnsureKanbanClaim(context.Background(), link.ID, "doc1", "cards/x.md")
	if claim.RunID != "" {
		t.Fatalf("run_id should stay empty for recovery, got %q", claim.RunID)
	}
	if len(api.comments["doc1"]) != 1 || !contains(api.comments["doc1"][0], "no model is configured") {
		t.Fatalf("want one not-configured comment, got %v", api.comments["doc1"])
	}

	// Now configure the model and tick again: the card auto-dispatches.
	poller.models = stubFor(true)
	// Bump clock so the cursor re-surfaces the card.
	api.docs["doc1"] = jtype.Doc{ID: "doc1", Path: "cards/x.md", UpdatedClock: 9, Title: "T"}
	poller.Tick(context.Background())
	runs2, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs2) != 1 {
		t.Fatalf("after config want auto-dispatch of 1 run, got %d", len(runs2))
	}
	if len(api.comments["doc1"]) != 1 {
		t.Fatalf("must not re-comment once configured; got %d comments", len(api.comments["doc1"]))
	}
}

// P5: when several models are granted but the service has no default, a headless
// card can't pick — the comment must point the SERVICE OWNER at the default-model
// setting, DISTINCT from the "not configured / grant a model" cluster-admin notice.
func TestPollerNotSelectedNoticeMentionsDefault(t *testing.T) {
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	poller.models = modelStub{outcome: modelcfg.SelectNotSelected}
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)

	poller.Tick(context.Background())
	runs, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs) != 0 {
		t.Fatalf("not-selected must not create a run, got %d", len(runs))
	}
	if len(api.comments["doc1"]) != 1 {
		t.Fatalf("want exactly one comment, got %v", api.comments["doc1"])
	}
	body := api.comments["doc1"][0]
	if !contains(body, "default model") {
		t.Fatalf("not-selected comment should point at the default-model setting; got %q", body)
	}
	if contains(body, "grant") || contains(body, "no model is configured") {
		t.Fatalf("not-selected comment must NOT read as the not-configured/grant notice; got %q", body)
	}
}

// A previously-dispatched card bumped again (moved out and back) must not
// re-dispatch — the claim is the once-per-card guarantee.
func TestPollerOncePerCard(t *testing.T) {
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)
	poller.Tick(context.Background())

	api.docs["doc1"] = jtype.Doc{ID: "doc1", Path: "cards/x.md", UpdatedClock: 9, Title: "T"}
	poller.Tick(context.Background())
	runs, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs) != 1 {
		t.Fatalf("re-surfaces must not re-dispatch; got %d", len(runs))
	}
}

// seedLinkedProject builds a project/service/link (with the given token blob) and
// a fake API, returning the pieces for a token-selection test.
func seedLinkedProject(t *testing.T, tokenEnc []byte) (*store.MemStore, *fakeAPI, *domain.KanbanLink) {
	t.Helper()
	m := store.NewMemStore()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "kan", CreatedAt: time.Now()}
	_ = m.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = m.CreateService(ctx, svc)
	link := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", Enabled: true,
		TokenEnc: tokenEnc, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = m.CreateKanbanLink(ctx, link)
	api := newFakeAPI()
	return m, api, link
}

// F6 / D25: a link with its own encrypted PAT is polled with the DECRYPTED
// per-link token, not the cluster fallback.
func TestPollerUsesPerLinkToken(t *testing.T) {
	m, api, link := seedLinkedProject(t, []byte("ENCPAT"))
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)
	clientFor := func(_ *jtype.Factory, tok string) DocumentAPI { api.tokens = append(api.tokens, tok); return api }
	decrypt := func(b []byte) (string, error) { return "PLAIN-" + string(b), nil }
	poller := New(m, envResolver(m, "http://jtype.test", "cluster-tok"), clientFor, decrypt, stubFor(true), testLogger(t), "http://console", time.Second)

	poller.Tick(context.Background())

	if len(api.tokens) == 0 || api.tokens[0] != "PLAIN-ENCPAT" {
		t.Fatalf("poller used token %v, want decrypted per-link PLAIN-ENCPAT", api.tokens)
	}
	runs, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs) != 1 {
		t.Fatalf("want 1 dispatched run, got %d", len(runs))
	}
}

// F6 / D25: a link with neither a per-link token nor a cluster fallback is
// fail-visible — the poller never builds a client (no empty-credential call) and
// dispatches nothing.
func TestPollerSkipsLinkWithoutToken(t *testing.T) {
	m, api, link := seedLinkedProject(t, nil) // no per-link token
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)
	clientFor := func(_ *jtype.Factory, tok string) DocumentAPI { api.tokens = append(api.tokens, tok); return api }
	// Integration ENABLED (base URL set) but NO effective cluster token, and the
	// link has none of its own + nil decrypt → ResolveToken returns ErrNoToken.
	poller := New(m, envResolver(m, "http://jtype.test", ""), clientFor, nil, stubFor(true), testLogger(t), "http://console", time.Second)

	poller.Tick(context.Background())

	if len(api.tokens) != 0 {
		t.Fatalf("poller must not build a client without a token, built %v", api.tokens)
	}
	runs, _ := m.ListRunsByService(context.Background(), link.ServiceID, 10)
	if len(runs) != 0 {
		t.Fatalf("no-credential link must dispatch nothing, got %d", len(runs))
	}
}

// D27: the integration activates at RUNTIME. With no cluster config the poller is
// a clean no-op (zero client calls, zero runs); the moment a base URL is stored in
// the DB and the resolver is invalidated, the next Tick dispatches — no restart.
func TestPollerRuntimeActivation(t *testing.T) {
	ctx := context.Background()
	m, api, link := seedLinkedProject(t, []byte("ENCPAT")) // per-link token
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)
	clientFor := func(_ *jtype.Factory, tok string) DocumentAPI { api.tokens = append(api.tokens, tok); return api }
	decrypt := func(b []byte) (string, error) { return "PLAIN-" + string(b), nil }
	// Resolver starts OFF: no DB row and no env JTYPE_BASE_URL.
	resolver := kanbancfg.NewResolver(m, testCipher(t), &config.Config{})
	poller := New(m, resolver, clientFor, decrypt, stubFor(true), testLogger(t), "http://console", time.Second)

	// Off => the whole tick is a no-op: no client built, no run dispatched.
	poller.Tick(ctx)
	if len(api.tokens) != 0 {
		t.Fatalf("off: poller must not build a client, built %v", api.tokens)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 0 {
		t.Fatalf("off: must dispatch nothing, got %d", len(runs))
	}

	// Flip on: store a DB base URL, invalidate the shared resolver. Next tick
	// dispatches via the per-link token — no restart.
	if err := m.UpsertClusterKanbanConfig(ctx, &domain.KanbanConfig{BaseURL: "http://jtype.db", UpdatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	resolver.Invalidate()
	poller.Tick(ctx)
	runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10)
	if len(runs) != 1 {
		t.Fatalf("after activation want 1 dispatched run, got %d", len(runs))
	}
	if len(api.tokens) == 0 || api.tokens[len(api.tokens)-1] != "PLAIN-ENCPAT" {
		t.Fatalf("activated tick used token %v, want per-link PLAIN-ENCPAT", api.tokens)
	}
}

// D27 source-coupling: a DB config's cluster fallback token comes ONLY from the DB
// row (decrypted), never the env JTYPE_TOKEN. A tokenless link under a DB source
// resolves to the DB cluster token even when a DIFFERENT env token is set.
func TestPollerTokenOrderUnderDBSource(t *testing.T) {
	ctx := context.Background()
	m, api, link := seedLinkedProject(t, nil) // tokenless link => cluster fallback
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)
	cipher := testCipher(t)
	enc, err := cipher.EncryptString("db-cluster-tok")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertClusterKanbanConfig(ctx, &domain.KanbanConfig{BaseURL: "http://jtype.db", TokenEnc: enc, UpdatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	clientFor := func(_ *jtype.Factory, tok string) DocumentAPI { api.tokens = append(api.tokens, tok); return api }
	// A DIFFERENT env token is set — it must NOT be used (source is db).
	resolver := kanbancfg.NewResolver(m, cipher, &config.Config{JtypeBaseURL: "http://jtype.env", JtypeToken: "env-should-not-be-used"})
	poller := New(m, resolver, clientFor, cipher.DecryptString, stubFor(true), testLogger(t), "http://console", time.Second)

	poller.Tick(ctx)

	if len(api.tokens) == 0 || api.tokens[0] != "db-cluster-tok" {
		t.Fatalf("DB-source link used token %v, want the DB cluster token db-cluster-tok (never env)", api.tokens)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 1 {
		t.Fatalf("want 1 dispatched run, got %d", len(runs))
	}
}

// D27: a base-URL change between ticks (stored in the DB, resolver invalidated)
// routes the next tick's client at the NEW base — the factory is rebuilt.
func TestPollerBaseURLChangeBetweenTicks(t *testing.T) {
	ctx := context.Background()
	m, api, link := seedLinkedProject(t, []byte("ENCPAT")) // per-link token so client is built
	_ = link
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)
	var bases []string
	clientFor := func(f *jtype.Factory, _ string) DocumentAPI { bases = append(bases, f.BaseURL()); return api }
	decrypt := func(b []byte) (string, error) { return "PLAIN-" + string(b), nil }
	resolver := kanbancfg.NewResolver(m, testCipher(t), &config.Config{})

	if err := m.UpsertClusterKanbanConfig(ctx, &domain.KanbanConfig{BaseURL: "http://jtype.one", UpdatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	poller := New(m, resolver, clientFor, decrypt, stubFor(true), testLogger(t), "http://console", time.Second)
	poller.Tick(ctx)

	if err := m.UpsertClusterKanbanConfig(ctx, &domain.KanbanConfig{BaseURL: "http://jtype.two", UpdatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	resolver.Invalidate()
	poller.Tick(ctx)

	if len(bases) < 2 {
		t.Fatalf("want a client built on each tick, got bases %v", bases)
	}
	if bases[0] != "http://jtype.one" {
		t.Fatalf("tick1 base = %q want http://jtype.one", bases[0])
	}
	if bases[len(bases)-1] != "http://jtype.two" {
		t.Fatalf("tick2 base = %q want http://jtype.two (factory rebuilt on URL change)", bases[len(bases)-1])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
