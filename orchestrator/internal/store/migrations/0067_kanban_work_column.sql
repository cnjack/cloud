-- A Kanban pickup has an explicit user-visible ownership state. Cloud moves a
-- Card from the trigger queue into this work column before a Run is queued;
-- delivery completion, not agent-process completion, controls the later move
-- to done.
ALTER TABLE automation_kanban_triggers
    ADD COLUMN IF NOT EXISTS work_column TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS work_label TEXT NOT NULL DEFAULT '';
