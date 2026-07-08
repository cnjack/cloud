package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// seedRun creates a project+run so the event methods (which require the run to
// exist for the FOR UPDATE lock analogue) can operate.
func seedRun(t *testing.T, m *MemStore) string {
	t.Helper()
	ctx := context.Background()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := m.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	r := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, ServiceID: svc.ID, Prompt: "x", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := m.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// TestRunnerSeqIsServerAllocated proves the runner's client seq is NOT used as
// the durable seq: the store assigns a monotonic global seq starting at 1.
func TestRunnerSeqIsServerAllocated(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	runID := seedRun(t, m)

	// Runner sends its own seq starting high; the store must renumber from 1.
	stored, err := m.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 100, Type: domain.EventAgentText, Payload: map[string]any{"n": 1}},
		{Seq: 101, Type: domain.EventAgentText, Payload: map[string]any{"n": 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d want 2", len(stored))
	}
	if stored[0].Seq != 1 || stored[1].Seq != 2 {
		t.Fatalf("server seqs = %d,%d want 1,2", stored[0].Seq, stored[1].Seq)
	}
}

// TestRunnerDedupeByClientSeq proves a re-sent batch (same client seq) is a
// no-op and consumes no new global seq.
func TestRunnerDedupeByClientSeq(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	runID := seedRun(t, m)

	batch := []EventInput{
		{Seq: 1, Type: domain.EventAgentText, Payload: map[string]any{"t": "a"}},
		{Seq: 2, Type: domain.EventAgentText, Payload: map[string]any{"t": "b"}},
	}
	if s, _ := m.AppendRunnerEvents(ctx, runID, batch); len(s) != 2 {
		t.Fatalf("first send inserted %d want 2", len(s))
	}
	// Re-send identical batch: idempotent, nothing new.
	if s, _ := m.AppendRunnerEvents(ctx, runID, batch); len(s) != 0 {
		t.Fatalf("replay inserted %d want 0", len(s))
	}
	// Partial overlap: seq 2 dup, seq 3 new -> only 3 inserted.
	s, _ := m.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 2, Type: domain.EventAgentText},
		{Seq: 3, Type: domain.EventAgentText},
	})
	if len(s) != 1 || s[0].Seq != 3 {
		t.Fatalf("partial replay = %+v want one event seq 3", s)
	}
}

// TestNoCollisionRunnerVsInternal is the regression test for the original
// hazard: interleaving runner ingest with internal emission must produce a
// gapless, unique, monotonic seq sequence with NO dropped events.
func TestNoCollisionRunnerVsInternal(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	runID := seedRun(t, m)

	// Interleave: internal, runner(x2), internal, runner.
	if _, err := m.AppendInternalEvent(ctx, runID, domain.EventRunStatus, map[string]any{"status": "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 1, Type: domain.EventAgentText}, {Seq: 2, Type: domain.EventAgentToolCall},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendInternalEvent(ctx, runID, domain.EventRunArtifact, map[string]any{"kind": "diff"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendRunnerEvents(ctx, runID, []EventInput{{Seq: 3, Type: domain.EventAgentToolResult}}); err != nil {
		t.Fatal(err)
	}

	events, _ := m.ListEvents(ctx, runID, 0, 100)
	if len(events) != 5 {
		t.Fatalf("total events = %d want 5 (none dropped)", len(events))
	}
	// seqs must be exactly 1..5, unique and monotonic.
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d want %d (gap/dup)", i, e.Seq, i+1)
		}
	}
}

