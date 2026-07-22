-- 0037_service_deletion: make destructive service removal retry-safe.
--
-- The API stamps deleting_at before it starts terminating Jobs/PVCs/object
-- storage.  While the marker is set no new run may be dispatched.  If an
-- external cleanup fails, the service remains visible and a later DELETE can
-- safely resume instead of leaving an untracked runtime resource behind.

ALTER TABLE services
    ADD COLUMN IF NOT EXISTS deleting_at TIMESTAMPTZ;

