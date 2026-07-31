-- 0062_usage_ledger: normalized model usage, immutable pricing revisions, and
-- UTC hourly rollups. Snapshot columns deliberately have no cascading foreign
-- keys: usage remains explainable after Model/Card/Automation deletion.

CREATE TABLE IF NOT EXISTS model_pricing_revisions (
    id                              TEXT PRIMARY KEY,
    model_resource_id               TEXT NOT NULL,
    provider_id                     TEXT NOT NULL DEFAULT '',
    provider_name                   TEXT NOT NULL DEFAULT '',
    model_name                      TEXT NOT NULL,
    currency                        TEXT NOT NULL,
    input_micros_per_million        BIGINT,
    output_micros_per_million       BIGINT,
    cache_read_micros_per_million   BIGINT,
    cache_write_micros_per_million  BIGINT,
    effective_at                    TIMESTAMPTZ NOT NULL,
    created_by                      TEXT NOT NULL DEFAULT '',
    created_at                      TIMESTAMPTZ NOT NULL,
    CHECK (currency ~ '^[A-Z]{3,8}$'),
    CHECK (input_micros_per_million IS NULL OR input_micros_per_million >= 0),
    CHECK (output_micros_per_million IS NULL OR output_micros_per_million >= 0),
    CHECK (cache_read_micros_per_million IS NULL OR cache_read_micros_per_million >= 0),
    CHECK (cache_write_micros_per_million IS NULL OR cache_write_micros_per_million >= 0)
);

CREATE INDEX IF NOT EXISTS model_pricing_revisions_effective_idx
    ON model_pricing_revisions (
        model_resource_id, effective_at DESC, created_at DESC, id DESC
    );

CREATE TABLE IF NOT EXISTS usage_events (
    id                      TEXT PRIMARY KEY,
    request_id              TEXT NOT NULL UNIQUE,
    subject_kind            TEXT NOT NULL CHECK (subject_kind IN ('run','device')),
    subject_id              TEXT NOT NULL,
    run_id                  TEXT NOT NULL DEFAULT '',
    project_id              TEXT NOT NULL DEFAULT '',
    project_name            TEXT NOT NULL DEFAULT '',
    service_id              TEXT NOT NULL DEFAULT '',
    service_name            TEXT NOT NULL DEFAULT '',
    automation_id           TEXT NOT NULL DEFAULT '',
    automation_name         TEXT NOT NULL DEFAULT '',
    card_workspace          TEXT NOT NULL DEFAULT '',
    card_document_id        TEXT NOT NULL DEFAULT '',
    card_path               TEXT NOT NULL DEFAULT '',
    accountable_user_id     TEXT NOT NULL DEFAULT '',
    accountable_label       TEXT NOT NULL DEFAULT '',
    user_id                 TEXT NOT NULL DEFAULT '',
    device_id               TEXT NOT NULL DEFAULT '',
    device_name             TEXT NOT NULL DEFAULT '',
    grant_scope             TEXT NOT NULL DEFAULT '',
    grant_scope_id          TEXT NOT NULL DEFAULT '',
    grant_scope_name        TEXT NOT NULL DEFAULT '',
    provider_id             TEXT NOT NULL DEFAULT '',
    provider_kind           TEXT NOT NULL DEFAULT '',
    provider_name           TEXT NOT NULL DEFAULT '',
    model_id                TEXT NOT NULL DEFAULT '',
    model_name              TEXT NOT NULL DEFAULT '',
    input_tokens            BIGINT,
    output_tokens           BIGINT,
    cache_read_tokens       BIGINT,
    cache_write_tokens      BIGINT,
    reported_cost_micros    BIGINT,
    reported_currency       TEXT NOT NULL DEFAULT '',
    pricing_revision_id     TEXT NOT NULL DEFAULT '',
    estimated_cost_micros   BIGINT,
    estimated_currency      TEXT NOT NULL DEFAULT '',
    uncosted_input_tokens    BIGINT NOT NULL DEFAULT 0,
    uncosted_output_tokens   BIGINT NOT NULL DEFAULT 0,
    uncosted_cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    uncosted_cache_write_tokens BIGINT NOT NULL DEFAULT 0,
    capture_status          TEXT NOT NULL CHECK (capture_status IN (
                                'reported','partial','unavailable','parse_error')),
    error_category          TEXT NOT NULL DEFAULT '',
    occurred_at             TIMESTAMPTZ NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL,
    replacement_of          TEXT NOT NULL DEFAULT '',
    version                 INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (input_tokens IS NULL OR input_tokens >= 0),
    CHECK (output_tokens IS NULL OR output_tokens >= 0),
    CHECK (cache_read_tokens IS NULL OR cache_read_tokens >= 0),
    CHECK (cache_write_tokens IS NULL OR cache_write_tokens >= 0),
    CHECK (reported_cost_micros IS NULL OR reported_cost_micros >= 0),
    CHECK (estimated_cost_micros IS NULL OR estimated_cost_micros >= 0)
);

