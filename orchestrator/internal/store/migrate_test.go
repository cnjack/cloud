package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cnjack/jcloud/internal/domain"
)

func mustExec(t *testing.T, ctx context.Context, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %.40q: %v", sql, err)
	}
}

func mustScan(t *testing.T, ctx context.Context, c *pgx.Conn, sql string, dest any, args ...any) {
	t.Helper()
	if err := c.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
		t.Fatalf("scan %.40q: %v", sql, err)
	}
}

func mustRow(t *testing.T, ctx context.Context, c *pgx.Conn, sql string, arg any, dests ...any) {
	t.Helper()
	if err := c.QueryRow(ctx, sql, arg).Scan(dests...); err != nil {
		t.Fatalf("row %.40q: %v", sql, err)
	}
}

// TestPGServicesMigrationBackfill is the §6 data-migration regression: a
// pre-0005 database (projects carrying repo_url/git_mode/provider config + runs
// referencing only their project) must, after 0005, have exactly one 'default'
// service per project carrying the classified repo config, every run backfilled
// to its project's default service, and the old project columns dropped.
//
// It runs the real 0005 SQL against a synthetic pre-0005 schema in a throwaway
// Postgres schema, so it needs JCLOUD_PG_DSN and is skipped otherwise.
func TestPGServicesMigrationBackfill(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed migration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Pin a single connection so SET search_path sticks for the whole test.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	c := conn.Conn()

	schema := "mig_" + domain.NewID()[:16]
	if _, err := c.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = c.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })
	if _, err := c.Exec(ctx, `SET search_path TO `+schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	// --- pre-0005 schema (the relevant subset of migrations 0001 + 0004) ------
	if _, err := c.Exec(ctx, `
		CREATE TABLE projects (
			id text PRIMARY KEY, name text NOT NULL,
			repo_url text NOT NULL, default_branch text NOT NULL DEFAULT 'main',
			created_at timestamptz NOT NULL DEFAULT now(),
			git_mode text NOT NULL DEFAULT 'readonly',
			provider text NOT NULL DEFAULT '',
			provider_url text NOT NULL DEFAULT '',
			provider_repo text NOT NULL DEFAULT ''
		);
		CREATE TABLE runs (
			id text PRIMARY KEY,
			project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			prompt text NOT NULL DEFAULT '', status text NOT NULL DEFAULT 'queued',
			created_at timestamptz NOT NULL DEFAULT now()
		);`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	// --- legacy data ----------------------------------------------------------
	// A: raw git:// readonly project (the smoke/j1 shape).
	// B: draft_pr gitea provider project (the j4 shape).
	// C: github readonly project (host classification).
	mustExec(t, ctx, c, `INSERT INTO projects (id,name,repo_url,default_branch,git_mode,provider,provider_url,provider_repo) VALUES
		('pA','A','git://git.jcloud.svc/seed.git','main','readonly','','',''),
		('pB','B','http://gitea.jcloud.svc:3000/jcloud/seed.git','main','draft_pr','gitea','http://gitea.jcloud.svc:3000','jcloud/seed'),
		('pC','C','https://github.com/acme/app.git','trunk','readonly','','','')`)
	mustExec(t, ctx, c, `INSERT INTO runs (id,project_id) VALUES ('rA','pA'),('rB','pB'),('rC','pC')`)

	// --- apply the real 0005 migration ---------------------------------------
	sql, err := migrationsFS.ReadFile("migrations/0005_services.sql")
	if err != nil {
		t.Fatalf("read 0005: %v", err)
	}
	if _, err := c.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply 0005: %v", err)
	}

	// --- assert services ------------------------------------------------------
	type svcRow struct {
		kind, provider, owner, raw, branch, gitMode string
		count                                       int
	}
	getSvc := func(projectID string) svcRow {
		var r svcRow
		var provider, owner, raw *string
		err := c.QueryRow(ctx, `SELECT count(*) OVER (), repo_kind, provider, repo_owner_name, raw_repo_url, default_branch, git_mode
			FROM services WHERE project_id=$1 AND name='default'`, projectID).
			Scan(&r.count, &r.kind, &provider, &owner, &raw, &r.branch, &r.gitMode)
		if err != nil {
			t.Fatalf("get service for %s: %v", projectID, err)
		}
		if provider != nil {
			r.provider = *provider
		}
		if owner != nil {
			r.owner = *owner
		}
		if raw != nil {
			r.raw = *raw
		}
		return r
	}

	a := getSvc("pA")
	if a.kind != "raw" || a.raw != "git://git.jcloud.svc/seed.git" || a.gitMode != "readonly" {
		t.Fatalf("A default service wrong: %+v", a)
	}
	b := getSvc("pB")
	if b.kind != "provider" || b.provider != "gitea" || b.owner != "jcloud/seed" || b.gitMode != "draft_pr" {
		t.Fatalf("B default service wrong: %+v", b)
	}
	c2 := getSvc("pC")
	if c2.kind != "provider" || c2.provider != "github" || c2.owner != "acme/app" || c2.branch != "trunk" || c2.gitMode != "readonly" {
		t.Fatalf("C default service wrong: %+v", c2)
	}

	// Exactly one service per project.
	var svcCount int
	mustScan(t, ctx, c, `SELECT count(*) FROM services`, &svcCount)
	if svcCount != 3 {
		t.Fatalf("services count = %d want 3", svcCount)
	}

	// --- assert run backfill --------------------------------------------------
	for _, tc := range []struct{ runID, projectID string }{{"rA", "pA"}, {"rB", "pB"}, {"rC", "pC"}} {
		var serviceID, kind string
		mustRow(t, ctx, c, `SELECT service_id, kind FROM runs WHERE id=$1`, tc.runID, &serviceID, &kind)
		var wantSvc string
		mustRow(t, ctx, c, `SELECT id FROM services WHERE project_id=$1 AND name='default'`, tc.projectID, &wantSvc)
		if serviceID != wantSvc {
			t.Fatalf("run %s service_id=%q want %q (backfill)", tc.runID, serviceID, wantSvc)
		}
		if kind != "agent" {
			t.Fatalf("run %s kind=%q want agent", tc.runID, kind)
		}
	}

	// --- assert dropped columns ----------------------------------------------
	var repoURLExists bool
	mustScan(t, ctx, c, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema=$1 AND table_name='projects' AND column_name='repo_url')`, &repoURLExists, schema)
	if repoURLExists {
		t.Fatal("projects.repo_url should have been dropped by 0005")
	}
	var injectedExists bool
	mustScan(t, ctx, c, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema=$1 AND table_name='projects' AND column_name='injected_env')`, &injectedExists, schema)
	if !injectedExists {
		t.Fatal("projects.injected_env guardrail column should exist after 0005")
	}
}
