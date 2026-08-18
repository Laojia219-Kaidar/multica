ALTER TABLE project
    DROP CONSTRAINT IF EXISTS project_repo_inheritance_policy_check,
    DROP COLUMN IF EXISTS repo_inheritance_policy;
