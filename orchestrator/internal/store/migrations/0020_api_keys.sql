-- 0020_api_keys: project-scoped, revocable API keys (F12 / D24).
--
-- Replaces borrowing the cluster-wide CONSOLE_TOKEN for external/CI
-- automation (D24 — rejected: a leaked CONSOLE_TOKEN reaches every project and
-- cannot be revoked individually). A key resolves to a principal capped at
-- RoleMember on EXACTLY its own project — see api/principal.go effectiveRole
-- and docs/11-api.md § "Project-scoped API keys".
--
-- Credential discipline (CLAUDE.md fail-visible / D24): key_hash is
-- sha256(plaintext) hex, the SAME one-way hashing already used for
-- sessions.token_hash / runs.token_hash — never reversible, never re-readable.
-- The plaintext is generated and returned exactly once, at creation (see
-- api/apikeys.go handleCreateAPIKey); this table stores no secret in the
-- clear. prefix is the plaintext's first few characters (e.g. "jck_a1b2"),
-- retained ONLY for the owner's list view to tell keys apart — never
-- sufficient to authenticate.
--
-- Idempotent: CREATE TABLE / INDEX IF NOT EXISTS so a re-apply is a no-op.

CREATE TABLE IF NOT EXISTS api_keys (
    id            TEXT        PRIMARY KEY,
    project_id    TEXT        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name          TEXT        NOT NULL,
    key_hash      TEXT        NOT NULL,
    prefix        TEXT        NOT NULL,
    created_by    TEXT        REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ
);

-- Principal resolution hot path (api/principal.go): a Bearer token's sha256 is
-- looked up here on every request bearing a "jck_"-prefixed token, so it must
-- be a unique index, not just an index — two keys can never share a hash
-- (astronomically unlikely with 32 random bytes, but the constraint is free
-- insurance and doubles as the idempotency guard for a retried create).
CREATE UNIQUE INDEX IF NOT EXISTS api_keys_key_hash_idx ON api_keys (key_hash);

-- Owner management view: list a project's keys, newest first.
CREATE INDEX IF NOT EXISTS api_keys_project_idx ON api_keys (project_id);
