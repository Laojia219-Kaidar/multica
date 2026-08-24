-- work_event.sql — A2 Work Wall projection reads for the canonical work_event
-- ledger (migration 401). The ledger is append-only; these queries are
-- strictly read-only and must never be extended with writes: the Work Wall is
-- a projection, not a second authority.

-- name: ListRecentWorkEvents :many
-- Newest-first window over the workspace ledger. observed_at leads (arrival
-- order is the projection truth), created_at then id break ties so the same
-- stored rows always project in one deterministic order.
SELECT * FROM work_event
WHERE workspace_id = $1
ORDER BY observed_at DESC NULLS LAST, created_at DESC, id DESC
LIMIT $2;
