package kanban

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/store"
)

// testLogger is a quiet slog logger for tests (discards output).
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeAPI is an in-memory jtype stand-in for poller tests.
type fakeAPI struct {
	mu       sync.Mutex
	docs     map[string]jtype.Doc        // id -> list item
	contents map[string]string           // id -> content
	comments map[string][]string         // docID -> bodies
	getCalls int                         // number of GetDocument calls (cursor probe)
	listErr  error
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

// modelStub returns a fixed resolution.
type modelStub struct{ configured bool }

func (m modelStub) Resolve(ctx context.Context) (modelcfg.Resolved, error) {
	if !m.configured {
		return modelcfg.Resolved{Source: modelcfg.SourceNone}, nil
	}
	return modelcfg.Resolved{Source: modelcfg.SourceDB, BaseURL: "http://x", ModelName: "p/m", APIKeySet: true}, nil
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
	poller := New(m, api, modelStub{configured: configured}, testLogger(t), "http://console", time.Second)
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
	api.addCard("a", "cards/a.md", "b", "todo", "T", "x", 1)  // wrong column
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
	if len(api.comments["doc1"]) != 1 || !contains(api.comments["doc1"][0], "not configured") {
		t.Fatalf("want one not-configured comment, got %v", api.comments["doc1"])
	}

	// Now configure the model and tick again: the card auto-dispatches.
	poller.models = modelStub{configured: true}
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
