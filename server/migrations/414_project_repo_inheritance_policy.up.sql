ALTER TABLE project
    ADD COLUMN repo_inheritance_policy TEXT NOT NULL DEFAULT 'workspace_fallback',
    ADD CONSTRAINT project_repo_inheritance_policy_check
        CHECK (repo_inheritance_policy IN ('workspace_fallback', 'project_only'));
