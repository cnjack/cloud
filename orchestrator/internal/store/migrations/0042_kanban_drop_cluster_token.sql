-- 0042_kanban_drop_cluster_token: remove the cluster-level jtype fallback token
-- (D36).
--
-- D36 moves kanban credentials entirely to per-link tokens (kanban_links.token_enc,
-- D25) and slims the cluster-level config to JUST the jtype base URL — an
-- infrastructure fact with no secret material. cluster_kanban_config therefore
-- drops token_enc (0022) and token_expires_at (0023). Any previously stored
-- cluster fallback token is DISCARDED here; links that relied on it surface
-- fail-visibly as credential_status "missing" until their owner sets a per-link
-- token (paste or "Connect with jtype") — never a silent failure.
--
-- Idempotent (DROP COLUMN IF EXISTS), so re-applying the full migration set is
-- a clean no-op. kanban_links.token_enc / token_expires_at (the per-link
-- credentials) are NOT touched.
ALTER TABLE cluster_kanban_config DROP COLUMN IF EXISTS token_enc;
ALTER TABLE cluster_kanban_config DROP COLUMN IF EXISTS token_expires_at;
