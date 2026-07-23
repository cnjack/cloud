package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestPRReviewAutomationMigrationUsesModelCatalog(t *testing.T) {
	sql, err := migrationsFS.ReadFile("migrations/0026_pr_review_automations.sql")
	if err != nil {
		t.Fatalf("read 0026: %v", err)
	}
	migration := string(sql)
	if strings.Contains(migration, "REFERENCES models(") {
		t.Fatal("0026 references the nonexistent models table; use model_configs")
	}
	if !strings.Contains(migration, "REFERENCES model_configs(id)") {
		t.Fatal("0026 must reference the model_configs catalog")
	}
}

func TestModelProvidersMigrationContract(t *testing.T) {
	sql, err := migrationsFS.ReadFile("migrations/0027_model_providers.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(sql)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS model_providers",
		"provider_id",
		"model_id",
		"context_window",
		"supports_reasoning",
		"supports_tools",
		"supports_image",
		"model_source",
		"ON DELETE CASCADE",
		"INSERT INTO model_providers",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("0027 migration missing %q", required)
		}
	}
}

func TestKanbanEventCursorMigrationContract(t *testing.T) {
	sql, err := migrationsFS.ReadFile("migrations/0028_kanban_event_cursor.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(sql)
	for _, required := range []string{
		"ALTER TABLE kanban_links",
		"ADD COLUMN IF NOT EXISTS event_sequence BIGINT",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("0028 migration missing %q", required)
		}
	}
	if strings.Contains(migration, "event_sequence BIGINT NOT NULL") {
		t.Fatal("event_sequence must be nullable so existing links run the compatibility bootstrap")
	}
}

func TestProjectModelManagementMigrationContract(t *testing.T) {
	sql, err := migrationsFS.ReadFile("migrations/0029_project_model_management.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(sql)
	// Idempotent, project-scoped shape (M1). Every column/index is guarded so a
	// re-apply is a clean no-op; the global UNIQUE(name) becomes scope-aware.
	for _, required := range []string{
		"ALTER TABLE model_providers ADD COLUMN IF NOT EXISTS project_id TEXT",
		"ALTER TABLE model_providers ADD COLUMN IF NOT EXISTS headers_enc BYTEA",
		"ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS project_id TEXT",
		"ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT true",
		"ON DELETE CASCADE",
		"model_providers_name_key",
		"model_configs_name_key",
		"CREATE UNIQUE INDEX IF NOT EXISTS model_providers_scope_name_idx",
		"CREATE UNIQUE INDEX IF NOT EXISTS model_configs_scope_name_idx",
		"COALESCE(project_id, '')",
		"CREATE INDEX IF NOT EXISTS model_providers_project_idx",
		"CREATE INDEX IF NOT EXISTS model_configs_project_idx",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("0029 migration missing %q", required)
		}
	}
	// The scope-name uniqueness must be enabled, not NOT NULL (cluster rows stay
	// project_id NULL); a bare NOT NULL on project_id would break cluster catalog.
	if strings.Contains(migration, "project_id TEXT NOT NULL") {
		t.Fatal("project_id must be nullable so cluster-global rows keep project_id NULL")
	}
}

func TestDesktopConfigMeshMigrationContract(t *testing.T) {
	sql, err := migrationsFS.ReadFile("migrations/0039_desktop_config_mesh.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(sql)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS account_sync_keys",
		"CREATE TABLE IF NOT EXISTS account_sync_key_wraps",
		"CREATE TABLE IF NOT EXISTS account_provider_configs",
		"ON DELETE CASCADE",
		"CHECK (status IN ('pending','approved','denied'))",
		"PRIMARY KEY (user_id, provider_id)",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("0039 migration missing %q", required)
		}
	}
}

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

