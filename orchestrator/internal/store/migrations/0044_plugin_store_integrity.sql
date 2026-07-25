-- 0044_plugin_store_integrity: make Project Plugin aggregates impossible to
-- corrupt through a direct SQL writer.  0043 deliberately established the
-- clean-cut schema; this follow-up is append-only and validates new writes.

CREATE OR REPLACE FUNCTION jcloud_assert_service_repository_binding()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_service_id TEXT;
    target_installation_id TEXT;
    service_project_id TEXT;
    service_repo_kind TEXT;
    service_provider TEXT;
    installation_project_id TEXT;
    installation_provider TEXT;
BEGIN
    IF TG_TABLE_NAME = 'service_repository_bindings' THEN
        target_service_id := NEW.service_id;
        target_installation_id := NEW.installation_id;
    ELSE
        target_service_id := NEW.id;
        SELECT installation_id INTO target_installation_id
        FROM service_repository_bindings
        WHERE service_id = target_service_id;
        IF NOT FOUND THEN
            RETURN NEW;
        END IF;
    END IF;

    SELECT s.project_id, s.repo_kind, s.provider, i.project_id, i.provider
    INTO service_project_id, service_repo_kind, service_provider,
         installation_project_id, installation_provider
    FROM services s
    JOIN plugin_installations i ON i.id = target_installation_id
    WHERE s.id = target_service_id;

    IF NOT FOUND
       OR service_project_id <> installation_project_id
       OR service_repo_kind <> 'provider'
       OR service_provider IS DISTINCT FROM installation_provider THEN
        RAISE EXCEPTION 'service repository binding must use a same-project matching SCM Plugin'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS service_repository_bindings_scope_guard ON service_repository_bindings;
CREATE TRIGGER service_repository_bindings_scope_guard
BEFORE INSERT OR UPDATE OF service_id, installation_id ON service_repository_bindings
FOR EACH ROW EXECUTE FUNCTION jcloud_assert_service_repository_binding();

DROP TRIGGER IF EXISTS services_repository_binding_scope_guard ON services;
CREATE TRIGGER services_repository_binding_scope_guard
AFTER UPDATE OF project_id, repo_kind, provider ON services
FOR EACH ROW EXECUTE FUNCTION jcloud_assert_service_repository_binding();

CREATE OR REPLACE FUNCTION jcloud_assert_automation_aggregate()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_automation_id TEXT;
    expected_kind TEXT;
    expected_service_id TEXT;
    expected_installation_id TEXT;
    service_project_id TEXT;
    installation_project_id TEXT;
    installation_provider TEXT;
    scm_count INT;
    kanban_count INT;
    cron_count INT;
    action_count INT;
    kanban_installation_id TEXT;
