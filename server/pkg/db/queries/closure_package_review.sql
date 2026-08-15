-- name: HasApprovedClosureReview :one
SELECT EXISTS (
    SELECT 1 FROM closure_package_review
    WHERE workspace_id = $1 AND project_id = $2 AND decision = 'approve'
) AS approved;

-- name: InsertClosurePackageReview :one
INSERT INTO closure_package_review (workspace_id, project_id, reviewer_user_id, decision)
VALUES ($1, $2, $3, $4)
RETURNING id, workspace_id, project_id, reviewer_user_id, decision, reviewed_at;
