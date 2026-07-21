-- 0031_device_events_fk.sql — referential integrity for the device event log.
--
-- 0030 left device_events without any foreign key, so deleting a user (or a
-- device) cascaded everywhere EXCEPT the event log, orphaning rows. Clean up
-- any orphans that already exist, then add the composite FK to
-- device_sessions (which itself cascades from devices → users).

-- Remove orphans whose parent session row is already gone.
DELETE FROM device_events e
WHERE NOT EXISTS (
    SELECT 1 FROM device_sessions s
    WHERE s.device_id = e.device_id AND s.session_id = e.session_id
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'device_events_session_fk'
    ) THEN
        ALTER TABLE device_events
            ADD CONSTRAINT device_events_session_fk
            FOREIGN KEY (device_id, session_id)
            REFERENCES device_sessions (device_id, session_id)
            ON DELETE CASCADE;
    END IF;
END $$;
