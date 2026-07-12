-- 0025_integration_oauth: project integrations may be authorized through a
-- provider OAuth app as an alternative to pasting a personal access token.
-- The resulting access token remains AES-GCM sealed in token_enc.

ALTER TABLE integrations DROP CONSTRAINT IF EXISTS integrations_cred_type_check;
ALTER TABLE integrations ADD CONSTRAINT integrations_cred_type_check
    CHECK (cred_type IN ('pat', 'oauth', 'github_app'));
