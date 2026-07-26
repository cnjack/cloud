-- A staged upload is consumed once, but its immutable object binding may be
-- referenced by a retry/resume. The unique identity is (run_id,stage_id), not
-- stage_id globally.
ALTER TABLE run_attachment_bindings DROP CONSTRAINT IF EXISTS run_attachment_bindings_stage_id_key;