CREATE INDEX IF NOT EXISTS usage_events_run_idx
    ON usage_events (run_id, occurred_at);
CREATE INDEX IF NOT EXISTS usage_events_project_idx
    ON usage_events (project_id, occurred_at);
CREATE INDEX IF NOT EXISTS usage_events_device_idx
    ON usage_events (user_id, device_id, occurred_at);
CREATE INDEX IF NOT EXISTS usage_events_retention_idx
    ON usage_events (occurred_at);

-- Keep the idempotency fence for as long as the corresponding rollup survives,
-- even after the raw observation is removed at the shorter retention boundary.
CREATE TABLE IF NOT EXISTS usage_request_receipts (
    request_id  TEXT PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL
);

INSERT INTO usage_request_receipts (request_id, occurred_at)
SELECT request_id, occurred_at FROM usage_events
ON CONFLICT (request_id) DO NOTHING;

CREATE INDEX IF NOT EXISTS usage_request_receipts_retention_idx
    ON usage_request_receipts (occurred_at);

CREATE TABLE IF NOT EXISTS usage_hourly_rollups (
    bucket_at                TIMESTAMPTZ NOT NULL,
    subject_kind             TEXT NOT NULL CHECK (subject_kind IN ('run','device')),
    subject_id               TEXT NOT NULL,
    run_id                   TEXT NOT NULL DEFAULT '',
    project_id               TEXT NOT NULL DEFAULT '',
    project_name             TEXT NOT NULL DEFAULT '',
    service_id               TEXT NOT NULL DEFAULT '',
    service_name             TEXT NOT NULL DEFAULT '',
    automation_id            TEXT NOT NULL DEFAULT '',
    automation_name          TEXT NOT NULL DEFAULT '',
    card_workspace           TEXT NOT NULL DEFAULT '',
    card_document_id         TEXT NOT NULL DEFAULT '',
    card_path                TEXT NOT NULL DEFAULT '',
    accountable_user_id      TEXT NOT NULL DEFAULT '',
    accountable_label        TEXT NOT NULL DEFAULT '',
    user_id                  TEXT NOT NULL DEFAULT '',
    device_id                TEXT NOT NULL DEFAULT '',
    device_name              TEXT NOT NULL DEFAULT '',
    grant_scope              TEXT NOT NULL DEFAULT '',
    grant_scope_id           TEXT NOT NULL DEFAULT '',
    grant_scope_name         TEXT NOT NULL DEFAULT '',
    provider_id              TEXT NOT NULL DEFAULT '',
    provider_kind            TEXT NOT NULL DEFAULT '',
    provider_name            TEXT NOT NULL DEFAULT '',
    model_id                 TEXT NOT NULL DEFAULT '',
    model_name               TEXT NOT NULL DEFAULT '',
    reported_currency        TEXT NOT NULL DEFAULT '',
    pricing_revision_id      TEXT NOT NULL DEFAULT '',
    estimated_currency       TEXT NOT NULL DEFAULT '',
    requests                 BIGINT NOT NULL DEFAULT 0,
    reported_count           BIGINT NOT NULL DEFAULT 0,
    partial_count            BIGINT NOT NULL DEFAULT 0,
    unavailable_count        BIGINT NOT NULL DEFAULT 0,
    parse_error_count        BIGINT NOT NULL DEFAULT 0,
    input_tokens             BIGINT,
    output_tokens            BIGINT,
    cache_read_tokens        BIGINT,
    cache_write_tokens       BIGINT,
    reported_cost_micros     BIGINT,
    estimated_cost_micros    BIGINT,
    uncosted_input_tokens     BIGINT NOT NULL DEFAULT 0,
    uncosted_output_tokens    BIGINT NOT NULL DEFAULT 0,
    uncosted_cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    uncosted_cache_write_tokens BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (
        bucket_at, subject_kind, subject_id, run_id, project_id, service_id,
        automation_id, card_workspace, card_document_id, card_path,
        accountable_user_id, user_id, device_id, grant_scope, grant_scope_id,
        provider_id, model_id, reported_currency, pricing_revision_id,
        estimated_currency
    )
);

CREATE INDEX IF NOT EXISTS usage_hourly_rollups_project_idx
    ON usage_hourly_rollups (project_id, bucket_at);
CREATE INDEX IF NOT EXISTS usage_hourly_rollups_device_idx
    ON usage_hourly_rollups (user_id, device_id, bucket_at);
