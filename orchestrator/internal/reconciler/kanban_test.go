package reconciler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/kanbancfg"
	"github.com/cnjack/jcloud/internal/store"
)

// testKanbanResolver builds a kanbancfg.Resolver over st. A non-empty baseURL
// enables the integration via the env source (DB-source tests seed a row and
// pass an empty baseURL so the DB row wins). D36: base URL only — no cluster token.
func testKanbanResolver(st store.Store, baseURL string) *kanbancfg.Resolver {
	return kanbancfg.NewResolver(st, &config.Config{JtypeBaseURL: baseURL})
}

// testDecrypt opens the harness's fake sealed tokens: "PLAIN-"+blob, so a test
// can assert the DECRYPTED per-link token (never anything else) reached jtype.
func testDecrypt(b []byte) (string, error) { return "PLAIN-" + string(b), nil }

// fakeKanbanWriter captures MoveCard + AddComment calls (and can fail on demand).
// tokens records every PAT the token->writer factory was asked to bind, so the
// per-link token-selection tests can assert which credential was used.
type fakeKanbanWriter struct {
	comments    []commentCall
	moves       []moveCall
	commentErr  error
	listErr     error
	moveErr     error
	moveErrOnce bool
	tokens      []string
}

// writerFor returns fk as a (factory,token)->writer builder, recording each
// resolved token (and ignoring the resolved jtype factory).
func (fk *fakeKanbanWriter) writerFor() func(*jtype.Factory, string) KanbanWriter {
	return func(_ *jtype.Factory, tok string) KanbanWriter {
		fk.tokens = append(fk.tokens, tok)
		return fk
	}
}

// wire attaches fk to rec with an enabled env-source resolver, so seeded links
// (which carry the sealed "ENCPAT" per-link token) write back via testDecrypt.
func wire(st store.Store, rec *Reconciler, fk *fakeKanbanWriter, consoleURL string) *Reconciler {
	return rec.WithKanban(testKanbanResolver(st, "http://jtype.test"), fk.writerFor(), testDecrypt, consoleURL)
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

func (f *fakeKanbanWriter) ListComments(_ context.Context, ws, docID string) ([]jtype.Comment, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]jtype.Comment, 0)
	for index, comment := range f.comments {
		if comment.ws == ws && comment.docID == docID {
			out = append(out, jtype.Comment{ID: fmt.Sprintf("comment-%d", index+1), Body: comment.body})
		}
	}
	return out, nil
}

