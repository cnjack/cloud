-- 0010_model_config: admin-managed cluster LLM configuration (Feature A).
--
-- A single-row table (id is pinned to 1 by a CHECK) holding the effective model
-- config a cluster admin sets from the console. It takes precedence over the
-- MODEL_* environment variables — see internal/modelcfg.Resolve. The API key is
-- stored ENCRYPTED (AES-256-GCM, AUTH_TOKEN_KEY) in api_key_enc; a NULL/empty
-- api_key_enc means "no key" (some OpenAI-compatible endpoints are unauthed).
-- The plaintext key is NEVER read back over the API.
CREATE TABLE IF NOT EXISTS cluster_model_config (
    id          SMALLINT     PRIMARY KEY CHECK (id = 1),
    base_url    TEXT         NOT NULL,
    model_name  TEXT         NOT NULL,
    api_key_enc BYTEA,
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_by  TEXT         NOT NULL DEFAULT ''
);