BEGIN
    IF TG_TABLE_NAME = 'automations_v2' THEN
        IF TG_OP = 'DELETE' THEN
            target_automation_id := OLD.id;
        ELSE
            target_automation_id := NEW.id;
        END IF;
    ELSE
        IF TG_OP = 'DELETE' THEN
            target_automation_id := OLD.automation_id;
        ELSE
            target_automation_id := NEW.automation_id;
        END IF;
    END IF;
    SELECT trigger_kind, service_id, installation_id
    INTO expected_kind, expected_service_id, expected_installation_id
    FROM automations_v2
    WHERE id = target_automation_id;
    IF NOT FOUND THEN
        -- A parent delete (including cascades) has no aggregate left to check.
        RETURN NULL;
    END IF;

    SELECT count(*) INTO scm_count FROM automation_scm_triggers WHERE automation_id = target_automation_id;
    SELECT count(*) INTO kanban_count FROM automation_kanban_triggers WHERE automation_id = target_automation_id;
    SELECT count(*) INTO cron_count FROM automation_cron_triggers WHERE automation_id = target_automation_id;
    SELECT count(*) INTO action_count FROM automation_scm_actions WHERE automation_id = target_automation_id;

    IF (expected_kind = 'scm' AND (scm_count <> 1 OR kanban_count <> 0 OR cron_count <> 0 OR action_count = 0))
       OR (expected_kind = 'kanban' AND (scm_count <> 0 OR kanban_count <> 1 OR cron_count <> 0 OR action_count <> 0))
       OR (expected_kind = 'cron' AND (scm_count <> 0 OR kanban_count <> 0 OR cron_count <> 1 OR action_count <> 0)) THEN
        RAISE EXCEPTION 'Automation requires exactly one typed trigger matching trigger_kind'
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1 FROM automation_scm_actions x
        WHERE x.automation_id = target_automation_id
          AND x.service_id <> expected_service_id
    ) THEN
        RAISE EXCEPTION 'SCM action service must match its Automation service'
            USING ERRCODE = '23514';
    END IF;

    IF expected_installation_id IS NOT NULL THEN
        SELECT s.project_id, i.project_id, i.provider
        INTO service_project_id, installation_project_id, installation_provider
        FROM services s
        JOIN plugin_installations i ON i.id = expected_installation_id
        WHERE s.id = expected_service_id;
        IF NOT FOUND OR service_project_id <> installation_project_id THEN
            RAISE EXCEPTION 'Automation installation must belong to its Service project'
                USING ERRCODE = '23514';
        END IF;
    END IF;

    IF expected_kind = 'scm' AND NOT EXISTS (
        SELECT 1 FROM service_repository_bindings b
        WHERE b.service_id = expected_service_id
          AND b.installation_id = expected_installation_id
    ) THEN
        RAISE EXCEPTION 'SCM Automation must use its Service repository Plugin'
            USING ERRCODE = '23514';
    ELSIF expected_kind = 'kanban' THEN
        SELECT installation_id INTO kanban_installation_id
        FROM automation_kanban_triggers
        WHERE automation_id = target_automation_id;
        IF expected_installation_id IS DISTINCT FROM kanban_installation_id
           OR installation_provider <> 'jtype' THEN
            RAISE EXCEPTION 'Kanban Automation must use its JType Plugin'
                USING ERRCODE = '23514';
        END IF;
    ELSIF expected_kind = 'cron' AND expected_installation_id IS NOT NULL THEN
        RAISE EXCEPTION 'Cron Automation must not carry a Plugin installation'
            USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;

-- Constraint triggers run at commit, allowing Store transactions to replace
-- the parent and typed children without exposing an invalid intermediate row.
DROP TRIGGER IF EXISTS automations_v2_aggregate_guard ON automations_v2;
CREATE CONSTRAINT TRIGGER automations_v2_aggregate_guard
AFTER INSERT OR UPDATE OF trigger_kind, service_id, installation_id OR DELETE ON automations_v2
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION jcloud_assert_automation_aggregate();

DROP TRIGGER IF EXISTS automation_scm_triggers_aggregate_guard ON automation_scm_triggers;
CREATE CONSTRAINT TRIGGER automation_scm_triggers_aggregate_guard
AFTER INSERT OR UPDATE OR DELETE ON automation_scm_triggers
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION jcloud_assert_automation_aggregate();

DROP TRIGGER IF EXISTS automation_scm_actions_aggregate_guard ON automation_scm_actions;
CREATE CONSTRAINT TRIGGER automation_scm_actions_aggregate_guard
AFTER INSERT OR UPDATE OR DELETE ON automation_scm_actions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION jcloud_assert_automation_aggregate();

DROP TRIGGER IF EXISTS automation_kanban_triggers_aggregate_guard ON automation_kanban_triggers;
CREATE CONSTRAINT TRIGGER automation_kanban_triggers_aggregate_guard
AFTER INSERT OR UPDATE OR DELETE ON automation_kanban_triggers
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION jcloud_assert_automation_aggregate();

DROP TRIGGER IF EXISTS automation_cron_triggers_aggregate_guard ON automation_cron_triggers;
CREATE CONSTRAINT TRIGGER automation_cron_triggers_aggregate_guard
AFTER INSERT OR UPDATE OR DELETE ON automation_cron_triggers
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION jcloud_assert_automation_aggregate();
