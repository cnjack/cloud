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
	mu         sync.Mutex
	docs       map[string]jtype.Doc // id -> list item
	contents   map[string]string    // id -> content
	events     []jtype.KanbanEvent
	comments   map[string][]string     // docID -> bodies
	boards     map[string]*jtype.Board // ref -> resolved board (D30 runtime check)
	boardErr   error                   // when set, GetBoard returns it (transient/definitive)
	getCalls   int                     // number of GetDocument calls (cursor probe)
	listCalls  int
	boardCalls int // number of GetBoard calls (revalidation probe)
	pullCalls  []int64
	pageSize   int
	listErr    error
	pullErr    error
	getErrOnce map[string]error
	tokens     []string // PATs the token->client factory was asked to bind (F6)
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		docs:       map[string]jtype.Doc{},
		contents:   map[string]string{},
		comments:   map[string][]string{},
		boards:     map[string]*jtype.Board{},
		getErrOnce: map[string]error{},
	}
}

// GetBoard resolves a board by ref for the D30 runtime revalidation check.
// boardErr (when set) simulates a transient/definitive jtype failure; otherwise a
// ref present in boards resolves, and a missing one is jtype.ErrDocNotFound.
func (f *fakeAPI) GetBoard(ctx context.Context, ws, ref string) (*jtype.Board, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boardCalls++
	if f.boardErr != nil {
		return nil, f.boardErr
	}
	if b, ok := f.boards[ref]; ok {
		return b, nil
	}
	return nil, jtype.ErrDocNotFound
}

func (f *fakeAPI) addCard(id, path, board, status, title, body string, clock int64) {
	f.addCardWithoutEvent(id, path, board, status, title, body, clock)
	f.addEvent(clock, board, path, status, title)
}

func (f *fakeAPI) addCardWithoutEvent(id, path, board, status, title, body string, clock int64) {
	content := "---\nboard: " + board + "\nstatus: " + status + "\ntitle: " + title + "\n---\n" + body + "\n"
	f.docs[id] = jtype.Doc{ID: id, Path: path, UpdatedClock: clock, Title: title}
	f.contents[id] = content
}

func (f *fakeAPI) addEvent(sequence int64, board, path, status, title string) {
	f.events = append(f.events, jtype.KanbanEvent{
		Sequence: sequence, Event: "kanban:card-updated", WorkspaceID: "ws", Board: board,
		Card: jtype.KanbanEventCard{Path: path, Status: status, Title: title}, UpdatedClock: sequence,
	})
}

func (f *fakeAPI) ListDocuments(ctx context.Context, ws string) ([]jtype.Doc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]jtype.Doc, 0, len(f.docs))
	for _, d := range f.docs {
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeAPI) PullBoardEvents(ctx context.Context, ws, board string, afterSequence int64, limit int) (*jtype.KanbanEventPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullCalls = append(f.pullCalls, afterSequence)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	if f.pageSize > 0 && f.pageSize < limit {
		limit = f.pageSize
	}
	page := &jtype.KanbanEventPage{NextSequence: afterSequence}
	for _, event := range f.events {
		if event.Board != board || event.Sequence <= afterSequence {
			continue
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, event)
		page.NextSequence = event.Sequence
	}
	return page, nil
}

func (f *fakeAPI) GetDocument(ctx context.Context, ws, id string) (*jtype.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if err := f.getErrOnce[id]; err != nil {
		delete(f.getErrOnce, id)
		return nil, err
	}
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
		EventSequence: int64Ptr(0),
		CreatedAt:     time.Now(), UpdatedAt: time.Now()}
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

// A second tick must not re-dispatch (idempotent), and the durable cursor must suppress
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
	// The second pull starts after sequence 5, so it never refetches doc1.
	if api.getCalls != first {
		t.Fatalf("cursor did not suppress fetch: getCalls went %d -> %d", first, api.getCalls)
	}
	if got := api.pullCalls[len(api.pullCalls)-1]; got != 5 {
		t.Fatalf("second pull afterSequence = %d, want 5", got)
	}
}

