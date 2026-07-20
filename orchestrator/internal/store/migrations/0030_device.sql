-- 0030_device: jcode device relay namespace (docs/17-jcode-device-relay §5).
--
-- A local jcode install logs in over the RFC 8628 device-code flow and becomes
-- a first-class `device` under a user — the namespace the relay (P2+) hangs
-- sessions, events, commands and pairings off. Ids follow the rest of the
-- schema (TEXT, domain.NewID), NOT the uuid shown in the docs §5 sketch.
--
-- devices.pubkey is NOT NULL DEFAULT '' rather than plain NOT NULL: the device
-- row is created when the device token is issued (POST /auth/device/token),
-- but the X25519 public key only arrives with the first
-- POST /internal/v1/device/register — the '' placeholder is replaced there.
--
-- Idempotent: every CREATE TABLE / INDEX uses IF NOT EXISTS so a re-apply is a
-- clean no-op.

-- A device = one local jcode installation. last_seen_at drives the online
-- signal (a heartbeat every 30s; >90s without one means offline, docs/17 §4.1).
-- revoked_at kills the device: token resolution joins devices and excludes
-- revoked rows, so revocation is effective on the very next lookup.
CREATE TABLE IF NOT EXISTS devices (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    hostname      TEXT,
    jcode_version TEXT,
    pubkey        TEXT NOT NULL DEFAULT '',
    key_gen       INT  NOT NULL DEFAULT 1,
    last_seen_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS devices_user_idx ON devices (user_id);

-- Device bearer tokens (principal kind=device, docs/17 §3.2). Only the
-- SHA-256 hash is persisted — the plaintext is returned exactly once, at
-- issuance; there is no read-back path (same discipline as api_keys/sessions).
CREATE TABLE IF NOT EXISTS device_tokens (
    id         TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS device_tokens_device_idx ON device_tokens (device_id);

-- Session metadata mirror (E2EE ciphertext payload; status is the plaintext
-- routing state for list UIs). Used from P2.
CREATE TABLE IF NOT EXISTS device_sessions (
    device_id  TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    meta       BYTEA,
    status     TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, session_id)
);

-- Append-only durable event log per session; (device_id, session_id, seq) is
-- the idempotency key (a redelivered seq is skipped). kind stays plaintext so
-- the server can route/render the envelope skeleton. Used from P2.
CREATE TABLE IF NOT EXISTS device_events (
    device_id  TEXT NOT NULL,
    session_id TEXT NOT NULL,
    seq        BIGINT NOT NULL,
    kind       TEXT NOT NULL,
    envelope   BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, session_id, seq)
);

-- Downlink command queue (orchestrator → device, long-polled). Used from P2.
CREATE TABLE IF NOT EXISTS device_commands (
    id         TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    session_id TEXT,
    envelope   BYTEA NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending',
    result     BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    acked_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS device_commands_device_status_idx ON device_commands (device_id, status);

-- Client pairing requests (CEK distribution, docs/17 §6.3). Used from P3.
CREATE TABLE IF NOT EXISTS device_pairings (
    id               TEXT PRIMARY KEY,
    device_id        TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    requester_label  TEXT NOT NULL,
    requester_pubkey TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending',
    wrapped_cek      BYTEA,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS device_pairings_device_idx ON device_pairings (device_id);
