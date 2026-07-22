-- 0038_account_settings: account-wide portable preferences, encrypted by the
-- account CEK before upload. The orchestrator stores only the opaque envelope.
CREATE TABLE IF NOT EXISTS account_settings (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    version    BIGINT NOT NULL,
    envelope   BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