func (f *fakeKanbanWriter) MoveCard(_ context.Context, ws, docID, status string) error {
	if f.moveErrOnce {
		f.moveErrOnce = false
		return errors.New("temporary move failure")
	}
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
		// D36: every working link needs its own per-link token (no cluster
		// fallback); tests that exercise the no-credential path clear it.
		TokenEnc: []byte("ENCPAT"),
		Enabled:  true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
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

func TestPluginKanbanWritebackUsesInstallationWorkspace(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	project := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, project)
	service := &domain.Service{ID: domain.NewID(), ProjectID: project.ID, Name: "svc", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea, RepoOwnerName: "acme/repo", DefaultBranch: "main", CreatedAt: time.Now()}
	_ = st.CreateService(ctx, service)
	cfg := &domain.ProviderConfig{Provider: domain.PluginJType, BaseURL: "http://jtype.plugin", PluginEnabled: true}
	_ = st.UpsertProviderConfig(ctx, cfg)
	installation := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: project.ID, Provider: domain.PluginJType, Status: domain.PluginStatusEnabled, WorkspaceID: "workspace-fixed", AccessTokenEnc: []byte("PLUGINPAT"), ConfigRevision: cfg.ConfigRevision, ConsentedAt: time.Now(), CreatedAt: time.Now()}
	_ = st.CreatePluginInstallation(ctx, installation)
	automation := &domain.PluginAutomation{ID: domain.NewID(), ServiceID: service.ID, InstallationID: installation.ID, Name: "board", TriggerKind: "kanban", PromptTemplate: "p", Enabled: true, CreatedAt: time.Now()}
	trigger := &domain.KanbanTrigger{AutomationID: automation.ID, InstallationID: installation.ID, BoardRef: "b", TriggerColumn: "ai", DoneColumn: "done"}
	if err := st.CreatePluginAutomation(ctx, automation, nil, nil, trigger, nil); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID, Prompt: "p", Status: domain.StatusSucceeded, Origin: domain.RunOriginKanban, OriginAutomationID: automation.ID, Attempt: 1, CreatedAt: time.Now()}
	observed, err := st.ObservePluginKanbanCard(ctx, store.PluginKanbanObservation{
		AutomationID: automation.ID, ServiceID: service.ID,
		InstallationID: installation.ID, WorkspaceID: installation.WorkspaceID,
		DocumentID: "doc-plugin", DocumentPath: "cards/x.md",
		TriggerColumn: "ai", DoneColumn: "done", ObservedColumn: "ai",
		EventKey: "event:1", ObservedAt: time.Now().UTC(),
	})
	if err != nil || observed.Occurrence == nil {
		t.Fatalf("observe occurrence=%+v err=%v", observed, err)
	}
	if attached, err := st.CreatePluginKanbanOccurrenceRun(
		ctx, observed.Occurrence.ID, run,
	); err != nil || !attached {
		t.Fatalf("attach run=%v err=%v", attached, err)
	}
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{RunID: run.ID, InstallationID: installation.ID, CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	// Reconnect after launch: writeback must retain the frozen credential and
	// claim workspace/done column, never follow the Installation's new target.
	installation.WorkspaceID = "workspace-after-reconnect"
	installation.AccessTokenEnc = []byte("NEWPAT")
	if err := st.UpdatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePluginAutomation(ctx, automation.ID); err != nil {
		t.Fatal(err)
	}
	writer := &fakeKanbanWriter{moveErrOnce: true}
	rec := wire(st, newWritebackRec(st), writer, "http://console")
	rec.Tick(ctx)
	rec.Tick(ctx)
	if len(writer.moves) != 1 || writer.moves[0].ws != "workspace-fixed" || writer.moves[0].status != "done" {
		t.Fatalf("moves=%+v", writer.moves)
	}
	if len(writer.comments) != 2 ||
		writer.comments[0].docID != "doc-plugin" ||
		writer.comments[1].docID != "doc-plugin" {
		t.Fatalf("comments=%+v", writer.comments)
	}
	if !strings.Contains(writer.comments[0].body, ":accepted -->") ||
		!strings.Contains(writer.comments[1].body, ":terminal -->") {
		t.Fatalf("receipt phase order=%+v", writer.comments)
	}
	if len(writer.tokens) == 0 || writer.tokens[0] != "PLAIN-PLUGINPAT" {
		t.Fatalf("tokens=%v", writer.tokens)
	}
}

