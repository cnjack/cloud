-- 0071_device_artifact_shares.sql
--
-- Immutable, end-to-end encrypted Artifact snapshots uploaded by an
-- authenticated jcode device. Cloud stores routing/lifecycle data and
-- ciphertext only; the decryption key remains in the URL fragment.

CREATE TABLE IF NOT EXISTS device_artifact_shares (
    id                      TEXT PRIMARY KEY,
    -- Keep lifecycle rows until their object generations have been erased.
    -- Account deletion must revoke/GC shares first instead of orphaning blobs.
    user_id                 TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    device_id               TEXT REFERENCES devices(id) ON DELETE SET NULL,
    artifact_id             TEXT NOT NULL,
    revision                INT NOT NULL CHECK (revision > 0),
    protocol                TEXT NOT NULL CHECK (protocol = 'jcode-artifact-share-v1'),
    state                   TEXT NOT NULL DEFAULT 'pending'
                                CHECK (state IN ('pending','uploading','uploaded','complete','revoked')),
    object_key              TEXT UNIQUE,
    upload_generation       INT NOT NULL DEFAULT 0 CHECK (upload_generation BETWEEN 0 AND 3),
    upload_claim_id         TEXT,
    ciphertext_size         BIGINT NOT NULL CHECK (ciphertext_size >= 28),
    ciphertext_sha256       TEXT,
    encrypted_metadata      JSONB,
    intent_expires_at       TIMESTAMPTZ NOT NULL,
    upload_claimed_at       TIMESTAMPTZ,
    upload_lease_expires_at TIMESTAMPTZ,
    expires_at              TIMESTAMPTZ NOT NULL,
    uploaded_at             TIMESTAMPTZ,
    completed_at            TIMESTAMPTZ,
    revoked_at              TIMESTAMPTZ,
    object_deleted_at       TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS device_artifact_shares_user_artifact_idx
    ON device_artifact_shares (user_id, artifact_id, created_at DESC);
CREATE INDEX IF NOT EXISTS device_artifact_shares_gc_idx
    ON device_artifact_shares (object_deleted_at, revoked_at, intent_expires_at, expires_at);
