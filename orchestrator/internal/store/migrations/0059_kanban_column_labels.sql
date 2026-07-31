-- Preserve human-readable Kanban column names independently of stable column
-- keys so policy and receipts remain understandable after the board is linked.

ALTER TABLE automation_kanban_triggers
    ADD COLUMN IF NOT EXISTS trigger_label TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS done_label TEXT NOT NULL DEFAULT '';

UPDATE automation_kanban_triggers
SET trigger_label = trigger_column
WHERE trigger_label = '';

UPDATE automation_kanban_triggers
SET done_label = done_column
WHERE done_column <> '' AND done_label = '';
