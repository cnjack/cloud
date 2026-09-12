-- Cube runtime identities and verified workspace checkpoints never contain credentials.
CREATE TABLE IF NOT EXISTS cube_runtime_jobs (
    owner TEXT NOT NULL,
    job_name TEXT NOT NULL,
    service_id TEXT REFERENCES services(id) ON DELETE CASCADE,
    run_id TEXT REFERENCES runs(id) ON DELETE CASCADE,
    sandbox_id TEXT NOT NULL DEFAULT '',
    checkpoint_key TEXT NOT NULL DEFAULT '',
    checkpoint_saved BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    cleaned_at TIMESTAMPTZ,
    PRIMARY KEY (owner, job_name)
);
CREATE INDEX IF NOT EXISTS cube_runtime_jobs_service_pending
    ON cube_runtime_jobs(owner, service_id) WHERE cleaned_at IS NULL;

CREATE TABLE IF NOT EXISTS cube_runtime_workspaces (
    owner TEXT NOT NULL,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    checkpoint_key TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner, service_id)
);
