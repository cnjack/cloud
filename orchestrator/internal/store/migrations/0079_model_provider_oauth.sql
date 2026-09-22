-- OAuth secrets and pending device codes are opaque AES-GCM ciphertext.
ALTER TABLE model_providers DROP CONSTRAINT IF EXISTS model_providers_auth_type_check;
ALTER TABLE model_providers ADD CONSTRAINT model_providers_auth_type_check
  CHECK (auth_type IN ('api_key', 'service_identity', 'none', 'oauth'));
CREATE TABLE IF NOT EXISTS model_provider_oauth (
  provider_id TEXT PRIMARY KEY REFERENCES model_providers(id) ON DELETE CASCADE,
  state_enc BYTEA NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
