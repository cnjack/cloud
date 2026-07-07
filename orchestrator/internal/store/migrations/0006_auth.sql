-- 0006_auth: identity, session and membership schema for M2 (multitenant
-- blueprint §1/§2). Adds users / user_identities / sessions / project_members and
-- back-fills the FK columns migration 0005 deferred (projects.owner_user_id,
-- runs.triggered_by_user_id).
--
-- RULING (follows M1): the blueprint DDL types ids as uuid, but this codebase's
-- convention (projects/runs/services, domain.NewID) is a TEXT 32-hex id. We keep
-- TEXT here so every table shares one id shape and the FKs line up without a
-- uuid<->text cast. access_token_enc/refresh_token_enc are BYTEA (AES-256-GCM
-- ciphertext, key = env AUTH_TOKEN_KEY); the plaintext token never lands here.

-- 1. users --------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
    id               TEXT PRIMARY KEY,
    display_name     TEXT NOT NULL,
    avatar_url       TEXT NOT NULL DEFAULT '',
    is_cluster_admin BOOLEAN NOT NULL DEFAULT false,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2. user_identities ----------------------------------------------------------
-- One row per (provider account) linked to a user. A single user may link
-- several providers (the /auth/link flow). UNIQUE(provider, provider_uid) is the
-- login lookup key and the guard the link flow uses to reject an identity that
-- already belongs to someone else.
CREATE TABLE IF NOT EXISTS user_identities (
    id                TEXT PRIMARY KEY,
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider          TEXT NOT NULL CHECK (provider IN ('gitea','github','gitlab')),
    provider_uid      TEXT NOT NULL,
    username          TEXT NOT NULL,
    access_token_enc  BYTEA NOT NULL,       -- AES-256-GCM ciphertext (nonce||ct)
    refresh_token_enc BYTEA,                -- nullable: provider may not issue one
    token_expires_at  TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_uid)
);

CREATE INDEX IF NOT EXISTS user_identities_user_idx ON user_identities (user_id);

-- 3. sessions -----------------------------------------------------------------
-- Opaque browser session tokens. Only the sha256 hash is stored (same convention
-- as runs.token_hash). A session is valid iff revoked_at IS NULL AND
-- expires_at > now().
CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,       -- default 30d (config SESSION_TTL)
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);

-- 4. project_members ----------------------------------------------------------
CREATE TABLE IF NOT EXISTS project_members (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('owner','member','viewer')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX IF NOT EXISTS project_members_user_idx ON project_members (user_id);

-- 5. FK columns deferred from 0005 --------------------------------------------
-- owner_user_id: the project's creator/owner (NULL for a project a service
-- principal — CONSOLE_TOKEN — created). triggered_by_user_id: the user who
-- triggered a run (NULL for a service-principal/legacy run; M3 falls back to the
-- global GITEA_TOKEN when it is NULL).
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(id);

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS triggered_by_user_id TEXT REFERENCES users(id);
