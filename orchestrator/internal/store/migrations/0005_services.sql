-- 0005_services: introduce the Service layer (multitenant blueprint §1, M1).
--
-- BEFORE: a project carried its repository config directly (repo_url,
-- default_branch, git_mode, provider, provider_url, provider_repo) and a run
-- referenced only its project.
--
-- AFTER: repository config moves to a new `services` table (one row per repo).
-- The simple "one repo = one project" UX is a project with a single service
-- named 'default'. Each run references the service it targets (service_id),
-- keeping project_id as a redundant convenience/compat column.
--
-- This migration is data-preserving and safe to run on a populated database:
--   1. create `services`;
--   2. materialise one 'default' service per existing project, classifying the
--      old repo_url into a provider ("owner/name") or raw (git://, file://,
--      opaque http) service — the same classification the server-side
--      ParseRepoURL implements;
--   3. add runs.service_id / kind / review_output and backfill service_id to the
--      project's default service;
--   4. add the project guardrail columns;
--   5. DROP the now-migrated project repo columns.
--
-- NOTE (M2): projects.owner_user_id and runs.triggered_by_user_id are deferred
-- to M2 — they FK the users table which M2 creates. They are intentionally NOT
-- added here.

-- 1. services table -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS services (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT NOT NULL DEFAULT 'default',
    repo_kind       TEXT NOT NULL CHECK (repo_kind IN ('provider','raw')),
    provider        TEXT CHECK (provider IN ('gitea','github','gitlab')),  -- required when repo_kind='provider'
    repo_owner_name TEXT,                 -- "owner/name"; required when repo_kind='provider'
    raw_repo_url    TEXT,                 -- required when repo_kind='raw'
    default_branch  TEXT NOT NULL DEFAULT 'main',
    git_mode        TEXT NOT NULL DEFAULT 'readonly' CHECK (git_mode IN ('readonly','draft_pr')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX IF NOT EXISTS services_project_idx ON services (project_id);

-- 2. one default service per existing project ---------------------------------
-- IDs are 32-hex like projects/runs (domain.NewID); md5(project_id||…) needs no
-- extension and is unique per project (one default service each). The `spec`
-- subquery classifies repo_url once so git_mode can be gated to readonly when
-- the repo is raw (draft_pr requires a provider repo).
INSERT INTO services
    (id, project_id, name, repo_kind, provider, repo_owner_name, raw_repo_url, default_branch, git_mode, created_at)
SELECT
    md5(spec.id || ':default:service'),
    spec.id,
    'default',
    spec.repo_kind,
    spec.provider,
    spec.repo_owner_name,
    spec.raw_repo_url,
    spec.default_branch,
    CASE WHEN spec.repo_kind = 'raw' THEN 'readonly' ELSE spec.git_mode END,
    spec.created_at
FROM (
    SELECT
        p.id,
        p.created_at,
        COALESCE(NULLIF(p.default_branch, ''), 'main') AS default_branch,
        COALESCE(NULLIF(p.git_mode, ''), 'readonly')   AS git_mode,
        CASE
            WHEN NULLIF(p.provider_repo, '') IS NOT NULL           THEN 'provider'
            WHEN p.repo_url ~* '^https?://[^/]+/[^/]+/[^/]+'       THEN 'provider'
            ELSE 'raw'
        END AS repo_kind,
        CASE
            WHEN NULLIF(p.provider_repo, '') IS NOT NULL           THEN COALESCE(NULLIF(p.provider, ''), 'gitea')
            WHEN p.repo_url ~* '^https?://github\.com/[^/]+/[^/]+' THEN 'github'
            WHEN p.repo_url ~* '^https?://gitlab\.com/[^/]+/[^/]+' THEN 'gitlab'
            WHEN p.repo_url ~* '^https?://[^/]+/[^/]+/[^/]+'       THEN 'gitea'
            ELSE NULL
        END AS provider,
        CASE
            WHEN NULLIF(p.provider_repo, '') IS NOT NULL           THEN p.provider_repo
            WHEN p.repo_url ~* '^https?://[^/]+/[^/]+/[^/]+'
                -- capture "owner/name" (first two path segments), then strip a
                -- trailing ".git". Postgres regex greediness makes a single
                -- non-greedy pattern unreliable, so strip .git separately.
                THEN regexp_replace(
                    (regexp_match(p.repo_url, '^https?://[^/]+/([^/?#]+/[^/?#]+)'))[1],
                    '\.git$', '')
            ELSE NULL
        END AS repo_owner_name,
        CASE
            WHEN NULLIF(p.provider_repo, '') IS NOT NULL           THEN NULL
            WHEN p.repo_url ~* '^https?://[^/]+/[^/]+/[^/]+'       THEN NULL
            ELSE p.repo_url
        END AS raw_repo_url
    FROM projects p
) AS spec;

-- 3. runs: service_id + kind + review_output ----------------------------------
ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS service_id    TEXT REFERENCES services(id),
    ADD COLUMN IF NOT EXISTS kind          TEXT NOT NULL DEFAULT 'agent',
    ADD COLUMN IF NOT EXISTS review_output TEXT NOT NULL DEFAULT '';

-- Backfill every existing run to its project's default service.
UPDATE runs r
SET service_id = s.id
FROM services s
WHERE s.project_id = r.project_id
  AND s.name = 'default'
  AND r.service_id IS NULL;

-- Every project got exactly one default service and every run has a project, so
-- service_id is now populated for all rows and can be made mandatory.
ALTER TABLE runs ALTER COLUMN service_id SET NOT NULL;
ALTER TABLE runs ADD CONSTRAINT runs_kind_check CHECK (kind IN ('agent','review'));

CREATE INDEX IF NOT EXISTS runs_service_idx ON runs (service_id);

-- 4. project guardrails (blueprint §1) ----------------------------------------
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS max_concurrent_runs INT,
    ADD COLUMN IF NOT EXISTS run_timeout_secs    BIGINT,
    ADD COLUMN IF NOT EXISTS provider_allowlist  TEXT[],
    ADD COLUMN IF NOT EXISTS injected_env        JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 5. drop the migrated project repo columns -----------------------------------
ALTER TABLE projects
    DROP COLUMN IF EXISTS repo_url,
    DROP COLUMN IF EXISTS default_branch,
    DROP COLUMN IF EXISTS git_mode,
    DROP COLUMN IF EXISTS provider,
    DROP COLUMN IF EXISTS provider_url,
    DROP COLUMN IF EXISTS provider_repo;