func TestPollerRetriesFailedEventWithoutAdvancingCursor(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)
	api.getErrOnce["doc1"] = &jtype.Error{StatusCode: 503, Code: "unavailable", Message: "retry"}

	poller.Tick(ctx)
	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 0 {
		t.Fatalf("failed event advanced cursor to %v, want 0", got.EventSequence)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 0 {
		t.Fatalf("failed fetch dispatched %d runs", len(runs))
	}

	poller.Tick(ctx)
	got, _ = m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 5 {
		t.Fatalf("successful retry cursor = %v, want 5", got.EventSequence)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 1 {
		t.Fatalf("successful retry want 1 run, got %d", len(runs))
	}
	if len(api.pullCalls) < 2 || api.pullCalls[0] != 0 || api.pullCalls[1] != 0 {
		t.Fatalf("failed sequence was not retried: pull cursors = %v", api.pullCalls)
	}
}

func TestPollerCommitsOnlySuccessfulEventPrefix(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("skip", "cards/skip.md", "b", "todo", "Skip", "body", 2)
	api.addCard("retry", "cards/retry.md", "b", "ai", "Retry", "body", 5)
	api.getErrOnce["retry"] = &jtype.Error{StatusCode: 503, Code: "unavailable", Message: "retry"}

	poller.Tick(ctx)
	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 2 {
		t.Fatalf("partial failure cursor = %v, want successful prefix 2", got.EventSequence)
	}

	poller.Tick(ctx)
	if len(api.pullCalls) < 2 || api.pullCalls[1] != 2 {
		t.Fatalf("retry did not resume after committed prefix: %v", api.pullCalls)
	}
	got, _ = m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 5 {
		t.Fatalf("retry cursor = %v, want 5", got.EventSequence)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 1 {
		t.Fatalf("retry want 1 run, got %d", len(runs))
	}
}

func TestPollerPersistsCursorAcrossRestart(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)
	poller.Tick(ctx)
	firstGets := api.getCalls

	clientFor := func(_ *jtype.Factory, tok string) DocumentAPI {
		api.tokens = append(api.tokens, tok)
		return api
	}
	restarted := New(m, envResolver(m, "http://jtype.test", "cluster-tok"), clientFor, nil,
		stubFor(true), testLogger(t), "http://console", time.Second)
	restarted.Tick(ctx)

	if api.getCalls != firstGets {
		t.Fatalf("restart re-fetched committed event: getCalls %d -> %d", firstGets, api.getCalls)
	}
	if got := api.pullCalls[len(api.pullCalls)-1]; got != 5 {
		t.Fatalf("restart pull afterSequence = %d, want persisted 5", got)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 1 {
		t.Fatalf("restart re-dispatched; got %d runs", len(runs))
	}
}

func TestPollerDrainsEventPagesInOrder(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.pageSize = 1
	api.addCard("doc1", "cards/one.md", "b", "ai", "One", "body", 5)
	api.addCard("doc2", "cards/two.md", "b", "ai", "Two", "body", 8)

	poller.Tick(ctx)

	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 2 {
		t.Fatalf("paged pull want 2 runs, got %d", len(runs))
	}
	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 8 {
		t.Fatalf("paged cursor = %v, want 8", got.EventSequence)
	}
	if len(api.pullCalls) != 2 || api.pullCalls[0] != 0 || api.pullCalls[1] != 5 {
		t.Fatalf("paged pull cursors = %v, want [0 5]", api.pullCalls)
	}
}

func TestPollerAdvancesPastNonTriggerEventWithoutFetching(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("doc1", "cards/x.md", "b", "todo", "T", "body", 4)

	poller.Tick(ctx)

	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 4 {
		t.Fatalf("non-trigger cursor = %v, want 4", got.EventSequence)
	}
	if api.getCalls != 0 {
		t.Fatalf("non-trigger event fetched document %d times", api.getCalls)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 0 {
		t.Fatalf("non-trigger event dispatched %d runs", len(runs))
	}
}

func TestPollerDispatchesTriggerEventAfterCardMovesAgain(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 4)
	// The immutable sequence says the card entered the trigger column, but by the
	// time Cloud fetches the body the current document has already moved to done.
	api.addCardWithoutEvent("doc1", "cards/x.md", "b", "done", "T", "body", 5)
	api.addEvent(5, "b", "cards/x.md", "done", "T")

	poller.Tick(ctx)

	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 1 {
		t.Fatalf("durable trigger transition want 1 run, got %d", len(runs))
	}
	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 5 {
		t.Fatalf("moved-again cursor = %v, want 5", got.EventSequence)
	}
}

func TestPollerBootstrapsExistingCardsBeforeSequencePull(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	if err := m.DeleteKanbanLink(ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	link.EventSequence = nil
	if err := m.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	api.addCardWithoutEvent("legacy", "cards/legacy.md", "b", "ai", "Legacy", "body", 3)

	poller.Tick(ctx)
	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 0 {
		t.Fatalf("bootstrap cursor = %v, want committed 0", got.EventSequence)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 1 {
		t.Fatalf("bootstrap want legacy card dispatched, got %d runs", len(runs))
	}

	api.addCard("new", "cards/new.md", "b", "ai", "New", "body", 9)
	poller.Tick(ctx)
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 2 {
		t.Fatalf("post-bootstrap event want second run, got %d", len(runs))
	}
	got, _ = m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 9 {
		t.Fatalf("post-bootstrap cursor = %v, want 9", got.EventSequence)
	}
}

func TestPollerRetriesBlockedCompatibilityBootstrap(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, false)
	if err := m.DeleteKanbanLink(ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	link.EventSequence = nil
	if err := m.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	api.addCardWithoutEvent("legacy", "cards/legacy.md", "b", "ai", "Legacy", "body", 3)

	poller.Tick(ctx)
	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence != nil {
		t.Fatalf("blocked bootstrap committed cursor %v, want nil", got.EventSequence)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 0 {
		t.Fatalf("blocked bootstrap dispatched %d runs", len(runs))
	}

	poller.models = stubFor(true)
	poller.Tick(ctx)
	got, _ = m.GetKanbanLink(ctx, link.ID)
	if got.EventSequence == nil || *got.EventSequence != 0 {
		t.Fatalf("recovered bootstrap cursor = %v, want 0", got.EventSequence)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 1 {
		t.Fatalf("recovered bootstrap want 1 run, got %d", len(runs))
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
	api.addEvent(9, "b", "cards/x.md", "ai", "T")
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
		EventSequence: int64Ptr(0),
		TokenEnc:      tokenEnc, CreatedAt: time.Now(), UpdatedAt: time.Now()}
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

// D30 C3 — the poller's runtime fail-visible board check.

// A soft-created ("unvalidated") link whose board_ref is a NAME is resolved at
// runtime: board_ref is canonicalized to the board's config id, board_status
// flips to "ok", and the card (carrying that config id) dispatches in the same
// tick. This is the load-bearing self-heal that makes a bootstrap link live.
func TestPollerUnvalidatedLinkCanonicalizes(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	// Re-create the link as unvalidated with a NAME ref (the harness link is "ok").
	_ = m.DeleteKanbanLink(ctx, link.ID)
	link.BoardRef = "jtype"
	link.BoardStatus = domain.KanbanBoardUnvalidated
	if err := m.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	api.boards["jtype"] = &jtype.Board{ID: "b_ab12cd34", Title: "jtype",
		Columns: []jtype.BoardColumn{{Key: "ai", Name: "AI"}}}
	// The card carries the CONFIG ID (b_…), not the name — matches only post-canon.
	api.addCard("doc1", "cards/x.md", "b_ab12cd34", "ai", "T", "body", 5)

	poller.Tick(ctx)

	got, err := m.GetKanbanLink(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BoardRef != "b_ab12cd34" {
		t.Fatalf("board_ref not canonicalized: %q want b_ab12cd34", got.BoardRef)
	}
	if got.BoardStatus != domain.KanbanBoardOK {
		t.Fatalf("board_status = %q want ok", got.BoardStatus)
	}
	if got.BoardTitle != "jtype" {
		t.Fatalf("board_title = %q want jtype", got.BoardTitle)
	}
	runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10)
	if len(runs) != 1 {
		t.Fatalf("want the card dispatched in the same tick, got %d runs", len(runs))
	}
}

// An unvalidated link whose board cannot be resolved becomes "invalid" (persisted,
// logged once), dispatches nothing, and does not advance — fail-visible, never a
// silent no-op.
func TestPollerUnvalidatedLinkBoardGoneInvalid(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	_ = m.DeleteKanbanLink(ctx, link.ID)
	link.BoardRef = "ghost"
	link.BoardStatus = domain.KanbanBoardUnvalidated
	if err := m.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	// No board named "ghost" in api.boards => GetBoard returns ErrDocNotFound.
	api.addCard("doc1", "cards/x.md", "b_whatever", "ai", "T", "body", 5)

	poller.Tick(ctx)

	got, err := m.GetKanbanLink(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BoardStatus != domain.KanbanBoardInvalid {
		t.Fatalf("board_status = %q want invalid", got.BoardStatus)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 0 {
		t.Fatalf("invalid link must dispatch nothing, got %d", len(runs))
	}
}

// A column mismatch (board resolves but the trigger column no longer exists) is a
// definitive "invalid", not a transient skip.
func TestPollerUnvalidatedLinkColumnMismatchInvalid(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	_ = m.DeleteKanbanLink(ctx, link.ID)
	link.BoardRef = "jtype"
	link.BoardStatus = domain.KanbanBoardUnvalidated
	if err := m.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	// Board resolves but has no "ai" column (link.TriggerColumn is "ai").
	api.boards["jtype"] = &jtype.Board{ID: "b_x", Title: "jtype",
		Columns: []jtype.BoardColumn{{Key: "todo"}}}

	poller.Tick(ctx)

	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.BoardStatus != domain.KanbanBoardInvalid {
		t.Fatalf("column mismatch board_status = %q want invalid", got.BoardStatus)
	}
}

// A transient GetBoard error (jtype 5xx) leaves the status UNCHANGED (still
// unvalidated) so the link retries next tick — not flipped to invalid.
func TestPollerUnvalidatedLinkTransientRetries(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	_ = m.DeleteKanbanLink(ctx, link.ID)
	link.BoardRef = "jtype"
	link.BoardStatus = domain.KanbanBoardUnvalidated
	if err := m.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	api.boardErr = &jtype.Error{StatusCode: 503, Code: "unavailable", Message: "down"}

	poller.Tick(ctx)

	got, _ := m.GetKanbanLink(ctx, link.ID)
	if got.BoardStatus != domain.KanbanBoardUnvalidated {
		t.Fatalf("transient error must not flip status; got %q want unvalidated", got.BoardStatus)
	}
}

// An "ok" link is never revalidated at runtime (the check is one-time) — the
// poller stays cheap and never calls GetBoard on the hot path.
func TestPollerOkLinkNoRevalidate(t *testing.T) {
	ctx := context.Background()
	poller, m, api, link, _, _ := newPollerHarness(t, true)
	// The harness link defaults to board_status "ok"; keep board_ref "b".
	api.addCard("doc1", "cards/x.md", "b", "ai", "T", "body", 5)

	poller.Tick(ctx)

	if api.boardCalls != 0 {
		t.Fatalf("ok link must not call GetBoard, got %d calls", api.boardCalls)
	}
	if runs, _ := m.ListRunsByService(ctx, link.ServiceID, 10); len(runs) != 1 {
		t.Fatalf("ok link should dispatch normally, got %d runs", len(runs))
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

func int64Ptr(v int64) *int64 { return &v }
