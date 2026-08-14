package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cnjack/jcloud/internal/domain"
)

const reviewStatusCommentCols = `service_id,provider,provider_repo_id,pr_number,
		repository_path,current_run_id,head_sha,accepted_sequence,comment_id,comment_url,
	desired_state,applied_state,desired_body_hash,applied_body_hash,
	claim_token,claimed_at,attempts,last_error,next_attempt_at,
	observed_run_status,observed_run_phase,observed_failure_reason,observed_delivery_status,
	observed_review_posted,observed_review_plan_hash,created_at,updated_at`

const reviewStatusProjectionPendingPredicate = `(c.current_run_id IS NOT NULL AND
	(c.comment_id='' OR c.desired_state<>c.applied_state OR c.desired_body_hash<>c.applied_body_hash OR EXISTS (
		SELECT 1 FROM runs r
		WHERE r.id=c.current_run_id AND (
			c.observed_run_status<>r.status OR c.observed_run_phase<>r.phase OR
			c.observed_failure_reason<>r.failure_reason OR c.observed_delivery_status<>r.delivery_status OR
			c.observed_review_posted<>(r.review_posted_at IS NOT NULL) OR
			c.observed_review_plan_hash<>COALESCE(r.review_plan->>'plan_hash','') OR
			(c.applied_state='completed' AND COALESCE(r.review_result->'completion'->>'status','')<>'complete') OR
			(c.applied_state='partial' AND COALESCE(r.review_result->'completion'->>'status','')='complete')
		)
	)))`

type reviewStatusCursor struct {
	Key              domain.ReviewStatusCommentKey
	HeadSHA          string
	AcceptedSequence int64
	UpdatedAt        time.Time
}

func validateReviewStatusKey(key domain.ReviewStatusCommentKey) error {
	if strings.TrimSpace(key.ServiceID) == "" || !domain.ValidProvider(key.Provider) ||
		strings.TrimSpace(key.ProviderRepoID) == "" || key.PRNumber <= 0 {
		return errors.New("review status comment identity is required")
	}
	return nil
}

func validateReviewStatusIntent(execution *domain.AutomationExecution, run *domain.Run, intent *domain.ReviewStatusComment) error {
	if execution == nil || run == nil || intent == nil {
		return errors.New("review status intent requires an execution and run")
	}
	if err := validateReviewStatusKey(intent.Key); err != nil {
		return err
	}
	if strings.TrimSpace(intent.RepositoryPath) == "" || strings.TrimSpace(intent.CurrentRunID) == "" ||
		!domain.ValidCommitSHA(strings.TrimSpace(intent.HeadSHA)) || !intent.DesiredState.Valid() ||
		strings.TrimSpace(intent.DesiredBodyHash) == "" || intent.AcceptedSequence <= 0 {
		return errors.New("review status intent is incomplete")
	}
	if execution.RunID != run.ID || execution.ServiceID != run.ServiceID || execution.ProjectID != run.ProjectID ||
		intent.CurrentRunID != run.ID || intent.Key.ServiceID != run.ServiceID || intent.Key.PRNumber != run.PRNumber ||
		intent.HeadSHA != run.PRHeadSHA {
		return errors.New("review status intent does not match its execution and run")
	}
	// Store the projection of the Run as it will be inserted, rather than trusting
	// a caller-supplied observation. normalizeRunForCreate supplies the same
	// compatibility defaults used by both PG and memory run creation.
	observedRun := *run
	normalizeRunForCreate(&observedRun)
	intent.ObservedRun = domain.ReviewStatusObservationForRun(observedRun)
	return nil
}

func validateReviewStatusObservation(observed domain.ReviewStatusRunObservation) error {
	if !observed.Status.Valid() || !observed.DeliveryStatus.Valid() ||
		(observed.FailureReason != "" && !domain.ValidFailureReason(observed.FailureReason)) {
		return errors.New("invalid review status run observation")
	}
	return nil
}

func normalizeReviewStatusIntent(intent *domain.ReviewStatusComment) domain.ReviewStatusComment {
	value := *intent
	value.Key.ServiceID = strings.TrimSpace(value.Key.ServiceID)
	value.Key.ProviderRepoID = strings.TrimSpace(value.Key.ProviderRepoID)
	value.RepositoryPath = strings.TrimSpace(value.RepositoryPath)
	value.CurrentRunID = strings.TrimSpace(value.CurrentRunID)
	value.HeadSHA = strings.TrimSpace(value.HeadSHA)
	value.DesiredBodyHash = strings.TrimSpace(value.DesiredBodyHash)
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	} else {
		value.CreatedAt = value.CreatedAt.UTC()
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = value.CreatedAt
	} else {
		value.UpdatedAt = value.UpdatedAt.UTC()
	}
	// The atomic API accepts an intent, never a claimed/provider-applied row.
	value.CommentID = ""
	value.CommentURL = ""
	value.AppliedState = ""
	value.AppliedBodyHash = ""
	value.ClaimToken = ""
	value.ClaimedAt = nil
	value.Attempts = 0
	value.LastError = ""
	value.NextAttemptAt = nil
	return value
}

