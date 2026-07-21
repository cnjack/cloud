-- 0036_device_fingerprint: stable machine fingerprint for login dedup (M16).
--
-- The jcode CLI derives a stable machine fingerprint (macOS IOPlatformUUID,
-- Linux /etc/machine-id, Windows MachineGuid, or a persisted random fallback)
-- and sends ONLY its sha256 hex: on the device-code token poll (so a re-login
-- from the same machine reuses the existing devices row instead of stacking a
-- new one) and on /internal/v1/device/register (backfill for rows minted
-- without one). The raw hardware id never leaves the machine.
--
-- The partial unique index enforces the dedup invariant: at most one
-- NON-REVOKED device per (user_id, fingerprint_hash). Rows without a
-- fingerprint (pre-M16) and revoked (deleted) rows stay outside the index, so
-- deleting a device and re-logging in from the same machine mints a fresh row.
--
-- Idempotent: ADD COLUMN / CREATE INDEX IF NOT EXISTS make a re-apply a no-op.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS fingerprint_hash TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS devices_user_fingerprint_idx
    ON devices (user_id, fingerprint_hash)
    WHERE fingerprint_hash IS NOT NULL AND revoked_at IS NULL;
