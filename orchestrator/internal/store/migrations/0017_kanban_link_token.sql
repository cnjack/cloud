-- 0017_kanban_link_token: per-link jtype credential (Feature F6 / D25).
--
-- D25 downshifts kanban link management from cluster-admin to the project OWNER
-- and moves the jtype credential from a single cluster env (JTYPE_TOKEN) to a
-- per-link encrypted PAT. This column carries that per-link token, AES-256-GCM
-- sealed with the same AUTH_TOKEN_KEY the model catalog uses (nonce||ciphertext,
-- never readable back in plaintext).
--
-- NULL token_enc = fall back to the cluster JTYPE_TOKEN env (backward compatible
-- with links created before F6, and with per-link-less deployments). The poller
-- and writeback resolve per-link: token_enc present -> decrypt & use; else the
-- cluster fallback; neither -> the link is skipped fail-visibly (never a silent
-- run with an empty credential).
--
-- Idempotent: ADD COLUMN IF NOT EXISTS so a re-apply is a no-op.
ALTER TABLE kanban_links
    ADD COLUMN IF NOT EXISTS token_enc BYTEA;
