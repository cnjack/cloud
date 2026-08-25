-- Repository Agent Workflows execute under an explicit account. The account
-- grant is independent from project membership and is revalidated at dispatch.
ALTER TABLE automations_v2
    ADD COLUMN IF NOT EXISTS execution_account_id TEXT
        REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS automations_v2_execution_account_idx
    ON automations_v2(execution_account_id)
    WHERE execution_account_id IS NOT NULL;

-- Repository is the public repair boundary for Agent Board occurrences. Keep
-- the historic project_owner value readable for older Automation rows while
-- allowing new Repository-native blockers.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'automation_kanban_occurrences_repair_role_check'
    ) THEN
        ALTER TABLE automation_kanban_occurrences DROP CONSTRAINT automation_kanban_occurrences_repair_role_check;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'automation_kanban_occurrences_repair_role_check'
    ) THEN
        ALTER TABLE automation_kanban_occurrences
            ADD CONSTRAINT automation_kanban_occurrences_repair_role_check
            CHECK (repair_role IN ('','project_owner','repository_owner','cluster_admin'));
    END IF;
END $$;
