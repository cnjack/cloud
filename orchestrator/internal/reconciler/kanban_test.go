package reconciler

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/kanbancfg"
	"github.com/cnjack/jcloud/internal/store"
)

// testKanbanResolver builds a kanbancfg.Resolver over st. A non-empty baseURL
// enables the integration via the env source (the DB-source tests seed a row and
// pass an empty baseURL so the DB row wins).
func testKanbanResolver(st store.Store, cipher *auth.Cipher, baseURL, clusterToken string) *kanbancfg.Resolver {
	return kanbancfg.NewResolver(st, cipher, &config.Config{JtypeBaseURL: baseURL, JtypeToken: clusterToken})
}

// testCipher builds a live AES-256-GCM cipher (all-zero 32-byte key) for the
// DB-source token tests.
func testCipher(t *testing.T) *auth.Cipher {
	t.Helper()
	c, err := auth.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return c
}

// fakeKanbanWriter captures MoveCard + AddComment calls (and can fail on demand).
// tokens records every PAT the token->writer factory was asked to bind, so the
// per-link/fallback token-selection tests can assert which credential was used.
type fakeKanbanWriter struct {
	comments   []commentCall
	moves      []moveCall
	commentErr error
	moveErr    error
	tokens     []string
}

// writerFor returns fk as a (factory,token)->writer builder, recording each
// resolved token (and ignoring the resolved jtype factory).
func (fk *fakeKanbanWriter) writerFor() func(*jtype.Factory, string) KanbanWriter {
	return func(_ *jtype.Factory, tok string) KanbanWriter {
		fk.tokens = append(fk.tokens, tok)
		return fk
	}
}

// wire attaches fk to rec with an enabled env-source resolver carrying the default
// cluster fallback token, so tokenless seeded links resolve via
// TokenClusterFallback (the pre-F6 behavior).
func wire(st store.Store, rec *Reconciler, fk *fakeKanbanWriter, consoleURL string) *Reconciler {
	return rec.WithKanban(testKanbanResolver(st, nil, "http://jtype.test", "cluster-tok"), fk.writerFor(), nil, consoleURL)
}

type commentCall struct {
	ws, docID, body string
}
type moveCall struct {
	ws, docID, status string
}

func (f *fakeKanbanWriter) AddComment(_ context.Context, ws, docID, body string) error {
	if f.commentErr != nil {
		return f.commentErr
	}
	f.comments = append(f.comments, commentCall{ws, docID, body})
	return nil
}

func (f *fakeKanbanWriter) MoveCard(_ context.Context, ws, docID, status string) error {
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moves = append(f.moves, moveCall{ws, docID, status})
	return nil
}

// seedKanbanTerminal sets up a project/service/link + a terminal kanban-origin
// run with a claim, returning the pieces a writeback test asserts against.
func seedKanbanTerminal(t *testing.T, st *store.MemStore, status domain.RunStatus, doneColumn string) (*domain.Run, *domain.KanbanLink, *domain.KanbanClaim) {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	link := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: "ws", BoardRef: "b",
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", DoneColumn: doneColumn,
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "p",
		Status: status, Attempt: 1, CreatedAt: time.Now(), Origin: domain.RunOriginKanban}
	if status == domain.StatusFailed {
		run.FailureReason = domain.FailureAgentError
		run.FailureMessage = "boom"
	}
	if status == domain.StatusSucceeded {
		run.PRURL = "http://gitea/pr/1"
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	claim, err := st.EnsureKanbanClaim(ctx, link.ID, "doc1", "cards/x.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKanbanClaimRun(ctx, link.ID, "doc1", run.ID); err != nil {
		t.Fatal(err)
	}
	return run, link, claim
}

