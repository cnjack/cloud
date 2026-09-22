-- Personal providers are private to an account and never grantable to projects.
ALTER TABLE model_providers ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(id) ON DELETE CASCADE;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='model_providers_owner_scope_check') THEN
  ALTER TABLE model_providers ADD CONSTRAINT model_providers_owner_scope_check CHECK (owner_user_id IS NULL OR project_id IS NULL);
 END IF;
END $$;
DROP INDEX IF EXISTS model_providers_scope_name_idx;
CREATE UNIQUE INDEX IF NOT EXISTS model_providers_owner_scope_name_idx
 ON model_providers (COALESCE(owner_user_id,''), COALESCE(project_id,''), name);
CREATE INDEX IF NOT EXISTS model_providers_owner_idx ON model_providers(owner_user_id);
