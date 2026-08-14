-- Remove mappings whose Program or workspace owner no longer exists. This is
-- a one-time hygiene gate for the FK-free governed boundary; future writes
-- are protected by ordered row locks and native Project-delete cleanup.
DELETE FROM workflow_operating_program_project mapping
WHERE NOT EXISTS (
    SELECT 1
    FROM workflow_operating_program program
    WHERE program.id = mapping.program_id
      AND program.workspace_id = mapping.workspace_id
)
OR NOT EXISTS (
    SELECT 1
    FROM project
    WHERE project.id = mapping.project_id
      AND project.workspace_id = mapping.workspace_id
);
