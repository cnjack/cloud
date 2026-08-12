package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/credentials"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/store"
)

const (
	reviewStatusCommentBatchSize = 100
	reviewStatusCommentWorkers   = 8
	reviewStatusCommentListLimit = 100
	reviewStatusCommentClaimTTL  = 2 * time.Minute
	// Keep the complete provider operation below the lease lifetime. Individual
	// provider requests are already capped at 15 seconds; this is the outer bound
	// across marker lookup and create/update recovery.
	reviewStatusProviderTimeout = 90 * time.Second

	reviewStatusProviderFailure = "The provider status comment could not be synchronized; Cloud will retry automatically."
	reviewStatusInternalFailure = "The review status projection could not be synchronized; Cloud will retry automatically."
)

func (r *Reconciler) runReviewStatusCommentLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.ReconcileInterval)
	defer ticker.Stop()
	r.reconcileReviewStatusComments(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reconcileReviewStatusComments(ctx)
		}
	}
}

// reconcileReviewStatusComments maintains one mutable progress comment for
// each automatically accepted Service+PR review. The provider write is an
// outbox consumer: execution creation never depends on provider availability,
// leases fence concurrent reconcilers, and the hidden marker recovers an
// ambiguous create after a process or database failure.
func (r *Reconciler) reconcileReviewStatusComments(ctx context.Context) {
	now := r.now().UTC()
	comments, err := r.st.ListPendingReviewStatusComments(
		ctx, now, now.Add(-reviewStatusCommentClaimTTL), reviewStatusCommentBatchSize,
	)
	if err != nil {
		r.log.Error("reconcile review status: list pending comments", "err", err)
		return
	}
	workers := reviewStatusCommentWorkers
	if workers > len(comments) {
		workers = len(comments)
	}
	if workers == 0 {
		return
	}
	jobs := make(chan domain.ReviewStatusComment)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range jobs {
				// Claim timestamps must be sampled immediately before each row. A
				// batch-level timestamp can already be stale after earlier HTTP calls.
				r.reconcileReviewStatusComment(ctx, &row, r.now().UTC())
			}
		}()
	}
	for i := range comments {
		select {
		case jobs <- comments[i]:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
}

func (r *Reconciler) reconcileReviewStatusComment(ctx context.Context, row *domain.ReviewStatusComment, now time.Time) {
	run, err := r.st.GetRun(ctx, row.CurrentRunID)
	if err != nil {
		r.log.Warn("reconcile review status: load run", "run", row.CurrentRunID, "err", err)
		return
	}
	state := reviewStatusStateForRun(run)
	renderRun := *run
	renderRun.FailureMessage = reviewStatusFailureMessage(run, state)
	body, err := domain.RenderReviewStatusComment(domain.ReviewStatusCommentInput{
		Provider: row.Key.Provider, State: state, Run: renderRun,
		Plan:   reviewStatusPlanCounts(run),
		RunURL: reviewStatusRunURL(r.cfg.ConsoleURL, run.ID),
		Marker: domain.ReviewStatusCommentMarker(row.Key),
	})
	if err != nil {
		r.failReviewStatusProjection(ctx, row, now, reviewStatusInternalFailure, err)
		return
	}
	bodyHash := domain.ReviewStatusCommentBodyHash(body)
	observation := domain.ReviewStatusObservationForRun(*run)
	if row.DesiredState != state || row.DesiredBodyHash != bodyHash || row.ObservedRun != observation {
		updated, updateErr := r.st.UpdateReviewStatusCommentDesired(
			ctx, row.Key, run.ID, run.PRHeadSHA, state, bodyHash, observation, now,
		)
		if errors.Is(updateErr, store.ErrConflict) || errors.Is(updateErr, store.ErrNotFound) {
			return // a newer accepted revision owns the shared PR comment
		}
		if updateErr != nil {
			r.log.Warn("reconcile review status: update desired projection", "run", run.ID, "err", updateErr)
			return
		}
		row = updated
	}
	if row.CommentID != "" && row.AppliedState == row.DesiredState && row.AppliedBodyHash == row.DesiredBodyHash {
		return
	}
	claimToken := domain.NewID()
	claimed, won, err := r.st.ClaimReviewStatusComment(
		ctx, row.Key, claimToken, now, now.Add(-reviewStatusCommentClaimTTL),
	)
	if err != nil {
		r.log.Warn("reconcile review status: claim", "run", run.ID, "err", err)
		return
	}
	if !won {
		return
	}
	// The claim API is keyed by the stable Service+PR identity. Re-check the
	// revision and projection after claiming so a webhook that advanced the row
	// between List and Claim can never receive an older run's body.
	if claimed.CurrentRunID != run.ID || claimed.HeadSHA != run.PRHeadSHA ||
		claimed.DesiredState != state || claimed.DesiredBodyHash != bodyHash ||
		claimed.ObservedRun != observation {
		r.markReviewStatusFailed(ctx, claimed, reviewStatusInternalFailure, now)
		return
	}
	// The run can advance after UpdateDesired and before Claim. Re-read it under
	// the acquired lease and refuse the provider call unless the durable
	// observation still describes the live projection.
	freshRun, freshErr := r.st.GetRun(ctx, claimed.CurrentRunID)
	if freshErr != nil || freshRun == nil || domain.ReviewStatusObservationForRun(*freshRun) != claimed.ObservedRun {
		r.markReviewStatusFailed(ctx, claimed, reviewStatusInternalFailure, now)
		return
	}

	providerCtx, cancelProvider := context.WithTimeout(ctx, reviewStatusProviderTimeout)
	comment, syncErr := r.syncReviewStatusComment(providerCtx, claimed, run, body)
	cancelProvider()
	if syncErr != nil {
		r.log.Warn("reconcile review status: provider sync", "run", run.ID, "provider", claimed.Key.Provider, "err", syncErr)
		r.markReviewStatusFailed(ctx, claimed, reviewStatusProviderFailure, r.now().UTC())
		return
	}
	if comment == nil || strings.TrimSpace(comment.ID) == "" {
		r.markReviewStatusFailed(ctx, claimed, reviewStatusProviderFailure, r.now().UTC())
		return
	}
	if _, err := r.st.MarkReviewStatusCommentApplied(
		ctx, claimed.Key, claimed.ClaimToken, comment.ID, comment.URL,
		claimed.DesiredState, claimed.DesiredBodyHash, r.now().UTC(),
	); err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
		// If the provider write was a create and this persistence step failed, the
		// marker-adoption path will recover the same comment after the lease expires.
		r.log.Warn("reconcile review status: record applied projection", "run", run.ID, "err", err)
	}
}

