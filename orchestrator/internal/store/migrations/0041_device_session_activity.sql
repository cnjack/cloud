-- 0041_device_session_activity: preserve local session activity separately
-- from the cloud mirror refresh timestamp.
--
-- NULL is intentional for legacy connectors. It must not be filled from
-- updated_at, because that value records mirror delivery rather than activity.

ALTER TABLE device_sessions
    ADD COLUMN IF NOT EXISTS last_activity_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS device_sessions_activity_idx
    ON device_sessions (device_id, last_activity_at DESC NULLS LAST, session_id);
