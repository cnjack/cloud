-- Freeze writeback routing before converging duplicate bindings. Claims that
-- already own a run must outlive Automation/Service/Installation deletion: the
-- run's immutable Plugin snapshot remains its authorization source.
ALTER TABLE automation_kanban_claims
    ADD COLUMN IF NOT EXISTS installation_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS done_column TEXT NOT NULL DEFAULT '';

UPDATE automation_kanban_claims c
SET installation_id = t.installation_id,
    workspace_id = pi.workspace_id,
    done_column = t.done_column
FROM automation_kanban_triggers t
JOIN plugin_installations pi ON pi.id = t.installation_id
WHERE c.automation_id = t.automation_id;

ALTER TABLE automation_kanban_claims
    DROP CONSTRAINT IF EXISTS automation_kanban_claims_automation_id_fkey;

-- Deployments that briefly exposed public Kanban Automations may already
-- contain duplicates. Converge deterministically before adding constraints.
-- The oldest aggregate wins. Unclaimed observations disappear with the losing
-- aggregate; run-bound claims survive independently for frozen writeback.
DELETE FROM automation_kanban_claims
WHERE run_id IS NULL
  AND automation_id IN (
      SELECT id
      FROM (
          SELECT id,
                 row_number() OVER (
                     PARTITION BY service_id
                     ORDER BY created_at, id
                 ) AS position
          FROM automations_v2
          WHERE trigger_kind = 'kanban'
      ) ranked_unclaimed
      WHERE position > 1
  );

WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY service_id
               ORDER BY created_at, id
           ) AS position
    FROM automations_v2
    WHERE trigger_kind = 'kanban'
)
DELETE FROM automations_v2 a
USING ranked r
WHERE a.id = r.id AND r.position > 1;

WITH ranked AS (
    SELECT t.automation_id,
           row_number() OVER (
               PARTITION BY t.installation_id, t.board_ref
               ORDER BY a.created_at, a.id
           ) AS position
    FROM automation_kanban_triggers t
    JOIN automations_v2 a ON a.id = t.automation_id
)
DELETE FROM automations_v2 a
USING ranked r
WHERE a.id = r.automation_id AND r.position > 1;

DELETE FROM automation_kanban_claims c
WHERE c.run_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM automations_v2 a WHERE a.id = c.automation_id);

CREATE UNIQUE INDEX IF NOT EXISTS automations_v2_one_kanban_per_service
    ON automations_v2(service_id) WHERE trigger_kind = 'kanban';

CREATE UNIQUE INDEX IF NOT EXISTS automation_kanban_one_service_per_board
    ON automation_kanban_triggers(installation_id, board_ref);
