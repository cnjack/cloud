-- 0018_integrations: project-level git Integration + robot credential (F5 / D19),
-- plus the services.integration_id binding.
--
-- D19: an Integration is a PROJECT-level entity an owner manages. Its credential
-- is an org/group-level SERVICE token (Gitea org PAT / GitLab group token / GitHub
-- PAT), AES-256-GCM sealed with AUTH_TOKEN_KEY (nonce||ciphertext) exactly like the
-- model catalog + kanban link tokens — never readable back in plaintext. A service
-- bound to an integration performs ALL its git operations (clone/push/PR/review) as
-- the integration's BOT identity, never the triggering user's personal OAuth
-- (credentials resolver; the PR body annotates the real trigger for traceability).
--
-- cred_type is an abstraction slot: 'pat' is implemented today; 'github_app' is a
-- future expansion槽, accepted by the CHECK but not wired this cycle.
--
-- bot_username is best-effort discovered from the provider at create time (the
-- token's current user) so the console can show who the bot acts as.
--
-- services.integration_id NULL = legacy service: it keeps the "triggering user's
-- personal OAuth → gitea PAT fallback" path (backward compatible, not forced to
-- migrate). ON DELETE SET NULL so removing an integration never blocks the delete —
-- affected services fall back to the legacy path (and fail-visibly if the repo is
-- private and no personal credential resolves).
--
-- Idempotent: CREATE TABLE / ADD COLUMN IF NOT EXISTS so a re-apply is a no-op.

CREATE TABLE IF NOT EXISTS integrations (
    id           TEXT        PRIMARY KEY,
    project_id   TEXT        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL DEFAULT 'default',
    provider     TEXT        NOT NULL CHECK (provider IN ('gitea','github','gitlab')),
    host         TEXT        NOT NULL,
    cred_type    TEXT        NOT NULL DEFAULT 'pat' CHECK (cred_type IN ('pat','github_app')),
    token_enc    BYTEA       NOT NULL,
    bot_username TEXT        NOT NULL DEFAULT '',
    created_by   TEXT        REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX IF NOT EXISTS integrations_project_idx ON integrations (project_id);

ALTER TABLE services ADD COLUMN IF NOT EXISTS integration_id TEXT
    REFERENCES integrations(id) ON DELETE SET NULL;