func (r *Reconciler) syncReviewStatusComment(
	ctx context.Context,
	row *domain.ReviewStatusComment,
	run *domain.Run,
	body string,
) (*provider.IssueComment, error) {
	if r.factory == nil || r.creds == nil {
		return nil, errors.New("provider review stack is unavailable")
	}
	service, err := r.st.GetService(ctx, run.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("load service: %w", err)
	}
	scm, err := r.scmContextForReviewStatus(ctx, run, service, row)
	if err != nil {
		return nil, fmt.Errorf("resolve frozen provider grant: %w", err)
	}
	if scm.Provider != row.Key.Provider || scm.RepositoryID != row.Key.ProviderRepoID ||
		scm.RepositoryPath != row.RepositoryPath {
		return nil, errors.New("frozen provider does not match the status comment")
	}
	owner, repo, ok := provider.SplitRepo(row.RepositoryPath)
	if !ok {
		return nil, errors.New("frozen repository path is invalid")
	}
	managed, ok := scm.Client.(provider.ManagedIssueCommentProvider)
	if !ok {
		return nil, errors.New("provider does not support managed issue comments")
	}

	if row.CommentID != "" {
		updated, updateErr := managed.UpdateIssueComment(ctx, owner, repo, row.Key.PRNumber, row.CommentID, body)
		if updateErr == nil {
			return updated, nil
		}
		status, isHTTP := provider.IsProviderHTTPStatusError(updateErr)
		if !isHTTP || (status != http.StatusNotFound && status != http.StatusGone) {
			return nil, updateErr
		}
		// A human may have deleted the stored comment. Search by marker before
		// recreating so a stale local id cannot produce a second status card.
	}

	comments, err := managed.ListIssueComments(ctx, owner, repo, row.Key.PRNumber, reviewStatusCommentListLimit)
	if err != nil {
		return nil, err
	}
	expectedAuthor, err := reviewStatusCommentAuthor(ctx, row.Key.Provider, scm, managed)
	if err != nil {
		return nil, err
	}
	marker := domain.ReviewStatusCommentMarker(row.Key)
	for i := range comments {
		comment := &comments[i]
		if !reviewStatusBodyHasMarker(comment.Body, marker) || !expectedAuthor.MatchesIssueComment(*comment) {
			continue
		}
		if comment.Body == body {
			return comment, nil
		}
		return managed.UpdateIssueComment(ctx, owner, repo, row.Key.PRNumber, comment.ID, body)
	}
	return managed.CreateManagedIssueComment(ctx, owner, repo, row.Key.PRNumber, body)
}

