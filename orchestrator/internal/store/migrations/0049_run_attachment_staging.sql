-- 0049_run_attachment_staging: staged object-store upload intents and immutable
-- run bindings. Object bytes never enter PostgreSQL.
CREATE TABLE IF NOT EXISTS run_attachment_stages (
    id TEXT PRIMARY KEY,
    -- Intentionally no FK cascade: a deleted project/user must not erase the
    -- opaque object key before the reconciler has deleted the S3 object.
    project_id TEXT NOT NULL,
    created_by_user_id TEXT NOT NULL,
    object_key TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    upload_state TEXT NOT NULL DEFAULT 'pending' CHECK (upload_state IN ('pending','uploading','uploaded')),
    uploaded_at TIMESTAMPTZ NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS run_attachment_stages_expiry_idx ON run_attachment_stages(expires_at);

CREATE TABLE IF NOT EXISTS run_attachment_bindings (
    -- No FK cascade for the same object-GC reason. A retry/resume can have
    -- multiple rows for one stage/object; GC deletes only when every run ref
    -- is gone.
    run_id TEXT NOT NULL,
    stage_id TEXT NOT NULL,
    object_key TEXT NOT NULL,
    display_name TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    PRIMARY KEY (run_id, stage_id)
);
CREATE INDEX IF NOT EXISTS run_attachment_bindings_run_idx ON run_attachment_bindings(run_id);
