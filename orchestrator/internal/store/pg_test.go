package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// pgTestStore connects to a real Postgres when JCLOUD_PG_DSN is set, applies
// migrations, and returns a store scoped to a fresh run. Skips otherwise so
// `go test ./...` stays green without a database.
//
//	JCLOUD_PG_DSN=postgres://jcloud:jcloud@localhost:5432/jcloud?sslmode=disable \
//	    go test ./internal/store/ -run PG -v
func pgTestStore(t *testing.T) (*PGStore, string) {
	t.Helper()
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed store test")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	p := &domain.Project{ID: domain.NewID(), Name: "pgtest", RepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now()}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	r := &domain.Run{ID: domain.NewID(), ProjectID: p.ID, Prompt: "x", Status: domain.StatusQueued, Attempt: 1, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteProject(ctx, p.ID) }) // cascades runs/events
	return st, r.ID
}

func TestPGRunnerSeqAllocationAndDedupe(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	// Runner sends high client seqs; server must renumber from 1.
	stored, err := st.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 900, Type: domain.EventAgentText}, {Seq: 901, Type: domain.EventAgentText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Seq != 1 || stored[1].Seq != 2 {
		t.Fatalf("stored seqs = %+v want 1,2", stored)
	}
	// Replay identical batch: idempotent.
	again, _ := st.AppendRunnerEvents(ctx, runID, []EventInput{
		{Seq: 900, Type: domain.EventAgentText}, {Seq: 901, Type: domain.EventAgentText},
	})
	if len(again) != 0 {
		t.Fatalf("replay inserted %d want 0", len(again))
	}
}

