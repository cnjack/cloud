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

func (s *PGStore) CreateProject(ctx context.Context, p *domain.Project) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO projects (id, name, repo_url, default_branch, created_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		p.ID, p.Name, p.RepoURL, p.DefaultBranch, p.CreatedAt)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	return nil
}

func (s *PGStore) GetProject(ctx context.Context, id string) (*domain.Project, error) {
	var p domain.Project
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, repo_url, default_branch, created_at FROM projects WHERE id=$1`, id).
		Scan(&p.ID, &p.Name, &p.RepoURL, &p.DefaultBranch, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return &p, nil
}

func (s *PGStore) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, repo_url, default_branch, created_at FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var out []domain.Project
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.RepoURL, &p.DefaultBranch, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateProject(ctx context.Context, p *domain.Project) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE projects SET name=$2, repo_url=$3, default_branch=$4 WHERE id=$1`,
		p.ID, p.Name, p.RepoURL, p.DefaultBranch)
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

// --- Runs -------------------------------------------------------------------

const runCols = `id, project_id, prompt, status, phase, error, k8s_job_name,
	retried_from, failure_reason, failure_message, attempt, token_hash,
	created_at, started_at, finished_at`

func scanRun(row pgx.Row) (*domain.Run, error) {
	var r domain.Run
	err := row.Scan(&r.ID, &r.ProjectID, &r.Prompt, &r.Status, &r.Phase, &r.Error,
		&r.K8sJobName, &r.RetriedFrom, &r.FailureReason, &r.FailureMessage,
		&r.Attempt, &r.TokenHash,
		&r.CreatedAt, &r.StartedAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}
	return &r, nil
}

func (s *PGStore) CreateRun(ctx context.Context, r *domain.Run) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO runs (`+runCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		r.ID, r.ProjectID, r.Prompt, r.Status, r.Phase, r.Error, r.K8sJobName,
		r.RetriedFrom, r.FailureReason, r.FailureMessage, r.Attempt, r.TokenHash,
		r.CreatedAt, r.StartedAt, r.FinishedAt)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	return nil
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

// UpdateRun persists a run, enforcing the state-machine transition against the
// currently-stored status inside a transaction (compare-and-set). This prevents
// two reconcile passes (or a cancel racing a reconcile) from making conflicting
// illegal transitions.
func (s *PGStore) UpdateRun(ctx context.Context, r *domain.Run) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update run: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var current domain.RunStatus
	err = tx.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1 FOR UPDATE`, r.ID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock run: %w", err)
	}
	if !domain.CanTransition(current, r.Status) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current, r.Status)
	}

	_, err = tx.Exec(ctx,
		`UPDATE runs SET status=$2, phase=$3, error=$4, k8s_job_name=$5,
		    failure_reason=$6, failure_message=$7, attempt=$8, token_hash=$9,
		    started_at=$10, finished_at=$11
		 WHERE id=$1`,
		r.ID, r.Status, r.Phase, r.Error, r.K8sJobName,
		r.FailureReason, r.FailureMessage, r.Attempt, r.TokenHash,
		r.StartedAt, r.FinishedAt)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	return tx.Commit(ctx)
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
