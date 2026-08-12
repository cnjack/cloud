package reconciler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

type blockingReviewStatusProvider struct {
	*provider.FakeProvider
	blockMu sync.Mutex
	blocked bool
	started chan struct{}
	release chan struct{}
}

func (p *blockingReviewStatusProvider) UpdateIssueComment(
	ctx context.Context,
	owner, repo string,
	issueNumber int,
	commentID, body string,
) (*provider.IssueComment, error) {
	p.blockMu.Lock()
	shouldBlock := !p.blocked
	p.blocked = true
	p.blockMu.Unlock()
	if shouldBlock {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}
	return p.FakeProvider.UpdateIssueComment(ctx, owner, repo, issueNumber, commentID, body)
}

func seedReviewStatusRun(t *testing.T, rec *Reconciler, st *store.MemStore) (domain.Run, domain.ReviewStatusCommentKey) {
	return seedReviewStatusRunWithSnapshot(t, rec, st, true)
}

func seedReviewStatusRunWithoutSnapshot(t *testing.T, rec *Reconciler, st *store.MemStore) (domain.Run, domain.ReviewStatusCommentKey) {
	return seedReviewStatusRunWithSnapshot(t, rec, st, false)
}

func seedReviewStatusRunWithSnapshot(t *testing.T, rec *Reconciler, st *store.MemStore, freezeSnapshot bool) (domain.Run, domain.ReviewStatusCommentKey) {
	t.Helper()
	ctx := context.Background()
	now := rec.now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "review status", CreatedAt: now}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	cipher := prTestCipher(t)
	sealedToken, err := cipher.EncryptString("review-status-token")
	if err != nil {
		t.Fatal(err)
	}
	providerConfig := &domain.ProviderConfig{
		Provider: domain.PluginGitea, BaseURL: "https://gitea.example", PluginEnabled: true,
	}
	if err := st.UpsertProviderConfig(ctx, providerConfig); err != nil {
		t.Fatal(err)
	}
	providerConfig, err = st.GetProviderConfig(ctx, domain.PluginGitea)
	if err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID: domain.NewID(), ProjectID: project.ID, Provider: domain.PluginGitea,
		Status: domain.PluginStatusEnabled, AccessTokenEnc: sealedToken,
		ConfigRevision: providerConfig.ConfigRevision, CreatedAt: now,
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	rec.creds = credentials.NewResolver(st, cipher, nil, "", nil)
	repoID := int64(42)
	service := &domain.Service{
		ID: domain.NewID(), ProjectID: project.ID, Name: "default",
		RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitea,
		RepoOwnerName: "jcloud/seed", ProviderRepoID: &repoID, DefaultBranch: "main",
		GitMode: domain.GitModeReadonly, CreatedAt: now,
	}
	binding := &domain.ServiceRepositoryBinding{
		ServiceID: service.ID, InstallationID: installation.ID, ProviderRepoID: "42",
		RepositoryPath: service.RepoOwnerName, CloneURL: "https://gitea.example/jcloud/seed.git",
		DefaultBranch: "main", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreatePluginBoundService(ctx, service, binding); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: project.ID, ServiceID: service.ID,
		Prompt: "review the pull request", Status: domain.StatusQueued, Kind: domain.RunKindReview,
		Origin: domain.RunOriginAutomation, PRNumber: 7, PRTitle: "Keep transaction boundaries atomic",
		PRURL:        "https://gitea.example/jcloud/seed/pulls/7",
		PRHeadBranch: "feature", PRBaseBranch: "main",
		PRHeadSHA: strings.Repeat("a", 40), PRBaseSHA: strings.Repeat("b", 40),
		DeliveryStatus: domain.DeliveryPending, DeliveryKind: domain.DeliveryReviewComment,
		CreatedAt: now,
	}
	key := domain.ReviewStatusCommentKey{
		ServiceID: service.ID, Provider: domain.ProviderGitea,
		ProviderRepoID: "42", PRNumber: run.PRNumber,
	}
	body, err := domain.RenderReviewStatusComment(domain.ReviewStatusCommentInput{
		Provider: key.Provider, State: domain.ReviewStatusQueued, Run: *run,
		RunURL: strings.TrimRight(rec.cfg.ConsoleURL, "/") + "/runs/" + run.ID,
		Marker: domain.ReviewStatusCommentMarker(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	execution := &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: domain.NewID(), AutomationName: "review",
		PromptSnapshot: run.Prompt, ProjectID: project.ID, ServiceID: service.ID,
		TriggerKind: "scm", EventKey: domain.NewID(), State: domain.AutomationExecutionQueued,
		RunID: run.ID, CreatedAt: now,
	}
	intent := &domain.ReviewStatusComment{
		Key: key, RepositoryPath: service.RepoOwnerName,
		CurrentRunID: run.ID, HeadSHA: run.PRHeadSHA, AcceptedSequence: 1,
		DesiredState:    domain.ReviewStatusQueued,
		DesiredBodyHash: domain.ReviewStatusCommentBodyHash(body),
		CreatedAt:       now, UpdatedAt: now,
	}
	if freezeSnapshot {
		intent.InstallationID = installation.ID
	}
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, run, intent); err != nil || !created {
		t.Fatalf("create review status run: created=%v err=%v", created, err)
	}
	return *run, key
}