func reviewStatusCommentAuthor(
	ctx context.Context,
	providerKind domain.GitProvider,
	scm *runSCMContext,
	managed provider.ManagedIssueCommentProvider,
) (provider.ProviderUserIdentity, error) {
	if providerKind == domain.ProviderGitHub {
		if strings.TrimSpace(scm.ProviderAppID) == "" {
			return provider.ProviderUserIdentity{}, errors.New("frozen GitHub App identity is unavailable")
		}
		return provider.ProviderUserIdentity{AppID: scm.ProviderAppID}, nil
	}
	identityProvider, ok := managed.(provider.CurrentUserIdentity)
	if !ok {
		return provider.ProviderUserIdentity{}, errors.New("provider cannot prove the managed comment author")
	}
	identity, err := identityProvider.CurrentUserIdentity(ctx)
	if err != nil {
		return provider.ProviderUserIdentity{}, err
	}
	// AppID is meaningful only for GitHub installation-authored comments. Fake
	// and compatibility providers may expose it alongside a user identity.
	identity.AppID = ""
	if strings.TrimSpace(identity.ID) == "" && strings.TrimSpace(identity.Login) == "" {
		return provider.ProviderUserIdentity{}, errors.New("provider returned no usable managed comment author")
	}
	return identity, nil
}

