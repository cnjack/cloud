-- 0033_device_pairing_offers: QR scan-to-pair tickets (docs/17 §6.3 — M11).
--
-- A device mints a short-lived, single-use offer (POST
-- /internal/v1/device/pairing-offers) and renders {offer_id, secret} into its
-- pairing QR. The scanning client claims it (POST
-- /api/v1/pairing-offers/{id}/claim) with the secret + its P-256 pubkey; the
-- claim creates the ordinary device_pairings row and the pairing.request
-- command carries the offer_id so the device can auto-approve its own offer.
--
-- Only the SHA-256 hash of the secret is persisted — the plaintext lives in
-- the QR and is returned exactly once at creation (same discipline as
-- device_tokens). claimed_by/claimed_at make the offer single-use: the
-- conditional claim update wins exactly once.
--
-- Idempotent: CREATE TABLE / INDEX IF NOT EXISTS makes a re-apply a no-op.
CREATE TABLE IF NOT EXISTS device_pairing_offers (
    id          TEXT PRIMARY KEY,
    device_id   TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    secret_hash TEXT NOT NULL,
    claimed_by  TEXT REFERENCES users(id) ON DELETE SET NULL,
    claimed_at  TIMESTAMPTZ,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS device_pairing_offers_device_idx ON device_pairing_offers (device_id);