func newWritebackRec(st store.Store) *Reconciler {
	cfg := &config.Config{ReconcileInterval: time.Millisecond, MaxConcurrentRuns: 4, OrchBaseURL: "http://orch"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := New(st, k8s.NewFakeLauncher(), cfg, log, nil)
	return rec
}

func TestWritebackSucceededPostsAndMoves(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	run, link, _ := seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	fk := &fakeKanbanWriter{}
	rec := wire(st, newWritebackRec(st), fk, "http://console")

	rec.Tick(ctx)

	if len(fk.comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(fk.comments))
	}
	if !strings.Contains(fk.comments[0].body, "finished") || !strings.Contains(fk.comments[0].body, run.PRURL) {
		t.Fatalf("succeeded comment = %q", fk.comments[0].body)
	}
	if !strings.Contains(fk.comments[0].body, "http://console/runs/"+run.ID) {
		t.Fatalf("console link missing: %q", fk.comments[0].body)
	}
	if len(fk.moves) != 1 || fk.moves[0].status != "done" {
		t.Fatalf("want move to done, got %+v", fk.moves)
	}
	if fk.moves[0].ws != link.WorkspaceID {
		t.Fatalf("move used wrong workspace: %q", fk.moves[0].ws)
	}
	// writeback_at stamped → second tick is a no-op.
	rec.Tick(ctx)
	if len(fk.comments) != 1 || len(fk.moves) != 1 {
		t.Fatalf("second tick re-wrote; comments=%d moves=%d", len(fk.comments), len(fk.moves))
	}
}

func TestWritebackFailedPostsReasonNoMove(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	run, _, _ := seedKanbanTerminal(t, st, domain.StatusFailed, "done")
	fk := &fakeKanbanWriter{}
	rec := wire(st, newWritebackRec(st), fk, "http://console")

	rec.Tick(ctx)

	if len(fk.comments) != 1 || !strings.Contains(fk.comments[0].body, "failed") ||
		!strings.Contains(fk.comments[0].body, "boom") || !strings.Contains(fk.comments[0].body, string(run.FailureReason)) {
		t.Fatalf("failed comment = %q", fk.comments[0].body)
	}
	if len(fk.moves) != 0 {
		t.Fatalf("failed run must still move to done when configured; got %+v", fk.moves)
	}
}

func TestWritebackNoDoneColumnSkipsMove(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "") // no done column
	fk := &fakeKanbanWriter{}
	rec := wire(st, newWritebackRec(st), fk, "")

	rec.Tick(ctx)
	if len(fk.comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(fk.comments))
	}
	if len(fk.moves) != 0 {
		t.Fatalf("no done column => no move; got %+v", fk.moves)
	}
}

// TestKanbanCommentBodyNoChanges proves a succeeded run with the no_changes
// outcome (D18) gets a writeback comment that states no code changes were made,
// rather than the ordinary "finished" + draft-PR line. It still links the run.
func TestKanbanCommentBodyNoChanges(t *testing.T) {
	nc := domain.RunResultNoChanges
	run := &domain.Run{ID: "run-xyz", Status: domain.StatusSucceeded, Result: &nc}
	body := kanbanCommentBody(run, "http://console")
	if !strings.Contains(body, "no code changes") {
		t.Fatalf("no_changes comment should state no code changes: %q", body)
	}
	if strings.Contains(body, "Draft PR") {
		t.Fatalf("no_changes run has no PR; comment must not mention a draft PR: %q", body)
	}
	if !strings.Contains(body, "http://console/runs/run-xyz") {
		t.Fatalf("console run link missing: %q", body)
	}
	// A normal succeeded run (no result) keeps the ordinary "finished." wording.
	plain := kanbanCommentBody(&domain.Run{ID: "run-abc", Status: domain.StatusSucceeded}, "http://console")
	if strings.Contains(plain, "no code changes") {
		t.Fatalf("ordinary succeeded run must not claim no changes: %q", plain)
	}
}

// A transient jtype error leaves the claim unmarked so the next tick retries
// (and, having retried, succeeds exactly once).
func TestWritebackRetriesOnTransientError(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	fk := &fakeKanbanWriter{}
	rec := wire(st, newWritebackRec(st), fk, "")

	fk.moveErr = errors.New("jtype down")
	rec.Tick(ctx) // move fails → nothing committed
	if len(fk.comments) != 0 || len(fk.moves) != 0 {
		t.Fatalf("first tick should commit nothing on move error")
	}
	fk.moveErr = nil
	rec.Tick(ctx) // now it succeeds
	if len(fk.comments) != 1 || len(fk.moves) != 1 {
		t.Fatalf("retry should post+move once; comments=%d moves=%d", len(fk.comments), len(fk.moves))
	}
}

// When the kanban client is nil (integration off) the pass is a silent no-op.
func TestWritebackNilClientNoop(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	rec := newWritebackRec(st) // no WithKanban
	rec.Tick(ctx)              // must not panic / error
}

