ALTER TABLE services
  ADD COLUMN IF NOT EXISTS runner_profile text NOT NULL DEFAULT 'default';

DO $$ BEGIN
  ALTER TABLE services
    ADD CONSTRAINT services_runner_profile_format CHECK (
      runner_profile ~ '^[a-z0-9][a-z0-9-]{0,31}$'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