// TestConcurrentIngestOrderingAndUniqueness hammers the store with concurrent
// runner ingests and internal emissions and asserts the durable log has unique,
// gapless seqs and preserves every accepted event.
func TestConcurrentIngestOrderingAndUniqueness(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	runID := seedRun(t, m)

	const runnerBatches = 50
	const internalEmits = 50

	var wg sync.WaitGroup
	wg.Add(2)

	// Runner goroutine: 50 batches, each with a unique client seq.
	go func() {
		defer wg.Done()
		for i := 0; i < runnerBatches; i++ {
			_, err := m.AppendRunnerEvents(ctx, runID, []EventInput{
				{Seq: int64(i + 1), Type: domain.EventAgentText, Payload: map[string]any{"i": i}},
			})
			if err != nil {
				t.Errorf("runner ingest %d: %v", i, err)
				return
			}
		}
	}()

	// Internal goroutine: 50 status emissions.
	go func() {
		defer wg.Done()
		for i := 0; i < internalEmits; i++ {
			if _, err := m.AppendInternalEvent(ctx, runID, domain.EventRunStatus, map[string]any{"i": i}); err != nil {
				t.Errorf("internal emit %d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	events, _ := m.ListEvents(ctx, runID, 0, 10000)
	want := runnerBatches + internalEmits
	if len(events) != want {
		t.Fatalf("durable events = %d want %d (collisions dropped some)", len(events), want)
	}
	seen := map[int64]bool{}
	for i, e := range events {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
		if e.Seq != int64(i+1) {
			t.Fatalf("non-monotonic/gap at index %d: seq %d", i, e.Seq)
		}
	}
}

// seedRunAt creates a project and a run driven to `status` (via the real
// mutators) so mutator-preservation tests start from a realistic row.
func seedRunAt(t *testing.T, m *MemStore, status domain.RunStatus) string {
	t.Helper()
	ctx := context.Background()
	id := seedRun(t, m)
	switch status {
	case domain.StatusQueued:
	case domain.StatusScheduling:
		if _, err := m.ScheduleRun(ctx, id, "job-"+id, "hash-"+id, "PreparingWorkspace"); err != nil {
			t.Fatal(err)
		}
	case domain.StatusRunning:
		if _, err := m.ScheduleRun(ctx, id, "job-"+id, "hash-"+id, "PreparingWorkspace"); err != nil {
			t.Fatal(err)
		}
		if _, err := m.MarkRunning(ctx, id, "StreamingTurn", time.Now()); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("seedRunAt: unsupported status %s", status)
	}
	return id
}

// TestMarkFailedPreservesRunnerReason is the root-cause regression for the
// stale-full-row lost update (pg.go:223): a runner-reported specific reason set
// via SetRunnerFailure must survive a subsequent generic MarkFailed. With the
// old full-row UpdateRun the reconciler's stale empty copy clobbered it.
func TestMarkFailedPreservesRunnerReason(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	id := seedRunAt(t, m, domain.StatusRunning)

	// Runner reports the specific reason first.
	if _, err := m.SetRunnerFailure(ctx, id, domain.FailureCloneFailed, "fatal: repo not found"); err != nil {
		t.Fatal(err)
	}
	// Reconciler then fails from generic cluster state.
	got, err := m.MarkFailed(ctx, id, "Failed", domain.FailureAgentError, "runner Job failed", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureReason != domain.FailureCloneFailed {
		t.Fatalf("reason=%s want clone_failed (runner-reported must win)", got.FailureReason)
	}
	if got.FailureMessage != "fatal: repo not found" {
		t.Fatalf("message=%q want the runner message", got.FailureMessage)
	}
	if got.Error != got.FailureMessage {
		t.Fatalf("error=%q want to mirror failure_message %q", got.Error, got.FailureMessage)
	}
}

// TestMarkFailedNeverWipesJobOrToken proves the failure path does not blank
// k8s_job_name/token_hash the way the old full-row write (with a stale copy)
// could — those fields are only ever written by ScheduleRun.
func TestMarkFailedNeverWipesJobOrToken(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	id := seedRunAt(t, m, domain.StatusScheduling)

	got, err := m.MarkFailed(ctx, id, "Failed", domain.FailureAgentError, "boom", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.K8sJobName == "" {
		t.Fatal("k8s_job_name was wiped by MarkFailed")
	}
	if got.TokenHash == "" {
		t.Fatal("token_hash was wiped by MarkFailed")
	}
}

// TestSetRunnerFailureNoOpWhenTerminal proves a late runner failure ingest does
// not resurrect fields on an already-terminal run and never errors.
func TestSetRunnerFailureNoOpWhenTerminal(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	id := seedRunAt(t, m, domain.StatusScheduling)
	if _, err := m.MarkSucceeded(ctx, id, "Succeeded", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := m.SetRunnerFailure(ctx, id, domain.FailureCloneFailed, "too late")
	if err != nil {
		t.Fatalf("SetRunnerFailure on terminal run errored: %v", err)
	}
	if got.FailureReason != "" || got.FailureMessage != "" {
		t.Fatalf("terminal run got failure fields stamped: %+v", got)
	}
	if got.Status != domain.StatusSucceeded {
		t.Fatalf("status changed to %s; SetRunnerFailure must not alter status", got.Status)
	}
}

// TestConcurrentFailNeverLosesRunnerReason races SetRunnerFailure against
// MarkFailed under -race and asserts the specific reason always wins. This is
// the concurrency regression for the lost-update hazard.
func TestConcurrentFailNeverLosesRunnerReason(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		m := NewMemStore()
		id := seedRunAt(t, m, domain.StatusRunning)

		var wg sync.WaitGroup
		wg.Add(2)
		// Ingest records the specific reason.
		go func() {
			defer wg.Done()
			_, _ = m.SetRunnerFailure(ctx, id, domain.FailureCloneFailed, "specific")
		}()
		// Reconciler fails with a generic reason.
		go func() {
			defer wg.Done()
			_, _ = m.MarkFailed(ctx, id, "Failed", domain.FailureAgentError, "generic", time.Now())
		}()
		wg.Wait()

		got, _ := m.GetRun(ctx, id)
		if got.Status != domain.StatusFailed {
			t.Fatalf("iter %d: status=%s want failed", i, got.Status)
		}
		// Whichever ordering occurred, the field-set semantics guarantee the
		// specific reason is never overwritten by the generic one once set. If
		// SetRunnerFailure landed first, reason must be clone_failed. If MarkFailed
		// landed first, reason is agent_error and SetRunnerFailure was a no-op
		// (terminal). It must NEVER be an empty reason and never lose a set value.
		if got.FailureReason == "" {
			t.Fatalf("iter %d: empty failure reason after concurrent fail", i)
		}
		if got.FailureReason == domain.FailureAgentError && got.FailureMessage != "generic" {
			t.Fatalf("iter %d: agent_error but message=%q", i, got.FailureMessage)
		}
		if got.FailureReason == domain.FailureCloneFailed && got.FailureMessage != "specific" {
			t.Fatalf("iter %d: clone_failed but message=%q", i, got.FailureMessage)
		}
	}
}

// TestMemSetRunResult proves SetRunResult records the outcome first-writer-wins
// without changing status (D18): a fresh run has a nil result; the first
// run.result stamps it; a later differing call is a no-op; and a missing run is
// ErrNotFound.
func TestMemSetRunResult(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	id := seedRunAt(t, m, domain.StatusRunning)

	// Fresh run: no result yet.
	if r0, _ := m.GetRun(ctx, id); r0.Result != nil {
		t.Fatalf("fresh run already has result %v", *r0.Result)
	}

	got, err := m.SetRunResult(ctx, id, domain.RunResultNoChanges)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result == nil || *got.Result != domain.RunResultNoChanges {
		t.Fatalf("result=%v want no_changes", got.Result)
	}
	if got.Status != domain.StatusRunning {
		t.Fatalf("status changed to %s; SetRunResult must not alter status", got.Status)
	}

	// First-writer-wins: a differing later call is a no-op.
	got2, err := m.SetRunResult(ctx, id, domain.RunResult("something_else"))
	if err != nil {
		t.Fatal(err)
	}
	if got2.Result == nil || *got2.Result != domain.RunResultNoChanges {
		t.Fatalf("result changed to %v; first-writer must win", got2.Result)
	}

	// Missing run.
	if _, err := m.SetRunResult(ctx, "nope", domain.RunResultNoChanges); err != ErrNotFound {
		t.Fatalf("missing run err=%v want ErrNotFound", err)
	}
}

// TestMemMarkPRCreatedIdempotent is the MemStore regression for MarkPRCreated
// (ST-1): the FIRST writer wins; a later call with different values is a no-op,
// so a retried reconcile can never clobber an already-recorded draft PR.
func TestMemMarkPRCreatedIdempotent(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	id := seedRun(t, m)

	got, err := m.MarkPRCreated(ctx, id, "http://gitea/pulls/5", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "http://gitea/pulls/5" || got.PRNumber != 5 {
		t.Fatalf("first mark: url=%q num=%d", got.PRURL, got.PRNumber)
	}
	// Second call with different values MUST be ignored (first-writer-wins).
	got, err = m.MarkPRCreated(ctx, id, "http://gitea/pulls/9", 9)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "http://gitea/pulls/5" || got.PRNumber != 5 {
		t.Fatalf("second mark clobbered: url=%q num=%d want pulls/5 #5", got.PRURL, got.PRNumber)
	}
	// Status must be untouched.
	if got.Status != domain.StatusQueued {
		t.Fatalf("MarkPRCreated changed status to %s", got.Status)
	}
}

// TestMemMarkPRCreatedConcurrent races two PR creators; exactly one value wins
// and it is never lost or half-written.
func TestMemMarkPRCreatedConcurrent(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	id := seedRun(t, m)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = m.MarkPRCreated(ctx, id, "http://a/1", 1) }()
	go func() { defer wg.Done(); _, _ = m.MarkPRCreated(ctx, id, "http://b/2", 2) }()
	wg.Wait()

	got, _ := m.GetRun(ctx, id)
	if got.PRURL == "" || got.PRNumber == 0 {
		t.Fatal("no PR recorded after concurrent create")
	}
	// The url and number must correspond to the SAME winner (no torn write).
	if (got.PRURL == "http://a/1") != (got.PRNumber == 1) {
		t.Fatalf("torn write: url=%q num=%d", got.PRURL, got.PRNumber)
	}
}

// TestMemSetRunGitFirstWriterWins proves branch/commit are recorded once and a
// duplicate run.git event does not overwrite them.
func TestMemSetRunGitFirstWriterWins(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	id := seedRun(t, m)

	got, err := m.SetRunGit(ctx, id, "agent/run-1", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.GitBranch != "agent/run-1" || got.CommitSHA != "abc123" {
		t.Fatalf("first set: branch=%q commit=%q", got.GitBranch, got.CommitSHA)
	}
	got, _ = m.SetRunGit(ctx, id, "agent/run-2", "def456")
	if got.GitBranch != "agent/run-1" || got.CommitSHA != "abc123" {
		t.Fatalf("second set clobbered: branch=%q commit=%q", got.GitBranch, got.CommitSHA)
	}
}

// TestMemListRunsAwaitingPR proves the scan returns only succeeded runs with a
// branch and no PR.
func TestMemListRunsAwaitingPR(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = m.CreateProject(ctx, p)

	// r1: succeeded + branch + no PR -> included.
	r1 := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, Status: domain.StatusSucceeded, GitBranch: "agent/run-1", CreatedAt: time.Now()}
	_ = m.CreateRun(ctx, r1)
	// r2: succeeded + branch + PR already set -> excluded.
	r2 := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, Status: domain.StatusSucceeded, GitBranch: "agent/run-2", PRURL: "http://x/1", PRNumber: 1, CreatedAt: time.Now()}
	_ = m.CreateRun(ctx, r2)
	// r3: succeeded but no branch -> excluded.
	r3 := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, Status: domain.StatusSucceeded, CreatedAt: time.Now()}
	_ = m.CreateRun(ctx, r3)
	// r4: running with branch -> excluded (not terminal-succeeded).
	r4 := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, Status: domain.StatusRunning, GitBranch: "agent/run-4", CreatedAt: time.Now()}
	_ = m.CreateRun(ctx, r4)

	out, err := m.ListRunsAwaitingPR(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != r1.ID {
		ids := make([]string, len(out))
		for i, r := range out {
			ids[i] = r.ID
		}
		t.Fatalf("awaiting = %v want just %s", ids, r1.ID)
	}
}

// TestMemServiceGitConfigRoundTrip proves service repo config persists, defaults
// to readonly, and GetDefaultService resolves the 'default' service.
func TestMemServiceGitConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	p := &domain.Project{ID: domain.NewID(), Name: "p", CreatedAt: time.Now()}
	_ = m.CreateProject(ctx, p)

	// Default (no git_mode) -> readonly; raw service round-trips.
	s1 := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "git://x/seed.git", CreatedAt: time.Now()}
	_ = m.CreateService(ctx, s1)
	got1, _ := m.GetService(ctx, s1.ID)
	if got1.GitMode != domain.GitModeReadonly || got1.DefaultBranch != "main" {
		t.Fatalf("default git_mode/branch = %q/%q want readonly/main", got1.GitMode, got1.DefaultBranch)
	}
	def, err := m.GetDefaultService(ctx, p.ID)
	if err != nil || def.ID != s1.ID {
		t.Fatalf("GetDefaultService = %+v err=%v want %s", def, err, s1.ID)
	}

	// Explicit draft_pr provider service round-trips.
	s2 := &domain.Service{
		ID: domain.NewID(), ProjectID: p.ID, Name: "web", RepoKind: domain.RepoKindProvider,
		Provider: domain.ProviderGitea, RepoOwnerName: "o/r", DefaultBranch: "main",
		GitMode: domain.GitModeDraftPR, CreatedAt: time.Now(),
	}
	_ = m.CreateService(ctx, s2)
	got2, _ := m.GetService(ctx, s2.ID)
	if got2.GitMode != domain.GitModeDraftPR || got2.Provider != domain.ProviderGitea ||
		got2.RepoKind != domain.RepoKindProvider || got2.RepoOwnerName != "o/r" {
		t.Fatalf("draft_pr service not round-tripped: %+v", got2)
	}
	svcs, _ := m.ListServices(ctx, p.ID)
	if len(svcs) != 2 {
		t.Fatalf("ListServices len=%d want 2", len(svcs))
	}
}

// TestMemStoreModelCatalogRoundTrip covers the D21 catalog: create (unique name),
// read-back, update, list newest-first, count, defensive copy, and delete.
func TestMemStoreModelCatalogRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()

	if n, _ := m.CountModels(ctx); n != 0 {
		t.Fatalf("empty catalog count=%d want 0", n)
	}
	if _, err := m.GetModel(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("get missing: err=%v want ErrNotFound", err)
	}

	a := &domain.Model{ID: "m1", Name: "gpt", BaseURL: "http://x/v1", ModelName: "a/b", APIKeyEnc: []byte("enc"), CreatedAt: time.Now(), UpdatedBy: "u1"}
	if err := m.CreateModel(ctx, a); err != nil {
		t.Fatal(err)
	}
	// Duplicate name => ErrAlreadyExists.
	if err := m.CreateModel(ctx, &domain.Model{ID: "m2", Name: "gpt", BaseURL: "http://y/v1", ModelName: "c/d"}); err != ErrAlreadyExists {
		t.Fatalf("dup name: err=%v want ErrAlreadyExists", err)
	}

	got, err := m.GetModel(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "gpt" || got.BaseURL != "http://x/v1" || string(got.APIKeyEnc) != "enc" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// Defensive copy: mutating the returned key must not affect the store.
	got.APIKeyEnc[0] = 'X'
	again, _ := m.GetModel(ctx, "m1")
	if string(again.APIKeyEnc) != "enc" {
		t.Fatalf("store aliased its internal key: %q", again.APIKeyEnc)
	}

	// Update (rename + keyless).
	a.Name = "gpt4o"
	a.APIKeyEnc = nil
	if err := m.UpdateModel(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _ = m.GetModel(ctx, "m1")
	if got.Name != "gpt4o" || got.APIKeyEnc != nil {
		t.Fatalf("update mismatch: %+v", got)
	}

	// Second model + list newest-first + count.
	time.Sleep(time.Millisecond)
	b := &domain.Model{ID: "m2", Name: "claude", BaseURL: "http://z/v1", ModelName: "e/f", CreatedAt: time.Now()}
	if err := m.CreateModel(ctx, b); err != nil {
		t.Fatal(err)
	}
	list, _ := m.ListModels(ctx)
	if len(list) != 2 || list[0].ID != "m2" {
		t.Fatalf("list newest-first mismatch: %+v", list)
	}
	if n, _ := m.CountModels(ctx); n != 2 {
		t.Fatalf("count=%d want 2", n)
	}

	// Delete => gone; delete on a missing id => ErrNotFound.
	if err := m.DeleteModel(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetModel(ctx, "m1"); err != ErrNotFound {
		t.Fatalf("get after delete: err=%v want ErrNotFound", err)
	}
	if err := m.DeleteModel(ctx, "m1"); err != ErrNotFound {
		t.Fatalf("delete twice: err=%v want ErrNotFound", err)
	}
}

// TestMemStoreModelGrants covers grants: idempotent grant/revoke, project<->model
// listing, and the cascade to service defaults / run refs on model delete.
func TestMemStoreModelGrants(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()

	proj := &domain.Project{ID: "p1", Name: "p", CreatedAt: time.Now()}
	if err := m.CreateProject(ctx, proj); err != nil {
		t.Fatal(err)
	}
	mod := &domain.Model{ID: "m1", Name: "gpt", BaseURL: "http://x/v1", ModelName: "a/b", CreatedAt: time.Now()}
	if err := m.CreateModel(ctx, mod); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{ID: "s1", ProjectID: "p1", Name: "default", RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", DefaultModelID: strp("m1"), CreatedAt: time.Now()}
	if err := m.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: "r1", ProjectID: "p1", ServiceID: "s1", Prompt: "x", Status: domain.StatusQueued, ModelID: strp("m1"), CreatedAt: time.Now()}
	if err := m.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Grant to a missing project/model => ErrNotFound.
	if err := m.GrantModel(ctx, "m1", "nope"); err != ErrNotFound {
		t.Fatalf("grant missing project: err=%v want ErrNotFound", err)
	}
	// Grant (idempotent).
	if err := m.GrantModel(ctx, "m1", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := m.GrantModel(ctx, "m1", "p1"); err != nil {
		t.Fatalf("re-grant should be a no-op: %v", err)
	}
	if got, _ := m.ListModelsForProject(ctx, "p1"); len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("ListModelsForProject mismatch: %+v", got)
	}
	if got, _ := m.ListProjectIDsForModel(ctx, "m1"); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("ListProjectIDsForModel mismatch: %+v", got)
	}

	// Delete the model => grants cascade + service default / run ref nulled.
	if err := m.DeleteModel(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.ListModelsForProject(ctx, "p1"); len(got) != 0 {
		t.Fatalf("grants should cascade on model delete: %+v", got)
	}
	gotSvc, _ := m.GetService(ctx, "s1")
	if gotSvc.DefaultModelID != nil {
		t.Fatalf("service default should be nulled on model delete: %+v", gotSvc.DefaultModelID)
	}
	gotRun, _ := m.GetRun(ctx, "r1")
	if gotRun.ModelID != nil {
		t.Fatalf("run model ref should be nulled on model delete: %+v", gotRun.ModelID)
	}

	// Revoke is idempotent (no-op when absent).
	if err := m.RevokeModel(ctx, "m1", "p1"); err != nil {
		t.Fatalf("revoke absent should be a no-op: %v", err)
	}
}

func strp(s string) *string { return &s }