// TestPGKanbanLinkTokenMigration is the F6 / D25 migration check: 0017 adds a
// nullable token_enc column to kanban_links, and re-applying it is a clean no-op
// (ADD COLUMN IF NOT EXISTS). Runs against a synthetic pre-0017 kanban_links in a
// throwaway schema; needs JCLOUD_PG_DSN and is skipped otherwise.
func TestPGKanbanLinkTokenMigration(t *testing.T) {
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

	// Minimal pre-0017 kanban_links (the subset 0017 touches — no token_enc yet).
	if _, err := c.Exec(ctx, `
		CREATE TABLE kanban_links (
			id text PRIMARY KEY, workspace_id text NOT NULL, board_ref text NOT NULL,
			project_id text NOT NULL, service_id text NOT NULL, trigger_column text NOT NULL,
			done_column text, enabled boolean NOT NULL DEFAULT true,
			created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
		);`); err != nil {
		t.Fatalf("create legacy kanban_links: %v", err)
	}
	mustExec(t, ctx, c, `INSERT INTO kanban_links (id,workspace_id,board_ref,project_id,service_id,trigger_column)
		VALUES ('k1','ws','b','p','s','ai')`)

	sql, err := migrationsFS.ReadFile("migrations/0017_kanban_link_token.sql")
	if err != nil {
		t.Fatalf("read 0017: %v", err)
	}
	// Apply TWICE — the second application must be a clean no-op (idempotent).
	for i := 0; i < 2; i++ {
		if _, err := c.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply 0017 (pass %d): %v", i+1, err)
		}
	}

	// token_enc now exists, is bytea, and is nullable (the pre-existing row is NULL).
	var dataType, nullable string
	mustRow(t, ctx, c, `SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_schema=$1 AND table_name='kanban_links' AND column_name='token_enc'`,
		schema, &dataType, &nullable)
	if dataType != "bytea" || nullable != "YES" {
		t.Fatalf("token_enc column type=%q nullable=%q want bytea/YES", dataType, nullable)
	}
	var enc []byte
	mustScan(t, ctx, c, `SELECT token_enc FROM kanban_links WHERE id='k1'`, &enc)
	if enc != nil {
		t.Fatalf("pre-existing link token_enc should be NULL, got %v", enc)
	}
}

// TestPGKanbanConfigMigration is the D27 migration check: 0022 creates the
// single-row cluster_kanban_config table (id pinned to 1, nullable token_enc) and
// re-applying the full set is a clean no-op. Runs against real Postgres; needs
// JCLOUD_PG_DSN and is skipped otherwise.
func TestPGKanbanConfigMigration(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed migration test")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Re-apply: a clean no-op (schema_migrations gate + CREATE TABLE IF NOT EXISTS).
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("second migrate should be a no-op, got: %v", err)
	}

	// token_enc is bytea + nullable; id is a smallint pinned to 1 by a CHECK.
	var dataType, nullable string
	if err := st.Pool().QueryRow(ctx,
		`SELECT data_type, is_nullable FROM information_schema.columns
		 WHERE table_name='cluster_kanban_config' AND column_name='token_enc'`).
		Scan(&dataType, &nullable); err != nil {
		t.Fatal(err)
	}
	if dataType != "bytea" || nullable != "YES" {
		t.Fatalf("token_enc type=%q nullable=%q want bytea/YES", dataType, nullable)
	}
	// The id CHECK (id = 1) rejects any other id.
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO cluster_kanban_config (id, base_url) VALUES (2, 'http://x')`); err == nil {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cluster_kanban_config WHERE id=2`)
		t.Fatal("id=2 should violate the CHECK (id = 1)")
	}
}

// TestPGKanbanTokenExpiryMigration is the D28 migration check: 0023 adds a
// nullable token_expires_at TIMESTAMPTZ to BOTH cluster_kanban_config and
// kanban_links, and re-applying the full set is a clean no-op. Runs against real
// Postgres; needs JCLOUD_PG_DSN and is skipped otherwise.
func TestPGKanbanTokenExpiryMigration(t *testing.T) {
	dsn := os.Getenv("JCLOUD_PG_DSN")
	if dsn == "" {
		t.Skip("JCLOUD_PG_DSN not set; skipping Postgres-backed migration test")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Re-apply: a clean no-op (schema_migrations gate + ADD COLUMN IF NOT EXISTS).
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("second migrate should be a no-op, got: %v", err)
	}

	// Both columns are timestamptz + nullable.
	for _, table := range []string{"cluster_kanban_config", "kanban_links"} {
		var dataType, nullable string
		if err := st.Pool().QueryRow(ctx,
			`SELECT data_type, is_nullable FROM information_schema.columns
			 WHERE table_name=$1 AND column_name='token_expires_at'`, table).
			Scan(&dataType, &nullable); err != nil {
			t.Fatalf("%s.token_expires_at: %v", table, err)
		}
		if dataType != "timestamp with time zone" || nullable != "YES" {
			t.Fatalf("%s.token_expires_at type=%q nullable=%q want timestamptz/YES", table, dataType, nullable)
		}
	}
}

