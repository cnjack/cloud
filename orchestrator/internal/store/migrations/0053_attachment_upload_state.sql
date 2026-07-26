-- 0053_attachment_upload_state.sql
--
-- 0049 was deployed before the upload ownership state machine was added to its
-- checked-in definition. Existing databases have already recorded 0049 and do
-- not re-run edited migration text, so add the missing columns append-only.

ALTER TABLE run_attachment_stages
    ADD COLUMN IF NOT EXISTS upload_state TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS uploaded_at TIMESTAMPTZ NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'run_attachment_stages'::regclass
          AND conname = 'run_attachment_stages_upload_state_check'
    ) THEN
        ALTER TABLE run_attachment_stages
            ADD CONSTRAINT run_attachment_stages_upload_state_check
            CHECK (upload_state IN ('pending','uploading','uploaded'));
    END IF;
END $$;
