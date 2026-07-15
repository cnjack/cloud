package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cnjack/jcloud/internal/domain"
)

// PGStore is a Postgres-backed Store using a pgx connection pool.
type PGStore struct {
	pool *pgxpool.Pool
}

// New opens a pool against dsn and returns a PGStore. Callers should run
// Migrate separately.
func New(ctx context.Context, dsn string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &PGStore{pool: pool}, nil
}

// Pool exposes the underlying pool (used by Migrate at boot).
func (s *PGStore) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool.
func (s *PGStore) Close() { s.pool.Close() }

// --- Projects ---------------------------------------------------------------

const projectCols = `id, name, created_at,
	max_concurrent_runs, run_timeout_secs, provider_allowlist, injected_env, owner_user_id,
	max_live_sessions, session_idle_timeout_secs, session_ttl_secs`

func scanProject(row pgx.Row) (*domain.Project, error) {
	var p domain.Project
	var ownerUserID *string
	err := row.Scan(&p.ID, &p.Name, &p.CreatedAt,
		&p.MaxConcurrentRuns, &p.RunTimeoutSecs, &p.ProviderAllowlist, &p.InjectedEnv, &ownerUserID,
		&p.MaxLiveSessions, &p.SessionIdleTimeoutSecs, &p.SessionTTLSecs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan project: %w", err)
	}
	if ownerUserID != nil {
		p.OwnerUserID = *ownerUserID
	}
	return &p, nil
}