// F6 / D25: a link with its own encrypted PAT writes back with the DECRYPTED
// per-link token, not the cluster fallback.
func TestWritebackUsesPerLinkToken(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	run, link, _ := seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	// Give the link a per-link (sealed) token by re-inserting it — the mem store
	// keyed on id lets a fresh Create with the same board conflict, so mutate via a
	// dedicated link id. Simplest: create a second project-less link is not needed;
	// instead delete + recreate with TokenEnc.
	if err := st.DeleteKanbanLink(ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	link.TokenEnc = []byte("ENCPAT")
	if err := st.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	// Re-stamp the claim's run (cascade-deleted with the link above).
	if _, err := st.EnsureKanbanClaim(ctx, link.ID, "doc1", "cards/x.md"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKanbanClaimRun(ctx, link.ID, "doc1", run.ID); err != nil {
		t.Fatal(err)
	}

	fk := &fakeKanbanWriter{}
	decrypt := func(b []byte) (string, error) { return "PLAIN-" + string(b), nil }
	rec := newWritebackRec(st).WithKanban(testKanbanResolver(st, nil, "http://jtype.test", "cluster-tok"), fk.writerFor(), decrypt, "http://console")

	rec.Tick(ctx)

	if len(fk.comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(fk.comments))
	}
	if len(fk.tokens) == 0 || fk.tokens[len(fk.tokens)-1] != "PLAIN-ENCPAT" {
		t.Fatalf("writeback used token %v, want decrypted per-link PLAIN-ENCPAT", fk.tokens)
	}
}

// F6 / D25: a link with neither a per-link token nor a cluster fallback is
// fail-visible — no comment/move, and the claim stays PENDING so it resumes once
// a token is configured (never silently dropped).
func TestWritebackFailVisibleWhenNoToken(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	_, link, _ := seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	fk := &fakeKanbanWriter{}
	// Integration ENABLED (base URL set) but NO effective cluster token + no decrypt
	// → ResolveToken returns ErrNoToken for the tokenless link.
	rec := newWritebackRec(st).WithKanban(testKanbanResolver(st, nil, "http://jtype.test", ""), fk.writerFor(), nil, "http://console")

	rec.Tick(ctx)

	if len(fk.comments) != 0 || len(fk.moves) != 0 || len(fk.tokens) != 0 {
		t.Fatalf("no-credential link must not write back: comments=%d moves=%d tokens=%v",
			len(fk.comments), len(fk.moves), fk.tokens)
	}
	// The claim is still pending (writeback deferred, not dropped).
	pending, err := st.ListKanbanRunsAwaitingWriteback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Link.ID != link.ID {
		t.Fatalf("writeback should remain pending for later retry, got %+v", pending)
	}
}

// D27: the writeback pass activates at RUNTIME. With no cluster config it's a
// clean no-op and the claim stays pending; once a base URL + encrypted cluster
// token are stored in the DB and the resolver is invalidated, the next tick
// resolves the DB cluster token (source-coupled) and writes back — no restart.
func TestWritebackRuntimeActivationDBToken(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "done") // tokenless link
	fk := &fakeKanbanWriter{}
	cipher := testCipher(t)
	// Resolver starts OFF (no DB row, no env base URL).
	resolver := kanbancfg.NewResolver(st, cipher, &config.Config{})
	rec := newWritebackRec(st).WithKanban(resolver, fk.writerFor(), cipher.DecryptString, "http://console")

	rec.Tick(ctx)
	if len(fk.comments) != 0 || len(fk.moves) != 0 || len(fk.tokens) != 0 {
		t.Fatalf("off: writeback must be a no-op, got comments=%d moves=%d tokens=%v",
			len(fk.comments), len(fk.moves), fk.tokens)
	}
	if pending, _ := st.ListKanbanRunsAwaitingWriteback(ctx); len(pending) != 1 {
		t.Fatalf("off: claim must stay pending, got %d", len(pending))
	}

	// Flip on: DB row with base URL + encrypted cluster token, invalidate.
	enc, err := cipher.EncryptString("db-cluster-tok")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertClusterKanbanConfig(ctx, &domain.KanbanConfig{BaseURL: "http://jtype.db", TokenEnc: enc, UpdatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	resolver.Invalidate()

	rec.Tick(ctx)
	if len(fk.comments) != 1 || len(fk.moves) != 1 {
		t.Fatalf("after activation want 1 comment + 1 move, got comments=%d moves=%d", len(fk.comments), len(fk.moves))
	}
	if len(fk.tokens) == 0 || fk.tokens[len(fk.tokens)-1] != "db-cluster-tok" {
		t.Fatalf("writeback used token %v, want the DB cluster token db-cluster-tok", fk.tokens)
	}
}
