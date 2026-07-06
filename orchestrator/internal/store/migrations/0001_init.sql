-- 0001_init: core schema for projects, runs, run_events, run_artifacts.
-- Applied once, tracked in schema_migrations. Idempotent via IF NOT EXISTS so a
-- crash mid-apply is safe to retry.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version     BIGINT PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS projects (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    repo_url       TEXT NOT NULL,
    default_branch TEXT NOT NULL DEFAULT 'main',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS runs (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    prompt        TEXT NOT NULL,
    status        TEXT NOT NULL,
    phase         TEXT NOT NULL DEFAULT '',
    error         TEXT NOT NULL DEFAULT '',
    k8s_job_name  TEXT NOT NULL DEFAULT '',
    retried_from  TEXT REFERENCES runs(id) ON DELETE SET NULL,
    failure_reason  TEXT NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    attempt       INT NOT NULL DEFAULT 1,
    token_hash    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS runs_project_idx  ON runs (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS runs_status_idx   ON runs (status);
-- token_hash lookups for the runner-ingest auth path.
CREATE INDEX IF NOT EXISTS runs_token_idx    ON runs (token_hash) WHERE token_hash <> '';

CREATE TABLE IF NOT EXISTS run_events (
    id       BIGSERIAL PRIMARY KEY,
    run_id   TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq      BIGINT NOT NULL,
    ts       TIMESTAMPTZ NOT NULL DEFAULT now(),
    type     TEXT NOT NULL,
    payload  JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (run_id, seq)
);

CREATE INDEX IF NOT EXISTS run_events_stream_idx ON run_events (run_id, seq);

CREATE TABLE IF NOT EXISTS run_artifacts (
    run_id     TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, kind)
);
