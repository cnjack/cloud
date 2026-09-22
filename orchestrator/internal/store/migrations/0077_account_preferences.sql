-- User-owned client/task defaults. No credentials or model entitlements here.
ALTER TABLE users ADD COLUMN IF NOT EXISTS preferences JSONB NOT NULL DEFAULT '{}'::jsonb;
