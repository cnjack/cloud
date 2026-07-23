-- 0040_account_model_grants: direct Cluster-model entitlements for a Cloud
-- account, independent from Project membership.
--
-- Every authenticated, non-revoked Desktop device owned by the account inherits
-- the grant. Only cluster-global models are grantable; that scope rule is
-- enforced by the API and store while the FK guarantees model/account lifetime
-- cleanup. The optional granting user is retained for audit and becomes NULL if
-- that administrator is later removed. Service-principal grants carry NULL.

CREATE TABLE IF NOT EXISTS model_account_grants (
    model_id   TEXT NOT NULL REFERENCES model_configs(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    granted_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (model_id, user_id)
);

CREATE INDEX IF NOT EXISTS model_account_grants_user_idx
    ON model_account_grants (user_id);