func TestReviewStatusStateForRun(t *testing.T) {
	postedAt := time.Now().UTC()
	cases := []struct {
		name string
		run  domain.Run
		want domain.ReviewStatusState
	}{
		{"queued", domain.Run{Status: domain.StatusQueued}, domain.ReviewStatusQueued},
		{"scheduling", domain.Run{Status: domain.StatusScheduling}, domain.ReviewStatusQueued},
		{"running", domain.Run{Status: domain.StatusRunning}, domain.ReviewStatusRunning},
		{"awaiting input", domain.Run{Status: domain.StatusAwaitingInput}, domain.ReviewStatusRunning},
		{"publishing", domain.Run{Status: domain.StatusSucceeded, DeliveryStatus: domain.DeliveryPending}, domain.ReviewStatusPublishing},
		{"published", domain.Run{Status: domain.StatusSucceeded, DeliveryStatus: domain.DeliveryDelivered, ReviewPostedAt: &postedAt}, domain.ReviewStatusCompleted},
		{"delivery failed", domain.Run{Status: domain.StatusSucceeded, DeliveryStatus: domain.DeliveryFailed}, domain.ReviewStatusFailed},
		{"run failed", domain.Run{Status: domain.StatusFailed}, domain.ReviewStatusFailed},
		{"blocked", domain.Run{Status: domain.StatusBlocked}, domain.ReviewStatusFailed},
		{"canceled", domain.Run{Status: domain.StatusCanceled}, domain.ReviewStatusCanceled},
		{"superseded", domain.Run{Status: domain.StatusCanceled, Phase: "Superseded"}, domain.ReviewStatusSuperseded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewStatusStateForRun(&tc.run); got != tc.want {
				t.Fatalf("state=%q want %q", got, tc.want)
			}
		})
	}
}

func TestReviewStatusLiveGrantFallbackEligibility(t *testing.T) {
	startedAt := time.Now().UTC()
	cleanedAt := startedAt.Add(time.Second)
	base := domain.Run{
		Kind: domain.RunKindReview, Origin: domain.RunOriginAutomation,
		Status: domain.StatusCanceled,
	}
	cases := []struct {
		name string
		run  *domain.Run
		want bool
	}{
		{name: "queued automatic review before dispatch", run: func() *domain.Run { v := base; v.Status = domain.StatusQueued; return &v }(), want: true},
		{name: "canceled automatic review before dispatch", run: &base, want: true},
		{name: "failed automatic review before dispatch", run: func() *domain.Run { v := base; v.Status = domain.StatusFailed; return &v }(), want: true},
		{name: "blocked automatic review before dispatch", run: func() *domain.Run { v := base; v.Status = domain.StatusBlocked; return &v }(), want: true},
		{name: "claimed job", run: func() *domain.Run { v := base; v.K8sJobName = "review-job"; return &v }()},
		{name: "issued runner token", run: func() *domain.Run { v := base; v.TokenHash = "token-hash"; return &v }()},
		{name: "runner started", run: func() *domain.Run { v := base; v.StartedAt = &startedAt; return &v }()},
		{name: "job cleanup recorded", run: func() *domain.Run { v := base; v.JobCleanedAt = &cleanedAt; return &v }()},
		{name: "manual review", run: func() *domain.Run { v := base; v.Origin = domain.RunOriginAPI; return &v }()},
		{name: "automatic agent", run: func() *domain.Run { v := base; v.Kind = domain.RunKindAgent; return &v }()},
		{name: "succeeded state", run: func() *domain.Run { v := base; v.Status = domain.StatusSucceeded; return &v }()},
		{name: "nil", run: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewStatusCanUseLiveGrant(tc.run); got != tc.want {
				t.Fatalf("eligible=%v want %v run=%+v", got, tc.want, tc.run)
			}
		})
	}
}

