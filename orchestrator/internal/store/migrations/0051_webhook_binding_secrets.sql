-- 0051_webhook_binding_secrets: isolate self-hosted SCM webhook credentials per
-- Service binding and make body-authenticated GitHub/Gitea deliveries resistant
-- to replay with a forged delivery-id header.

ALTER TABLE webhook_bindings
    ADD COLUMN IF NOT EXISTS hook_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS secret_enc BYTEA;

CREATE UNIQUE INDEX IF NOT EXISTS webhook_bindings_hook_id_uq
    ON webhook_bindings(hook_id)
    WHERE hook_id <> '';

-- Existing GitLab/Gitea hooks used a cluster-wide Provider secret and a shared
-- URL. They must never be accepted as active after this release. Preserve the
-- old endpoint so the first explicit reconciliation can remove it before
-- installing a fresh per-binding hook.
UPDATE webhook_bindings
SET status = 'error',
    last_error = 'SCM webhook security upgrade requires Plugin reconnect and Automation reconciliation',
    hook_id = '',
    secret_enc = NULL,
    updated_at = now()
WHERE provider IN ('gitlab','gitea');

ALTER TABLE webhook_bindings
    DROP CONSTRAINT IF EXISTS webhook_bindings_active_secret_check;
ALTER TABLE webhook_bindings
    ADD CONSTRAINT webhook_bindings_active_secret_check CHECK (
        provider = 'github'
        OR status <> 'active'
        OR (hook_id <> '' AND secret_enc IS NOT NULL AND octet_length(secret_enc) > 0)
    );

-- Surface the migration boundary instead of silently leaving an enabled
-- Installation whose Provider hook can no longer authenticate.
UPDATE plugin_installations pi
SET status = 'action_required',
    last_health_error = 'SCM webhook security upgrade requires reconnecting this Plugin and resaving its Automations',
    updated_at = now()
WHERE pi.provider IN ('gitlab','gitea')
  AND EXISTS (
      SELECT 1
      FROM service_repository_bindings rb
      JOIN webhook_bindings wb ON wb.service_id = rb.service_id
      WHERE rb.installation_id = pi.id
  );

ALTER TABLE webhook_receipts
    ADD COLUMN IF NOT EXISTS payload_digest TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS webhook_receipts_authenticated_payload_uq
    ON webhook_receipts(provider, payload_digest)
    WHERE provider IN ('github','gitea') AND payload_digest <> '';
