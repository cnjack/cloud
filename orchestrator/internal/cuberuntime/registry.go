package cuberuntime

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Job struct {
	Name, ServiceID, RunID, SandboxID, CheckpointKey string
	CheckpointSaved                                  bool
	CreatedAt                                        time.Time
	TimeoutSeconds                                   int64
	CleanedAt                                        *time.Time
}

// Registry persists resource ownership independently of the API process.
// No run environment or provider access token is ever stored here.
type Registry interface {
	Lock(context.Context, string) (func(), error)
	Job(context.Context, string) (*Job, error)
	SaveJob(context.Context, *Job) error
	PendingJobs(context.Context, string) ([]Job, error)
	AllJobs(context.Context, string) ([]Job, error)
	Workspace(context.Context, string) (string, bool, error)
	SaveWorkspace(context.Context, string, string) error
	DeleteWorkspace(context.Context, string) error
}

type PGRegistry struct {
	pool  *pgxpool.Pool
	owner string
}

func NewPGRegistry(pool *pgxpool.Pool, owner string) *PGRegistry {
	return &PGRegistry{pool: pool, owner: owner}
}

func (r *PGRegistry) Lock(ctx context.Context, name string) (func(), error) {
	c, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = c.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, r.owner+":"+name); err != nil {
		c.Release()
		return nil, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, r.owner+":"+name); err != nil {
			_ = c.Conn().Close(ctx)
		}
		c.Release()
	}, nil
}

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.Name, &j.ServiceID, &j.RunID, &j.SandboxID, &j.CheckpointKey, &j.CheckpointSaved, &j.CreatedAt, &j.TimeoutSeconds, &j.CleanedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &j, err
}

const jobCols = `job_name,COALESCE(service_id,''),COALESCE(run_id,''),sandbox_id,checkpoint_key,checkpoint_saved,created_at,timeout_seconds,cleaned_at`

func (r *PGRegistry) Job(ctx context.Context, name string) (*Job, error) {
	return scanJob(r.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM cube_runtime_jobs WHERE owner=$1 AND job_name=$2`, r.owner, name))
}
func (r *PGRegistry) SaveJob(ctx context.Context, j *Job) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO cube_runtime_jobs(owner,job_name,service_id,run_id,sandbox_id,checkpoint_key,checkpoint_saved,created_at,cleaned_at,timeout_seconds)
 VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10) ON CONFLICT(owner,job_name) DO UPDATE SET sandbox_id=excluded.sandbox_id,checkpoint_key=excluded.checkpoint_key,checkpoint_saved=excluded.checkpoint_saved,cleaned_at=excluded.cleaned_at`, r.owner, j.Name, j.ServiceID, j.RunID, j.SandboxID, j.CheckpointKey, j.CheckpointSaved, j.CreatedAt, j.CleanedAt, j.TimeoutSeconds)
	return err
}
func (r *PGRegistry) PendingJobs(ctx context.Context, service string) ([]Job, error) {
	return r.jobs(ctx, service, true)
}
func (r *PGRegistry) AllJobs(ctx context.Context, service string) ([]Job, error) {
	return r.jobs(ctx, service, false)
}
func (r *PGRegistry) jobs(ctx context.Context, service string, pending bool) ([]Job, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+jobCols+` FROM cube_runtime_jobs WHERE owner=$1 AND service_id=$2 AND (NOT $3 OR cleaned_at IS NULL)`, r.owner, service, pending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *j)
	}
	return jobs, rows.Err()
}
func (r *PGRegistry) Workspace(ctx context.Context, service string) (string, bool, error) {
	var key string
	err := r.pool.QueryRow(ctx, `SELECT checkpoint_key FROM cube_runtime_workspaces WHERE owner=$1 AND service_id=$2`, r.owner, service).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return key, err == nil, err
}
func (r *PGRegistry) SaveWorkspace(ctx context.Context, service, key string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO cube_runtime_workspaces(owner,service_id,checkpoint_key) VALUES($1,$2,$3) ON CONFLICT(owner,service_id) DO UPDATE SET checkpoint_key=excluded.checkpoint_key,updated_at=now()`, r.owner, service, key)
	return err
}
func (r *PGRegistry) DeleteWorkspace(ctx context.Context, service string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM cube_runtime_workspaces WHERE owner=$1 AND service_id=$2`, r.owner, service)
	return err
}
