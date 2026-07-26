-- 0052_plugin_secret_version_gc.sql
--
-- Terminal run snapshots are immutable audit records. They retain provider,
-- revision, installation and credential-version identifiers, but must not keep
-- historical ciphertext forever. Active runs are protected by the GC query in
-- the Store; removing these two FKs permits terminal-only version rows to be
-- reclaimed without deleting the snapshot itself.

ALTER TABLE run_plugin_snapshots
    DROP CONSTRAINT IF EXISTS run_plugin_snapshots_provider_version_fk,
    DROP CONSTRAINT IF EXISTS run_plugin_snapshots_credential_version_fk;
