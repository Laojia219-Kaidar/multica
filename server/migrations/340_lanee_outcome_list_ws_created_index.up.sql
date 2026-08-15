-- Lane E (P4): index assignment_dispatch_receipt by workspace + created_at
-- to keep the outcome-center list and keyset cursor scan bounded to one
-- workspace instead of a full table scan as history grows.

CREATE INDEX CONCURRENTLY IF NOT EXISTS assignment_dispatch_receipt_ws_created_idx
    ON assignment_dispatch_receipt (workspace_id, created_at DESC);
