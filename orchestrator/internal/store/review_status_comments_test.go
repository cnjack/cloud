package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestReviewStatusCommentsMigrationContract(t *testing.T) {
	t.Parallel()

	sql, err := migrationsFS.ReadFile("migrations/0072_review_status_comments.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := strings.Join(strings.Fields(string(sql)), " ")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS review_status_comments",
		"service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE",
		"current_run_id TEXT REFERENCES runs(id) ON DELETE SET NULL",
		"accepted_sequence BIGINT NOT NULL CHECK (accepted_sequence > 0)",
		"UNIQUE (service_id, provider, provider_repo_id, pr_number)",
		"desired_state TEXT NOT NULL",
		"CHECK (desired_state IN ( 'queued','running','publishing','completed','failed','canceled','superseded' ))",
		"applied_state TEXT NOT NULL",
		"CHECK (applied_state = '' OR applied_state IN ( 'queued','running','publishing','completed','failed','canceled','superseded' ))",
		"desired_body_hash TEXT NOT NULL",
		"applied_body_hash TEXT NOT NULL",
		"claim_token TEXT NOT NULL",
		"claimed_at TIMESTAMPTZ",
		"attempts INTEGER NOT NULL",
		"last_error TEXT NOT NULL",
		"next_attempt_at TIMESTAMPTZ",
		"observed_run_status TEXT NOT NULL",
		"observed_run_phase TEXT NOT NULL",
		"observed_failure_reason TEXT NOT NULL",
		"observed_delivery_status TEXT NOT NULL",
		"observed_review_posted BOOLEAN NOT NULL",
		"observed_review_plan_hash TEXT NOT NULL",
		"CREATE INDEX IF NOT EXISTS review_status_comments_pending_idx",
		"CREATE INDEX IF NOT EXISTS review_status_comments_current_run_idx",
		"COALESCE(next_attempt_at, updated_at)",
		"CREATE TABLE IF NOT EXISTS review_status_cursors",
		"CREATE SEQUENCE IF NOT EXISTS webhook_receipt_ingress_sequence_seq",
		"ingress_sequence BIGINT",
		"ROW_NUMBER() OVER (ORDER BY received_at, id)",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("0072 migration missing %q", required)
		}
	}
	for _, line := range strings.Split(string(sql), "\n") {
		statement := strings.ToUpper(strings.TrimSpace(line))
		for _, forbidden := range []string{"DROP ", "DELETE ", "TRUNCATE "} {
			if strings.HasPrefix(statement, forbidden) {
				t.Fatalf("0072 migration must be append-only; found %q", strings.TrimSpace(line))
			}
		}
	}
}

func TestReviewCompletionStatusMigrationContract(t *testing.T) {
	sql, err := migrationsFS.ReadFile("migrations/0073_review_completion_status.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(sql)), " ")
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS review_status_comments_desired_state_check",
		"'queued','running','publishing','completed','partial','failed','canceled','superseded'",
		"DROP CONSTRAINT IF EXISTS review_status_comments_applied_state_check",
		"SET observed_review_posted = FALSE",
		"COALESCE(review_run.review_result->'completion'->>'status', '') <> 'complete'",
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("0073 migration missing %q", want)
		}
	}
}

