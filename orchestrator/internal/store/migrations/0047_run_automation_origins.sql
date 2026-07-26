-- Preserve the concrete trigger kind for runs created by the unified Plugin
-- Automation platform. The automation id remains attached for audit and
-- de-duplication; origin describes how the run actually started.
UPDATE runs AS r
SET origin = 'kanban'
FROM automations_v2 AS a
WHERE r.origin = 'automation'
  AND r.origin_automation_id = a.id
  AND a.trigger_kind = 'kanban';

UPDATE runs AS r
SET origin = 'schedule'
FROM automations_v2 AS a
WHERE r.origin = 'automation'
  AND r.origin_automation_id = a.id
  AND a.trigger_kind = 'cron';
