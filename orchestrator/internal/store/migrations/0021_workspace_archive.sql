-- 0021_workspace_archive: object-storage archive layer for persistent
-- workspaces (F10 / D23 ③).
--
-- Vision (docs/14 §4, D23): a persistent-workspace service (Feature C / D05)
-- whose per-service RWO PVC has sat idle for a long time (ARCHIVE_IDLE_DAYS) is
-- tarred to object storage (S3/MinIO) and its PVC deleted; the next run restores
-- the tarball back into a fresh PVC before it starts. The authoritative session
-- transcript already lives in the control-plane store (D12/D13) — the PVC and
-- its archive are only a working copy / cold backup, so archiving loses nothing
-- authoritative.
--
--   archived_at  when the PVC was tarred to object storage and deleted. NULL =
--                the service is not archived (PVC live, or persistent workspace
--                off). Set by the reconciler's archive pass, cleared by the
--                restore path when a new run wakes the service.
--   archive_key  the object key of the tarball (e.g. workspaces/<serviceID>.tar.zst).
--                NULL when not archived. Kept alongside archived_at so restore
--                knows exactly what to fetch even though the key is otherwise
--                deterministic.
--
-- Both columns are nullable and default-NULL, so this is a non-breaking add: an
-- existing service is simply "not archived". Idempotent (ADD COLUMN IF NOT
-- EXISTS), so a re-apply is a no-op.
--
-- A partial index over archived services keeps the archive/restore scans cheap
-- (the common case — no archived services — never touches it).

ALTER TABLE services
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS archive_key TEXT;

CREATE INDEX IF NOT EXISTS services_archived_idx
    ON services (archived_at)
    WHERE archived_at IS NOT NULL;