func TestMemReviewStatusCommentAtomicCreateAndRevisionReuse(t *testing.T) {
	ctx := context.Background()
	st, projectID, serviceID := seedMemReviewStatusStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	key := domain.ReviewStatusCommentKey{
		ServiceID: serviceID, Provider: domain.ProviderGitHub,
		ProviderRepoID: "repo-42", PRNumber: 17,
	}

	const callers = 24
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			execution, run, intent := reviewStatusExecution(projectID, serviceID, key, "same-event", strings.Repeat("a", 40), "queued-v1", now)
			_, won, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent)
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			if won {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := created.Load(); got != 1 {
		t.Fatalf("created=%d want 1", got)
	}
	runs, err := st.ListRunsByService(ctx, serviceID, -1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CurrentRunID != runs[0].ID || status.HeadSHA != strings.Repeat("a", 40) ||
		status.ObservedRun != domain.ReviewStatusObservationForRun(runs[0]) {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	// Replaying the same webhook identity is a no-op for all three atomic rows,
	// even if a malformed caller tries to attach a different revision intent.
	replayExecution, replayRun, replayIntent := reviewStatusExecution(projectID, serviceID, key, "same-event", strings.Repeat("f", 40), "must-not-win", now.Add(time.Second))
	if got, won, err := st.CreateAutomationExecutionWithReviewStatus(ctx, replayExecution, replayRun, replayIntent); err != nil || won || got.RunID != runs[0].ID {
		t.Fatalf("replay execution=%+v won=%v err=%v", got, won, err)
	}
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CurrentRunID != runs[0].ID || status.HeadSHA != strings.Repeat("a", 40) || status.DesiredBodyHash != "queued-v1" {
		t.Fatalf("replay advanced status=%+v err=%v", status, err)
	}

	claimed, won, err := st.ClaimReviewStatusComment(ctx, key, "claim-v1", now.Add(time.Second), now)
	if err != nil || !won || claimed.Attempts != 1 {
		t.Fatalf("claim=%+v won=%v err=%v", claimed, won, err)
	}
	if _, err := st.MarkReviewStatusCommentApplied(ctx, key, "claim-v1", "comment-9", "https://github.test/comment-9", domain.ReviewStatusQueued, "queued-v1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ScheduleRun(ctx, runs[0].ID, "old-review", "token", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	oldRun, err := st.MarkRunning(ctx, runs[0].ID, "Running", now.Add(2200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	oldObservation := domain.ReviewStatusObservationForRun(*oldRun)
	if _, err := st.UpdateReviewStatusCommentDesired(ctx, key, oldRun.ID, oldRun.PRHeadSHA,
		domain.ReviewStatusRunning, "running-v1", oldObservation, now.Add(2300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	oldHeadClaim, won, err := st.ClaimReviewStatusComment(ctx, key, "old-head-in-flight", now.Add(2500*time.Millisecond), now.Add(2*time.Second))
	if err != nil || !won {
		t.Fatalf("old head claim=%+v won=%v err=%v", oldHeadClaim, won, err)
	}

	// A new revision has a distinct execution key, but reuses the one mutable PR
	// status comment and preserves its provider identity/applied projection.
	execution, run, intent := reviewStatusExecution(projectID, serviceID, key, "new-event", strings.Repeat("b", 40), "queued-v2", now.Add(3*time.Second))
	if _, won, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent); err != nil || !won {
		t.Fatalf("new revision won=%v err=%v", won, err)
	}
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentRunID != run.ID || status.HeadSHA != run.PRHeadSHA || status.DesiredBodyHash != "queued-v2" ||
		status.CommentID != "comment-9" || status.AppliedBodyHash != "queued-v1" ||
		status.ClaimedAt == nil || status.Attempts != 0 || status.LastError != "" {
		t.Fatalf("reused status=%+v", status)
	}
	if status.ClaimToken != "old-head-in-flight" {
		t.Fatalf("new head revoked active provider lease: status=%+v", status)
	}
	if _, err := st.MarkReviewStatusCommentApplied(ctx, key, "old-head-in-flight", "comment-9", "", domain.ReviewStatusRunning, "running-v1", now.Add(3500*time.Millisecond)); !errors.Is(err, ErrConflict) {
		t.Fatalf("old head apply err=%v want conflict", err)
	}
	if _, err := st.UpdateReviewStatusCommentDesired(ctx, key, runs[0].ID, strings.Repeat("a", 40),
		domain.ReviewStatusFailed, "stale-failed", oldObservation, now.Add(4*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Run update err=%v want conflict", err)
	}
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CurrentRunID != run.ID || status.DesiredState != domain.ReviewStatusQueued || status.DesiredBodyHash != "queued-v2" {
		t.Fatalf("new head overwritten by stale Run: status=%+v err=%v", status, err)
	}
	pending, err := st.ListPendingReviewStatusComments(ctx, now.Add(4*time.Second), now.Add(4*time.Second), 10)
	if err != nil || len(pending) != 1 || pending[0].CurrentRunID != run.ID {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestMemReviewStatusCommentLeaseFencingAndTerminalQuiescence(t *testing.T) {
	ctx := context.Background()
	st, projectID, serviceID := seedMemReviewStatusStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	key := domain.ReviewStatusCommentKey{ServiceID: serviceID, Provider: domain.ProviderGitHub, ProviderRepoID: "repo-7", PRNumber: 7}
	execution, run, intent := reviewStatusExecution(projectID, serviceID, key, "event", strings.Repeat("c", 40), "queued", now)
	if _, won, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent); err != nil || !won {
		t.Fatalf("create won=%v err=%v", won, err)
	}

	first, won, err := st.ClaimReviewStatusComment(ctx, key, "first", now.Add(time.Second), now)
	if err != nil || !won || first.Attempts != 1 {
		t.Fatalf("first=%+v won=%v err=%v", first, won, err)
	}
	if _, won, err := st.ClaimReviewStatusComment(ctx, key, "too-early", now.Add(2*time.Second), now); err != nil || won {
		t.Fatalf("early reclaim won=%v err=%v", won, err)
	}
	second, won, err := st.ClaimReviewStatusComment(ctx, key, "second", now.Add(3*time.Second), now.Add(2*time.Second))
	if err != nil || !won || second.Attempts != 2 {
		t.Fatalf("second=%+v won=%v err=%v", second, won, err)
	}
	if _, err := st.MarkReviewStatusCommentApplied(ctx, key, "first", "comment", "", domain.ReviewStatusQueued, "queued", now.Add(4*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale apply err=%v want conflict", err)
	}
	if _, err := st.MarkReviewStatusCommentFailed(ctx, key, "second", "temporary provider failure", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	failed, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || failed.ClaimToken != "" || failed.ClaimedAt != nil || failed.LastError != "temporary provider failure" || failed.Attempts != 2 {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}

	if failed.NextAttemptAt == nil || !failed.NextAttemptAt.Equal(now.Add(14*time.Second)) {
		t.Fatalf("failed retry=%v want %v", failed.NextAttemptAt, now.Add(14*time.Second))
	}
	if _, won, err := st.ClaimReviewStatusComment(ctx, key, "too-early-backoff", now.Add(5*time.Second), now.Add(4*time.Second)); err != nil || won {
		t.Fatalf("backoff claim won=%v err=%v", won, err)
	}
	if pending, err := st.ListPendingReviewStatusComments(ctx, now.Add(5*time.Second), now.Add(4*time.Second), 10); err != nil || len(pending) != 0 {
		t.Fatalf("backoff pending=%+v err=%v", pending, err)
	}
	if _, err := st.ScheduleRun(ctx, run.ID, "review-job", "token-hash", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	runningRun, err := st.MarkRunning(ctx, run.ID, "Running", now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	runningObservation := domain.ReviewStatusObservationForRun(*runningRun)
	status, err := st.UpdateReviewStatusCommentDesired(ctx, key, run.ID, run.PRHeadSHA,
		domain.ReviewStatusRunning, "running", runningObservation, now.Add(6*time.Second))
	if err != nil || status.Attempts != 0 || status.LastError != "" || status.NextAttemptAt != nil {
		t.Fatalf("desired update did not clear backoff: status=%+v err=%v", status, err)
	}
	running, won, err := st.ClaimReviewStatusComment(ctx, key, "running", now.Add(8*time.Second), now.Add(7*time.Second))
	if err != nil || !won || running.Attempts != 1 {
		t.Fatalf("running claim=%+v won=%v err=%v", running, won, err)
	}
	applied, err := st.MarkReviewStatusCommentApplied(ctx, key, "running", "comment", "https://github.test/comment", domain.ReviewStatusRunning, "running", now.Add(9*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if applied.Attempts != 0 {
		t.Fatalf("successful apply attempts=%d want 0", applied.Attempts)
	}
	// Applied active rows are omitted until their linked Run fingerprint changes,
	// so a batch cannot be monopolized by no-op comments.
	pending, err := st.ListPendingReviewStatusComments(ctx, now.Add(9*time.Second), now.Add(9*time.Second), 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("unchanged running pending=%+v err=%v", pending, err)
	}
	succeededRun, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	pending, err = st.ListPendingReviewStatusComments(ctx, now.Add(10*time.Second), now.Add(10*time.Second), 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("changed run pending=%+v err=%v", pending, err)
	}
	completedObservation := domain.ReviewStatusObservationForRun(*succeededRun)
	if _, err := st.UpdateReviewStatusCommentDesired(ctx, key, run.ID, run.PRHeadSHA,
		domain.ReviewStatusCompleted, "completed", completedObservation, now.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, won, err := st.ClaimReviewStatusComment(ctx, key, "complete", now.Add(12*time.Second), now.Add(11*time.Second)); err != nil || !won {
		t.Fatalf("complete claim won=%v err=%v", won, err)
	}
	if _, err := st.MarkReviewStatusCommentApplied(ctx, key, "complete", "comment", "https://github.test/comment", domain.ReviewStatusCompleted, "completed", now.Add(13*time.Second)); err != nil {
		t.Fatal(err)
	}
	pending, err = st.ListPendingReviewStatusComments(ctx, now.Add(14*time.Second), now.Add(14*time.Second), 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("terminal pending=%+v err=%v", pending, err)
	}
}

func TestMemReviewStatusCommentPendingOrderMatchesPG(t *testing.T) {
	ctx := context.Background()
	st, projectID, serviceID := seedMemReviewStatusStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, prNumber := range []int{10, 2} {
		key := domain.ReviewStatusCommentKey{
			ServiceID: serviceID, Provider: domain.ProviderGitHub,
			ProviderRepoID: "same-repo", PRNumber: prNumber,
		}
		execution, run, intent := reviewStatusExecution(projectID, serviceID, key, domain.NewID(), strings.Repeat("a", 40), "queued", now)
		if _, won, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent); err != nil || !won {
			t.Fatalf("create PR %d won=%v err=%v", prNumber, won, err)
		}
	}
	pending, err := st.ListPendingReviewStatusComments(ctx, now, now, 1)
	if err != nil || len(pending) != 1 || pending[0].Key.PRNumber != 2 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestMemReviewStatusCommentDueRetryPrecedesNewerIntent(t *testing.T) {
	ctx := context.Background()
	st, projectID, serviceID := seedMemReviewStatusStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	retryKey := domain.ReviewStatusCommentKey{
		ServiceID: serviceID, Provider: domain.ProviderGitHub,
		ProviderRepoID: "fair-retry-repo", PRNumber: 1,
	}
	execution, run, intent := reviewStatusExecution(
		projectID, serviceID, retryKey, domain.NewID(), strings.Repeat("a", 40), "retry", now,
	)
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent); err != nil || !created {
		t.Fatalf("create retry intent: created=%v err=%v", created, err)
	}
	if _, won, err := st.ClaimReviewStatusComment(ctx, retryKey, "retry-claim", now.Add(time.Second), now); err != nil || !won {
		t.Fatalf("claim retry intent: won=%v err=%v", won, err)
	}
	failed, err := st.MarkReviewStatusCommentFailed(ctx, retryKey, "retry-claim", "temporary", now.Add(2*time.Second))
	if err != nil || failed.NextAttemptAt == nil || !failed.NextAttemptAt.Equal(now.Add(7*time.Second)) {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}

	newKey := domain.ReviewStatusCommentKey{
		ServiceID: serviceID, Provider: domain.ProviderGitHub,
		ProviderRepoID: "fair-retry-repo", PRNumber: 2,
	}
	execution, run, intent = reviewStatusExecution(
		projectID, serviceID, newKey, domain.NewID(), strings.Repeat("b", 40), "new", now.Add(8*time.Second),
	)
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent); err != nil || !created {
		t.Fatalf("create newer intent: created=%v err=%v", created, err)
	}
	pending, err := st.ListPendingReviewStatusComments(ctx, now.Add(9*time.Second), now.Add(9*time.Second), 1)
	if err != nil || len(pending) != 1 || pending[0].Key != retryKey {
		t.Fatalf("pending=%+v err=%v want due retry", pending, err)
	}
}

func TestMemReviewStatusCommentAppliedRowsDoNotStarveChangedRun(t *testing.T) {
	ctx := context.Background()
	st, projectID, serviceID := seedMemReviewStatusStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	var targetRun *domain.Run
	var targetKey domain.ReviewStatusCommentKey
	for i := 1; i <= 101; i++ {
		key := domain.ReviewStatusCommentKey{
			ServiceID: serviceID, Provider: domain.ProviderGitHub,
			ProviderRepoID: "fairness-repo", PRNumber: i,
		}
		execution, run, intent := reviewStatusExecution(
			projectID, serviceID, key, domain.NewID(), strings.Repeat("a", 40), "queued", now,
		)
		if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent); err != nil || !created {
			t.Fatalf("create PR %d: created=%v err=%v", i, created, err)
		}
		claim := "claim-" + domain.NewID()
		if _, won, err := st.ClaimReviewStatusComment(ctx, key, claim, now.Add(time.Second), now); err != nil || !won {
			t.Fatalf("claim PR %d: won=%v err=%v", i, won, err)
		}
		if _, err := st.MarkReviewStatusCommentApplied(ctx, key, claim, fmt.Sprint(i), "",
			domain.ReviewStatusQueued, "queued", now.Add(2*time.Second)); err != nil {
			t.Fatalf("apply PR %d: %v", i, err)
		}
		if i == 101 {
			targetRun, targetKey = run, key
		}
	}
	if _, err := st.ScheduleRun(ctx, targetRun.ID, "review-job", "token", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, targetRun.ID, "Running", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListPendingReviewStatusComments(ctx, now.Add(3*time.Second), now.Add(3*time.Second), 1)
	if err != nil || len(pending) != 1 || pending[0].Key != targetKey {
		t.Fatalf("pending=%+v err=%v want changed PR %d", pending, err, targetKey.PRNumber)
	}
}

func TestMemReviewStatusIngressSequenceRejectsLateOlderRevision(t *testing.T) {
	st, projectID, serviceID := seedMemReviewStatusStore(t)
	assertReviewStatusIngressSequenceFence(t, st, projectID, serviceID)
}

func TestMemReviewStatusCursorFencesIgnoredRevision(t *testing.T) {
	st, projectID, serviceID := seedMemReviewStatusStore(t)
	assertReviewStatusCursorFencesIgnoredRevision(t, st, projectID, serviceID)
}

func TestMemReviewStatusReceiptReclaimFencesOldWorker(t *testing.T) {
	st, projectID, serviceID := seedMemReviewStatusStore(t)
	assertReviewStatusReceiptReclaimFence(t, st, projectID, serviceID)
}

func TestPGReviewStatusReceiptReclaimFencesOldWorker(t *testing.T) {
	ctx := context.Background()
	st, seedRunID := pgTestStore(t)
	seed, err := st.GetRun(ctx, seedRunID)
	if err != nil {
		t.Fatal(err)
	}
	assertReviewStatusReceiptReclaimFence(t, st, seed.ProjectID, seed.ServiceID)
}

func assertReviewStatusReceiptReclaimFence(t *testing.T, st Store, projectID, serviceID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	oldClaimedAt := now.Add(-3 * time.Minute)
	receipt := &domain.WebhookReceipt{
		ID: domain.NewID(), Provider: domain.PluginGitHub, DeliveryID: domain.NewID(),
		PayloadDigest: "review-status-claim-" + domain.NewID(), Status: "received",
		ReceivedAt: oldClaimedAt, ClaimedAt: &oldClaimedAt, ClaimToken: "claim-old",
	}
	if claimed, err := st.ClaimWebhookReceipt(ctx, receipt); err != nil || !claimed {
		t.Fatalf("claim old receipt=%v err=%v", claimed, err)
	}
	key := domain.ReviewStatusCommentKey{
		ServiceID: serviceID, Provider: domain.ProviderGitHub,
		ProviderRepoID: "claim-fence-" + domain.NewID(), PRNumber: 75,
	}
	oldExecution, oldRun, oldIntent := reviewStatusExecution(
		projectID, serviceID, key, "claim-old-"+domain.NewID(), strings.Repeat("a", 40), "old", now,
	)
	oldIntent.AcceptedSequence = receipt.IngressSequence
	oldIntent.ReceiptClaimToken = receipt.ClaimToken
	if accepted, err := st.AdvanceReviewStatusCursor(ctx, key, receipt.IngressSequence,
		receipt.ClaimToken, oldRun.PRHeadSHA, now); err != nil || !accepted {
		t.Fatalf("old worker cursor accepted=%v err=%v", accepted, err)
	}

	freshClaimedAt := now
	reclaimed := *receipt
	reclaimed.ID = domain.NewID()
	reclaimed.ClaimToken = "claim-new"
	reclaimed.ReceivedAt = now
	reclaimed.ClaimedAt = &freshClaimedAt
	reclaimed.ReclaimBefore = now.Add(-2 * time.Minute)
	if claimed, err := st.ClaimWebhookReceipt(ctx, &reclaimed); err != nil || !claimed {
		t.Fatalf("reclaim receipt=%v err=%v", claimed, err)
	}
	if reclaimed.IngressSequence <= receipt.IngressSequence {
		t.Fatalf("reclaimed sequence=%d want newer than %d", reclaimed.IngressSequence, receipt.IngressSequence)
	}
	newHead := strings.Repeat("b", 40)
	if accepted, err := st.AdvanceReviewStatusCursor(ctx, key, reclaimed.IngressSequence,
		reclaimed.ClaimToken, newHead, now.Add(time.Second)); err != nil || !accepted {
		t.Fatalf("new worker cursor accepted=%v err=%v", accepted, err)
	}

	if accepted, err := st.AdvanceReviewStatusCursor(ctx, key, receipt.IngressSequence,
		receipt.ClaimToken, oldRun.PRHeadSHA, now.Add(2*time.Second)); err != nil || accepted {
		t.Fatalf("stale cursor accepted=%v err=%v", accepted, err)
	}
	if got, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, oldExecution, oldRun, oldIntent); err != nil || created || got != nil {
		t.Fatalf("stale create execution=%+v created=%v err=%v", got, created, err)
	}
	if _, err := st.GetRun(ctx, oldRun.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale worker Run err=%v want not found", err)
	}
}

func TestPGReviewStatusCursorFencesIgnoredRevision(t *testing.T) {
	ctx := context.Background()
	st, seedRunID := pgTestStore(t)
	seed, err := st.GetRun(ctx, seedRunID)
	if err != nil {
		t.Fatal(err)
	}
	assertReviewStatusCursorFencesIgnoredRevision(t, st, seed.ProjectID, seed.ServiceID)
}

func assertReviewStatusCursorFencesIgnoredRevision(t *testing.T, st Store, projectID, serviceID string) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	key := domain.ReviewStatusCommentKey{
		ServiceID: serviceID, Provider: domain.ProviderGitHub,
		ProviderRepoID: "cursor-" + domain.NewID(), PRNumber: 72,
	}
	oldExecution, oldRun, oldIntent := reviewStatusExecution(
		projectID, serviceID, key, "cursor-old-"+domain.NewID(), strings.Repeat("a", 40), "old", base.Add(time.Second),
	)
	newHead := strings.Repeat("b", 40)
	accepted, err := st.AdvanceReviewStatusCursor(ctx, key, oldIntent.AcceptedSequence+1, "", newHead, base.Add(2*time.Second))
	if err != nil || !accepted {
		t.Fatalf("advance ignored current head: accepted=%v err=%v", accepted, err)
	}
	if accepted, err = st.AdvanceReviewStatusCursor(ctx, key, oldIntent.AcceptedSequence+1, "", newHead, base.Add(2*time.Second)); err != nil || !accepted {
		t.Fatalf("idempotent same receipt: accepted=%v err=%v", accepted, err)
	}
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, oldExecution, oldRun, oldIntent); err != nil || created {
		t.Fatalf("cursor-fenced first status: created=%v err=%v", created, err)
	}
	if _, err := st.GetRun(ctx, oldRun.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cursor-fenced Run err=%v, want not found", err)
	}
	if _, err := st.GetReviewStatusComment(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cursor-fenced status err=%v, want not found", err)
	}
	if accepted, err = st.AdvanceReviewStatusCursor(ctx, key, oldIntent.AcceptedSequence, "", oldRun.PRHeadSHA, base.Add(3*time.Second)); err != nil || accepted {
		t.Fatalf("older cursor accepted=%v err=%v", accepted, err)
	}

	// When a previous head already queued a Run, a newer ignored Provider head
	// still cancels that Run so it cannot dispatch without a replacement.
	queuedKey := domain.ReviewStatusCommentKey{
		ServiceID: serviceID, Provider: domain.ProviderGitHub,
		ProviderRepoID: "cursor-queued-" + domain.NewID(), PRNumber: 71,
	}
	queuedExecution, queuedRun, queuedIntent := reviewStatusExecution(
		projectID, serviceID, queuedKey, "cursor-queued-"+domain.NewID(), strings.Repeat("c", 40), "queued", base.Add(4*time.Second),
	)
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, queuedExecution, queuedRun, queuedIntent); err != nil || !created {
		t.Fatalf("create queued head: created=%v err=%v", created, err)
	}
	accepted, err = st.AdvanceReviewStatusCursor(ctx, queuedKey, queuedIntent.AcceptedSequence+1, "", strings.Repeat("d", 40), base.Add(5*time.Second))
	if err != nil || !accepted {
		t.Fatalf("advance ignored head over queued: accepted=%v err=%v", accepted, err)
	}
	gotQueued, err := st.GetRun(ctx, queuedRun.ID)
	if err != nil || gotQueued.Status != domain.StatusCanceled || gotQueued.Phase != "Superseded" {
		t.Fatalf("ignored current head did not supersede queued Run=%+v err=%v", gotQueued, err)
	}
}

func TestPGReviewStatusIngressSequenceRejectsLateOlderRevision(t *testing.T) {
	ctx := context.Background()
	st, seedRunID := pgTestStore(t)
	seed, err := st.GetRun(ctx, seedRunID)
	if err != nil {
		t.Fatal(err)
	}
	assertReviewStatusIngressSequenceFence(t, st, seed.ProjectID, seed.ServiceID)
}

func assertReviewStatusIngressSequenceFence(t *testing.T, st Store, projectID, serviceID string) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	key := domain.ReviewStatusCommentKey{
		ServiceID: serviceID, Provider: domain.ProviderGitHub,
		ProviderRepoID: "sequence-" + domain.NewID(), PRNumber: 73,
	}
	coalesceKey := "review-sequence:" + domain.NewID()
	oldExecution, oldRun, oldIntent := reviewStatusExecution(
		projectID, serviceID, key, "old-"+domain.NewID(), strings.Repeat("a", 40), "old", base.Add(time.Second),
	)
	newExecution, newRun, newIntent := reviewStatusExecution(
		projectID, serviceID, key, "new-"+domain.NewID(), strings.Repeat("b", 40), "new", base.Add(2*time.Second),
	)
	oldRun.CoalesceKey, newRun.CoalesceKey = coalesceKey, coalesceKey

	// The newer provider read reaches the Store first. A request that read the
	// older head earlier but resumes later must roll back every provisional row.
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, newExecution, newRun, newIntent); err != nil || !created {
		t.Fatalf("create newer revision: created=%v err=%v", created, err)
	}
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, oldExecution, oldRun, oldIntent); err != nil || created {
		t.Fatalf("late older revision: created=%v err=%v", created, err)
	}
	if _, err := st.GetAutomationExecutionByEventKey(ctx, oldExecution.AutomationID, oldExecution.EventKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late older execution err=%v, want not found", err)
	}
	if _, err := st.GetRun(ctx, oldRun.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late older Run err=%v, want not found", err)
	}
	gotNewRun, err := st.GetRun(ctx, newRun.ID)
	if err != nil || gotNewRun.Status != domain.StatusQueued {
		t.Fatalf("newer Run=%+v err=%v, want queued", gotNewRun, err)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CurrentRunID != newRun.ID || status.HeadSHA != newRun.PRHeadSHA ||
		status.AcceptedSequence != newIntent.AcceptedSequence {
		t.Fatalf("newer status=%+v err=%v", status, err)
	}

	// A fresh delivery for an already-seen head is execution-idempotent, but it
	// still advances the PR ingress cursor. Otherwise an older handler paused in
	// the provider-read/DB-write gap could become current after this observation.
	duplicateExecution, duplicateRun, duplicateIntent := reviewStatusExecution(
		projectID, serviceID, key, newExecution.EventKey, newRun.PRHeadSHA, "duplicate", base.Add(4*time.Second),
	)
	duplicateRun.CoalesceKey = coalesceKey
	if accepted, err := st.AdvanceReviewStatusCursor(ctx, key, duplicateIntent.AcceptedSequence, "", duplicateIntent.HeadSHA, duplicateIntent.UpdatedAt); err != nil || !accepted {
		t.Fatalf("accept same-head Provider observation: accepted=%v err=%v", accepted, err)
	}
	if got, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, duplicateExecution, duplicateRun, duplicateIntent); err != nil || created || got == nil || got.RunID != newRun.ID {
		t.Fatalf("same-head cursor bump: execution=%+v created=%v err=%v", got, created, err)
	}
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AcceptedSequence != duplicateIntent.AcceptedSequence ||
		status.CurrentRunID != newRun.ID || status.DesiredBodyHash != "new" {
		t.Fatalf("same-head cursor status=%+v err=%v", status, err)
	}

	middleExecution, middleRun, middleIntent := reviewStatusExecution(
		projectID, serviceID, key, "middle-"+domain.NewID(), strings.Repeat("c", 40), "middle", base.Add(3*time.Second),
	)
	middleRun.CoalesceKey = coalesceKey
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, middleExecution, middleRun, middleIntent); err != nil || created {
		t.Fatalf("cursor-fenced middle revision: created=%v err=%v", created, err)
	}
	if got, err := st.GetRun(ctx, newRun.ID); err != nil || got.Status != domain.StatusQueued {
		t.Fatalf("cursor-fenced middle revision changed newer Run=%+v err=%v", got, err)
	}

	// A provider can legitimately return to a previously reviewed SHA after a
	// force-push (A -> B -> A). The head-derived event key for the last A matches
	// the first A, but this is a new accepted revision and needs its own Run.
	returnKey := domain.ReviewStatusCommentKey{
		ServiceID: serviceID, Provider: domain.ProviderGitHub,
		ProviderRepoID: "sequence-return-" + domain.NewID(), PRNumber: 74,
	}
	returnCoalesceKey := "review-sequence-return:" + domain.NewID()
	firstAExecution, firstARun, firstAIntent := reviewStatusExecution(
		projectID, serviceID, returnKey, "return-a-"+domain.NewID(), strings.Repeat("d", 40), "first-a", base.Add(10*time.Second),
	)
	secondBExecution, secondBRun, secondBIntent := reviewStatusExecution(
		projectID, serviceID, returnKey, "return-b-"+domain.NewID(), strings.Repeat("e", 40), "second-b", base.Add(11*time.Second),
	)
	returnedAExecution, returnedARun, returnedAIntent := reviewStatusExecution(
		projectID, serviceID, returnKey, firstAExecution.EventKey, firstARun.PRHeadSHA, "returned-a", base.Add(12*time.Second),
	)
	firstARun.CoalesceKey, secondBRun.CoalesceKey, returnedARun.CoalesceKey =
		returnCoalesceKey, returnCoalesceKey, returnCoalesceKey
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, firstAExecution, firstARun, firstAIntent); err != nil || !created {
		t.Fatalf("create first A: created=%v err=%v", created, err)
	}
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, secondBExecution, secondBRun, secondBIntent); err != nil || !created {
		t.Fatalf("create second B: created=%v err=%v", created, err)
	}
	if accepted, err := st.AdvanceReviewStatusCursor(ctx, returnKey, returnedAIntent.AcceptedSequence, "", returnedARun.PRHeadSHA, returnedAIntent.UpdatedAt); err != nil || !accepted {
		t.Fatalf("accept returned A Provider observation: accepted=%v err=%v", accepted, err)
	}
	gotReturnedExecution, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, returnedAExecution, returnedARun, returnedAIntent)
	if err != nil || !created || gotReturnedExecution == nil {
		t.Fatalf("create returned A: execution=%+v created=%v err=%v", gotReturnedExecution, created, err)
	}
	if gotReturnedExecution.EventKey == firstAExecution.EventKey || !strings.Contains(gotReturnedExecution.EventKey, ":return:") {
		t.Fatalf("returned A event key=%q, want distinct return key", gotReturnedExecution.EventKey)
	}
	returnedStatus, err := st.GetReviewStatusComment(ctx, returnKey)
	if err != nil || returnedStatus.CurrentRunID != returnedARun.ID ||
		returnedStatus.HeadSHA != returnedARun.PRHeadSHA || returnedStatus.DesiredBodyHash != "returned-a" ||
		returnedStatus.AcceptedSequence != returnedAIntent.AcceptedSequence {
		t.Fatalf("returned A status=%+v err=%v", returnedStatus, err)
	}
	if got, err := st.GetRun(ctx, secondBRun.ID); err != nil || got.Status != domain.StatusCanceled || got.Phase != "Superseded" {
		t.Fatalf("returned A did not supersede B Run=%+v err=%v", got, err)
	}
	if got, err := st.GetRun(ctx, returnedARun.ID); err != nil || got.Status != domain.StatusQueued || got.OriginEventKey != gotReturnedExecution.EventKey {
		t.Fatalf("returned A Run=%+v err=%v", got, err)
	}
}

