-- 0070_provider_scoped_model_names: model identity belongs to its provider.
--
-- 0029 made model display names unique across an entire cluster/project scope.
-- That still rejected two different providers exposing the same model name,
-- even though runtime identity and the existing upstream-id constraint are both
-- provider-scoped. Keep names unambiguous within one provider while allowing
-- equivalent models to be configured through different providers.

DROP INDEX IF EXISTS model_configs_scope_name_idx;

CREATE UNIQUE INDEX IF NOT EXISTS model_configs_provider_name_idx
    ON model_configs (provider_id, name);