func upsertReviewStatusCommentTx(ctx context.Context, tx pgx.Tx, intent *domain.ReviewStatusComment) (bool, error) {
	value := normalizeReviewStatusIntent(intent)
	tag, err := tx.Exec(ctx, `INSERT INTO review_status_comments (
		service_id,provider,provider_repo_id,pr_number,repository_path,current_run_id,
		head_sha,accepted_sequence,desired_state,desired_body_hash,
		observed_run_status,observed_run_phase,observed_failure_reason,observed_delivery_status,
		observed_review_posted,observed_review_plan_hash,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (service_id,provider,provider_repo_id,pr_number) DO UPDATE SET
			repository_path=EXCLUDED.repository_path,
			current_run_id=EXCLUDED.current_run_id,
			head_sha=EXCLUDED.head_sha,
			accepted_sequence=EXCLUDED.accepted_sequence,
			desired_state=EXCLUDED.desired_state,
			desired_body_hash=EXCLUDED.desired_body_hash,
			attempts=0,last_error='',next_attempt_at=NULL,
			observed_run_status=EXCLUDED.observed_run_status,
			observed_run_phase=EXCLUDED.observed_run_phase,
			observed_failure_reason=EXCLUDED.observed_failure_reason,
			observed_delivery_status=EXCLUDED.observed_delivery_status,
			observed_review_posted=EXCLUDED.observed_review_posted,
			observed_review_plan_hash=EXCLUDED.observed_review_plan_hash,
			updated_at=EXCLUDED.updated_at
		WHERE review_status_comments.accepted_sequence < EXCLUDED.accepted_sequence`,
		value.Key.ServiceID, string(value.Key.Provider), value.Key.ProviderRepoID, value.Key.PRNumber,
		value.RepositoryPath, value.CurrentRunID, value.HeadSHA, value.AcceptedSequence, string(value.DesiredState),
		value.DesiredBodyHash, string(value.ObservedRun.Status), value.ObservedRun.Phase,
		string(value.ObservedRun.FailureReason), string(value.ObservedRun.DeliveryStatus),
		value.ObservedRun.ReviewPosted, value.ObservedRun.ReviewPlanHash,
		value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("upsert review status comment: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

type lockedReviewStatusGrant struct {
	providerRevision    int64
	credentialVersionID string
	repositoryID        string
	repositoryPath      string
	cloneURL            string
	defaultBranch       string
}

func lockReviewStatusGrantTx(ctx context.Context, tx pgx.Tx, intent *domain.ReviewStatusComment, projectID string) error {
	installationID := strings.TrimSpace(intent.InstallationID)
	if installationID == "" {
		return nil // Legacy/internal callers created before acceptance-time freezing.
	}
	var (
		providerRevision     int64
		pluginEnabled        bool
		installationRevision int64
		credentialVersionID  string
		cloneURL             string
		defaultBranch        string
	)
	var locked int
	// Project deletion locks the parent before cascading through installations.
	// Take the same parent lock first so the rest of this acceptance preflight
	// cannot invert Project -> installation ownership.
	if err := tx.QueryRow(ctx, `SELECT 1 FROM projects WHERE id=$1 FOR KEY SHARE`, projectID).Scan(&locked); err != nil {
		return fmt.Errorf("%w: accepted review Project changed", ErrConflict)
	}
	// Match the cluster-wide lock order used by dispatch and provider
	// reconfiguration: Project, provider config, installation, Service, then repository binding.
	// Holding the mutable aggregate rows through commit prevents a
	// credential rotation plus secret GC from deleting the accepted version in
	// the read-to-snapshot gap. Immutable version rows are only existence-checked:
	// refresh rotation locks a version before its Installation, so locking both
	// here would invert that order and permit a deadlock.
	if err := tx.QueryRow(ctx, `SELECT config_revision,plugin_enabled
		FROM provider_configs WHERE provider=$1 FOR SHARE`, intent.Key.Provider).
		Scan(&providerRevision, &pluginEnabled); err != nil || !pluginEnabled {
		if err == nil {
			err = ErrConflict
		}
		return fmt.Errorf("%w: accepted review provider configuration changed", err)
	}
	if err := tx.QueryRow(ctx, `SELECT pi.config_revision,pi.credential_version_id
		FROM plugin_installations pi
		WHERE pi.id=$1 AND pi.project_id=$2 AND pi.provider=$3
		  AND pi.status='enabled' AND pi.last_health_error=''
		  AND pi.credential_version_id<>''
		  AND ((pi.provider='github' AND pi.github_installation_id<>'')
		    OR (pi.provider<>'github' AND pi.access_token_enc IS NOT NULL))
		FOR SHARE OF pi`, installationID, projectID, intent.Key.Provider).
		Scan(&installationRevision, &credentialVersionID); err != nil {
		return fmt.Errorf("%w: accepted review installation changed", ErrConflict)
	}
	if installationRevision != providerRevision {
		return fmt.Errorf("%w: accepted review provider revision changed", ErrConflict)
	}
	if err := tx.QueryRow(ctx, `SELECT 1 FROM provider_config_versions
		WHERE provider=$1 AND config_revision=$2`, intent.Key.Provider, providerRevision).Scan(&locked); err != nil {
		return fmt.Errorf("%w: accepted review provider version is unavailable", ErrConflict)
	}
	if err := tx.QueryRow(ctx, `SELECT 1 FROM plugin_credential_versions
		WHERE id=$1 AND installation_id=$2 AND provider=$3`,
		credentialVersionID, installationID, intent.Key.Provider).Scan(&locked); err != nil {
		return fmt.Errorf("%w: accepted review credential version is unavailable", ErrConflict)
	}
	// Lock the parent Service before its binding. Direct Service deletion takes
	// the parent first and then cascades to the binding, so matching that order
	// avoids a Service/binding deadlock while still fencing repository rebinds.
	if err := tx.QueryRow(ctx, `SELECT 1 FROM services
		WHERE id=$1 AND project_id=$2 FOR KEY SHARE`, intent.Key.ServiceID, projectID).Scan(&locked); err != nil {
		return fmt.Errorf("%w: accepted review Service changed", ErrConflict)
	}
	var repositoryID, repositoryPath string
	if err := tx.QueryRow(ctx, `SELECT provider_repo_id,repository_path,clone_url,default_branch
		FROM service_repository_bindings
		WHERE service_id=$1 AND installation_id=$2 FOR SHARE`, intent.Key.ServiceID, installationID).
		Scan(&repositoryID, &repositoryPath, &cloneURL, &defaultBranch); err != nil ||
		repositoryID != intent.Key.ProviderRepoID || repositoryPath != intent.RepositoryPath {
		return fmt.Errorf("%w: accepted review repository binding changed", ErrConflict)
	}
	return nil
}

func freezeReviewStatusSnapshotTx(ctx context.Context, tx pgx.Tx, intent *domain.ReviewStatusComment) error {
	installationID := strings.TrimSpace(intent.InstallationID)
	if installationID == "" {
		return nil
	}
	var grant lockedReviewStatusGrant
	if err := tx.QueryRow(ctx, `SELECT pi.config_revision,pi.credential_version_id,
		rb.provider_repo_id,rb.repository_path,rb.clone_url,rb.default_branch
		FROM plugin_installations pi
		JOIN service_repository_bindings rb ON rb.installation_id=pi.id AND rb.service_id=$2
		WHERE pi.id=$1 AND pi.provider=$3`, installationID, intent.Key.ServiceID, intent.Key.Provider).
		Scan(&grant.providerRevision, &grant.credentialVersionID, &grant.repositoryID,
			&grant.repositoryPath, &grant.cloneURL, &grant.defaultBranch); err != nil {
		return fmt.Errorf("%w: accepted review provider grant changed", ErrConflict)
	}
	if grant.repositoryID != intent.Key.ProviderRepoID || grant.repositoryPath != intent.RepositoryPath {
		return fmt.Errorf("%w: accepted review repository binding changed", ErrConflict)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO run_plugin_snapshots(
		run_id,installation_id,provider,provider_config_revision,credential_version_id,created_at,
		repository_id,repository_path,clone_url,default_branch,acting_principal_kind,acting_principal_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'provider_bot',$2)
		ON CONFLICT(run_id,installation_id) DO NOTHING`,
		intent.CurrentRunID, installationID, string(intent.Key.Provider), grant.providerRevision,
		grant.credentialVersionID, intent.UpdatedAt.UTC(), grant.repositoryID, grant.repositoryPath,
		grant.cloneURL, grant.defaultBranch)
	if err != nil {
		return fmt.Errorf("freeze review status provider grant: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: accepted review provider grant changed", ErrConflict)
	}
	return nil
}

func scanReviewStatusComment(row pgx.Row) (*domain.ReviewStatusComment, error) {
	var (
		value        domain.ReviewStatusComment
		provider     string
		desired      string
		applied      string
		currentRunID *string
	)
	err := row.Scan(
		&value.Key.ServiceID, &provider, &value.Key.ProviderRepoID, &value.Key.PRNumber,
		&value.RepositoryPath, &currentRunID, &value.HeadSHA, &value.AcceptedSequence, &value.CommentID, &value.CommentURL,
		&desired, &applied, &value.DesiredBodyHash, &value.AppliedBodyHash,
		&value.ClaimToken, &value.ClaimedAt, &value.Attempts, &value.LastError, &value.NextAttemptAt,
		&value.ObservedRun.Status, &value.ObservedRun.Phase, &value.ObservedRun.FailureReason,
		&value.ObservedRun.DeliveryStatus, &value.ObservedRun.ReviewPosted, &value.ObservedRun.ReviewPlanHash,
		&value.CreatedAt, &value.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan review status comment: %w", err)
	}
	value.Key.Provider = domain.GitProvider(provider)
	value.DesiredState = domain.ReviewStatusState(desired)
	value.AppliedState = domain.ReviewStatusState(applied)
	if currentRunID != nil {
		value.CurrentRunID = *currentRunID
	}
	return &value, nil
}

func reviewStatusKeyArgs(key domain.ReviewStatusCommentKey) []any {
	return []any{key.ServiceID, string(key.Provider), key.ProviderRepoID, key.PRNumber}
}

func validateReviewStatusCursor(acceptedSequence int64, headSHA string, at time.Time) (string, time.Time, error) {
	headSHA = strings.ToLower(strings.TrimSpace(headSHA))
	if acceptedSequence <= 0 || !domain.ValidCommitSHA(headSHA) || at.IsZero() {
		return "", time.Time{}, errors.New("review status cursor revision is required")
	}
	return headSHA, at.UTC(), nil
}

func lockWebhookReceiptClaimTx(ctx context.Context, tx pgx.Tx, acceptedSequence int64, receiptClaimToken string) (bool, error) {
	receiptClaimToken = strings.TrimSpace(receiptClaimToken)
	if receiptClaimToken == "" {
		return true, nil // Store-contract tests and legacy internal callers.
	}
	var sequence int64
	err := tx.QueryRow(ctx, `SELECT ingress_sequence FROM webhook_receipts
		WHERE ingress_sequence=$1 AND claim_token=$2 AND status='received'
		FOR SHARE`, acceptedSequence, receiptClaimToken).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *PGStore) AdvanceReviewStatusCursor(ctx context.Context, key domain.ReviewStatusCommentKey, acceptedSequence int64, receiptClaimToken, headSHA string, at time.Time) (bool, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return false, err
	}
	var err error
	headSHA, at, err = validateReviewStatusCursor(acceptedSequence, headSHA, at)
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("advance review status cursor: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	activeReceipt, err := lockWebhookReceiptClaimTx(ctx, tx, acceptedSequence, receiptClaimToken)
	if err != nil {
		return false, fmt.Errorf("advance review status cursor: verify webhook receipt claim: %w", err)
	}
	if !activeReceipt {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"review-status:"+domain.ReviewStatusCommentMarker(key)); err != nil {
		return false, fmt.Errorf("advance review status cursor: lock: %w", err)
	}
	args := append(reviewStatusKeyArgs(key), headSHA, acceptedSequence, at)
	tag, err := tx.Exec(ctx, `INSERT INTO review_status_cursors(
		service_id,provider,provider_repo_id,pr_number,head_sha,accepted_sequence,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(service_id,provider,provider_repo_id,pr_number) DO UPDATE SET
			head_sha=EXCLUDED.head_sha,accepted_sequence=EXCLUDED.accepted_sequence,updated_at=EXCLUDED.updated_at
		WHERE review_status_cursors.accepted_sequence<EXCLUDED.accepted_sequence`, args...)
	if err != nil {
		return false, fmt.Errorf("advance review status cursor: upsert: %w", err)
	}
	if tag.RowsAffected() != 1 {
		var currentHeadSHA string
		var currentSequence int64
		if err = tx.QueryRow(ctx, `SELECT head_sha,accepted_sequence FROM review_status_cursors
			WHERE service_id=$1 AND provider=$2 AND provider_repo_id=$3 AND pr_number=$4`,
			reviewStatusKeyArgs(key)...).Scan(&currentHeadSHA, &currentSequence); err != nil {
			return false, fmt.Errorf("advance review status cursor: classify: %w", err)
		}
		return currentSequence == acceptedSequence && strings.EqualFold(currentHeadSHA, headSHA), nil
	}
	// A Provider-confirmed head can be ignored by a later draft/filter/model
	// gate. If it differs from the current comment owner, cancel only its queued
	// Run now so the older revision cannot dispatch after this observation.
	if _, err = tx.Exec(ctx, `UPDATE runs r
		SET status=$5,phase='Superseded',finished_at=COALESCE(finished_at,$6)
		FROM review_status_comments c
		WHERE c.service_id=$1 AND c.provider=$2 AND c.provider_repo_id=$3 AND c.pr_number=$4
		  AND c.current_run_id=r.id AND lower(c.head_sha)<>lower($7) AND r.status=$8`,
		key.ServiceID, string(key.Provider), key.ProviderRepoID, key.PRNumber,
		domain.StatusCanceled, at, headSHA, domain.StatusQueued); err != nil {
		return false, fmt.Errorf("advance review status cursor: supersede queued: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("advance review status cursor: commit: %w", err)
	}
	return true, nil
}

func (s *PGStore) GetReviewStatusComment(ctx context.Context, key domain.ReviewStatusCommentKey) (*domain.ReviewStatusComment, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, err
	}
	args := reviewStatusKeyArgs(key)
	return scanReviewStatusComment(s.pool.QueryRow(ctx, `SELECT `+reviewStatusCommentCols+`
		FROM review_status_comments WHERE service_id=$1 AND provider=$2 AND provider_repo_id=$3 AND pr_number=$4`, args...))
}

func (s *PGStore) ListPendingReviewStatusComments(ctx context.Context, now, staleBefore time.Time, limit int) ([]domain.ReviewStatusComment, error) {
	if limit <= 0 {
		return []domain.ReviewStatusComment{}, nil
	}
	if limit > 1000 {
		limit = 1000
	}
	if now.IsZero() || staleBefore.IsZero() {
		return nil, errors.New("review status pending timestamps are required")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+reviewStatusCommentCols+`
		FROM review_status_comments c
		WHERE (c.claim_token='' OR c.claimed_at<=$1)
		  AND (c.next_attempt_at IS NULL OR c.next_attempt_at<=$2)
		  AND `+reviewStatusProjectionPendingPredicate+`
		ORDER BY COALESCE(c.next_attempt_at,c.updated_at),c.service_id,c.provider,c.provider_repo_id,c.pr_number
		LIMIT $3`, staleBefore.UTC(), now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending review status comments: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ReviewStatusComment, 0)
	for rows.Next() {
		value, scanErr := scanReviewStatusComment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *value)
	}
	return out, rows.Err()
}

func (s *PGStore) ClaimReviewStatusComment(ctx context.Context, key domain.ReviewStatusCommentKey, claimToken string, claimedAt, staleBefore time.Time) (*domain.ReviewStatusComment, bool, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, false, err
	}
	claimToken = strings.TrimSpace(claimToken)
	if claimToken == "" || claimedAt.IsZero() || staleBefore.IsZero() {
		return nil, false, errors.New("review status claim token and timestamp are required")
	}
	args := append(reviewStatusKeyArgs(key), claimToken, claimedAt.UTC(), staleBefore.UTC())
	value, err := scanReviewStatusComment(s.pool.QueryRow(ctx, `UPDATE review_status_comments c
		SET claim_token=$5,claimed_at=$6,attempts=c.attempts+1,updated_at=$6
		WHERE c.service_id=$1 AND c.provider=$2 AND c.provider_repo_id=$3 AND c.pr_number=$4
		  AND (c.claim_token='' OR c.claimed_at<=$7)
		  AND (c.next_attempt_at IS NULL OR c.next_attempt_at<=$6)
		  AND `+reviewStatusProjectionPendingPredicate+`
		RETURNING `+reviewStatusCommentCols, args...))
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (s *PGStore) MarkReviewStatusCommentApplied(ctx context.Context, key domain.ReviewStatusCommentKey, claimToken, commentID, commentURL string, appliedState domain.ReviewStatusState, appliedBodyHash string, at time.Time) (*domain.ReviewStatusComment, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, err
	}
	claimToken, commentID, commentURL, appliedBodyHash = strings.TrimSpace(claimToken), strings.TrimSpace(commentID), strings.TrimSpace(commentURL), strings.TrimSpace(appliedBodyHash)
	if claimToken == "" || !appliedState.Valid() || appliedBodyHash == "" || at.IsZero() {
		return nil, errors.New("complete review status claim is invalid")
	}
	args := append(reviewStatusKeyArgs(key), claimToken, commentID, commentURL, string(appliedState), appliedBodyHash, at.UTC())
	value, err := scanReviewStatusComment(s.pool.QueryRow(ctx, `UPDATE review_status_comments
		SET comment_id=COALESCE(NULLIF($6,''),comment_id),
			comment_url=COALESCE(NULLIF($7,''),comment_url),
			applied_state=$8,applied_body_hash=$9,
			claim_token='',claimed_at=NULL,attempts=0,last_error='',next_attempt_at=NULL,updated_at=$10
		WHERE service_id=$1 AND provider=$2 AND provider_repo_id=$3 AND pr_number=$4
		  AND claim_token=$5 AND desired_state=$8 AND desired_body_hash=$9
		  AND (comment_id<>'' OR $6<>'')
		RETURNING `+reviewStatusCommentCols, args...))
	if errors.Is(err, ErrNotFound) {
		return nil, s.reviewStatusWriteMiss(ctx, key)
	}
	return value, err
}

func (s *PGStore) MarkReviewStatusCommentFailed(ctx context.Context, key domain.ReviewStatusCommentKey, claimToken, lastError string, at time.Time) (*domain.ReviewStatusComment, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, err
	}
	claimToken, lastError = strings.TrimSpace(claimToken), strings.TrimSpace(lastError)
	if claimToken == "" || lastError == "" || at.IsZero() {
		return nil, errors.New("failed review status claim is invalid")
	}
	args := append(reviewStatusKeyArgs(key), claimToken, lastError, at.UTC())
	value, err := scanReviewStatusComment(s.pool.QueryRow(ctx, `UPDATE review_status_comments
		SET claim_token='',claimed_at=NULL,last_error=$6,
			next_attempt_at=$7::timestamptz + make_interval(secs => LEAST(300, 5 * (1 << LEAST(GREATEST(attempts-1,0),8)))::double precision),
			updated_at=$7
		WHERE service_id=$1 AND provider=$2 AND provider_repo_id=$3 AND pr_number=$4 AND claim_token=$5
		RETURNING `+reviewStatusCommentCols, args...))
	if errors.Is(err, ErrNotFound) {
		return nil, s.reviewStatusWriteMiss(ctx, key)
	}
	return value, err
}

func (s *PGStore) UpdateReviewStatusCommentDesired(ctx context.Context, key domain.ReviewStatusCommentKey, currentRunID, headSHA string, desiredState domain.ReviewStatusState, desiredBodyHash string, observed domain.ReviewStatusRunObservation, at time.Time) (*domain.ReviewStatusComment, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, err
	}
	currentRunID, headSHA, desiredBodyHash = strings.TrimSpace(currentRunID), strings.TrimSpace(headSHA), strings.TrimSpace(desiredBodyHash)
	if currentRunID == "" || !domain.ValidCommitSHA(headSHA) || !desiredState.Valid() || desiredBodyHash == "" || at.IsZero() {
		return nil, errors.New("desired review status is invalid")
	}
	if err := validateReviewStatusObservation(observed); err != nil {
		return nil, err
	}
	args := append(reviewStatusKeyArgs(key), currentRunID, headSHA, string(desiredState), desiredBodyHash,
		string(observed.Status), observed.Phase, string(observed.FailureReason), string(observed.DeliveryStatus),
		observed.ReviewPosted, observed.ReviewPlanHash, at.UTC())
	changed := `(desired_state<>$7 OR desired_body_hash<>$8)`
	observationChanged := `(observed_run_status<>$9 OR observed_run_phase<>$10 OR
		observed_failure_reason<>$11 OR observed_delivery_status<>$12 OR
		observed_review_posted<>$13 OR observed_review_plan_hash<>$14)`
	value, err := scanReviewStatusComment(s.pool.QueryRow(ctx, `UPDATE review_status_comments c
		SET desired_state=$7,desired_body_hash=$8,
			observed_run_status=$9,observed_run_phase=$10,observed_failure_reason=$11,
			observed_delivery_status=$12,observed_review_posted=$13,observed_review_plan_hash=$14,
			attempts=CASE WHEN `+changed+` THEN 0 ELSE attempts END,
			last_error=CASE WHEN `+changed+` THEN '' ELSE last_error END,
			next_attempt_at=CASE WHEN `+changed+` THEN NULL ELSE next_attempt_at END,
			updated_at=CASE WHEN `+changed+` OR `+observationChanged+` THEN $15 ELSE updated_at END
		WHERE c.service_id=$1 AND c.provider=$2 AND c.provider_repo_id=$3 AND c.pr_number=$4
		  AND c.current_run_id=$5 AND c.head_sha=$6
		  AND EXISTS (SELECT 1 FROM runs r
			WHERE r.id=$5 AND r.service_id=c.service_id
			  AND r.status=$9 AND r.phase=$10 AND r.failure_reason=$11
			  AND r.delivery_status=$12 AND (r.review_posted_at IS NOT NULL)=$13
			  AND COALESCE(r.review_plan->>'plan_hash','')=$14)
		RETURNING `+reviewStatusCommentCols, args...))
	if errors.Is(err, ErrNotFound) {
		return nil, s.reviewStatusWriteMiss(ctx, key)
	}
	return value, err
}

func (s *PGStore) reviewStatusWriteMiss(ctx context.Context, key domain.ReviewStatusCommentKey) error {
	var exists bool
	args := reviewStatusKeyArgs(key)
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM review_status_comments
		WHERE service_id=$1 AND provider=$2 AND provider_repo_id=$3 AND pr_number=$4)`, args...).Scan(&exists)
	if err != nil {
		return fmt.Errorf("classify review status conflict: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return ErrConflict
}

func reviewStatusMapKey(key domain.ReviewStatusCommentKey) string {
	return key.ServiceID + "\x00" + string(key.Provider) + "\x00" + key.ProviderRepoID + "\x00" + fmt.Sprint(key.PRNumber)
}

func reviewStatusKeyLess(a, b domain.ReviewStatusCommentKey) bool {
	if a.ServiceID != b.ServiceID {
		return a.ServiceID < b.ServiceID
	}
	if a.Provider != b.Provider {
		return a.Provider < b.Provider
	}
	if a.ProviderRepoID != b.ProviderRepoID {
		return a.ProviderRepoID < b.ProviderRepoID
	}
	return a.PRNumber < b.PRNumber
}

func (m *MemStore) AdvanceReviewStatusCursor(_ context.Context, key domain.ReviewStatusCommentKey, acceptedSequence int64, receiptClaimToken, headSHA string, at time.Time) (bool, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return false, err
	}
	var err error
	headSHA, at, err = validateReviewStatusCursor(acceptedSequence, headSHA, at)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.webhookReceiptClaimActiveLocked(acceptedSequence, receiptClaimToken) {
		return false, nil
	}
	mapKey := reviewStatusMapKey(key)
	if current, ok := m.reviewStatusCursors[mapKey]; ok && current.AcceptedSequence >= acceptedSequence {
		return current.AcceptedSequence == acceptedSequence && strings.EqualFold(current.HeadSHA, headSHA), nil
	}
	m.reviewStatusCursors[mapKey] = reviewStatusCursor{
		Key: key, HeadSHA: headSHA, AcceptedSequence: acceptedSequence, UpdatedAt: at,
	}
	if status, ok := m.reviewStatusComments[mapKey]; ok && !strings.EqualFold(status.HeadSHA, headSHA) {
		if run, exists := m.runs[status.CurrentRunID]; exists && run.Status == domain.StatusQueued {
			run.Status = domain.StatusCanceled
			run.Phase = "Superseded"
			if run.FinishedAt == nil {
				finishedAt := at
				run.FinishedAt = &finishedAt
			}
			m.runs[run.ID] = run
		}
	}
	return true, nil
}

func (m *MemStore) webhookReceiptClaimActiveLocked(acceptedSequence int64, receiptClaimToken string) bool {
	receiptClaimToken = strings.TrimSpace(receiptClaimToken)
	if receiptClaimToken == "" {
		return true // Store-contract tests and legacy internal callers.
	}
	for _, receipt := range m.webhookReceipts {
		if receipt.IngressSequence == acceptedSequence && receipt.ClaimToken == receiptClaimToken && receipt.Status == "received" {
			return true
		}
	}
	return false
}

func cloneReviewStatusComment(value domain.ReviewStatusComment) domain.ReviewStatusComment {
	if value.ClaimedAt != nil {
		claimedAt := *value.ClaimedAt
		value.ClaimedAt = &claimedAt
	}
	if value.NextAttemptAt != nil {
		nextAttemptAt := *value.NextAttemptAt
		value.NextAttemptAt = &nextAttemptAt
	}
	return value
}

func (m *MemStore) upsertReviewStatusCommentLocked(intent *domain.ReviewStatusComment) bool {
	value := normalizeReviewStatusIntent(intent)
	key := reviewStatusMapKey(value.Key)
	if existing, ok := m.reviewStatusComments[key]; ok {
		if existing.AcceptedSequence >= value.AcceptedSequence {
			return false
		}
		value.CommentID = existing.CommentID
		value.CommentURL = existing.CommentURL
		value.AppliedState = existing.AppliedState
		value.AppliedBodyHash = existing.AppliedBodyHash
		// Preserve an active lease across a newer desired projection. Revoking it
		// here would allow the next worker to PATCH concurrently; the older
		// provider request could then arrive last and roll the remote comment back.
		value.ClaimToken = existing.ClaimToken
		value.ClaimedAt = existing.ClaimedAt
		value.CreatedAt = existing.CreatedAt
	}
	m.reviewStatusComments[key] = value
	return true
}

func (m *MemStore) buildReviewStatusSnapshotLocked(run *domain.Run, intent *domain.ReviewStatusComment) (domain.RunPluginSnapshot, bool, error) {
	installationID := strings.TrimSpace(intent.InstallationID)
	if installationID == "" {
		return domain.RunPluginSnapshot{}, false, nil
	}
	binding, ok := m.serviceRepoBindings[run.ServiceID]
	if !ok || binding.InstallationID != installationID || binding.ProviderRepoID != intent.Key.ProviderRepoID ||
		binding.RepositoryPath != intent.RepositoryPath {
		return domain.RunPluginSnapshot{}, false, fmt.Errorf("%w: accepted review repository binding changed", ErrConflict)
	}
	installation, ok := m.pluginInstallations[installationID]
	if !ok || installation.ProjectID != run.ProjectID || domain.GitProvider(installation.Provider) != intent.Key.Provider ||
		installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" ||
		installation.CredentialVersionID == "" ||
		(installation.Provider == domain.PluginGitHub && installation.GitHubInstallID == "") ||
		(installation.Provider != domain.PluginGitHub && !installation.TokenSet()) {
		return domain.RunPluginSnapshot{}, false, fmt.Errorf("%w: accepted review installation changed", ErrConflict)
	}
	cfg, ok := m.providerConfigs[installation.Provider]
	if !ok || !cfg.PluginEnabled || cfg.ConfigRevision != installation.ConfigRevision {
		return domain.RunPluginSnapshot{}, false, fmt.Errorf("%w: accepted review provider configuration changed", ErrConflict)
	}
	if _, ok := m.providerConfigVersions[providerConfigVersionKey(installation.Provider, installation.ConfigRevision)]; !ok {
		return domain.RunPluginSnapshot{}, false, fmt.Errorf("%w: accepted review provider version is unavailable", ErrConflict)
	}
	credentialVersion, ok := m.pluginCredentialVersions[installation.CredentialVersionID]
	if !ok || credentialVersion.InstallationID != installation.ID || credentialVersion.Provider != installation.Provider {
		return domain.RunPluginSnapshot{}, false, fmt.Errorf("%w: accepted review credential version is unavailable", ErrConflict)
	}
	createdAt := intent.UpdatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return domain.RunPluginSnapshot{
		RunID: run.ID, InstallationID: installation.ID, Provider: installation.Provider,
		ProviderConfigRevision: installation.ConfigRevision, CredentialVersionID: installation.CredentialVersionID,
		RepositoryID: binding.ProviderRepoID, RepositoryPath: binding.RepositoryPath,
		CloneURL: binding.CloneURL, DefaultBranch: binding.DefaultBranch,
		ActingPrincipalKind: "provider_bot", ActingPrincipalID: installation.ID, CreatedAt: createdAt,
	}, true, nil
}

func (m *MemStore) GetReviewStatusComment(_ context.Context, key domain.ReviewStatusCommentKey) (*domain.ReviewStatusComment, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.reviewStatusComments[reviewStatusMapKey(key)]
	if !ok {
		return nil, ErrNotFound
	}
	copyValue := cloneReviewStatusComment(value)
	return &copyValue, nil
}

func (m *MemStore) reviewStatusPendingLocked(value domain.ReviewStatusComment, now time.Time) bool {
	if value.CurrentRunID == "" || (value.NextAttemptAt != nil && value.NextAttemptAt.After(now)) {
		return false
	}
	if value.CommentID == "" || value.DesiredState != value.AppliedState || value.DesiredBodyHash != value.AppliedBodyHash {
		return true
	}
	if run, ok := m.runs[value.CurrentRunID]; ok {
		return value.ObservedRun != domain.ReviewStatusObservationForRun(run) ||
			!domain.ReviewStatusCompletionConverged(value.AppliedState, run.ReviewResult)
	}
	return false
}

func (m *MemStore) ListPendingReviewStatusComments(_ context.Context, now, staleBefore time.Time, limit int) ([]domain.ReviewStatusComment, error) {
	if limit <= 0 {
		return []domain.ReviewStatusComment{}, nil
	}
	if limit > 1000 {
		limit = 1000
	}
	if now.IsZero() || staleBefore.IsZero() {
		return nil, errors.New("review status pending timestamps are required")
	}
	now, staleBefore = now.UTC(), staleBefore.UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.ReviewStatusComment, 0)
	for _, value := range m.reviewStatusComments {
		if value.ClaimToken != "" && value.ClaimedAt != nil && value.ClaimedAt.After(staleBefore) {
			continue
		}
		if m.reviewStatusPendingLocked(value, now) {
			out = append(out, cloneReviewStatusComment(value))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		iDue, jDue := out[i].UpdatedAt, out[j].UpdatedAt
		if out[i].NextAttemptAt != nil {
			iDue = *out[i].NextAttemptAt
		}
		if out[j].NextAttemptAt != nil {
			jDue = *out[j].NextAttemptAt
		}
		if !iDue.Equal(jDue) {
			return iDue.Before(jDue)
		}
		return reviewStatusKeyLess(out[i].Key, out[j].Key)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ClaimReviewStatusComment(_ context.Context, key domain.ReviewStatusCommentKey, claimToken string, claimedAt, staleBefore time.Time) (*domain.ReviewStatusComment, bool, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, false, err
	}
	claimToken = strings.TrimSpace(claimToken)
	if claimToken == "" || claimedAt.IsZero() || staleBefore.IsZero() {
		return nil, false, errors.New("review status claim token and timestamp are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mapKey := reviewStatusMapKey(key)
	value, ok := m.reviewStatusComments[mapKey]
	claimedAt, staleBefore = claimedAt.UTC(), staleBefore.UTC()
	if !ok || !m.reviewStatusPendingLocked(value, claimedAt) ||
		(value.ClaimToken != "" && value.ClaimedAt != nil && value.ClaimedAt.After(staleBefore)) {
		return nil, false, nil
	}
	value.ClaimToken = claimToken
	value.ClaimedAt = &claimedAt
	value.Attempts++
	value.UpdatedAt = claimedAt
	m.reviewStatusComments[mapKey] = value
	copyValue := cloneReviewStatusComment(value)
	return &copyValue, true, nil
}

func (m *MemStore) MarkReviewStatusCommentApplied(_ context.Context, key domain.ReviewStatusCommentKey, claimToken, commentID, commentURL string, appliedState domain.ReviewStatusState, appliedBodyHash string, at time.Time) (*domain.ReviewStatusComment, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, err
	}
	claimToken, commentID, commentURL, appliedBodyHash = strings.TrimSpace(claimToken), strings.TrimSpace(commentID), strings.TrimSpace(commentURL), strings.TrimSpace(appliedBodyHash)
	if claimToken == "" || !appliedState.Valid() || appliedBodyHash == "" || at.IsZero() {
		return nil, errors.New("complete review status claim is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mapKey := reviewStatusMapKey(key)
	value, ok := m.reviewStatusComments[mapKey]
	if !ok {
		return nil, ErrNotFound
	}
	if value.ClaimToken != claimToken || value.DesiredState != appliedState || value.DesiredBodyHash != appliedBodyHash ||
		(value.CommentID == "" && commentID == "") {
		return nil, ErrConflict
	}
	if commentID != "" {
		value.CommentID = commentID
	}
	if commentURL != "" {
		value.CommentURL = commentURL
	}
	value.AppliedState = appliedState
	value.AppliedBodyHash = appliedBodyHash
	value.ClaimToken = ""
	value.ClaimedAt = nil
	value.Attempts = 0
	value.LastError = ""
	value.NextAttemptAt = nil
	value.UpdatedAt = at.UTC()
	m.reviewStatusComments[mapKey] = value
	copyValue := cloneReviewStatusComment(value)
	return &copyValue, nil
}

func (m *MemStore) MarkReviewStatusCommentFailed(_ context.Context, key domain.ReviewStatusCommentKey, claimToken, lastError string, at time.Time) (*domain.ReviewStatusComment, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, err
	}
	claimToken, lastError = strings.TrimSpace(claimToken), strings.TrimSpace(lastError)
	if claimToken == "" || lastError == "" || at.IsZero() {
		return nil, errors.New("failed review status claim is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mapKey := reviewStatusMapKey(key)
	value, ok := m.reviewStatusComments[mapKey]
	if !ok {
		return nil, ErrNotFound
	}
	if value.ClaimToken != claimToken {
		return nil, ErrConflict
	}
	value.ClaimToken = ""
	value.ClaimedAt = nil
	value.LastError = lastError
	at = at.UTC()
	nextAttemptAt := at.Add(reviewStatusRetryDelay(value.Attempts))
	value.NextAttemptAt = &nextAttemptAt
	value.UpdatedAt = at
	m.reviewStatusComments[mapKey] = value
	copyValue := cloneReviewStatusComment(value)
	return &copyValue, nil
}

func (m *MemStore) UpdateReviewStatusCommentDesired(_ context.Context, key domain.ReviewStatusCommentKey, currentRunID, headSHA string, desiredState domain.ReviewStatusState, desiredBodyHash string, observed domain.ReviewStatusRunObservation, at time.Time) (*domain.ReviewStatusComment, error) {
	if err := validateReviewStatusKey(key); err != nil {
		return nil, err
	}
	currentRunID, headSHA, desiredBodyHash = strings.TrimSpace(currentRunID), strings.TrimSpace(headSHA), strings.TrimSpace(desiredBodyHash)
	if currentRunID == "" || !domain.ValidCommitSHA(headSHA) || !desiredState.Valid() || desiredBodyHash == "" || at.IsZero() {
		return nil, errors.New("desired review status is invalid")
	}
	if err := validateReviewStatusObservation(observed); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mapKey := reviewStatusMapKey(key)
	value, ok := m.reviewStatusComments[mapKey]
	if !ok {
		return nil, ErrNotFound
	}
	run, ok := m.runs[currentRunID]
	if !ok || run.ServiceID != key.ServiceID || domain.ReviewStatusObservationForRun(run) != observed {
		return nil, ErrConflict
	}
	if value.CurrentRunID != currentRunID || value.HeadSHA != headSHA {
		return nil, ErrConflict
	}
	desiredChanged := value.DesiredState != desiredState || value.DesiredBodyHash != desiredBodyHash
	observationChanged := value.ObservedRun != observed
	value.DesiredState = desiredState
	value.DesiredBodyHash = desiredBodyHash
	value.ObservedRun = observed
	if desiredChanged {
		value.Attempts = 0
		value.LastError = ""
		value.NextAttemptAt = nil
	}
	if desiredChanged || observationChanged {
		value.UpdatedAt = at.UTC()
	}
	m.reviewStatusComments[mapKey] = value
	copyValue := cloneReviewStatusComment(value)
	return &copyValue, nil
}

func reviewStatusRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 8 {
		shift = 8
	}
	delay := 5 * time.Second * time.Duration(1<<shift)
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
