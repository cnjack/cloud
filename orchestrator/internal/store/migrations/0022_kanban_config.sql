-- 0022_kanban_config: cluster-admin-managed jtype kanban config (D27).
--
-- A single-row table (id is pinned to 1 by a CHECK) holding the cluster-level
-- jtype kanban config a cluster admin sets from the console — modeled on
-- 0010_model_config. It takes precedence over the JTYPE_BASE_URL/JTYPE_TOKEN
-- environment variables (env is retained as a compatibility fallback, D25) —
-- see internal/kanbancfg.Resolver. The optional cluster fallback token is stored
-- ENCRYPTED (AES-256-GCM, AUTH_TOKEN_KEY) in token_enc, exactly like
-- cluster_model_config.api_key_enc; a NULL/empty token_enc means "no cluster
-- fallback token" (links then need their own per-link PAT). The plaintext token
-- is NEVER read back over the API. A missing row means "fall back to env".
CREATE TABLE IF NOT EXISTS cluster_kanban_config (
    id         SMALLINT    PRIMARY KEY CHECK (id = 1),
    base_url   TEXT        NOT NULL,
    token_enc  BYTEA,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT        NOT NULL DEFAULT ''
);
