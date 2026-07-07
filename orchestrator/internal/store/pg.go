package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
	max_concurrent_runs, run_timeout_secs, provider_allowlist, injected_env, owner_user_id`

func scanProject(row pgx.Row) (*domain.Project, error) {
	var p domain.Project
	var ownerUserID *string
	err := row.Scan(&p.ID, &p.Name, &p.CreatedAt,
		&p.MaxConcurrentRuns, &p.RunTimeoutSecs, &p.ProviderAllowlist, &p.InjectedEnv, &ownerUserID)
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
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.ID, p.Name, p.CreatedAt,
		p.MaxConcurrentRuns, p.RunTimeoutSecs, p.ProviderAllowlist, env, nullStr(p.OwnerUserID))
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
		    provider_allowlist=$5, injected_env=$6
		 WHERE id=$1`,
		p.ID, p.Name, p.MaxConcurrentRuns, p.RunTimeoutSecs, p.ProviderAllowlist, env)
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
	raw_repo_url, default_branch, git_mode, created_at`

// nullStr maps an empty Go string to a SQL NULL so nullable columns (provider,
// repo_owner_name, raw_repo_url) stay NULL rather than ” — the services CHECK
// constraint on provider only permits NULL or an enum value, not ”.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func scanService(row pgx.Row) (*domain.Service, error) {
	var s domain.Service
	var provider, ownerName, rawURL *string
	err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.RepoKind,
		&provider, &ownerName, &rawURL, &s.DefaultBranch, &s.GitMode, &s.CreatedAt)
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
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		svc.ID, svc.ProjectID, svc.Name, string(svc.RepoKind),
		nullStr(string(svc.Provider)), nullStr(svc.RepoOwnerName), nullStr(svc.RawRepoURL),
		svc.DefaultBranch, string(svc.GitMode), svc.CreatedAt)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	return nil
}

func (s *PGStore) GetService(ctx context.Context, id string) (*domain.Service, error) {
	return scanService(s.pool.QueryRow(ctx,
		`SELECT `+serviceCols+` FROM services WHERE id=$1`, id))
}

func (s *PGStore) ListServices(ctx context.Context, projectID string) ([]domain.Service, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+serviceCols+` FROM services WHERE project_id=$1 ORDER BY created_at ASC`, projectID)
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
		`SELECT `+serviceCols+` FROM services WHERE project_id=$1 AND name='default'`, projectID))
}

func (s *PGStore) UpdateService(ctx context.Context, svc *domain.Service) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE services SET name=$2, repo_kind=$3, provider=$4, repo_owner_name=$5,
		    raw_repo_url=$6, default_branch=$7, git_mode=$8
		 WHERE id=$1`,
		svc.ID, svc.Name, string(svc.RepoKind), nullStr(string(svc.Provider)),
		nullStr(svc.RepoOwnerName), nullStr(svc.RawRepoURL), svc.DefaultBranch, string(svc.GitMode))
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

// --- Runs -------------------------------------------------------------------

const runCols = `id, project_id, service_id, prompt, status, kind, phase, error, k8s_job_name,
	retried_from, failure_reason, failure_message, attempt, token_hash,
	created_at, started_at, finished_at, job_cleaned_at,
	git_branch, commit_sha, pr_url, pr_number, review_output, triggered_by_user_id,
	review_posted_at, pr_head_branch, pr_base_branch`

func scanRun(row pgx.Row) (*domain.Run, error) {
	var r domain.Run
	err := row.Scan(&r.ID, &r.ProjectID, &r.ServiceID, &r.Prompt, &r.Status, &r.Kind, &r.Phase, &r.Error,
		&r.K8sJobName, &r.RetriedFrom, &r.FailureReason, &r.FailureMessage,
		&r.Attempt, &r.TokenHash,
		&r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.JobCleanedAt,
		&r.GitBranch, &r.CommitSHA, &r.PRURL, &r.PRNumber, &r.ReviewOutput, &r.TriggeredByUserID,
		&r.ReviewPostedAt, &r.PRHeadBranch, &r.PRBaseBranch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}
	return &r, nil
}

func (s *PGStore) CreateRun(ctx context.Context, r *domain.Run) error {
	if r.Kind == "" {
		r.Kind = domain.RunKindAgent
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO runs (`+runCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`,
		r.ID, r.ProjectID, r.ServiceID, r.Prompt, r.Status, string(r.Kind), r.Phase, r.Error, r.K8sJobName,
		r.RetriedFrom, r.FailureReason, r.FailureMessage, r.Attempt, r.TokenHash,
		r.CreatedAt, r.StartedAt, r.FinishedAt, r.JobCleanedAt,
		r.GitBranch, r.CommitSHA, r.PRURL, r.PRNumber, r.ReviewOutput, r.TriggeredByUserID,
		r.ReviewPostedAt, r.PRHeadBranch, r.PRBaseBranch)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	return nil
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

// ListRunsAwaitingPR returns succeeded agent runs with a recorded branch (set
// when their bundle was received) but no PR yet. The mode/provider gate is
// applied by the reconciler after joining the service.
func (s *PGStore) ListRunsAwaitingPR(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runCols+` FROM runs
		 WHERE status=$1 AND kind='agent' AND git_branch <> '' AND pr_url = ''
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
// (run_id, kind). content stays '' (the payload is in content_bytes).
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

var _ Store = (*PGStore)(nil)
