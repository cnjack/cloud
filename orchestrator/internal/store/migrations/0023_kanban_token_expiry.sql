-- 0023_kanban_token_expiry: record the expiry of a jtype credential minted by the
-- "Connect with jtype" OAuth device flow (D28).
--
-- The device-flow token is a 90-day scoped session (create_scoped_session,
-- scope=mcp) with NO refresh token, so its wall-clock expiry is knowable at mint
-- time. token_expires_at stores it so the console can proactively surface
-- "expires in N days / expired — reconnect" instead of discovering a dead token
-- on the next poll tick. Both columns are NULLABLE: NULL means "unknown expiry"
-- — a hand-pasted PAT (D25/D27), a JTYPE_* env token, or no token at all. Only a
-- successful device connect populates it. Additive + idempotent (ADD COLUMN IF
-- NOT EXISTS), so re-applying the full migration set is a clean no-op.
ALTER TABLE cluster_kanban_config ADD COLUMN IF NOT EXISTS token_expires_at TIMESTAMPTZ;
ALTER TABLE kanban_links          ADD COLUMN IF NOT EXISTS token_expires_at TIMESTAMPTZ;
