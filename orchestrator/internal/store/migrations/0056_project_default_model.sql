-- A project default resolves headless runs when a Service has not chosen a
-- more specific default. Model deletion returns the project to inheritance.
ALTER TABLE projects
  ADD COLUMN IF NOT EXISTS default_model_id TEXT
  REFERENCES model_configs(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_projects_default_model
  ON projects(default_model_id)
  WHERE default_model_id IS NOT NULL;

-- Preserve the currently approved product default for existing projects that
-- can already use GLM-5.2. A later explicit project choice always wins.
WITH available AS (
  SELECT mc.project_id, mc.id, mc.created_at
  FROM model_configs mc
  WHERE mc.project_id IS NOT NULL
    AND mc.enabled
    AND (lower(mc.model_id) = 'glm-5.2' OR lower(mc.model_name) LIKE '%/glm-5.2')
  UNION ALL
  SELECT mg.project_id, mc.id, mc.created_at
  FROM model_grants mg
  JOIN model_configs mc ON mc.id = mg.model_id
  WHERE lower(mc.model_id) = 'glm-5.2' OR lower(mc.model_name) LIKE '%/glm-5.2'
), preferred AS (
  SELECT DISTINCT ON (project_id) project_id, id
  FROM available
  ORDER BY project_id, created_at DESC, id
)
UPDATE projects p
SET default_model_id = preferred.id
FROM preferred
WHERE p.id = preferred.project_id
  AND p.default_model_id IS NULL;

-- Existing Services inherit that approved Project default. Preserve any
-- Service-specific choice already made by an owner.
UPDATE services s
SET default_model_id = p.default_model_id
FROM projects p
WHERE s.project_id = p.id
  AND s.default_model_id IS NULL
  AND p.default_model_id IS NOT NULL;