// TestPGModelCatalogMigrationBackfill is the D21 §6 data migration: a pre-0013
// database with a single cluster_model_config row + N projects must, after 0013,
// have that config as the catalog's first entry GRANTED to every project, the old
// table dropped, and services/runs carrying the new nullable FK columns. It also
// asserts idempotency: re-applying the raw 0013 SQL is a clean no-op.
func TestPGModelCatalogMigrationBackfill(t *testing.T) {
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

	// --- minimal pre-0013 schema (the subset 0013 touches) --------------------
	if _, err := c.Exec(ctx, `
		CREATE TABLE projects (id text PRIMARY KEY, name text NOT NULL);
		CREATE TABLE services (id text PRIMARY KEY, project_id text NOT NULL REFERENCES projects(id));
		CREATE TABLE runs (id text PRIMARY KEY, project_id text NOT NULL REFERENCES projects(id));
		CREATE TABLE cluster_model_config (
			id smallint PRIMARY KEY CHECK (id = 1),
			base_url text NOT NULL, model_name text NOT NULL, api_key_enc bytea,
			updated_at timestamptz NOT NULL DEFAULT now(), updated_by text NOT NULL DEFAULT ''
		);`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	mustExec(t, ctx, c, `INSERT INTO projects (id,name) VALUES ('pA','A'),('pB','B')`)
	mustExec(t, ctx, c, `INSERT INTO services (id,project_id) VALUES ('sA','pA')`)
	mustExec(t, ctx, c, `INSERT INTO runs (id,project_id) VALUES ('rA','pA')`)
	mustExec(t, ctx, c, `INSERT INTO cluster_model_config (id,base_url,model_name,api_key_enc,updated_by)
		VALUES (1,'https://api.openai.com/v1','openai/gpt-4o','\x00ff'::bytea,'admin')`)

	sql, err := migrationsFS.ReadFile("migrations/0013_model_catalog.sql")
	if err != nil {
		t.Fatalf("read 0013: %v", err)
	}
	// Apply TWICE — the second application must be a clean no-op (idempotent).
	for i := 0; i < 2; i++ {
		if _, err := c.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply 0013 (pass %d): %v", i+1, err)
		}
	}

	// The single config became the catalog's first entry, key bytes preserved.
	var modelCount int
	mustScan(t, ctx, c, `SELECT count(*) FROM model_configs`, &modelCount)
	if modelCount != 1 {
		t.Fatalf("model_configs count=%d want 1 (idempotent, not duplicated)", modelCount)
	}
	var name, base, model string
	var enc []byte
	mustRow(t, ctx, c, `SELECT name, base_url, model_name, api_key_enc FROM model_configs WHERE model_name=$1`,
		"openai/gpt-4o", &name, &base, &model, &enc)
	if name != "openai/gpt-4o" || base != "https://api.openai.com/v1" || len(enc) != 2 || enc[1] != 0xff {
		t.Fatalf("migrated model wrong: name=%q base=%q enc=%v", name, base, enc)
	}

	// Granted to EVERY existing project.
	var grantCount int
	mustScan(t, ctx, c, `SELECT count(*) FROM model_grants`, &grantCount)
	if grantCount != 2 {
		t.Fatalf("model_grants count=%d want 2 (one per project)", grantCount)
	}

	// Old table dropped; services/runs carry the new FK columns.
	var oldExists, svcCol, runCol bool
	mustScan(t, ctx, c, `SELECT EXISTS (SELECT 1 FROM information_schema.tables
		WHERE table_schema=$1 AND table_name='cluster_model_config')`, &oldExists, schema)
	if oldExists {
		t.Fatal("cluster_model_config should have been dropped by 0013")
	}
	mustScan(t, ctx, c, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema=$1 AND table_name='services' AND column_name='default_model_id')`, &svcCol, schema)
	mustScan(t, ctx, c, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema=$1 AND table_name='runs' AND column_name='model_id')`, &runCol, schema)
	if !svcCol || !runCol {
		t.Fatalf("new FK columns missing: services.default_model_id=%v runs.model_id=%v", svcCol, runCol)
	}
}
