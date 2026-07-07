-- 0009_service_provider_repo_id: rename-proof provider repo identity.
--
-- Services created from the Drone-style repo picker capture the provider's
-- NUMERIC repo id alongside the human "owner/name". owner/name breaks when a
-- repo is renamed/transferred; the numeric id does not — it is also what a
-- future webhook payload match should key on. Nullable: hand-entered and
-- pre-0009 services simply have no id (additive, non-breaking).
ALTER TABLE services
    ADD COLUMN IF NOT EXISTS provider_repo_id BIGINT;