func TestReconcileReviewStatusFailedBeforeFirstDispatch(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	run, key := seedReviewStatusRunWithoutSnapshot(t, rec, st)

	failed, err := st.MarkFailed(ctx, run.ID, "Failed", domain.FailureSetupFailed,
		"the configured model is unavailable", rec.now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if failed.K8sJobName != "" || failed.TokenHash != "" || failed.StartedAt != nil || failed.JobCleanedAt != nil {
		t.Fatalf("failure-before-dispatch has dispatch evidence: %+v", failed)
	}

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || len(fake.UpdatedComments) != 0 ||
		!strings.Contains(fake.Comments[0].Body, "Review failed") ||
		!strings.Contains(fake.Comments[0].Body, domain.ReviewStatusCommentMarker(key)) {
		t.Fatalf("failed status comments=%+v updates=%+v", fake.Comments, fake.UpdatedComments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AppliedState != domain.ReviewStatusFailed ||
		status.DesiredState != domain.ReviewStatusFailed || status.LastError != "" {
		t.Fatalf("failed status=%+v err=%v", status, err)
	}
}

func TestReconcileReviewStatusCanceledBeforeFirstDispatch(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	run, key := seedReviewStatusRunWithoutSnapshot(t, rec, st)

	canceled, err := st.CancelRun(ctx, run.ID, "CanceledByOperator", rec.now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if canceled.K8sJobName != "" || canceled.TokenHash != "" || canceled.StartedAt != nil || canceled.JobCleanedAt != nil {
		t.Fatalf("cancel-before-dispatch has dispatch evidence: %+v", canceled)
	}

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || len(fake.UpdatedComments) != 0 ||
		!strings.Contains(fake.Comments[0].Body, "Review canceled") ||
		!strings.Contains(fake.Comments[0].Body, domain.ReviewStatusCommentMarker(key)) {
		t.Fatalf("canceled status comments=%+v updates=%+v", fake.Comments, fake.UpdatedComments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AppliedState != domain.ReviewStatusCanceled ||
		status.DesiredState != domain.ReviewStatusCanceled || status.LastError != "" {
		t.Fatalf("canceled status=%+v err=%v", status, err)
	}
}

func TestReconcileReviewStatusUsesAcceptedSnapshotAfterRepositoryRebind(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	run, key := seedReviewStatusRun(t, rec, st)

	snapshots, err := st.ListRunPluginSnapshots(ctx, run.ID)
	if err != nil || len(snapshots) != 1 || snapshots[0].RepositoryID != key.ProviderRepoID ||
		snapshots[0].RepositoryPath != "jcloud/seed" {
		t.Fatalf("acceptance snapshots=%+v err=%v", snapshots, err)
	}
	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || !strings.Contains(fake.Comments[0].Body, "Review queued") {
		t.Fatalf("queued comments=%+v", fake.Comments)
	}

	binding, err := st.GetServiceRepositoryBinding(ctx, run.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	binding.ProviderRepoID = "99"
	binding.RepositoryPath = "jcloud/rebound"
	binding.CloneURL = "https://gitea.example/jcloud/rebound.git"
	if err := st.UpsertServiceRepositoryBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelRun(ctx, run.ID, "CanceledAfterRebind", rec.now().UTC()); err != nil {
		t.Fatal(err)
	}

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || len(fake.UpdatedComments) != 1 ||
		!strings.Contains(fake.UpdatedComments[0].Body, "Review canceled") {
		t.Fatalf("rebound comments=%+v updates=%+v", fake.Comments, fake.UpdatedComments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AppliedState != domain.ReviewStatusCanceled || status.LastError != "" {
		t.Fatalf("rebound status=%+v err=%v", status, err)
	}
}

func TestReconcileReviewStatusFailedAfterDispatchClaimKeepsAcceptedSnapshot(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	run, key := seedReviewStatusRun(t, rec, st)

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || !strings.Contains(fake.Comments[0].Body, "Review queued") {
		t.Fatalf("queued comments=%+v", fake.Comments)
	}
	snapshots, err := rec.runPluginSnapshotCandidates(ctx, &run)
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("dispatch snapshots=%+v err=%v", snapshots, err)
	}
	binding, err := st.GetServiceRepositoryBinding(ctx, run.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimRunDispatch(ctx, run.ID, "review-job", "token-hash", "PreparingWorkspace",
		binding.InstallationID, snapshots)
	if err != nil || claimed.Status != domain.StatusScheduling {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	failed, err := st.FailRunDispatch(ctx, run.ID, "review-job", "Failed", "job create failed", rec.now().UTC())
	if err != nil || failed.Status != domain.StatusFailed {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	retained, err := st.ListRunPluginSnapshots(ctx, run.ID)
	if err != nil || len(retained) != 1 || retained[0].RepositoryID != key.ProviderRepoID {
		t.Fatalf("retained snapshots=%+v err=%v", retained, err)
	}

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.UpdatedComments) != 1 || !strings.Contains(fake.UpdatedComments[0].Body, "Review failed") {
		t.Fatalf("failed updates=%+v", fake.UpdatedComments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AppliedState != domain.ReviewStatusFailed || status.LastError != "" {
		t.Fatalf("failed status=%+v err=%v", status, err)
	}
}

func TestReviewDispatchFailureAfterBindingDeletionFailsVisibly(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	run, key := seedReviewStatusRun(t, rec, st)

	if err := st.DeleteServiceRepositoryBinding(ctx, run.ServiceID); err != nil {
		t.Fatal(err)
	}
	project, err := st.GetProject(ctx, run.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if created := rec.createJob(ctx, &run, project); created {
		t.Fatal("binding-less accepted review unexpectedly created a Job")
	}
	failed, err := st.GetRun(ctx, run.ID)
	if err != nil || failed.Status != domain.StatusFailed || failed.FailureReason != domain.FailureSetupFailed {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || !strings.Contains(fake.Comments[0].Body, "Review failed") {
		t.Fatalf("failed comments=%+v", fake.Comments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AppliedState != domain.ReviewStatusFailed || status.LastError != "" {
		t.Fatalf("failed status=%+v err=%v", status, err)
	}
}

func TestReconcileReviewStatusNeverFallsBackPastMismatchedFrozenGrant(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	run, key := seedReviewStatusRunWithoutSnapshot(t, rec, st)

	snapshots, err := rec.runPluginSnapshotCandidates(ctx, &run)
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("snapshot candidates=%+v err=%v", snapshots, err)
	}
	snapshots[0].RepositoryID = "a-different-repository"
	if err := st.CreateRunPluginSnapshots(ctx, snapshots); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelRun(ctx, run.ID, "CanceledByOperator", rec.now().UTC()); err != nil {
		t.Fatal(err)
	}

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 0 || len(fake.UpdatedComments) != 0 {
		t.Fatalf("mismatched frozen grant fell back to live credentials: comments=%+v updates=%+v", fake.Comments, fake.UpdatedComments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.Attempts != 1 || status.LastError != reviewStatusProviderFailure || status.CommentID != "" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestReconcileReviewStatusCommentLifecycleAndIdempotency(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	run, key := seedReviewStatusRun(t, rec, st)

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || !strings.Contains(fake.Comments[0].Body, "Review queued") ||
		!strings.Contains(fake.Comments[0].Body, domain.ReviewStatusCommentMarker(key)) {
		t.Fatalf("created status comments=%+v", fake.Comments)
	}
	commentID := fake.Comments[0].ID
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CommentID != commentID || status.AppliedState != domain.ReviewStatusQueued {
		t.Fatalf("queued status=%+v err=%v", status, err)
	}

	// An unchanged non-terminal projection remains observable in the outbox, but
	// must not issue another provider request.
	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || len(fake.UpdatedComments) != 0 {
		t.Fatalf("unchanged status wrote provider: comments=%d updates=%d", len(fake.Comments), len(fake.UpdatedComments))
	}

	if _, err := st.ScheduleRun(ctx, run.ID, "review-job", "token-hash", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "Running", rec.now()); err != nil {
		t.Fatal(err)
	}
	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || len(fake.UpdatedComments) != 1 ||
		fake.UpdatedComments[0].ID != commentID || !strings.Contains(fake.UpdatedComments[0].Body, "Review in progress") {
		debugStatus, _ := st.GetReviewStatusComment(ctx, key)
		debugRun, _ := st.GetRun(ctx, run.ID)
		debugPending, _ := st.ListPendingReviewStatusComments(ctx, rec.now(), rec.now().Add(-reviewStatusCommentClaimTTL), 10)
		t.Fatalf("running status comments=%+v updates=%+v status=%+v run=%+v pending=%+v", fake.Comments, fake.UpdatedComments, debugStatus, debugRun, debugPending)
	}

	if _, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", rec.now()); err != nil {
		t.Fatal(err)
	}
	rec.reconcileReviewStatusComments(ctx)
	if len(fake.UpdatedComments) != 2 || !strings.Contains(fake.UpdatedComments[1].Body, "Publishing review") {
		t.Fatalf("publishing updates=%+v", fake.UpdatedComments)
	}
	if _, err := st.MarkReviewPosted(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRunDelivery(ctx, run.ID, domain.DeliveryDelivered, domain.DeliveryReviewComment, "", rec.now()); err != nil {
		t.Fatal(err)
	}
	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || len(fake.UpdatedComments) != 3 ||
		fake.UpdatedComments[2].ID != commentID || !strings.Contains(fake.UpdatedComments[2].Body, "Review completed") {
		t.Fatalf("completed comments=%+v updates=%+v", fake.Comments, fake.UpdatedComments)
	}
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AppliedState != domain.ReviewStatusCompleted || status.DesiredState != domain.ReviewStatusCompleted || status.LastError != "" {
		t.Fatalf("completed status=%+v err=%v", status, err)
	}
	now := rec.now().UTC()
	pending, err := st.ListPendingReviewStatusComments(ctx, now, now.Add(-reviewStatusCommentClaimTTL), 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("terminal pending=%+v err=%v", pending, err)
	}
}

func TestReconcileReviewStatusAdoptsMarkerAfterAmbiguousCreate(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	_, key := seedReviewStatusRun(t, rec, st)
	marker := domain.ReviewStatusCommentMarker(key)
	if err := fake.SeedIssueComment("jcloud", "seed", 7, provider.IssueComment{
		ID: "88", URL: "https://gitea.example/jcloud/seed/issues/7#issuecomment-88",
		Body: marker + "\n\nInterrupted before the comment id was persisted.", AuthorID: fake.UserID,
	}); err != nil {
		t.Fatal(err)
	}

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || len(fake.UpdatedComments) != 1 || fake.UpdatedComments[0].ID != "88" {
		t.Fatalf("marker recovery created a duplicate: comments=%+v updates=%+v", fake.Comments, fake.UpdatedComments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CommentID != "88" || status.AppliedState != domain.ReviewStatusQueued {
		t.Fatalf("adopted status=%+v err=%v", status, err)
	}
}

func TestReconcileReviewStatusNeverAdoptsCopiedMarkerFromAnotherAuthor(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	_, key := seedReviewStatusRun(t, rec, st)
	marker := domain.ReviewStatusCommentMarker(key)
	if err := fake.SeedIssueComment("jcloud", "seed", 7, provider.IssueComment{
		ID: "88", Body: marker + "\n\nCopied by a participant.", AuthorID: "77", AuthorLogin: "participant",
	}); err != nil {
		t.Fatal(err)
	}

	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 2 || len(fake.UpdatedComments) != 0 {
		t.Fatalf("copied marker was adopted: comments=%+v updates=%+v", fake.Comments, fake.UpdatedComments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CommentID == "" || status.CommentID == "88" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestReconcileReviewStatusSerializesNewHeadAgainstInflightPatch(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	now := time.Now().UTC().Truncate(time.Second)
	rec.now = func() time.Time { return now }
	fake := &blockingReviewStatusProvider{
		FakeProvider: provider.NewFakeProvider(),
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	wirePRStack(rec, st, fake)
	oldRun, key := seedReviewStatusRun(t, rec, st)
	rec.reconcileReviewStatusComments(ctx) // create the one queued comment
	if len(fake.Comments) != 1 {
		t.Fatalf("initial comments=%+v", fake.Comments)
	}
	if _, err := st.ScheduleRun(ctx, oldRun.ID, "review-job", "token-hash", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, oldRun.ID, "Running", now); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.reconcileReviewStatusComments(ctx) // blocks in the provider PATCH
	}()
	select {
	case <-fake.started:
	case <-time.After(5 * time.Second):
		t.Fatal("old-head provider update did not start")
	}

	// A webhook accepts a newer head while the old provider PATCH is in flight.
	// The transaction advances desired ownership but must preserve the lease.
	newRun := &domain.Run{
		ID: domain.NewID(), ProjectID: oldRun.ProjectID, ServiceID: oldRun.ServiceID,
		Prompt: oldRun.Prompt, Status: domain.StatusQueued, Kind: domain.RunKindReview,
		Origin: domain.RunOriginAutomation, PRNumber: oldRun.PRNumber, PRTitle: oldRun.PRTitle,
		PRURL: oldRun.PRURL, PRHeadBranch: oldRun.PRHeadBranch, PRBaseBranch: oldRun.PRBaseBranch,
		PRHeadSHA: strings.Repeat("c", 40), PRBaseSHA: oldRun.PRBaseSHA,
		DeliveryStatus: domain.DeliveryPending, DeliveryKind: domain.DeliveryReviewComment,
		CreatedAt: now.Add(time.Second),
	}
	body, err := domain.RenderReviewStatusComment(domain.ReviewStatusCommentInput{
		Provider: key.Provider, State: domain.ReviewStatusQueued, Run: *newRun,
		RunURL: reviewStatusRunURL(rec.cfg.ConsoleURL, newRun.ID),
		Marker: domain.ReviewStatusCommentMarker(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	execution := &domain.AutomationExecution{
		ID: domain.NewID(), AutomationID: domain.NewID(), AutomationName: "review",
		PromptSnapshot: newRun.Prompt, ProjectID: newRun.ProjectID, ServiceID: newRun.ServiceID,
		TriggerKind: "scm", EventKey: domain.NewID(), State: domain.AutomationExecutionQueued,
		RunID: newRun.ID, CreatedAt: newRun.CreatedAt,
	}
	intent := &domain.ReviewStatusComment{
		Key: key, RepositoryPath: "jcloud/seed", CurrentRunID: newRun.ID, HeadSHA: newRun.PRHeadSHA, AcceptedSequence: 2,
		DesiredState: domain.ReviewStatusQueued, DesiredBodyHash: domain.ReviewStatusCommentBodyHash(body),
		CreatedAt: newRun.CreatedAt, UpdatedAt: newRun.CreatedAt,
	}
	if _, created, err := st.CreateAutomationExecutionWithReviewStatus(ctx, execution, newRun, intent); err != nil || !created {
		t.Fatalf("new head created=%v err=%v", created, err)
	}
	rec.reconcileReviewStatusComments(ctx)
	if len(fake.UpdatedComments) != 0 {
		t.Fatalf("new head overlapped old provider PATCH: updates=%+v", fake.UpdatedComments)
	}

	close(fake.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("old-head provider update did not finish")
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CurrentRunID != newRun.ID || status.ClaimToken == "" ||
		status.AppliedBodyHash == status.DesiredBodyHash {
		t.Fatalf("fenced status=%+v err=%v", status, err)
	}

	// Once the old lease expires, the next worker applies the new head last.
	now = now.Add(reviewStatusCommentClaimTTL + time.Second)
	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || len(fake.UpdatedComments) != 2 {
		t.Fatalf("comments=%+v updates=%+v", fake.Comments, fake.UpdatedComments)
	}
	last := fake.UpdatedComments[len(fake.UpdatedComments)-1]
	if !strings.Contains(last.Body, "Review queued") || !strings.Contains(last.Body, strings.Repeat("c", 12)) {
		t.Fatalf("remote comment did not converge to new head: %+v", last)
	}
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AppliedBodyHash != status.DesiredBodyHash || status.ClaimToken != "" {
		t.Fatalf("converged status=%+v err=%v", status, err)
	}
}

func TestReconcileReviewStatusFailureIsSanitizedAndBackedOff(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	now := time.Now().UTC().Truncate(time.Second)
	rec.now = func() time.Time { return now }
	fake := provider.NewFakeProvider()
	fake.CommentErr = errors.New("provider outage: secret-token-123")
	wirePRStack(rec, st, fake)
	_, key := seedReviewStatusRun(t, rec, st)

	rec.reconcileReviewStatusComments(ctx)
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.Attempts != 1 || status.LastError == "" || strings.Contains(status.LastError, "secret-token-123") {
		t.Fatalf("failed status=%+v err=%v", status, err)
	}
	rec.reconcileReviewStatusComments(ctx)
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil || status.Attempts != 1 {
		t.Fatalf("backoff did not suppress immediate retry: status=%+v err=%v", status, err)
	}

	fake.CommentErr = nil
	now = now.Add(time.Minute)
	rec.reconcileReviewStatusComments(ctx)
	status, err = st.GetReviewStatusComment(ctx, key)
	if err != nil || status.CommentID == "" || status.LastError != "" || status.Attempts != 0 {
		t.Fatalf("recovered status=%+v err=%v", status, err)
	}
}

func TestNativeReviewTickIsIndependentFromStatusCommentWorker(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := provider.NewFakeProvider()
	wirePRStack(rec, st, fake)
	run, key := seedReviewStatusRun(t, rec, st)
	if _, err := st.SetReviewOutput(ctx, run.ID, "No actionable findings."); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ScheduleRun(ctx, run.ID, "review-job", "token-hash", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "Running", rec.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", rec.now()); err != nil {
		t.Fatal(err)
	}

	rec.Tick(ctx)
	if fake.ReviewCount() != 1 || len(fake.Comments) != 0 {
		t.Fatalf("native reviews=%d status comments=%+v", fake.ReviewCount(), fake.Comments)
	}
	rec.reconcileReviewStatusComments(ctx)
	if len(fake.Comments) != 1 || !strings.Contains(fake.Comments[0].Body, "Review completed") {
		t.Fatalf("status comments=%+v", fake.Comments)
	}
	status, err := st.GetReviewStatusComment(ctx, key)
	if err != nil || status.AppliedState != domain.ReviewStatusCompleted {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestBlockedStatusProviderDoesNotBlockNativeReviewTick(t *testing.T) {
	ctx := context.Background()
	rec, st, _ := testRec(t, 4)
	rec.cfg.ConsoleURL = "https://cloud.example"
	fake := &blockingReviewStatusProvider{
		FakeProvider: provider.NewFakeProvider(), started: make(chan struct{}), release: make(chan struct{}),
	}
	wirePRStack(rec, st, fake)
	run, _ := seedReviewStatusRun(t, rec, st)
	rec.reconcileReviewStatusComments(ctx) // establish the mutable comment
	if _, err := st.SetReviewOutput(ctx, run.ID, "No actionable findings."); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ScheduleRun(ctx, run.ID, "review-job", "token-hash", "Scheduling"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning(ctx, run.ID, "Running", rec.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSucceeded(ctx, run.ID, "Succeeded", rec.now()); err != nil {
		t.Fatal(err)
	}

	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		rec.reconcileReviewStatusComments(ctx)
	}()
	select {
	case <-fake.started:
	case <-time.After(5 * time.Second):
		t.Fatal("status provider update did not block")
	}

	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		rec.Tick(ctx)
	}()
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("main reconciler tick was blocked by status provider I/O")
	}
	if fake.ReviewCount() != 1 {
		t.Fatalf("native reviews=%d want 1", fake.ReviewCount())
	}
	close(fake.release)
	select {
	case <-statusDone:
	case <-time.After(5 * time.Second):
		t.Fatal("status provider update did not finish")
	}
}