func TestPluginKanbanDeletedCardCompletesAsUnavailable(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	project := &domain.Project{ID: "deleted-card-project", Name: "p", CreatedAt: time.Now()}
	_ = st.CreateProject(ctx, project)
	service := &domain.Service{
		ID: "deleted-card-service", ProjectID: project.ID, Name: "svc",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "https://git.test/acme/repo.git",
		CreatedAt: time.Now(),
	}
	_ = st.CreateService(ctx, service)
	cfg := &domain.ProviderConfig{
		Provider: domain.PluginJType, BaseURL: "http://jtype.plugin", PluginEnabled: true,
	}
	_ = st.UpsertProviderConfig(ctx, cfg)
	installation := &domain.PluginInstallation{
		ID: "deleted-card-installation", ProjectID: project.ID, Provider: domain.PluginJType,
		Status: domain.PluginStatusEnabled, WorkspaceID: "workspace-fixed",
		AccessTokenEnc: []byte("PLUGINPAT"), ConfigRevision: cfg.ConfigRevision,
		ConsentedAt: time.Now(), CreatedAt: time.Now(),
	}
	_ = st.CreatePluginInstallation(ctx, installation)
	automation := &domain.PluginAutomation{
		ID: "deleted-card-automation", ServiceID: service.ID, InstallationID: installation.ID,
		Name: "board", TriggerKind: "kanban", Enabled: true, CreatedAt: time.Now(),
	}
	trigger := &domain.KanbanTrigger{
		AutomationID: automation.ID, InstallationID: installation.ID,
		BoardRef: "b", TriggerColumn: "ai", DoneColumn: "done",
	}
	if err := st.CreatePluginAutomation(ctx, automation, nil, nil, trigger, nil); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: "deleted-card-run", ProjectID: project.ID, ServiceID: service.ID,
		Status: domain.StatusSucceeded, Origin: domain.RunOriginKanban,
		OriginAutomationID: automation.ID, CreatedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsurePluginKanbanClaim(
		ctx, automation.ID, "deleted-card", "cards/deleted.md", "workspace-fixed", "done",
	); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPluginKanbanClaimRun(ctx, automation.ID, "deleted-card", run.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRunPluginSnapshots(ctx, []domain.RunPluginSnapshot{{
		RunID: run.ID, InstallationID: installation.ID, CreatedAt: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	writer := &fakeKanbanWriter{listErr: jtype.ErrDocNotFound}
	rec := wire(st, newWritebackRec(st), writer, "http://console")
	rec.Tick(ctx)
	rec.Tick(ctx)

	if pending, err := st.ListPluginKanbanRunsAwaitingWriteback(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	claim, err := st.GetPluginKanbanClaimByPath(
		ctx, automation.ID, "workspace-fixed", "cards/deleted.md",
	)
	if err != nil || claim.ExternalRefAvailable {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	history, err := st.ListPluginKanbanOccurrences(ctx, automation.ID, "deleted-card", 10)
	if err != nil || len(history) != 1 ||
		history[0].WritebackState != "unavailable" ||
		history[0].Outcome != string(domain.StatusSucceeded) {
		t.Fatalf("history=%+v err=%v", history, err)
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

// F6 / D25 / D36: a link with its own encrypted PAT writes back with the
// DECRYPTED per-link token.
func TestWritebackUsesPerLinkToken(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "done") // link carries ENCPAT
	fk := &fakeKanbanWriter{}
	rec := wire(st, newWritebackRec(st), fk, "http://console")

	rec.Tick(ctx)

	if len(fk.comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(fk.comments))
	}
	if len(fk.tokens) == 0 || fk.tokens[len(fk.tokens)-1] != "PLAIN-ENCPAT" {
		t.Fatalf("writeback used token %v, want decrypted per-link PLAIN-ENCPAT", fk.tokens)
	}
}

// D36: a link without a per-link token has NO fallback — no comment/move, and
// the claim stays PENDING so it resumes once a token is configured (never
// silently dropped).
func TestWritebackFailVisibleWhenNoToken(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	_, link, _ := seedKanbanTerminal(t, st, domain.StatusSucceeded, "done")
	// Strip the per-link token: the link now has no credential at all.
	if err := st.SetKanbanLinkToken(ctx, link.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	fk := &fakeKanbanWriter{}
	// Integration ENABLED (base URL set), but the link has no token of its own →
	// ResolveToken returns ErrNoToken (D36: no cluster fallback).
	rec := wire(st, newWritebackRec(st), fk, "http://console")

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

	// Recovery: set a per-link token → the next tick writes the pending card back.
	if err := st.SetKanbanLinkToken(ctx, link.ID, []byte("ENCPAT"), nil); err != nil {
		t.Fatal(err)
	}
	rec.Tick(ctx)
	if len(fk.comments) != 1 || len(fk.moves) != 1 {
		t.Fatalf("after setting a token want 1 comment + 1 move, got comments=%d moves=%d",
			len(fk.comments), len(fk.moves))
	}
}

// D27/D36: the writeback pass activates at RUNTIME. With no cluster config it's
// a clean no-op and the claim stays pending; once a base URL is stored in the DB
// and the resolver is invalidated, the next tick writes back with the link's
// per-link token — no restart.
func TestWritebackRuntimeActivation(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemStore()
	seedKanbanTerminal(t, st, domain.StatusSucceeded, "done") // link carries ENCPAT
	fk := &fakeKanbanWriter{}
	// Resolver starts OFF (no DB row, no env base URL).
	resolver := kanbancfg.NewResolver(st, &config.Config{})
	rec := newWritebackRec(st).WithKanban(resolver, fk.writerFor(), testDecrypt, "http://console")

	rec.Tick(ctx)
	if len(fk.comments) != 0 || len(fk.moves) != 0 || len(fk.tokens) != 0 {
		t.Fatalf("off: writeback must be a no-op, got comments=%d moves=%d tokens=%v",
			len(fk.comments), len(fk.moves), fk.tokens)
	}
	if pending, _ := st.ListKanbanRunsAwaitingWriteback(ctx); len(pending) != 1 {
		t.Fatalf("off: claim must stay pending, got %d", len(pending))
	}

	// Flip on: store a DB base URL, invalidate the shared resolver.
	if err := st.UpsertClusterKanbanConfig(ctx, &domain.KanbanConfig{BaseURL: "http://jtype.db", UpdatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	resolver.Invalidate()

	rec.Tick(ctx)
	if len(fk.comments) != 1 || len(fk.moves) != 1 {
		t.Fatalf("after activation want 1 comment + 1 move, got comments=%d moves=%d", len(fk.comments), len(fk.moves))
	}
	if len(fk.tokens) == 0 || fk.tokens[len(fk.tokens)-1] != "PLAIN-ENCPAT" {
		t.Fatalf("writeback used token %v, want per-link PLAIN-ENCPAT", fk.tokens)
	}
}
