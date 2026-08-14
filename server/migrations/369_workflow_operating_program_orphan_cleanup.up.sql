-- Remove mappings whose Program or workspace owner no longer exists. This is
-- the first hygiene gate for the FK-free governed boundary. Project-side
-- cleanup is intentionally a separate 370 migration, preserving the history
-- already applied to the candidate database.
DELETE FROM workflow_operating_program_project mapping
WHERE NOT EXISTS (
    SELECT 1
    FROM workflow_operating_program program
    WHERE program.id = mapping.program_id
      AND program.workspace_id = mapping.workspace_id
);