// TestPGConcurrentIngestNoCollision is the real-DB regression test for the seq
// hazard: concurrent runner ingest + internal emission must yield a unique,
// gapless seq log with every accepted event preserved.
func TestPGConcurrentIngestNoCollision(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	const n = 40
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := st.AppendRunnerEvents(ctx, runID, []EventInput{
				{Seq: int64(i + 1), Type: domain.EventAgentText},
			}); err != nil {
				t.Errorf("runner %d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := st.AppendInternalEvent(ctx, runID, domain.EventRunStatus, map[string]any{"i": i}); err != nil {
				t.Errorf("internal %d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	events, err := st.ListEvents(ctx, runID, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2*n {
		t.Fatalf("durable events = %d want %d (collision dropped some)", len(events), 2*n)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("gap/dup at index %d: seq %d", i, e.Seq)
		}
	}
}

// TestPGMarkFailedPreservesRunnerReason is the real-DB regression for the
// stale-full-row lost-update finding: a runner-reported specific reason recorded
// via SetRunnerFailure must survive a subsequent generic MarkFailed. Requires
// JCLOUD_PG_DSN.
func TestPGMarkFailedPreservesRunnerReason(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	if _, err := st.ScheduleRun(ctx, runID, "job", "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, runID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Runner records the specific reason first.
	if _, err := st.SetRunnerFailure(ctx, runID, domain.FailureCloneFailed, "fatal: repo not found"); err != nil {
		t.Fatal(err)
	}
	// Reconciler fails from generic cluster state.
	got, err := st.MarkFailed(ctx, runID, "Failed", domain.FailureAgentError, "runner Job failed", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureReason != domain.FailureCloneFailed {
		t.Fatalf("reason=%s want clone_failed (runner-reported must win)", got.FailureReason)
	}
	if got.FailureMessage != "fatal: repo not found" {
		t.Fatalf("message=%q want runner message", got.FailureMessage)
	}
	if got.Error != got.FailureMessage {
		t.Fatalf("error=%q want to mirror failure_message", got.Error)
	}
	// Job name / token hash must not have been wiped.
	if got.K8sJobName != "job" || got.TokenHash != "hash" {
		t.Fatalf("MarkFailed wiped job/token: job=%q token=%q", got.K8sJobName, got.TokenHash)
	}
}

// TestPGConcurrentFailPreservesReason races SetRunnerFailure against MarkFailed
// on a real DB (row-lock serialised) and asserts the specific reason is never
// lost. Requires JCLOUD_PG_DSN.
func TestPGConcurrentFailPreservesReason(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)
	if _, err := st.ScheduleRun(ctx, runID, "job", "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, runID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = st.SetRunnerFailure(ctx, runID, domain.FailureCloneFailed, "specific") }()
	go func() { defer wg.Done(); _, _ = st.MarkFailed(ctx, runID, "Failed", domain.FailureAgentError, "generic", time.Now()) }()
	wg.Wait()

	got, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusFailed {
		t.Fatalf("status=%s want failed", got.Status)
	}
	if got.FailureReason == "" {
		t.Fatal("empty failure reason after concurrent fail")
	}
	if got.FailureReason == domain.FailureCloneFailed && got.FailureMessage != "specific" {
		t.Fatalf("clone_failed but message=%q", got.FailureMessage)
	}
}

// TestPGMarkJobCleanedPreservesJobName is the real-DB regression for the
// job_cleaned_at rework (migration 0003): cleanup bookkeeping must stamp
// job_cleaned_at while KEEPING k8s_job_name (the run's historical record), and
// ListTerminalRunsWithJob must stop returning the run once stamped. Requires
// JCLOUD_PG_DSN.
func TestPGMarkJobCleanedPreservesJobName(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	if _, err := st.ScheduleRun(ctx, runID, "jcloud-run-"+runID, "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelRun(ctx, runID, "CanceledByOperator", time.Now()); err != nil {
		t.Fatal(err)
	}

	// Un-cleaned terminal run with a job: must be listed.
	pending, err := st.ListTerminalRunsWithJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range pending {
		if r.ID == runID {
			found = true
		}
	}
	if !found {
		t.Fatal("terminal run with un-cleaned job not listed for cleanup")
	}

	if err := st.MarkJobCleaned(ctx, runID); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.K8sJobName != "jcloud-run-"+runID {
		t.Fatalf("k8s_job_name=%q want preserved (historical record)", got.K8sJobName)
	}
	if got.JobCleanedAt == nil {
		t.Fatal("job_cleaned_at not stamped")
	}
	first := *got.JobCleanedAt

	// Idempotent: a second stamp must not move the timestamp.
	if err := st.MarkJobCleaned(ctx, runID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetRun(ctx, runID)
	if got.JobCleanedAt == nil || !got.JobCleanedAt.Equal(first) {
		t.Fatalf("job_cleaned_at moved on re-stamp: %v -> %v", first, got.JobCleanedAt)
	}

	// Cleaned run must no longer be listed.
	pending, err = st.ListTerminalRunsWithJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.ID == runID {
			t.Fatal("cleaned run still returned by ListTerminalRunsWithJob")
		}
	}
}

// TestPGMarkPRCreatedIdempotent is the real-DB regression for MarkPRCreated
// (ST-1 migration 0004): first-writer-wins so a retried reconcile cannot
// double-open, and status/other columns are untouched. Requires JCLOUD_PG_DSN.
func TestPGMarkPRCreatedIdempotent(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	// Record a branch first (as the runner would), then open the PR.
	if _, err := st.SetRunGit(ctx, runID, "agent/run-x", "sha1"); err != nil {
		t.Fatal(err)
	}
	got, err := st.MarkPRCreated(ctx, runID, "http://gitea/pulls/5", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "http://gitea/pulls/5" || got.PRNumber != 5 {
		t.Fatalf("first mark: url=%q num=%d", got.PRURL, got.PRNumber)
	}
	// A second (racing/retried) mark with different values must be ignored.
	got, err = st.MarkPRCreated(ctx, runID, "http://gitea/pulls/9", 9)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "http://gitea/pulls/5" || got.PRNumber != 5 {
		t.Fatalf("second mark clobbered PR: url=%q num=%d", got.PRURL, got.PRNumber)
	}
	// Branch/commit preserved; status unchanged.
	if got.GitBranch != "agent/run-x" || got.CommitSHA != "sha1" {
		t.Fatalf("git fields lost: branch=%q commit=%q", got.GitBranch, got.CommitSHA)
	}
}

// TestPGListRunsAwaitingPR proves the awaiting-PR scan filters correctly on a
// real DB. Requires JCLOUD_PG_DSN.
func TestPGListRunsAwaitingPR(t *testing.T) {
	ctx := context.Background()
	st, runID := pgTestStore(t)

	// The seeded run is queued with no branch: not awaiting.
	if runs, _ := st.ListRunsAwaitingPR(ctx); containsRun(runs, runID) {
		t.Fatal("queued run should not be awaiting PR")
	}
	// Move it to succeeded with a branch.
	if _, err := st.ScheduleRun(ctx, runID, "job", "hash", "PreparingWorkspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, runID, "StreamingTurn", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, runID, "Succeeded", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunGit(ctx, runID, "agent/run-x", "sha"); err != nil {
		t.Fatal(err)
	}
	if runs, _ := st.ListRunsAwaitingPR(ctx); !containsRun(runs, runID) {
		t.Fatal("succeeded run with branch + no PR should be awaiting")
	}
	// Once the PR is stamped it drops out of the scan.
	if _, err := st.MarkPRCreated(ctx, runID, "http://gitea/pulls/1", 1); err != nil {
		t.Fatal(err)
	}
	if runs, _ := st.ListRunsAwaitingPR(ctx); containsRun(runs, runID) {
		t.Fatal("run with PR should no longer be awaiting")
	}
}

func containsRun(runs []domain.Run, id string) bool {
	for _, r := range runs {
		if r.ID == id {
			return true
		}
	}
	return false
}
