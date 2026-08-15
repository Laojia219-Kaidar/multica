-- Remove mappings whose Project is absent or belongs to another workspace.
-- Project remains the lifecycle authority; this migration only cleans the
-- HiveCrew organizational projection.
DELETE FROM workflow_operating_program_project mapping
WHERE NOT EXISTS (
    SELECT 1
    FROM project
    WHERE project.id = mapping.project_id
      AND project.workspace_id = mapping.workspace_id
);