// scmContextForReviewStatus selects the exact repository grant captured by the
// status row. A Run may contain several project Plugin snapshots, so the generic
// first-frozen-grant helper is not sufficient here. An automatic review can
// post before its first dispatch (including a queued Run canceled before that
// dispatch) only while the live binding remains exactly the accepted
// repository; the binding is checked both before and after credential
// resolution to close a reconnect/rebind race.
func (r *Reconciler) scmContextForReviewStatus(
	ctx context.Context,
	run *domain.Run,
	service *domain.Service,
	row *domain.ReviewStatusComment,
) (*runSCMContext, error) {
	snapshots, err := r.st.ListRunPluginSnapshots(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for i := range snapshots {
		snapshot := &snapshots[i]
		if !snapshot.HasFrozenRepositoryGrant() {
			continue
		}
		if domain.GitProvider(snapshot.Provider) != row.Key.Provider ||
			snapshot.RepositoryID != row.Key.ProviderRepoID || snapshot.RepositoryPath != row.RepositoryPath {
			continue
		}
		credential, issueErr := r.creds.IssueRunPluginSnapshotCredential(ctx, snapshot)
		if issueErr != nil {
			return nil, issueErr
		}
		var client provider.Provider
		var clientErr error
		if frozenFactory, ok := r.factory.(provider.FrozenFactory); ok {
			client, clientErr = frozenFactory.FrozenPRClient(
				domain.GitProvider(snapshot.Provider), credential.BaseURL, credential.AccessToken, credential.Scheme,
			)
		} else {
			client, clientErr = provider.IntegrationClientWithScheme(
				domain.GitProvider(snapshot.Provider), credential.BaseURL, credential.AccessToken, credential.Scheme,
			)
		}
		if clientErr != nil {
			return nil, clientErr
		}
		return &runSCMContext{
			Client: client, Token: credentials.Token{Value: credential.AccessToken, Scheme: credential.Scheme, Source: "plugin_snapshot"},
			Provider: domain.GitProvider(snapshot.Provider), RepositoryID: snapshot.RepositoryID,
			RepositoryPath: snapshot.RepositoryPath, CloneURL: snapshot.CloneURL,
			DefaultBranch: snapshot.DefaultBranch, ProviderAppID: snapshot.ProviderAppID, Frozen: true,
		}, nil
	}
	if len(snapshots) != 0 {
		return nil, errors.New("run snapshots exist without the exact frozen repository grant")
	}

	if !reviewStatusCanUseLiveGrant(run) {
		return nil, errors.New("exact frozen repository grant is unavailable")
	}
	binding, err := r.st.GetServiceRepositoryBinding(ctx, run.ServiceID)
	if errors.Is(err, store.ErrNotFound) {
		// Rolling-upgrade/test compatibility for a legacy Service predating
		// repository bindings. New automatic status rows always have a binding.
		providerRepoID := ""
		if service.ProviderRepoID != nil {
			providerRepoID = strconv.FormatInt(*service.ProviderRepoID, 10)
		}
		if service.Provider != row.Key.Provider || providerRepoID != row.Key.ProviderRepoID ||
			service.RepoOwnerName != row.RepositoryPath {
			return nil, errors.New("legacy repository identity does not match the status comment")
		}
		return r.scmContextForRun(ctx, run, service)
	}
	if err != nil {
		return nil, err
	}
	if service.Provider != row.Key.Provider || binding.ProviderRepoID != row.Key.ProviderRepoID ||
		binding.RepositoryPath != row.RepositoryPath {
		return nil, errors.New("current repository binding does not match the accepted review")
	}
	token, err := r.creds.ResolveForService(ctx, service, run.TriggeredByUserID)
	if err != nil {
		return nil, err
	}
	client, err := r.factory.PRClient(service.Provider, token.Value, token.Scheme)
	if err != nil {
		return nil, err
	}
	after, err := r.st.GetServiceRepositoryBinding(ctx, run.ServiceID)
	if err != nil || after.InstallationID != binding.InstallationID ||
		after.ProviderRepoID != binding.ProviderRepoID || after.RepositoryPath != binding.RepositoryPath {
		return nil, errors.New("repository binding changed while resolving the provider grant")
	}
	scm := &runSCMContext{
		Client: client, Token: token, Provider: service.Provider,
		RepositoryID: binding.ProviderRepoID, RepositoryPath: binding.RepositoryPath,
		CloneURL: binding.CloneURL, DefaultBranch: binding.DefaultBranch,
	}
	if cfg, cfgErr := r.st.GetProviderConfig(ctx, domain.ProviderKind(service.Provider)); cfgErr == nil {
		scm.ProviderAppID = cfg.AppID
	}
	return scm, nil
}

// reviewStatusCanUseLiveGrant is intentionally narrower than the generic
// rolling-upgrade fallback in scmContextForRun. A review status card is allowed
// to use mutable live credentials only before the Run has ever been dispatched.
// Cancellation or a visible setup failure can win the race before
// ClaimRunDispatch; in that case there is no frozen snapshot, Job name, runner
// token, start time, or cleanup marker, but the terminal status still needs to
// reach the already accepted PR.
func reviewStatusCanUseLiveGrant(run *domain.Run) bool {
	if run == nil || run.Kind != domain.RunKindReview || run.Origin != domain.RunOriginAutomation {
		return false
	}
	if run.Status != domain.StatusQueued && run.Status != domain.StatusCanceled &&
		run.Status != domain.StatusFailed && run.Status != domain.StatusBlocked {
		return false
	}
	return strings.TrimSpace(run.K8sJobName) == "" && strings.TrimSpace(run.TokenHash) == "" &&
		run.StartedAt == nil && run.JobCleanedAt == nil
}

func (r *Reconciler) failReviewStatusProjection(
	ctx context.Context,
	row *domain.ReviewStatusComment,
	now time.Time,
	message string,
	cause error,
) {
	claimed, won, err := r.st.ClaimReviewStatusComment(
		ctx, row.Key, domain.NewID(), now, now.Add(-reviewStatusCommentClaimTTL),
	)
	if err != nil {
		r.log.Warn("reconcile review status: claim invalid projection", "run", row.CurrentRunID, "err", err)
		return
	}
	if !won {
		return
	}
	r.log.Warn("reconcile review status: render projection", "run", row.CurrentRunID, "err", cause)
	r.markReviewStatusFailed(ctx, claimed, message, now)
}

func (r *Reconciler) markReviewStatusFailed(ctx context.Context, row *domain.ReviewStatusComment, message string, at time.Time) {
	if _, err := r.st.MarkReviewStatusCommentFailed(ctx, row.Key, row.ClaimToken, message, at); err != nil &&
		!errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
		r.log.Warn("reconcile review status: record failure", "run", row.CurrentRunID, "err", err)
	}
}