func TestPGReviewStatusCommentLifecycle(t *testing.T) {
	ctx := context.Background()
	st, seedRunID := pgTestStore(t)
	seed, err := st.GetRun(ctx, seedRunID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	key := domain.ReviewStatusCommentKey{ServiceID: seed.ServiceID, Provider: domain.ProviderGitHub, ProviderRepoID: domain.NewID(), PRNumber: 31}
	execution, run, intent := reviewStatusExecution(seed.ProjectID, seed.ServiceID, key, domain.NewID(), strings.Repeat("d", 40), "queued", now)
	if _, won, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent); err != nil || !won {
		t.Fatalf("create won=%v err=%v", won, err)
	}
	replayExecution, replayRun, replayIntent := reviewStatusExecution(seed.ProjectID, seed.ServiceID, key, execution.EventKey, strings.Repeat("e", 40), "replayed", now.Add(time.Second))
	if got, won, err := st.CreateAutomationExecutionWithReviewStatus(ctx, replayExecution, replayRun, replayIntent); err != nil || won || got.RunID != run.ID {
		t.Fatalf("replay execution=%+v won=%v err=%v", got, won, err)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CurrentRunID != run.ID || status.DesiredBodyHash != "queued" {
		t.Fatalf("replay advanced status=%+v err=%v", status, err)
	}
	const contenders = 16
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, won, err := st.ClaimReviewStatusComment(ctx, key, "claim-"+domain.NewID(), now.Add(time.Second), now)
			if err != nil {
				t.Errorf("claim %d: %v", i, err)
			} else if won {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("claim wins=%d want 1", got)
	}
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil || status.ClaimToken == "" || status.Attempts != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err := st.MarkReviewStatusCommentApplied(ctx, key, status.ClaimToken, "comment-31", "", domain.ReviewStatusQueued, "queued", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListPendingReviewStatusComments(ctx, now.Add(3*time.Second), now.Add(3*time.Second), 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range pending {
		if candidate.Key == key {
			found = true
			break
		}
	}
	if found {
		t.Fatalf("unchanged applied status remained pending=%+v", pending)
	}

	if _, err := st.ScheduleRun(ctx, run.ID, "review-status-pg", "token", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	runningRun, err := st.MarkRunning(ctx, run.ID, "Running", now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	pending, err = st.ListPendingReviewStatusComments(ctx, now.Add(4*time.Second), now.Add(4*time.Second), 1000)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, candidate := range pending {
		if candidate.Key == key {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("changed Run observation was not pending=%+v", pending)
	}
	runningObservation := domain.ReviewStatusObservationForRun(*runningRun)
	if _, err := st.UpdateReviewStatusCommentDesired(ctx, key, run.ID, run.PRHeadSHA,
		domain.ReviewStatusRunning, "running", runningObservation, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	claimed, won, err := st.ClaimReviewStatusComment(ctx, key, "running-claim", now.Add(6*time.Second), now.Add(5*time.Second))
	if err != nil || !won || claimed.Attempts != 1 {
		t.Fatalf("running claim=%+v won=%v err=%v", claimed, won, err)
	}
	failed, err := st.MarkReviewStatusCommentFailed(ctx, key, "running-claim", "temporary", now.Add(7*time.Second))
	if err != nil || failed.NextAttemptAt == nil || !failed.NextAttemptAt.Equal(now.Add(12*time.Second)) {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	pending, err = st.ListPendingReviewStatusComments(ctx, now.Add(8*time.Second), now.Add(8*time.Second), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range pending {
		if candidate.Key == key {
			t.Fatalf("backed-off status remained pending=%+v", candidate)
		}
	}
	succeededRun, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", now.Add(9*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	completedObservation := domain.ReviewStatusObservationForRun(*succeededRun)
	updated, err := st.UpdateReviewStatusCommentDesired(ctx, key, run.ID, run.PRHeadSHA,
		domain.ReviewStatusCompleted, "completed", completedObservation, now.Add(10*time.Second))
	if err != nil || updated.Attempts != 0 || updated.LastError != "" || updated.NextAttemptAt != nil {
		t.Fatalf("completed update=%+v err=%v", updated, err)
	}
	if _, won, err := st.ClaimReviewStatusComment(ctx, key, "completed-claim", now.Add(11*time.Second), now.Add(10*time.Second)); err != nil || !won {
		t.Fatalf("completed claim won=%v err=%v", won, err)
	}
	if _, err := st.MarkReviewStatusCommentApplied(ctx, key, "completed-claim", "comment-31", "",
		domain.ReviewStatusCompleted, "completed", now.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	pending, err = st.ListPendingReviewStatusComments(ctx, now.Add(13*time.Second), now.Add(13*time.Second), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range pending {
		if candidate.Key == key {
			t.Fatalf("terminal applied status remained pending=%+v", candidate)
		}
	}
}

func seedMemReviewStatusStore(t *testing.T) (*MemStore, string, string) {
	t.Helper()
	ctx := context.Background()
	st := NewMemStore()
	now := time.Now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "review status", CreatedAt: now}
	service := &domain.Service{ID: domain.NewID(), ProjectID: project.ID, Name: "svc", RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: now}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	return st, project.ID, service.ID
}

func reviewStatusExecution(projectID, serviceID string, key domain.ReviewStatusCommentKey, eventKey, headSHA, bodyHash string, at time.Time) (*domain.AutomationExecution, *domain.Run, *domain.ReviewStatusComment) {
	runID := domain.NewID()
	execution := &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: "review-automation", AutomationName: "PR review",
		ProjectID: projectID, ServiceID: serviceID, TriggerKind: "scm", EventKey: eventKey,
		State: domain.AutomationExecutionQueued, OutputMode: domain.AutomationOutputRunOnly,
		RunID: runID, CreatedAt: at, UpdatedAt: at,
	}
	run := &domain.Run{
		ID: runID, ProjectID: projectID, ServiceID: serviceID, Prompt: "review",
		Status: domain.StatusQueued, Kind: domain.RunKindReview, Phase: "Queued",
		PRNumber: key.PRNumber, PRHeadBranch: "feature", PRBaseBranch: "main",
		PRHeadSHA: headSHA, PRBaseSHA: strings.Repeat("e", 40), Attempt: 1, CreatedAt: at,
		OriginEventKey: eventKey,
	}
	intent := &domain.ReviewStatusComment{
		Key: key, RepositoryPath: "acme/repo", CurrentRunID: runID, HeadSHA: headSHA,
		DesiredState: domain.ReviewStatusQueued, DesiredBodyHash: bodyHash,
		ObservedRun:      domain.ReviewStatusObservationForRun(*run),
		AcceptedSequence: at.UnixNano(), CreatedAt: at, UpdatedAt: at,
	}
	return execution, run, intent
}