func (s *PGStore) CreateProject(ctx context.Context, p *domain.Project) error {
	env := p.InjectedEnv
	if env == nil {
		env = map[string]string{} // column is NOT NULL DEFAULT '{}'
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO projects (`+projectCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		p.ID, p.Name, p.CreatedAt,
		p.MaxConcurrentRuns, p.RunTimeoutSecs, p.ProviderAllowlist, env, nullStr(p.OwnerUserID),
		p.MaxLiveSessions, p.SessionIdleTimeoutSecs, p.SessionTTLSecs)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	return nil
}

func (s *PGStore) GetProject(ctx context.Context, id string) (*domain.Project, error) {
	return scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectCols+` FROM projects WHERE id=$1`, id))
}

func (s *PGStore) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectCols+` FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var out []domain.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateProject(ctx context.Context, p *domain.Project) error {
	env := p.InjectedEnv
	if env == nil {
		env = map[string]string{}
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE projects SET name=$2, max_concurrent_runs=$3, run_timeout_secs=$4,
		    provider_allowlist=$5, injected_env=$6,
		    max_live_sessions=$7, session_idle_timeout_secs=$8, session_ttl_secs=$9
		 WHERE id=$1`,
		p.ID, p.Name, p.MaxConcurrentRuns, p.RunTimeoutSecs, p.ProviderAllowlist, env,
		p.MaxLiveSessions, p.SessionIdleTimeoutSecs, p.SessionTTLSecs)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) DeleteProject(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Services ----------------------------------------------------------------

const serviceCols = `id, project_id, name, repo_kind, provider, repo_owner_name,
	raw_repo_url, provider_repo_id, default_branch, git_mode, default_model_id, integration_id, created_at`

// serviceSelectCols is serviceCols plus the archive columns (F10). It is used by
// every SELECT/scan of a service; INSERT/UPDATE keep using serviceCols because
// archived_at/archive_key are written only by MarkServiceArchived /
// ClearServiceArchive, never by service create/update.
const serviceSelectCols = serviceCols + `, archived_at, archive_key`

// nullStr maps an empty Go string to a SQL NULL so nullable columns (provider,
// repo_owner_name, raw_repo_url) stay NULL rather than ” — the services CHECK
// constraint on provider only permits NULL or an enum value, not ”.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullRunResult maps a nil *RunResult to a SQL NULL and a set one to its text,
// so the nullable runs.result column stays NULL for ordinary (produced-a-diff)
// runs rather than storing an empty string.
func nullRunResult(r *domain.RunResult) *string {
	if r == nil {
		return nil
	}
	s := string(*r)
	return &s
}

func scanService(row pgx.Row) (*domain.Service, error) {
	var s domain.Service
	var provider, ownerName, rawURL, archiveKey *string
	err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.RepoKind,
		&provider, &ownerName, &rawURL, &s.ProviderRepoID, &s.DefaultBranch, &s.GitMode,
		&s.DefaultModelID, &s.IntegrationID, &s.CreatedAt, &s.ArchivedAt, &archiveKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan service: %w", err)
	}
	if provider != nil {
		s.Provider = domain.GitProvider(*provider)
	}
	if ownerName != nil {
		s.RepoOwnerName = *ownerName
	}
	if rawURL != nil {
		s.RawRepoURL = *rawURL
	}
	if archiveKey != nil {
		s.ArchiveKey = *archiveKey
	}
	return &s, nil
}

func (s *PGStore) CreateService(ctx context.Context, svc *domain.Service) error {
	if svc.GitMode == "" {
		svc.GitMode = domain.GitModeReadonly
	}
	if svc.DefaultBranch == "" {
		svc.DefaultBranch = "main"
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO services (`+serviceCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		svc.ID, svc.ProjectID, svc.Name, string(svc.RepoKind),
		nullStr(string(svc.Provider)), nullStr(svc.RepoOwnerName), nullStr(svc.RawRepoURL),
		svc.ProviderRepoID, svc.DefaultBranch, string(svc.GitMode), svc.DefaultModelID, svc.IntegrationID, svc.CreatedAt)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	return nil
}

func (s *PGStore) GetService(ctx context.Context, id string) (*domain.Service, error) {
	return scanService(s.pool.QueryRow(ctx,
		`SELECT `+serviceSelectCols+` FROM services WHERE id=$1`, id))
}

func (s *PGStore) ListServices(ctx context.Context, projectID string) ([]domain.Service, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+serviceSelectCols+` FROM services WHERE project_id=$1 ORDER BY created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()
	var out []domain.Service
	for rows.Next() {
		svc, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *svc)
	}
	return out, rows.Err()
}

func (s *PGStore) GetDefaultService(ctx context.Context, projectID string) (*domain.Service, error) {
	return scanService(s.pool.QueryRow(ctx,
		`SELECT `+serviceSelectCols+` FROM services WHERE project_id=$1 AND name='default'`, projectID))
}

func (s *PGStore) ListServicesByRepo(ctx context.Context, provider domain.GitProvider, repoOwnerName string) ([]domain.Service, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+serviceSelectCols+` FROM services
		 WHERE repo_kind='provider' AND provider=$1 AND repo_owner_name=$2
		 ORDER BY created_at ASC`,
		string(provider), repoOwnerName)
	if err != nil {
		return nil, fmt.Errorf("list services by repo: %w", err)
	}
	defer rows.Close()
	var out []domain.Service
	for rows.Next() {
		svc, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *svc)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateService(ctx context.Context, svc *domain.Service) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE services SET name=$2, repo_kind=$3, provider=$4, repo_owner_name=$5,
		    raw_repo_url=$6, provider_repo_id=$7, default_branch=$8, git_mode=$9,
		    default_model_id=$10, integration_id=$11
		 WHERE id=$1`,
		svc.ID, svc.Name, string(svc.RepoKind), nullStr(string(svc.Provider)),
		nullStr(svc.RepoOwnerName), nullStr(svc.RawRepoURL), svc.ProviderRepoID,
		svc.DefaultBranch, string(svc.GitMode), svc.DefaultModelID, svc.IntegrationID)
	if err != nil {
		return fmt.Errorf("update service: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) DeleteService(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM services WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListArchiveCandidates returns services eligible for cold archival (F10 /
// D23 ③). A candidate is a service that (a) is not already archived, (b) has at
// least one run, (c) whose most-recent run predates idleBefore, and (d) has NO
// run currently in a non-terminal state. The GROUP BY + HAVING computes the
// idle window and the "no live run" guard in one scan; ordering oldest-idle
// first makes the reconciler drain the longest-idle services first.
func (s *PGStore) ListArchiveCandidates(ctx context.Context, idleBefore time.Time) ([]ArchiveCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.project_id, MAX(r.created_at) AS last_activity
		FROM services s
		JOIN runs r ON r.service_id = s.id
		WHERE s.archived_at IS NULL
		GROUP BY s.id, s.project_id
		HAVING MAX(r.created_at) < $1
		   AND COUNT(*) FILTER (
		       WHERE r.status IN ('queued','scheduling','running','awaiting_input')
		   ) = 0
		ORDER BY last_activity ASC`, idleBefore)
	if err != nil {
		return nil, fmt.Errorf("list archive candidates: %w", err)
	}
	defer rows.Close()
	var out []ArchiveCandidate
	for rows.Next() {
		var c ArchiveCandidate
		if err := rows.Scan(&c.ServiceID, &c.ProjectID, &c.LastActivity); err != nil {
			return nil, fmt.Errorf("scan archive candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkServiceArchived stamps archived_at + archive_key (F10). Last-write-wins.
func (s *PGStore) MarkServiceArchived(ctx context.Context, serviceID, archiveKey string, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE services SET archived_at=$2, archive_key=$3 WHERE id=$1`,
		serviceID, at, archiveKey)
	if err != nil {
		return fmt.Errorf("mark service archived: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearServiceArchive clears archived_at + archive_key when a run restores the
// workspace (F10). Idempotent: clearing an already-clear service still succeeds.
func (s *PGStore) ClearServiceArchive(ctx context.Context, serviceID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE services SET archived_at=NULL, archive_key=NULL WHERE id=$1`, serviceID)
	if err != nil {
		return fmt.Errorf("clear service archive: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Runs -------------------------------------------------------------------

const runCols = `id, project_id, service_id, prompt, status, kind, phase, error, k8s_job_name,
	retried_from, failure_reason, failure_message, attempt, token_hash,
	created_at, started_at, finished_at, job_cleaned_at,
	git_branch, commit_sha, pr_url, pr_number, review_output, triggered_by_user_id,
	review_posted_at, pr_head_branch, pr_base_branch,
	origin, origin_comment_id, origin_comment_url, origin_automation_id, origin_event_key,
	result, model_id, model_name,
	session, awaiting_since, session_finalizing, bundle_rev, pushed_rev, permission_mode,
	acp_session_id, resumed_from`

func scanRun(row pgx.Row) (*domain.Run, error) {
	var r domain.Run
	var commentID, commentURL, automationID, eventKey, result *string
	err := row.Scan(&r.ID, &r.ProjectID, &r.ServiceID, &r.Prompt, &r.Status, &r.Kind, &r.Phase, &r.Error,
		&r.K8sJobName, &r.RetriedFrom, &r.FailureReason, &r.FailureMessage,
		&r.Attempt, &r.TokenHash,
		&r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.JobCleanedAt,
		&r.GitBranch, &r.CommitSHA, &r.PRURL, &r.PRNumber, &r.ReviewOutput, &r.TriggeredByUserID,
		&r.ReviewPostedAt, &r.PRHeadBranch, &r.PRBaseBranch,
		&r.Origin, &commentID, &commentURL, &automationID, &eventKey, &result, &r.ModelID, &r.ModelName,
		&r.Session, &r.AwaitingSince, &r.SessionFinalizing, &r.BundleRev, &r.PushedRev, &r.PermissionMode,
		&r.AcpSessionID, &r.ResumedFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}
	if commentID != nil {
		r.OriginCommentID = *commentID
	}
	if commentURL != nil {
		r.OriginCommentURL = *commentURL
	}
	if automationID != nil {
		r.OriginAutomationID = *automationID
	}
	if eventKey != nil {
		r.OriginEventKey = *eventKey
	}
	if result != nil {
		rr := domain.RunResult(*result)
		r.Result = &rr
	}
	return &r, nil
}

func (s *PGStore) CreateRun(ctx context.Context, r *domain.Run) error {
	if r.Kind == "" {
		r.Kind = domain.RunKindAgent
	}
	if r.Origin == "" {
		r.Origin = domain.RunOriginAPI
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO runs (`+runCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43)`,
		r.ID, r.ProjectID, r.ServiceID, r.Prompt, r.Status, string(r.Kind), r.Phase, r.Error, r.K8sJobName,
		r.RetriedFrom, r.FailureReason, r.FailureMessage, r.Attempt, r.TokenHash,
		r.CreatedAt, r.StartedAt, r.FinishedAt, r.JobCleanedAt,
		r.GitBranch, r.CommitSHA, r.PRURL, r.PRNumber, r.ReviewOutput, r.TriggeredByUserID,
		r.ReviewPostedAt, r.PRHeadBranch, r.PRBaseBranch,
		string(r.Origin), nullStr(r.OriginCommentID), nullStr(r.OriginCommentURL), nullStr(r.OriginAutomationID), nullStr(r.OriginEventKey),
		nullRunResult(r.Result), r.ModelID, r.ModelName,
		r.Session, r.AwaitingSince, r.SessionFinalizing, r.BundleRev, r.PushedRev, r.PermissionMode,
		r.AcpSessionID, r.ResumedFrom)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	return nil
}

// GetRunByOriginCommentID looks up the run a webhook comment already triggered
// (M7 de-dup). An empty id never matches (api-origin runs store NULL).
func (s *PGStore) GetRunByOriginCommentID(ctx context.Context, commentID string) (*domain.Run, error) {
	if commentID == "" {
		return nil, ErrNotFound
	}
	return scanRun(s.pool.QueryRow(ctx,
		`SELECT `+runCols+` FROM runs WHERE origin_comment_id=$1`, commentID))
}

func (s *PGStore) GetRunByOriginEventKey(ctx context.Context, eventKey string) (*domain.Run, error) {
	if eventKey == "" {
		return nil, ErrNotFound
	}
	return scanRun(s.pool.QueryRow(ctx,
		`SELECT `+runCols+` FROM runs WHERE origin_event_key=$1`, eventKey))
}

func (s *PGStore) ListRunsByService(ctx context.Context, serviceID string, limit int) ([]domain.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+runCols+` FROM runs WHERE service_id=$1 ORDER BY created_at DESC LIMIT $2`,
		serviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs by service: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PGStore) GetRun(ctx context.Context, id string) (*domain.Run, error) {
	return scanRun(s.pool.QueryRow(ctx, `SELECT `+runCols+` FROM runs WHERE id=$1`, id))
}

func (s *PGStore) GetRunByTokenHash(ctx context.Context, tokenHash string) (*domain.Run, error) {
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	return scanRun(s.pool.QueryRow(ctx,
		`SELECT `+runCols+` FROM runs WHERE token_hash=$1`, tokenHash))
}

func (s *PGStore) ListRuns(ctx context.Context, projectID string, limit int) ([]domain.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		rows pgx.Rows
		err  error
	)
	if projectID == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT `+runCols+` FROM runs ORDER BY created_at DESC LIMIT $1`, limit)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT `+runCols+` FROM runs WHERE project_id=$1 ORDER BY created_at DESC LIMIT $2`,
			projectID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PGStore) ListRunsByStatus(ctx context.Context, statuses ...domain.RunStatus) ([]domain.Run, error) {
	strs := make([]string, len(statuses))
	for i, st := range statuses {
		strs[i] = string(st)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+runCols+` FROM runs WHERE status = ANY($1) ORDER BY created_at ASC`, strs)
	if err != nil {
		return nil, fmt.Errorf("list runs by status: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PGStore) ListTerminalRunsWithJob(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runCols+` FROM runs
		 WHERE k8s_job_name <> '' AND job_cleaned_at IS NULL AND status = ANY($1)
		 ORDER BY created_at ASC`,
		[]string{string(domain.StatusSucceeded), string(domain.StatusFailed), string(domain.StatusCanceled)})
	if err != nil {
		return nil, fmt.Errorf("list terminal runs with job: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PGStore) CountActiveRuns(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM runs WHERE status = ANY($1)`,
		[]string{string(domain.StatusScheduling), string(domain.StatusRunning)}).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count active runs: %w", err)
	}
	return n, nil
}

func (s *PGStore) CountRunsByStatus(ctx context.Context, statuses ...domain.RunStatus) (map[domain.RunStatus]int, error) {
	out := make(map[domain.RunStatus]int, len(statuses))
	if len(statuses) == 0 {
		return out, nil
	}
	strs := make([]string, len(statuses))
	for i, st := range statuses {
		strs[i] = string(st)
		out[st] = 0 // every requested status is present as a key, defaulting to 0
	}
	rows, err := s.pool.Query(ctx,
		`SELECT status, count(*) FROM runs WHERE status = ANY($1) GROUP BY status`, strs)
	if err != nil {
		return nil, fmt.Errorf("count runs by status: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, fmt.Errorf("scan count row: %w", err)
		}
		out[domain.RunStatus(st)] = n
	}
	return out, rows.Err()
}

// lockRunTx begins a transaction and locks the run row FOR UPDATE, returning the
// committed row. Callers mutate only the fields they own via tx.Exec and then
// commit with commitAndReload. The returned tx MUST be rolled back by the caller
// (deferred) if not committed.
func (s *PGStore) lockRunTx(ctx context.Context, id string) (pgx.Tx, *domain.Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin run tx: %w", err)
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runCols+` FROM runs WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err // ErrNotFound already normalised by scanRun
	}
	return tx, run, nil
}

// commitAndReload commits and returns the freshly-committed row so the caller
// never has to write a stale in-memory copy back.
func (s *PGStore) commitAndReload(ctx context.Context, tx pgx.Tx, id string) (*domain.Run, error) {
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit run tx: %w", err)
	}
	return s.GetRun(ctx, id)
}

// ScheduleRun: queued -> scheduling. The ONLY writer of k8s_job_name/token_hash.
func (s *PGStore) ScheduleRun(ctx context.Context, id, jobName, tokenHash, phase string) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if !domain.CanTransition(cur.Status, domain.StatusScheduling) {
		return nil, fmt.Errorf("%w: %s -> scheduling", ErrInvalidTransition, cur.Status)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET status=$2, phase=$3, k8s_job_name=$4, token_hash=$5 WHERE id=$1`,
		id, domain.StatusScheduling, phase, jobName, tokenHash); err != nil {
		return nil, fmt.Errorf("schedule run: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// MarkRunning: scheduling -> running, stamping started_at if null.
func (s *PGStore) MarkRunning(ctx context.Context, id, phase string, startedAt time.Time) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if !domain.CanTransition(cur.Status, domain.StatusRunning) {
		return nil, fmt.Errorf("%w: %s -> running", ErrInvalidTransition, cur.Status)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET status=$2, phase=$3,
		    started_at=COALESCE(started_at,$4) WHERE id=$1`,
		id, domain.StatusRunning, phase, startedAt); err != nil {
		return nil, fmt.Errorf("mark running: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// MarkSucceeded: -> succeeded, stamping finished_at if null.
func (s *PGStore) MarkSucceeded(ctx context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if !domain.CanTransition(cur.Status, domain.StatusSucceeded) {
		return nil, fmt.Errorf("%w: %s -> succeeded", ErrInvalidTransition, cur.Status)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET status=$2, phase=$3,
		    finished_at=COALESCE(finished_at,$4) WHERE id=$1`,
		id, domain.StatusSucceeded, phase, finishedAt); err != nil {
		return nil, fmt.Errorf("mark succeeded: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// MarkFailed: -> failed, preserving an already-set failure_reason/message. The
// given reason/message are applied only where the stored field is empty, so a
// specific runner-reported reason recorded by a concurrent ingest is never
// clobbered by the reconciler's generic cluster-derived classification.
func (s *PGStore) MarkFailed(ctx context.Context, id, phase string, reason domain.FailureReason, msg string, finishedAt time.Time) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if !domain.CanTransition(cur.Status, domain.StatusFailed) {
		return nil, fmt.Errorf("%w: %s -> failed", ErrInvalidTransition, cur.Status)
	}
	// CASE preserves any non-empty stored value; error mirrors the resulting
	// failure_message (also preserved if already set).
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET status=$2, phase=$3,
		    failure_reason  = CASE WHEN failure_reason=''  THEN $4 ELSE failure_reason  END,
		    failure_message = CASE WHEN failure_message='' THEN $5 ELSE failure_message END,
		    error           = CASE WHEN failure_message='' THEN $5 ELSE failure_message END,
		    finished_at     = COALESCE(finished_at,$6)
		 WHERE id=$1`,
		id, domain.StatusFailed, phase, string(reason), msg, finishedAt); err != nil {
		return nil, fmt.Errorf("mark failed: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// SetRunnerFailure records a runner-reported reason/message without changing
// status, first-writer-wins and only while non-terminal. A no-op is not an error.
func (s *PGStore) SetRunnerFailure(ctx context.Context, id string, reason domain.FailureReason, msg string) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if cur.Status.Terminal() {
		_ = tx.Rollback(ctx)
		return cur, nil // already terminal: leave it
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET
		    failure_reason  = CASE WHEN failure_reason=''  THEN $2 ELSE failure_reason  END,
		    failure_message = CASE WHEN failure_message='' THEN $3 ELSE failure_message END
		 WHERE id=$1`,
		id, string(reason), msg); err != nil {
		return nil, fmt.Errorf("set runner failure: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// CancelRun: -> canceled. Leaves k8s_job_name/token_hash intact so the returned
// committed row still names the Job the caller must delete.
func (s *PGStore) CancelRun(ctx context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if !domain.CanTransition(cur.Status, domain.StatusCanceled) {
		return nil, fmt.Errorf("%w: %s -> canceled", ErrInvalidTransition, cur.Status)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET status=$2, phase=$3,
		    finished_at=COALESCE(finished_at,$4) WHERE id=$1`,
		id, domain.StatusCanceled, phase, finishedAt); err != nil {
		return nil, fmt.Errorf("cancel run: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// MarkJobCleaned stamps job_cleaned_at once the run's Job is confirmed deleted.
// k8s_job_name is KEPT (historical record; see 11-api.md Run schema). Idempotent
// via COALESCE: a prior stamp is preserved. No status change; a missing run is a
// no-op.
func (s *PGStore) MarkJobCleaned(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE runs SET job_cleaned_at=COALESCE(job_cleaned_at, now()) WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("mark job cleaned: %w", err)
	}
	return nil
}

// SetRunGit records the runner-reported branch/commit (from a run.git event)
// without changing status. First-writer-wins per field via CASE, so a duplicate
// event is a no-op. Locks the row to serialise with a concurrent PR-create read.
func (s *PGStore) SetRunGit(ctx context.Context, id, branch, commitSHA string) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_ = cur
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET
		    git_branch = CASE WHEN git_branch='' THEN $2 ELSE git_branch END,
		    commit_sha = CASE WHEN commit_sha='' THEN $3 ELSE commit_sha END
		 WHERE id=$1`,
		id, branch, commitSHA); err != nil {
		return nil, fmt.Errorf("set run git: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// SetRunResult records a runner-reported run outcome (from a run.result event)
// without changing status. First-writer-wins via COALESCE, so a duplicate event
// is a no-op — result is written only while it is still NULL. Locks the row to
// serialise with a concurrent terminal-status reconcile.
func (s *PGStore) SetRunResult(ctx context.Context, id string, result domain.RunResult) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_ = cur
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET result = COALESCE(result, $2) WHERE id=$1`,
		id, string(result)); err != nil {
		return nil, fmt.Errorf("set run result: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// SetRunACPSession records the run's ACP session id (from a run.session event)
// without changing status. First-writer-wins via CASE: written only where
// acp_session_id is still empty, so a duplicate event — or a resume run whose id
// was pre-filled at creation — is a no-op. An empty id is ignored (the CASE
// leaves the column untouched, but the caller also guards against it). Locks the
// row so the empty-check and write are atomic against a concurrent write.
func (s *PGStore) SetRunACPSession(ctx context.Context, id, acpSessionID string) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_ = cur
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET acp_session_id = CASE WHEN acp_session_id='' THEN $2 ELSE acp_session_id END
		 WHERE id=$1`,
		id, acpSessionID); err != nil {
		return nil, fmt.Errorf("set run acp session: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// MarkPRCreated stamps pr_url/pr_number, idempotent + first-writer-wins: it
// writes only where pr_url is currently empty (CASE), so a retry or a raced
// second tick never double-opens or clobbers an already-recorded PR. The row is
// locked FOR UPDATE so the empty-check and the write are atomic.
func (s *PGStore) MarkPRCreated(ctx context.Context, id, prURL string, prNumber int) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_ = cur
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET
		    pr_url    = CASE WHEN pr_url=''   THEN $2 ELSE pr_url    END,
		    pr_number = CASE WHEN pr_url=''   THEN $3 ELSE pr_number END
		 WHERE id=$1`,
		id, prURL, prNumber); err != nil {
		return nil, fmt.Errorf("mark pr created: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// --- Session runs (D22) -----------------------------------------------------

// SetRunAwaitingInput: running -> awaiting_input, stamping awaiting_since only
// where it is still NULL (COALESCE) so a duplicate turn-complete does not reset
// the idle timer. Idempotent: from==awaiting_input is a no-op transition.
func (s *PGStore) SetRunAwaitingInput(ctx context.Context, id string, at time.Time) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if !domain.CanTransition(cur.Status, domain.StatusAwaitingInput) {
		return nil, fmt.Errorf("%w: %s -> awaiting_input", ErrInvalidTransition, cur.Status)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET status=$2, awaiting_since=COALESCE(awaiting_since,$3) WHERE id=$1`,
		id, domain.StatusAwaitingInput, at); err != nil {
		return nil, fmt.Errorf("set awaiting input: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// ResumeRun: awaiting_input -> running, clearing awaiting_since. Idempotent
// (already-running is a no-op transition).
func (s *PGStore) ResumeRun(ctx context.Context, id, phase string) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if !domain.CanTransition(cur.Status, domain.StatusRunning) {
		return nil, fmt.Errorf("%w: %s -> running", ErrInvalidTransition, cur.Status)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET status=$2, phase=$3, awaiting_since=NULL WHERE id=$1`,
		id, domain.StatusRunning, phase); err != nil {
		return nil, fmt.Errorf("resume run: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// MarkSessionFinalizing sets session_finalizing (idempotent) while the run is
// non-terminal so next-prompt answers 410. A terminal run is left untouched (not
// an error).
func (s *PGStore) MarkSessionFinalizing(ctx context.Context, id string) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if cur.Status.Terminal() {
		_ = tx.Rollback(ctx)
		return cur, nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET session_finalizing=TRUE WHERE id=$1`, id); err != nil {
		return nil, fmt.Errorf("mark session finalizing: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// FinalizeIdleSession — the CONDITIONAL finalize for the idle-timeout pass. The
// status/awaiting_since/flag checks live in the WHERE clause, so a run that was
// resumed (a message arrived after the reconciler's list) or already finalized
// between list and act is left untouched (no TOCTOU). rows==1 iff this call
// flipped the flag.
func (s *PGStore) FinalizeIdleSession(ctx context.Context, id string, cutoff time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE runs SET session_finalizing=TRUE
		 WHERE id=$1 AND status=$2 AND NOT session_finalizing
		   AND awaiting_since IS NOT NULL AND awaiting_since <= $3`,
		id, string(domain.StatusAwaitingInput), cutoff)
	if err != nil {
		return false, fmt.Errorf("finalize idle session: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// AppendRunMessage enqueues a follow-up prompt, allocating the next per-run seq
// under a transaction so concurrent posts never collide on (run_id, seq).
func (s *PGStore) AppendRunMessage(ctx context.Context, runID, prompt, createdBy string) (*domain.RunMessage, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin append message: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Lock the run so the seq allocation serialises with a concurrent post.
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM runs WHERE id=$1 FOR UPDATE`, runID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lock run for message: %w", err)
	}
	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(max(seq),0)+1 FROM run_messages WHERE run_id=$1`, runID).Scan(&seq); err != nil {
		return nil, fmt.Errorf("alloc message seq: %w", err)
	}
	msg := &domain.RunMessage{
		ID: domain.NewID(), RunID: runID, Seq: seq, Prompt: prompt,
		CreatedBy: createdBy, CreatedAt: time.Now().UTC(),
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO run_messages (id, run_id, seq, prompt, created_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		msg.ID, msg.RunID, msg.Seq, msg.Prompt, nullStr(msg.CreatedBy), msg.CreatedAt); err != nil {
		return nil, fmt.Errorf("insert run message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit append message: %w", err)
	}
	return msg, nil
}

// runMessageCols are the run_messages columns in scanRunMessage order.
const runMessageCols = `id, run_id, seq, prompt, created_by, created_at, offered_at, consumed_at`

// scanRunMessage scans one run_messages row (see runMessageCols).
func scanRunMessage(row pgx.Row) (*domain.RunMessage, error) {
	var m domain.RunMessage
	var createdBy *string
	err := row.Scan(&m.ID, &m.RunID, &m.Seq, &m.Prompt, &createdBy, &m.CreatedAt, &m.OfferedAt, &m.ConsumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan run message: %w", err)
	}
	if createdBy != nil {
		m.CreatedBy = *createdBy
	}
	return &m, nil
}

// OfferNextMessage — phase 1 of the two-phase delivery. The whole decision runs
// in one transaction that first locks the RUN row, so two concurrent polls
// serialise and can never offer two DIFFERENT messages: the loser of the lock
// re-reads and sees the winner's offer, returning the SAME message as an
// idempotent re-delivery (fresh=false).
func (s *PGStore) OfferNextMessage(ctx context.Context, runID string, at time.Time) (*domain.RunMessage, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin offer message: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM runs WHERE id=$1 FOR UPDATE`, runID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		return nil, false, fmt.Errorf("lock run for offer: %w", err)
	}
	// An offered-but-not-consumed message is re-delivered verbatim: the runner
	// re-polling proves the previous response never started a turn.
	m, err := scanRunMessage(tx.QueryRow(ctx,
		`SELECT `+runMessageCols+` FROM run_messages
		 WHERE run_id=$1 AND offered_at IS NOT NULL AND consumed_at IS NULL
		 ORDER BY seq ASC LIMIT 1`, runID))
	if err == nil {
		if cerr := tx.Commit(ctx); cerr != nil {
			return nil, false, fmt.Errorf("commit re-offer: %w", cerr)
		}
		return m, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	// Nothing in flight: offer the oldest unoffered message.
	m, err = scanRunMessage(tx.QueryRow(ctx,
		`UPDATE run_messages SET offered_at=$2
		 WHERE id = (
		     SELECT id FROM run_messages
		     WHERE run_id=$1 AND offered_at IS NULL
		     ORDER BY seq ASC LIMIT 1
		 )
		 RETURNING `+runMessageCols, runID, at))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, false, ErrNotFound
		}
		return nil, false, fmt.Errorf("offer next message: %w", err)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return nil, false, fmt.Errorf("commit offer: %w", cerr)
	}
	return m, true, nil
}

// ConsumeOfferedMessage — phase 2: the turn the offered message started has
// completed (turn-complete). Idempotent: no offered message => (false, nil).
func (s *PGStore) ConsumeOfferedMessage(ctx context.Context, runID string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE run_messages SET consumed_at=$2
		 WHERE run_id=$1 AND offered_at IS NOT NULL AND consumed_at IS NULL`,
		runID, at)
	if err != nil {
		return false, fmt.Errorf("consume offered message: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListRunMessages returns a run's queued messages, oldest first.
func (s *PGStore) ListRunMessages(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runMessageCols+` FROM run_messages WHERE run_id=$1 ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run messages: %w", err)
	}
	defer rows.Close()
	var out []domain.RunMessage
	for rows.Next() {
		m, err := scanRunMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// --- Session permission approval (F8b) ---------------------------------------

// runPermissionCols are the run_permissions columns in scanRunPermission order.
const runPermissionCols = `request_id, run_id, tool_call_id, title, options, created_at,
	decided_option_id, decided_by, decided_at, resolved_option_id, resolution, resolved_at`

// scanRunPermission scans one run_permissions row (see runPermissionCols).
// options round-trips through JSONB as raw bytes.
func scanRunPermission(row pgx.Row) (*domain.RunPermission, error) {
	var p domain.RunPermission
	var options []byte
	err := row.Scan(&p.RequestID, &p.RunID, &p.ToolCallID, &p.Title, &options, &p.CreatedAt,
		&p.DecidedOptionID, &p.DecidedBy, &p.DecidedAt,
		&p.ResolvedOptionID, &p.Resolution, &p.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan run permission: %w", err)
	}
	if len(options) > 0 {
		if err := json.Unmarshal(options, &p.Options); err != nil {
			return nil, fmt.Errorf("unmarshal permission options: %w", err)
		}
	}
	return &p, nil
}

// UpsertRunPermission — insert-only idempotency: ON CONFLICT DO NOTHING, so a
// re-delivered request event can never reset an already-decided/resolved row.
// A missing run surfaces as ErrNotFound (FK violation) rather than a raw error
// so ingest can tolerate a race with run deletion.
func (s *PGStore) UpsertRunPermission(ctx context.Context, p *domain.RunPermission) error {
	options, err := json.Marshal(p.Options)
	if err != nil {
		return fmt.Errorf("marshal permission options: %w", err)
	}
	if p.Options == nil {
		options = []byte(`[]`)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO run_permissions (request_id, run_id, tool_call_id, title, options, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (request_id) DO NOTHING`,
		p.RequestID, p.RunID, p.ToolCallID, p.Title, options, p.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation: run gone
			return ErrNotFound
		}
		return fmt.Errorf("upsert run permission: %w", err)
	}
	return nil
}

func (s *PGStore) GetRunPermission(ctx context.Context, runID, requestID string) (*domain.RunPermission, error) {
	return scanRunPermission(s.pool.QueryRow(ctx,
		`SELECT `+runPermissionCols+` FROM run_permissions WHERE request_id=$1 AND run_id=$2`,
		requestID, runID))
}

// DecideRunPermission — the user's answer, conditional in the WHERE clause so
// two racing answers serialise on the row and exactly one wins (no TOCTOU).
func (s *PGStore) DecideRunPermission(ctx context.Context, runID, requestID, optionID, decidedBy string, at time.Time) (*domain.RunPermission, bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE run_permissions SET decided_option_id=$3, decided_by=$4, decided_at=$5
		 WHERE request_id=$1 AND run_id=$2 AND decided_option_id IS NULL AND resolved_at IS NULL`,
		requestID, runID, optionID, nullStr(decidedBy), at)
	if err != nil {
		return nil, false, fmt.Errorf("decide run permission: %w", err)
	}
	p, gerr := s.GetRunPermission(ctx, runID, requestID)
	if gerr != nil {
		return nil, false, gerr
	}
	return p, tag.RowsAffected() > 0, nil
}

// ResolveRunPermission — first-writer-wins on resolved_*; a missing or already-
// resolved row is a silent no-op (duplicate/orphan agent.permission_resolved).
func (s *PGStore) ResolveRunPermission(ctx context.Context, runID, requestID, optionID, resolution string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE run_permissions SET resolved_option_id=$3, resolution=$4, resolved_at=$5
		 WHERE request_id=$1 AND run_id=$2 AND resolved_at IS NULL`,
		requestID, runID, optionID, resolution, at)
	if err != nil {
		return fmt.Errorf("resolve run permission: %w", err)
	}
	return nil
}

// ListRunPermissions returns a run's permission requests, oldest first.
func (s *PGStore) ListRunPermissions(ctx context.Context, runID string) ([]domain.RunPermission, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runPermissionCols+` FROM run_permissions WHERE run_id=$1 ORDER BY created_at ASC, request_id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run permissions: %w", err)
	}
	defer rows.Close()
	var out []domain.RunPermission
	for rows.Next() {
		p, err := scanRunPermission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// BumpBundleRev increments bundle_rev (a fresh bundle awaits a push).
func (s *PGStore) BumpBundleRev(ctx context.Context, id string) (*domain.Run, error) {
	tx, _, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE runs SET bundle_rev=bundle_rev+1 WHERE id=$1`, id); err != nil {
		return nil, fmt.Errorf("bump bundle rev: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// SetPushedRev advances pushed_rev to at-least rev (GREATEST, monotonic) and
// records the pushed commit_sha (the session branch tip moves each turn). An
// EMPTY sha preserves the stored value — the PR-already-exists recovery path
// pushes nothing, so it must not wipe the last recorded tip. No status change.
func (s *PGStore) SetPushedRev(ctx context.Context, id string, rev int64, commitSHA string) (*domain.Run, error) {
	tx, _, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET pushed_rev=GREATEST(pushed_rev,$2),
		    commit_sha = CASE WHEN $3='' THEN commit_sha ELSE $3 END
		 WHERE id=$1`,
		id, rev, commitSHA); err != nil {
		return nil, fmt.Errorf("set pushed rev: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// ListSessionRunsAwaitingPush returns session AGENT runs with a recorded branch
// and a bundle newer than the last push (bundle_rev > pushed_rev), still in a
// non-final state. Ordered oldest-first.
func (s *PGStore) ListSessionRunsAwaitingPush(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runCols+` FROM runs
		 WHERE session AND kind='agent' AND git_branch <> '' AND bundle_rev > pushed_rev
		   AND status <> $1 AND status <> $2
		 ORDER BY created_at ASC`,
		string(domain.StatusFailed), string(domain.StatusCanceled))
	if err != nil {
		return nil, fmt.Errorf("list session runs awaiting push: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListAwaitingInputRuns returns every run currently in awaiting_input, oldest
// first (the idle-timeout reconcile pass).
func (s *PGStore) ListAwaitingInputRuns(ctx context.Context) ([]domain.Run, error) {
	return s.ListRunsByStatus(ctx, domain.StatusAwaitingInput)
}

// ListRunsAwaitingPR returns succeeded NON-session agent runs with a recorded
// branch (set when their bundle was received) but no PR yet. The mode/provider
// gate is applied by the reconciler after joining the service. Session runs are
// EXCLUDED — their per-turn draft-PR push is handled by the dedicated
// ListSessionRunsAwaitingPush pass so the two never double-process a run.
func (s *PGStore) ListRunsAwaitingPR(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runCols+` FROM runs
		 WHERE status=$1 AND kind='agent' AND git_branch <> '' AND pr_url = '' AND NOT session
		 ORDER BY created_at ASC`,
		string(domain.StatusSucceeded))
	if err != nil {
		return nil, fmt.Errorf("list runs awaiting pr: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListReviewRunsAwaitingPost returns succeeded review runs that have produced
// review output but whose comment has not yet been posted to the PR. The reconcile
// review pass posts them (M3/M5). Ordered oldest-first.
func (s *PGStore) ListReviewRunsAwaitingPost(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runCols+` FROM runs
		 WHERE status=$1 AND kind='review' AND review_output <> '' AND review_posted_at IS NULL
		 ORDER BY created_at ASC`,
		string(domain.StatusSucceeded))
	if err != nil {
		return nil, fmt.Errorf("list review runs awaiting post: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListRunsAwaitingUpdatePush returns succeeded webhook agent runs whose bundle
// was received onto an existing PR head branch but whose ff-only push has not
// completed (commit_sha unset). The reconciler pushes + stamps commit_sha (M7).
func (s *PGStore) ListRunsAwaitingUpdatePush(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runCols+` FROM runs
		 WHERE status=$1 AND origin='webhook' AND kind='agent'
		   AND git_branch <> '' AND pr_url <> '' AND commit_sha = ''
		 ORDER BY created_at ASC`,
		string(domain.StatusSucceeded))
	if err != nil {
		return nil, fmt.Errorf("list runs awaiting update push: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// SetReviewOutput records a review run's markdown output (from the runner's
// POST .../review) without changing status. First-writer-wins: it writes only
// while review_output is empty, so a re-POST is a no-op. Returns the committed row.
func (s *PGStore) SetReviewOutput(ctx context.Context, id, md string) (*domain.Run, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_ = cur
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET review_output = CASE WHEN review_output='' THEN $2 ELSE review_output END
		 WHERE id=$1`, id, md); err != nil {
		return nil, fmt.Errorf("set review output: %w", err)
	}
	return s.commitAndReload(ctx, tx, id)
}

// MarkReviewPosted stamps review_posted_at once the review comment has been
// posted to the PR. Idempotent + first-writer-wins via COALESCE under a row
// lock: returns posted=true only for the tick that actually stamped it, so two
// ticks never double-post. A missing run is ErrNotFound.
func (s *PGStore) MarkReviewPosted(ctx context.Context, id string) (bool, error) {
	tx, cur, err := s.lockRunTx(ctx, id)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if cur.ReviewPostedAt != nil {
		_ = tx.Rollback(ctx)
		return false, nil // already posted
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET review_posted_at=now() WHERE id=$1 AND review_posted_at IS NULL`, id); err != nil {
		return false, fmt.Errorf("mark review posted: %w", err)
	}
	if _, err := s.commitAndReload(ctx, tx, id); err != nil {
		return false, err
	}
	return true, nil
}

// --- Binary artifacts (bundle) ----------------------------------------------

// PutRunBundle stores a run's git bundle (kind='bundle') as bytea, upserting by
// (run_id, kind). content stays ” (the payload is in content_bytes).
func (s *PGStore) PutRunBundle(ctx context.Context, runID string, data []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO run_artifacts (run_id, kind, content, content_bytes, created_at)
		 VALUES ($1,$2,'',$3, now())
		 ON CONFLICT (run_id, kind) DO UPDATE SET content_bytes=EXCLUDED.content_bytes, created_at=EXCLUDED.created_at`,
		runID, string(domain.ArtifactBundle), data)
	if err != nil {
		return fmt.Errorf("put run bundle: %w", err)
	}
	return nil
}

// GetRunBundle returns a run's stored git bundle bytes (ErrNotFound if absent).
func (s *PGStore) GetRunBundle(ctx context.Context, runID string) ([]byte, error) {
	var data []byte
	err := s.pool.QueryRow(ctx,
		`SELECT content_bytes FROM run_artifacts WHERE run_id=$1 AND kind=$2`,
		runID, string(domain.ArtifactBundle)).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get run bundle: %w", err)
	}
	return data, nil
}

// --- Model catalog + project grants (D21) -----------------------------------

const modelProviderCols = `id, name, kind, base_url, auth_type, api_key_enc, catalog_mode,
	catalog_available, last_verified_at, last_verification_error, created_at, updated_at, updated_by`

func scanModelProvider(row pgx.Row) (*domain.ModelProvider, error) {
	var p domain.ModelProvider
	err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.AuthType, &p.APIKeyEnc,
		&p.CatalogMode, &p.CatalogAvailable, &p.LastVerifiedAt, &p.LastVerificationError,
		&p.CreatedAt, &p.UpdatedAt, &p.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan model provider: %w", err)
	}
	return &p, nil
}

func (s *PGStore) CreateModelProvider(ctx context.Context, p *domain.ModelProvider) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO model_providers (id, name, kind, base_url, auth_type, api_key_enc,
		 catalog_mode, catalog_available, last_verified_at, last_verification_error,
		 created_at, updated_at, updated_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now(),$12)`,
		p.ID, p.Name, p.Kind, p.BaseURL, p.AuthType, p.APIKeyEnc, p.CatalogMode,
		p.CatalogAvailable, p.LastVerifiedAt, p.LastVerificationError, p.CreatedAt, p.UpdatedBy)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create model provider: %w", err)
	}
	return nil
}

func (s *PGStore) GetModelProvider(ctx context.Context, id string) (*domain.ModelProvider, error) {
	return scanModelProvider(s.pool.QueryRow(ctx,
		`SELECT `+modelProviderCols+` FROM model_providers WHERE id=$1`, id))
}

func (s *PGStore) ListModelProviders(ctx context.Context) ([]domain.ModelProvider, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+modelProviderCols+` FROM model_providers ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list model providers: %w", err)
	}
	defer rows.Close()
	var out []domain.ModelProvider
	for rows.Next() {
		p, err := scanModelProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateModelProvider(ctx context.Context, p *domain.ModelProvider) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update model provider: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE model_providers SET name=$2, kind=$3, base_url=$4, auth_type=$5,
		 api_key_enc=$6, catalog_mode=$7, catalog_available=$8, last_verified_at=$9,
		 last_verification_error=$10, updated_at=now(), updated_by=$11 WHERE id=$1`,
		p.ID, p.Name, p.Kind, p.BaseURL, p.AuthType, p.APIKeyEnc, p.CatalogMode,
		p.CatalogAvailable, p.LastVerifiedAt, p.LastVerificationError, p.UpdatedBy)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("update model provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`UPDATE model_configs SET base_url=$2, api_key_enc=$3,
		 model_name=$4 || '/' || model_id, updated_at=now(), updated_by=$5
		 WHERE provider_id=$1`, p.ID, p.BaseURL, p.APIKeyEnc, p.Kind, p.UpdatedBy); err != nil {
		return fmt.Errorf("sync provider models: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update model provider: %w", err)
	}
	return nil
}

func (s *PGStore) DeleteModelProvider(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM model_providers WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete model provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const modelCols = `id, provider_id, name, base_url, model_name, model_id, api_key_enc,
	context_window, supports_reasoning, supports_tools, supports_image, model_source,
	created_at, updated_at, updated_by`

func scanModel(row pgx.Row) (*domain.Model, error) {
	var m domain.Model
	err := row.Scan(&m.ID, &m.ProviderID, &m.Name, &m.BaseURL, &m.ModelName, &m.ModelID, &m.APIKeyEnc,
		&m.ContextWindow, &m.Capabilities.Reasoning, &m.Capabilities.Tools,
		&m.Capabilities.Image, &m.Source, &m.CreatedAt, &m.UpdatedAt, &m.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan model: %w", err)
	}
	return &m, nil
}

// CreateModel inserts a catalog model. Duplicate name => ErrAlreadyExists.
func (s *PGStore) CreateModel(ctx context.Context, m *domain.Model) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.Source == "" {
		m.Source = "custom"
	}
	if m.ModelID == "" {
		_, bare, ok := strings.Cut(m.ModelName, "/")
		if ok {
			m.ModelID = bare
		} else {
			m.ModelID = m.ModelName
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create model: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if m.ProviderID == "" {
		m.ProviderID = m.ID
		authType := domain.ModelProviderAuthNone
		if len(m.APIKeyEnc) > 0 {
			authType = domain.ModelProviderAuthAPIKey
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO model_providers (id,name,kind,base_url,auth_type,api_key_enc,catalog_mode,created_at,updated_at,updated_by)
			 VALUES ($1,$2,'custom',$3,$4,$5,'disabled',$6,now(),$7)`,
			m.ProviderID, m.Name, m.BaseURL, authType, m.APIKeyEnc, m.CreatedAt, m.UpdatedBy); err != nil {
			if isUniqueViolation(err) {
				return ErrAlreadyExists
			}
			return fmt.Errorf("create implicit model provider: %w", err)
		}
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO model_configs (id, provider_id, name, base_url, model_name, model_id, api_key_enc,
		 context_window, supports_reasoning, supports_tools, supports_image, model_source,
		 created_at, updated_at, updated_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now(),$14)`,
		m.ID, m.ProviderID, m.Name, m.BaseURL, m.ModelName, m.ModelID, m.APIKeyEnc,
		m.ContextWindow, m.Capabilities.Reasoning, m.Capabilities.Tools,
		m.Capabilities.Image, m.Source, m.CreatedAt, m.UpdatedBy)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create model: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create model: %w", err)
	}
	return nil
}

// GetModel returns a catalog model by id.
func (s *PGStore) GetModel(ctx context.Context, id string) (*domain.Model, error) {
	return scanModel(s.pool.QueryRow(ctx, `SELECT `+modelCols+` FROM model_configs WHERE id=$1`, id))
}

// ListModels returns the whole catalog, newest first.
func (s *PGStore) ListModels(ctx context.Context) ([]domain.Model, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+modelCols+` FROM model_configs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	var out []domain.Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// CountModels returns the number of catalog models.
func (s *PGStore) CountModels(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM model_configs`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count models: %w", err)
	}
	return n, nil
}

// UpdateModel updates a model's mutable fields. Duplicate name => ErrAlreadyExists.
func (s *PGStore) UpdateModel(ctx context.Context, m *domain.Model) error {
	if m.Source == "" {
		m.Source = "custom"
	}
	if m.ModelID == "" {
		_, bare, ok := strings.Cut(m.ModelName, "/")
		if ok {
			m.ModelID = bare
		} else {
			m.ModelID = m.ModelName
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update model: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE model_configs SET name=$2, base_url=$3, model_name=$4, model_id=$5, api_key_enc=$6,
		 context_window=$7, supports_reasoning=$8, supports_tools=$9, supports_image=$10,
		 model_source=$11, updated_at=now(), updated_by=$12 WHERE id=$1`,
		m.ID, m.Name, m.BaseURL, m.ModelName, m.ModelID, m.APIKeyEnc, m.ContextWindow,
		m.Capabilities.Reasoning, m.Capabilities.Tools, m.Capabilities.Image,
		m.Source, m.UpdatedBy)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("update model: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if m.ProviderID != "" {
		providerKind, _, hasKind := strings.Cut(m.ModelName, "/")
		if !hasKind {
			providerKind = "custom"
		}
		authType := domain.ModelProviderAuthNone
		if len(m.APIKeyEnc) > 0 {
			authType = domain.ModelProviderAuthAPIKey
		}
		if _, err := tx.Exec(ctx,
			`UPDATE model_providers SET kind=$2, base_url=$3, api_key_enc=$4, auth_type=$5,
			 updated_at=now(), updated_by=$6 WHERE id=$1`,
			m.ProviderID, providerKind, m.BaseURL, m.APIKeyEnc, authType, m.UpdatedBy); err != nil {
			return fmt.Errorf("sync model provider: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE model_configs SET base_url=$2, api_key_enc=$3,
			 model_name=$4 || '/' || model_id, updated_at=now(), updated_by=$5
			 WHERE provider_id=$1`,
			m.ProviderID, m.BaseURL, m.APIKeyEnc, providerKind, m.UpdatedBy); err != nil {
			return fmt.Errorf("sync sibling provider models: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update model: %w", err)
	}
	return nil
}

func (s *PGStore) ListModelsForProvider(ctx context.Context, providerID string) ([]domain.Model, error) {
	if _, err := s.GetModelProvider(ctx, providerID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+modelCols+` FROM model_configs WHERE provider_id=$1 ORDER BY created_at ASC`, providerID)
	if err != nil {
		return nil, fmt.Errorf("list models for provider: %w", err)
	}
	defer rows.Close()
	var out []domain.Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// DeleteModel removes a catalog model (grants cascade; service/run refs SET NULL).
func (s *PGStore) DeleteModel(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM model_configs WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete model: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListModelsForProject returns the models granted to a project, newest first.
func (s *PGStore) ListModelsForProject(ctx context.Context, projectID string) ([]domain.Model, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+prefixCols("mc", modelCols)+`
		 FROM model_configs mc
		 JOIN model_grants g ON g.model_id = mc.id
		 WHERE g.project_id = $1
		 ORDER BY mc.created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list models for project: %w", err)
	}
	defer rows.Close()
	var out []domain.Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ListProjectIDsForModel returns the project ids a model is granted to.
func (s *PGStore) ListProjectIDsForModel(ctx context.Context, modelID string) ([]string, error) {
	// Confirm the model exists so a bad id is a 404 rather than an empty list.
	if _, err := s.GetModel(ctx, modelID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT project_id FROM model_grants WHERE model_id=$1 ORDER BY project_id`, modelID)
	if err != nil {
		return nil, fmt.Errorf("list project ids for model: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan grant project id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GrantModel authorizes a project to use a model (idempotent). A bad model/
// project id trips the FK and is normalised to ErrNotFound.
func (s *PGStore) GrantModel(ctx context.Context, modelID, projectID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO model_grants (model_id, project_id) VALUES ($1,$2)
		 ON CONFLICT (model_id, project_id) DO NOTHING`,
		modelID, projectID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
			return ErrNotFound
		}
		return fmt.Errorf("grant model: %w", err)
	}
	return nil
}

// RevokeModel removes a project's grant (idempotent no-op when absent).
func (s *PGStore) RevokeModel(ctx context.Context, modelID, projectID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM model_grants WHERE model_id=$1 AND project_id=$2`, modelID, projectID); err != nil {
		return fmt.Errorf("revoke model: %w", err)
	}
	return nil
}

// --- Cluster kanban config (D27) --------------------------------------------

// GetClusterKanbanConfig returns the single-row config (id pinned to 1), or
// ErrNotFound when absent. token_enc stays encrypted — decrypted only in the
// resolver.
func (s *PGStore) GetClusterKanbanConfig(ctx context.Context) (*domain.KanbanConfig, error) {
	var c domain.KanbanConfig
	err := s.pool.QueryRow(ctx,
		`SELECT base_url, token_enc, token_expires_at, updated_at, updated_by FROM cluster_kanban_config WHERE id=1`).
		Scan(&c.BaseURL, &c.TokenEnc, &c.TokenExpiresAt, &c.UpdatedAt, &c.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get cluster kanban config: %w", err)
	}
	return &c, nil
}

// UpsertClusterKanbanConfig writes the single-row config, stamping updated_at.
// A nil TokenEnc encodes to SQL NULL (no cluster fallback token); a nil
// TokenExpiresAt encodes to SQL NULL (unknown / manual-paste expiry, D28).
func (s *PGStore) UpsertClusterKanbanConfig(ctx context.Context, cfg *domain.KanbanConfig) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cluster_kanban_config (id, base_url, token_enc, token_expires_at, updated_at, updated_by)
		 VALUES (1, $1, $2, $3, now(), $4)
		 ON CONFLICT (id) DO UPDATE
		   SET base_url=EXCLUDED.base_url, token_enc=EXCLUDED.token_enc,
		       token_expires_at=EXCLUDED.token_expires_at,
		       updated_at=now(), updated_by=EXCLUDED.updated_by`,
		cfg.BaseURL, cfg.TokenEnc, nullTime(cfg.TokenExpiresAt), cfg.UpdatedBy)
	if err != nil {
		return fmt.Errorf("upsert cluster kanban config: %w", err)
	}
	return nil
}

// SetClusterKanbanToken conditionally seals a device-flow token onto the
// single-row config (D28): ONLY token_enc/token_expires_at/updated_by change,
// and ONLY where the row still carries baseURL — never base_url itself, so an
// admin PUT racing the completing poll wins. rows==0 (missing row OR changed
// base_url) is ErrNotFound; the caller expires the flow instead of storing a
// token minted against a stale instance.
func (s *PGStore) SetClusterKanbanToken(ctx context.Context, baseURL string, tokenEnc []byte, expiresAt *time.Time, updatedBy string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE cluster_kanban_config
		    SET token_enc=$2, token_expires_at=$3, updated_at=now(), updated_by=$4
		  WHERE id=1 AND base_url=$1`,
		baseURL, tokenEnc, nullTime(expiresAt), updatedBy)
	if err != nil {
		return fmt.Errorf("set cluster kanban token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteClusterKanbanConfig removes the single-row config (idempotent: a missing
// row is not an error — the resolver simply falls back to the JTYPE_* env).
func (s *PGStore) DeleteClusterKanbanConfig(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM cluster_kanban_config WHERE id=1`); err != nil {
		return fmt.Errorf("delete cluster kanban config: %w", err)
	}
	return nil
}

// --- Integrations (D19 / F5) ------------------------------------------------

const integrationCols = `id, project_id, name, provider, host, cred_type,
	token_enc, bot_username, created_by, created_at, updated_at`

func scanIntegration(row pgx.Row) (*domain.Integration, error) {
	var in domain.Integration
	var createdBy *string
	err := row.Scan(&in.ID, &in.ProjectID, &in.Name, &in.Provider, &in.Host, &in.CredType,
		&in.TokenEnc, &in.BotUsername, &createdBy, &in.CreatedAt, &in.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan integration: %w", err)
	}
	if createdBy != nil {
		in.CreatedBy = *createdBy
	}
	return &in, nil
}

func (s *PGStore) CreateIntegration(ctx context.Context, in *domain.Integration) error {
	if in.CredType == "" {
		in.CredType = domain.CredTypePAT
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO integrations (id, project_id, name, provider, host, cred_type,
		    token_enc, bot_username, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())`,
		in.ID, in.ProjectID, in.Name, string(in.Provider), in.Host, string(in.CredType),
		in.TokenEnc, in.BotUsername, nullStr(in.CreatedBy), in.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create integration: %w", err)
	}
	return nil
}

func (s *PGStore) GetIntegration(ctx context.Context, id string) (*domain.Integration, error) {
	return scanIntegration(s.pool.QueryRow(ctx,
		`SELECT `+integrationCols+` FROM integrations WHERE id=$1`, id))
}

func (s *PGStore) ListIntegrationsByProject(ctx context.Context, projectID string) ([]domain.Integration, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+integrationCols+` FROM integrations WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	defer rows.Close()
	var out []domain.Integration
	for rows.Next() {
		in, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateIntegration(ctx context.Context, in *domain.Integration) error {
	// Only name / token_enc / bot_username are mutable (host/provider/cred_type are
	// immutable — delete + recreate to change a host).
	tag, err := s.pool.Exec(ctx,
		`UPDATE integrations SET name=$2, token_enc=$3, bot_username=$4, updated_at=now()
		 WHERE id=$1`,
		in.ID, in.Name, in.TokenEnc, in.BotUsername)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("update integration: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) DeleteIntegration(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM integrations WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete integration: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) CountServicesUsingIntegration(ctx context.Context, integrationID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM services WHERE integration_id=$1`, integrationID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count services using integration: %w", err)
	}
	return n, nil
}

// --- Events -----------------------------------------------------------------

func (s *PGStore) AppendEvents(ctx context.Context, runID string, events []EventInput) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin append events: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	inserted := 0
	for _, e := range events {
		payload := e.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		// Caller-assigned seq: this is an internal emitter, so tag the row
		// source='internal' with client_seq=seq (the dedupe key mirrors the seq).
		tag, err := tx.Exec(ctx,
			`INSERT INTO run_events (run_id, seq, source, client_seq, type, payload)
			 VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (run_id, seq) DO NOTHING`,
			runID, e.Seq, SourceInternal, e.Seq, e.Type, payload)
		if err != nil {
			return 0, fmt.Errorf("insert event seq=%d: %w", e.Seq, err)
		}
		inserted += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit events: %w", err)
	}
	return inserted, nil
}

// AppendRunnerEvents ingests runner events with server-side seq allocation and
// per-source idempotency. The runs row is locked FOR UPDATE so concurrent
// allocators for the same run serialize and the per-run seq counter is
// gap-tolerant but strictly monotonic.
func (s *PGStore) AppendRunnerEvents(ctx context.Context, runID string, events []EventInput) ([]domain.RunEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin append runner events: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the run to serialize seq allocation across concurrent ingest calls.
	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM runs WHERE id=$1 FOR UPDATE`, runID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock run for seq alloc: %w", err)
	}

	var next int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM run_events WHERE run_id=$1`, runID).Scan(&next); err != nil {
		return nil, fmt.Errorf("read max seq: %w", err)
	}

	out := make([]domain.RunEvent, 0, len(events))
	for _, e := range events {
		payload := e.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		seq := next + 1
		var ts time.Time
		err := tx.QueryRow(ctx,
			`INSERT INTO run_events (run_id, seq, source, client_seq, type, payload)
			 VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (run_id, source, client_seq) DO NOTHING
			 RETURNING seq, ts`,
			runID, seq, SourceRunner, e.Seq, e.Type, payload).Scan(&seq, &ts)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // duplicate client_seq for this source: idempotent skip, no seq consumed
		}
		if err != nil {
			return nil, fmt.Errorf("insert runner event client_seq=%d: %w", e.Seq, err)
		}
		next = seq
		out = append(out, domain.RunEvent{
			RunID: runID, Seq: seq, TS: ts, Type: e.Type, Payload: payload,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit runner events: %w", err)
	}
	return out, nil
}

// AppendInternalEvent appends one internally-emitted event, allocating the next
// global seq under a run row lock so it never races runner ingest.
func (s *PGStore) AppendInternalEvent(ctx context.Context, runID, typ string, payload map[string]any) (domain.RunEvent, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("begin internal event: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM runs WHERE id=$1 FOR UPDATE`, runID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RunEvent{}, ErrNotFound
	}
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("lock run for internal event: %w", err)
	}

	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq),0)+1 FROM run_events WHERE run_id=$1`, runID).Scan(&seq); err != nil {
		return domain.RunEvent{}, fmt.Errorf("alloc internal seq: %w", err)
	}
	var ts time.Time
	if err := tx.QueryRow(ctx,
		`INSERT INTO run_events (run_id, seq, source, client_seq, type, payload)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 RETURNING ts`,
		runID, seq, SourceInternal, seq, typ, payload).Scan(&ts); err != nil {
		return domain.RunEvent{}, fmt.Errorf("insert internal event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunEvent{}, fmt.Errorf("commit internal event: %w", err)
	}
	return domain.RunEvent{RunID: runID, Seq: seq, TS: ts, Type: typ, Payload: payload}, nil
}

func (s *PGStore) NextEventSeq(ctx context.Context, runID string) (int64, error) {
	var next int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq),0)+1 FROM run_events WHERE run_id=$1`, runID).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("next event seq: %w", err)
	}
	return next, nil
}

func (s *PGStore) ListEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]domain.RunEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, run_id, seq, ts, type, payload FROM run_events
		 WHERE run_id=$1 AND seq > $2 ORDER BY seq ASC LIMIT $3`,
		runID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var out []domain.RunEvent
	for rows.Next() {
		var e domain.RunEvent
		if err := rows.Scan(&e.ID, &e.RunID, &e.Seq, &e.TS, &e.Type, &e.Payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Artifacts --------------------------------------------------------------

func (s *PGStore) PutArtifact(ctx context.Context, a *domain.RunArtifact) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO run_artifacts (run_id, kind, content, created_at)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (run_id, kind) DO UPDATE SET content=EXCLUDED.content, created_at=EXCLUDED.created_at`,
		a.RunID, a.Kind, a.Content, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("put artifact: %w", err)
	}
	return nil
}

func (s *PGStore) GetArtifact(ctx context.Context, runID string, kind domain.ArtifactKind) (*domain.RunArtifact, error) {
	var a domain.RunArtifact
	err := s.pool.QueryRow(ctx,
		`SELECT run_id, kind, content, created_at FROM run_artifacts WHERE run_id=$1 AND kind=$2`,
		runID, kind).Scan(&a.RunID, &a.Kind, &a.Content, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get artifact: %w", err)
	}
	return &a, nil
}

// --- Kanban (Feature E) -----------------------------------------------------

// kanbanLinkCols are the kanban_links columns in scan order. done_column,
// board_title, event_sequence and token_enc / token_expires_at may be NULL; the rest are NOT NULL
// (board_status defaults 'ok'). Nullable fields trail so scan-order edits stay
// localized (F6 / D25; token_expires_at D28; board_title/board_status D30).
const kanbanLinkCols = `id, workspace_id, board_ref, project_id, service_id,
	trigger_column, done_column, enabled, created_at, updated_at, board_status,
	board_title, token_enc, token_expires_at, event_sequence`

func scanKanbanLink(row pgx.Row) (*domain.KanbanLink, error) {
	var l domain.KanbanLink
	var doneColumn, boardTitle *string
	err := row.Scan(
		&l.ID, &l.WorkspaceID, &l.BoardRef, &l.ProjectID, &l.ServiceID,
		&l.TriggerColumn, &doneColumn, &l.Enabled, &l.CreatedAt, &l.UpdatedAt,
		&l.BoardStatus, &boardTitle, &l.TokenEnc, &l.TokenExpiresAt, &l.EventSequence)
	if err != nil {
		return nil, err
	}
	if doneColumn != nil {
		l.DoneColumn = *doneColumn
	}
	if boardTitle != nil {
		l.BoardTitle = *boardTitle
	}
	return &l, nil
}

func (s *PGStore) CreateKanbanLink(ctx context.Context, l *domain.KanbanLink) error {
	// A nil TokenEnc encodes to SQL NULL (cluster-fallback link); a non-nil blob
	// stores the sealed per-link PAT. An empty BoardStatus defaults to OK.
	status := l.BoardStatus
	if status == "" {
		status = domain.KanbanBoardOK
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO kanban_links (`+kanbanLinkCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		l.ID, l.WorkspaceID, l.BoardRef, l.ProjectID, l.ServiceID,
		l.TriggerColumn, nullStr(l.DoneColumn), l.Enabled, l.CreatedAt, l.UpdatedAt,
		status, nullStr(l.BoardTitle), l.TokenEnc, nullTime(l.TokenExpiresAt), l.EventSequence)
	if err != nil {
		return mapKanbanLinkErr("create kanban link", err)
	}
	return nil
}

func (s *PGStore) GetKanbanLink(ctx context.Context, id string) (*domain.KanbanLink, error) {
	l, err := scanKanbanLink(s.pool.QueryRow(ctx,
		`SELECT `+kanbanLinkCols+` FROM kanban_links WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get kanban link: %w", err)
	}
	return l, nil
}

func (s *PGStore) ListKanbanLinks(ctx context.Context) ([]domain.KanbanLink, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+kanbanLinkCols+` FROM kanban_links ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list kanban links: %w", err)
	}
	defer rows.Close()
	var out []domain.KanbanLink
	for rows.Next() {
		l, err := scanKanbanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *PGStore) ListKanbanLinksByProject(ctx context.Context, projectID string) ([]domain.KanbanLink, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+kanbanLinkCols+` FROM kanban_links WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list kanban links by project: %w", err)
	}
	defer rows.Close()
	var out []domain.KanbanLink
	for rows.Next() {
		l, err := scanKanbanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *PGStore) ListEnabledKanbanLinks(ctx context.Context) ([]domain.KanbanLink, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+kanbanLinkCols+` FROM kanban_links WHERE enabled=TRUE ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list enabled kanban links: %w", err)
	}
	defer rows.Close()
	var out []domain.KanbanLink
	for rows.Next() {
		l, err := scanKanbanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *PGStore) SetKanbanLinkToken(ctx context.Context, id string, tokenEnc []byte, expiresAt *time.Time) error {
	// ONLY token_enc/token_expires_at (+updated_at) change; the binding and its
	// claims are untouched (P2 — a rotation never re-dispatches already-claimed
	// cards). expiresAt nil => token_expires_at NULL (manual paste/clear, D28).
	tag, err := s.pool.Exec(ctx,
		`UPDATE kanban_links SET token_enc=$2, token_expires_at=$3, updated_at=now() WHERE id=$1`,
		id, tokenEnc, nullTime(expiresAt))
	if err != nil {
		return fmt.Errorf("set kanban link token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) SetKanbanLinkBoardStatus(ctx context.Context, id, status, canonicalRef, title string) error {
	// The poller's runtime fail-visible check (D30): on a successful re-validation
	// it canonicalizes board_ref to the board's config id and stores its title;
	// board_ref/board_title are only overwritten when a non-empty value is passed
	// (COALESCE(NULLIF(...))) so an "invalid" transition keeps the last-known ref.
	tag, err := s.pool.Exec(ctx,
		`UPDATE kanban_links
		    SET board_status=$2,
		        board_ref=COALESCE(NULLIF($3,''), board_ref),
		        board_title=COALESCE(NULLIF($4,''), board_title),
		        updated_at=now()
		  WHERE id=$1`,
		id, status, canonicalRef, title)
	if err != nil {
		return mapKanbanLinkErr("set kanban link board status", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) AdvanceKanbanLinkEventSequence(ctx context.Context, id string, sequence int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE kanban_links
		    SET event_sequence=GREATEST(COALESCE(event_sequence, 0), $2),
		        updated_at=now()
		  WHERE id=$1`, id, sequence)
	if err != nil {
		return fmt.Errorf("advance kanban link event sequence: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) DeleteKanbanLink(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM kanban_links WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete kanban link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) EnsureKanbanClaim(ctx context.Context, linkID, documentID, documentPath string) (*domain.KanbanClaim, error) {
	// INSERT … ON CONFLICT DO NOTHING keeps it idempotent; then SELECT the
	// committed row so the caller sees the authoritative run_id (which a racing
	// tick may have stamped between the two statements — that is fine: the caller
	// re-checks run_id and skips).
	var c domain.KanbanClaim
	var storedPath, runID *string
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO kanban_claims (id, link_id, document_id, document_path, claimed_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (link_id, document_id) DO NOTHING`,
		domain.NewID(), linkID, documentID, nullStr(documentPath)); err != nil {
		return nil, fmt.Errorf("ensure kanban claim: %w", err)
	}
	err := s.pool.QueryRow(ctx,
		`SELECT id, link_id, document_id, document_path, run_id,
		        notified_not_configured_at, writeback_at, claimed_at
		 FROM kanban_claims WHERE link_id=$1 AND document_id=$2`,
		linkID, documentID).Scan(
		&c.ID, &c.LinkID, &c.DocumentID, &storedPath, &runID,
		&c.NotifiedNotConfiguredAt, &c.WritebackAt, &c.ClaimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load kanban claim: %w", err)
	}
	if storedPath != nil {
		c.DocumentPath = *storedPath
	}
	if runID != nil {
		c.RunID = *runID
	}
	return &c, nil
}

func (s *PGStore) SetKanbanClaimRun(ctx context.Context, linkID, documentID, runID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE kanban_claims SET run_id=$3
		 WHERE link_id=$1 AND document_id=$2 AND (run_id IS NULL OR run_id='')`,
		linkID, documentID, runID)
	if err != nil {
		return fmt.Errorf("set kanban claim run: %w", err)
	}
	return nil
}

func (s *PGStore) MarkKanbanNotConfiguredNotified(ctx context.Context, linkID, documentID string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE kanban_claims SET notified_not_configured_at=$3
		 WHERE link_id=$1 AND document_id=$2 AND notified_not_configured_at IS NULL`,
		linkID, documentID, at)
	if err != nil {
		return false, fmt.Errorf("mark kanban not-configured notified: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PGStore) ListKanbanRunsAwaitingWriteback(ctx context.Context) ([]KanbanWriteback, error) {
	// Join claims -> links and filter to terminal runs whose writeback_at is NULL.
	// The run is loaded via GetRun per row (the canonical scanRun already packages
	// the runs column list; the writeback set is small and only terminal once, so
	// the extra hit per row is not on a hot path).
	linkSel := prefixCols("l", kanbanLinkCols)
	rows, err := s.pool.Query(ctx,
		`SELECT c.id, c.link_id, c.document_id, c.document_path, c.run_id,
		        c.notified_not_configured_at, c.writeback_at, c.claimed_at,
		        `+linkSel+`
		 FROM kanban_claims c
		 JOIN runs r ON r.id = c.run_id
		 JOIN kanban_links l ON l.id = c.link_id
		 WHERE c.run_id IS NOT NULL AND c.run_id <> ''
		   AND c.writeback_at IS NULL
		   AND r.status IN ('succeeded','failed','canceled')
		 ORDER BY c.claimed_at`)
	if err != nil {
		return nil, fmt.Errorf("list kanban writebacks: %w", err)
	}
	defer rows.Close()
	var out []KanbanWriteback
	for rows.Next() {
		var wb KanbanWriteback
		var claimPath, claimRunID, doneColumn, boardTitle *string
		if err := rows.Scan(
			&wb.Claim.ID, &wb.Claim.LinkID, &wb.Claim.DocumentID, &claimPath, &claimRunID,
			&wb.Claim.NotifiedNotConfiguredAt, &wb.Claim.WritebackAt, &wb.Claim.ClaimedAt,
			&wb.Link.ID, &wb.Link.WorkspaceID, &wb.Link.BoardRef, &wb.Link.ProjectID, &wb.Link.ServiceID,
			&wb.Link.TriggerColumn, &doneColumn, &wb.Link.Enabled, &wb.Link.CreatedAt, &wb.Link.UpdatedAt,
			&wb.Link.BoardStatus, &boardTitle, &wb.Link.TokenEnc, &wb.Link.TokenExpiresAt, &wb.Link.EventSequence,
		); err != nil {
			return nil, fmt.Errorf("scan kanban writeback: %w", err)
		}
		if claimPath != nil {
			wb.Claim.DocumentPath = *claimPath
		}
		if claimRunID != nil {
			wb.Claim.RunID = *claimRunID
		}
		if doneColumn != nil {
			wb.Link.DoneColumn = *doneColumn
		}
		if boardTitle != nil {
			wb.Link.BoardTitle = *boardTitle
		}
		run, err := s.GetRun(ctx, wb.Claim.RunID)
		if err != nil {
			// A run that vanished between the join and the lookup (cascade/edge)
			// is skipped this tick rather than failing the whole pass.
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("load kanban writeback run: %w", err)
		}
		wb.Run = *run
		out = append(out, wb)
	}
	return out, rows.Err()
}

func (s *PGStore) MarkKanbanWriteback(ctx context.Context, linkID, documentID string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE kanban_claims SET writeback_at=$3
		 WHERE link_id=$1 AND document_id=$2 AND writeback_at IS NULL`,
		linkID, documentID, at)
	if err != nil {
		return false, fmt.Errorf("mark kanban writeback: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// --- Schedules (F11 / D24) --------------------------------------------------

const scheduleCols = `id, service_id, cron_expr, prompt, enabled,
	last_fired_at, last_error, created_by, created_at, updated_at`

func scanSchedule(row pgx.Row) (*domain.Schedule, error) {
	var sc domain.Schedule
	var lastFired *time.Time
	var createdBy *string
	err := row.Scan(
		&sc.ID, &sc.ServiceID, &sc.CronExpr, &sc.Prompt, &sc.Enabled,
		&lastFired, &sc.LastError, &createdBy, &sc.CreatedAt, &sc.UpdatedAt)
	if err != nil {
		return nil, err
	}
	sc.LastFiredAt = lastFired
	sc.CreatedBy = createdBy
	return &sc, nil
}

func (s *PGStore) CreateSchedule(ctx context.Context, sc *domain.Schedule) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO schedules (`+scheduleCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		sc.ID, sc.ServiceID, sc.CronExpr, sc.Prompt, sc.Enabled,
		sc.LastFiredAt, sc.LastError, sc.CreatedBy, sc.CreatedAt, sc.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create schedule: %w", err)
	}
	return nil
}

func (s *PGStore) GetSchedule(ctx context.Context, id string) (*domain.Schedule, error) {
	sc, err := scanSchedule(s.pool.QueryRow(ctx,
		`SELECT `+scheduleCols+` FROM schedules WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get schedule: %w", err)
	}
	return sc, nil
}

func (s *PGStore) ListSchedulesByService(ctx context.Context, serviceID string) ([]domain.Schedule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+scheduleCols+` FROM schedules WHERE service_id=$1 ORDER BY created_at DESC`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list schedules by service: %w", err)
	}
	defer rows.Close()
	var out []domain.Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sc)
	}
	return out, rows.Err()
}

func (s *PGStore) ListEnabledSchedules(ctx context.Context) ([]domain.Schedule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+scheduleCols+` FROM schedules WHERE enabled=TRUE ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list enabled schedules: %w", err)
	}
	defer rows.Close()
	var out []domain.Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sc)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateSchedule(ctx context.Context, sc *domain.Schedule, resetWindow bool) error {
	// Only the owner-editable fields change; last_error is poller-owned and
	// intentionally excluded. resetWindow (cron changed / re-enabled) moves
	// last_fired_at to now() ATOMICALLY with the edit — the next fire is computed
	// from the edit instant, so a boundary that predates the edit is never
	// backfilled (C1), and a concurrent poller advance cannot be clobbered with a
	// stale value (the reset never rewinds: it always lands on the current time).
	err := s.pool.QueryRow(ctx,
		`UPDATE schedules SET cron_expr=$2, prompt=$3, enabled=$4,
		        last_fired_at = CASE WHEN $5 THEN now() ELSE last_fired_at END,
		        updated_at=now()
		 WHERE id=$1
		 RETURNING last_fired_at, updated_at`,
		sc.ID, sc.CronExpr, sc.Prompt, sc.Enabled, resetWindow).
		Scan(&sc.LastFiredAt, &sc.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	return nil
}

func (s *PGStore) DeleteSchedule(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM schedules WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) AdvanceSchedule(ctx context.Context, id string, prevFired *time.Time, newFired time.Time, lastErr string) (bool, error) {
	// Conditional claim (anti-double-dispatch): only the instance whose prevFired
	// still matches the row's last_fired_at wins. `IS NOT DISTINCT FROM` treats
	// two NULLs (never-fired) as equal, so the first fire is claimable too.
	tag, err := s.pool.Exec(ctx,
		`UPDATE schedules SET last_fired_at=$3, last_error=$4, updated_at=now()
		 WHERE id=$1 AND last_fired_at IS NOT DISTINCT FROM $2`,
		id, prevFired, newFired, lastErr)
	if err != nil {
		return false, fmt.Errorf("advance schedule: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PGStore) SetScheduleLastError(ctx context.Context, id, lastErr string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE schedules SET last_error=$2, updated_at=now() WHERE id=$1`, id, lastErr)
	if err != nil {
		return fmt.Errorf("set schedule last error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- PR review Automations --------------------------------------------------

const automationCols = `id, service_id, name, instructions, trigger_type, model_id,
	events, base_branch, include_drafts, enabled, last_triggered_at, last_run_id,
	last_error, created_by, created_at, updated_at`

func scanAutomation(row pgx.Row) (*domain.Automation, error) {
	var a domain.Automation
	var events []string
	var createdBy, lastRunID *string
	err := row.Scan(&a.ID, &a.ServiceID, &a.Name, &a.Instructions, &a.TriggerType, &a.ModelID,
		&events, &a.BaseBranch, &a.IncludeDrafts, &a.Enabled, &a.LastTriggeredAt, &lastRunID,
		&a.LastError, &createdBy, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan automation: %w", err)
	}
	a.Events = make([]domain.AutomationEvent, 0, len(events))
	for _, event := range events {
		a.Events = append(a.Events, domain.AutomationEvent(event))
	}
	if lastRunID != nil {
		a.LastRunID = *lastRunID
	}
	a.CreatedBy = createdBy
	return &a, nil
}

func automationEvents(events []domain.AutomationEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, string(event))
	}
	return out
}

func (s *PGStore) CreateAutomation(ctx context.Context, a *domain.Automation) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO automations (`+automationCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		a.ID, a.ServiceID, a.Name, a.Instructions, string(a.TriggerType), a.ModelID,
		automationEvents(a.Events), a.BaseBranch, a.IncludeDrafts, a.Enabled,
		a.LastTriggeredAt, nullStr(a.LastRunID), a.LastError, a.CreatedBy, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create automation: %w", err)
	}
	return nil
}

func (s *PGStore) GetAutomation(ctx context.Context, id string) (*domain.Automation, error) {
	return scanAutomation(s.pool.QueryRow(ctx, `SELECT `+automationCols+` FROM automations WHERE id=$1`, id))
}

func (s *PGStore) ListAutomationsByService(ctx context.Context, serviceID string) ([]domain.Automation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+automationCols+` FROM automations WHERE service_id=$1 ORDER BY created_at DESC`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list automations: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Automation, 0)
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateAutomation(ctx context.Context, a *domain.Automation) error {
	err := s.pool.QueryRow(ctx,
		`UPDATE automations SET name=$2, instructions=$3, model_id=$4, events=$5,
		        base_branch=$6, include_drafts=$7, enabled=$8, updated_at=now()
		 WHERE id=$1 RETURNING updated_at`,
		a.ID, a.Name, a.Instructions, a.ModelID, automationEvents(a.Events),
		a.BaseBranch, a.IncludeDrafts, a.Enabled).Scan(&a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("update automation: %w", err)
	}
	return nil
}

func (s *PGStore) DeleteAutomation(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM automations WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete automation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) RecordAutomationDispatch(ctx context.Context, id string, at time.Time, runID, lastErr string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE automations SET last_triggered_at=$2, last_run_id=$3, last_error=$4, updated_at=now() WHERE id=$1`,
		id, at, nullStr(runID), lastErr)
	if err != nil {
		return fmt.Errorf("record automation dispatch: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const webhookBindingCols = `service_id, provider, endpoint, status, last_synced_at,
	last_delivery_at, last_delivery_status, last_error, updated_at`

func scanWebhookBinding(row pgx.Row) (*domain.WebhookBinding, error) {
	var b domain.WebhookBinding
	err := row.Scan(&b.ServiceID, &b.Provider, &b.Endpoint, &b.Status, &b.LastSyncedAt,
		&b.LastDeliveryAt, &b.LastDeliveryStatus, &b.LastError, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan webhook binding: %w", err)
	}
	return &b, nil
}

func (s *PGStore) UpsertWebhookBinding(ctx context.Context, b *domain.WebhookBinding) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO webhook_bindings (`+webhookBindingCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (service_id) DO UPDATE SET
		   provider=EXCLUDED.provider, endpoint=EXCLUDED.endpoint, status=EXCLUDED.status,
		   last_synced_at=EXCLUDED.last_synced_at, last_error=EXCLUDED.last_error,
		   updated_at=EXCLUDED.updated_at`,
		b.ServiceID, string(b.Provider), b.Endpoint, string(b.Status), b.LastSyncedAt,
		b.LastDeliveryAt, b.LastDeliveryStatus, b.LastError, b.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert webhook binding: %w", err)
	}
	return nil
}

func (s *PGStore) GetWebhookBinding(ctx context.Context, serviceID string) (*domain.WebhookBinding, error) {
	return scanWebhookBinding(s.pool.QueryRow(ctx,
		`SELECT `+webhookBindingCols+` FROM webhook_bindings WHERE service_id=$1`, serviceID))
}

func (s *PGStore) RecordWebhookDelivery(ctx context.Context, serviceID string, at time.Time, status, lastErr string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE webhook_bindings SET last_delivery_at=$2, last_delivery_status=$3,
		        last_error=$4, updated_at=now() WHERE service_id=$1`,
		serviceID, at, status, lastErr)
	if err != nil {
		return fmt.Errorf("record webhook delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- API keys (F12 / D24) ----------------------------------------------------

const apiKeyCols = `id, project_id, name, key_hash, prefix, created_by,
	created_at, last_used_at, revoked_at`

func scanAPIKey(row pgx.Row) (*domain.APIKey, error) {
	var k domain.APIKey
	var createdBy *string
	var lastUsed, revoked *time.Time
	err := row.Scan(
		&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Prefix, &createdBy,
		&k.CreatedAt, &lastUsed, &revoked)
	if err != nil {
		return nil, err
	}
	k.CreatedBy = createdBy
	k.LastUsedAt = lastUsed
	k.RevokedAt = revoked
	return &k, nil
}

func (s *PGStore) CreateAPIKey(ctx context.Context, k *domain.APIKey) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO api_keys (`+apiKeyCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		k.ID, k.ProjectID, k.Name, k.KeyHash, k.Prefix, k.CreatedBy,
		k.CreatedAt, k.LastUsedAt, k.RevokedAt)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	return nil
}

func (s *PGStore) GetAPIKey(ctx context.Context, id string) (*domain.APIKey, error) {
	k, err := scanAPIKey(s.pool.QueryRow(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	return k, nil
}

// GetAPIKeyByHash excludes revoked rows — see the Store interface doc: this
// makes ErrNotFound cover both "unknown hash" and "revoked key" uniformly, and
// makes revocation effective on the very next lookup (no cache to bust).
func (s *PGStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*domain.APIKey, error) {
	k, err := scanAPIKey(s.pool.QueryRow(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys WHERE key_hash=$1 AND revoked_at IS NULL`, keyHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get api key by hash: %w", err)
	}
	return k, nil
}

func (s *PGStore) ListAPIKeysByProject(ctx context.Context, projectID string) ([]domain.APIKey, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list api keys by project: %w", err)
	}
	defer rows.Close()
	var out []domain.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateAPIKeyLastUsed(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=$2 WHERE id=$1`, id, at)
	if err != nil {
		return fmt.Errorf("update api key last used: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAPIKey is intentionally idempotent (see the Store interface doc): a
// second revoke of the same key, or one that races a delete, is a no-op, not
// an error — it never reports RowsAffected()==0 as ErrNotFound.
func (s *PGStore) RevokeAPIKey(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}

// mapKanbanLinkErr translates a kanban_links INSERT error into a typed store
// error (UNIQUE(workspace_id, board_ref) -> ErrAlreadyExists) so the API can
// return a 409 instead of a generic 500. Other errors are wrapped verbatim.
func mapKanbanLinkErr(op string, err error) error {
	if isUniqueViolation(err) {
		return fmt.Errorf("%s: %w", op, ErrAlreadyExists)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isUniqueViolation reports whether err is a Postgres unique-violation (SQLSTATE
// 23505). pgx/v5 returns *pgconn.PgError for server errors.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

var _ Store = (*PGStore)(nil)