func reviewStatusStateForRun(run *domain.Run) domain.ReviewStatusState {
	switch run.Status {
	case domain.StatusQueued, domain.StatusScheduling:
		return domain.ReviewStatusQueued
	case domain.StatusRunning, domain.StatusAwaitingInput:
		return domain.ReviewStatusRunning
	case domain.StatusSucceeded:
		if run.DeliveryStatus == domain.DeliveryFailed {
			return domain.ReviewStatusFailed
		}
		if run.ReviewPostedAt != nil && run.DeliveryStatus == domain.DeliveryDelivered {
			return domain.ReviewStatusCompleted
		}
		return domain.ReviewStatusPublishing
	case domain.StatusCanceled:
		if strings.EqualFold(strings.TrimSpace(run.Phase), "superseded") {
			return domain.ReviewStatusSuperseded
		}
		return domain.ReviewStatusCanceled
	case domain.StatusFailed, domain.StatusBlocked:
		return domain.ReviewStatusFailed
	default:
		return domain.ReviewStatusFailed
	}
}

func reviewStatusFailureMessage(run *domain.Run, state domain.ReviewStatusState) string {
	if state != domain.ReviewStatusFailed {
		return ""
	}
	if run.Status == domain.StatusSucceeded && run.DeliveryStatus == domain.DeliveryFailed {
		return "The native review could not be published. Open the Cloud Run for details."
	}
	if run.Status == domain.StatusBlocked {
		return "The review is blocked and needs attention. Open the Cloud Run for details."
	}
	switch run.FailureReason {
	case domain.FailureCloneFailed:
		return "The repository could not be prepared for review."
	case domain.FailureSetupFailed:
		return "The review runtime could not be prepared."
	case domain.FailureModelRateLimited:
		return "The selected model remained rate limited."
	case domain.FailureTimeout:
		return "The review runner timed out."
	default:
		return "The review runner failed. Open the Cloud Run for details."
	}
}

func reviewStatusPlanCounts(run *domain.Run) domain.ReviewStatusPlanCounts {
	if run.ReviewPlan == nil {
		return domain.ReviewStatusPlanCounts{}
	}
	return domain.ReviewStatusPlanCounts{
		ChangedFiles: run.ReviewPlan.ChangedFiles, EligibleFiles: run.ReviewPlan.EligibleFiles,
		IndexedFiles: run.ReviewPlan.IndexedFiles, ChangedLines: run.ReviewPlan.ChangedLines,
	}
}

func reviewStatusRunURL(consoleURL, runID string) string {
	consoleURL = strings.TrimRight(strings.TrimSpace(consoleURL), "/")
	if consoleURL == "" {
		return ""
	}
	return consoleURL + "/runs/" + url.PathEscape(runID)
}

func reviewStatusBodyHasMarker(body, marker string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}
