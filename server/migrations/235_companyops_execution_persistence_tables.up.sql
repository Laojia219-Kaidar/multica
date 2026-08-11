-- HiveCrew-owned execution persistence for an external WorkOrder projection.
-- These rows are provenance links and immutable execution evidence only. They
-- do not copy WorkOrder title/status, Project lifecycle, or any other company
-- truth owned by HiveCosm.
--
-- No foreign keys or cascades by repository policy. The service validates all
-- relationships inside its application transaction, and receipts survive
-- deletion of their local Issue/Run projections.

CREATE TABLE external_work_order_link (
    workspace_id UUID NOT NULL,
    work_order_ref TEXT NOT NULL CHECK (btrim(work_order_ref) <> ''),
    linked_revision TEXT NOT NULL CHECK (btrim(linked_revision) <> ''),
    linked_digest TEXT NOT NULL CHECK (linked_digest ~ '^sha256:[0-9a-f]{64}$'),
    source_observed_at TIMESTAMPTZ NOT NULL,
    freshness_at_link TEXT NOT NULL CHECK (freshness_at_link = 'current'),
    issue_id UUID NOT NULL,
    linked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE assignment_dispatch_receipt (
    command_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    local_agent_id UUID NOT NULL,
    initial_task_id UUID NOT NULL,

    work_order_ref TEXT NOT NULL,
    work_order_revision TEXT NOT NULL,
    work_order_digest TEXT NOT NULL CHECK (work_order_digest ~ '^sha256:[0-9a-f]{64}$'),
    input_digest TEXT NOT NULL CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
    employee_ref TEXT NOT NULL,
    employee_revision TEXT NOT NULL,
    employee_digest TEXT NOT NULL CHECK (employee_digest ~ '^sha256:[0-9a-f]{64}$'),
    binding_ref TEXT NOT NULL,
    binding_revision TEXT NOT NULL,
    binding_digest TEXT NOT NULL CHECK (binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    agent_ref TEXT NOT NULL,
    agent_revision TEXT NOT NULL,
    agent_digest TEXT NOT NULL CHECK (agent_digest ~ '^sha256:[0-9a-f]{64}$'),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        btrim(work_order_ref) <> '' AND btrim(work_order_revision) <> '' AND
        btrim(employee_ref) <> '' AND btrim(employee_revision) <> '' AND
        btrim(binding_ref) <> '' AND btrim(binding_revision) <> '' AND
        btrim(agent_ref) <> '' AND btrim(agent_revision) <> ''
    )
);

CREATE TABLE execution_receipt (
    task_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    assignment_command_id UUID NOT NULL,

    work_order_ref TEXT NOT NULL,
    work_order_revision TEXT NOT NULL,
    work_order_digest TEXT NOT NULL CHECK (work_order_digest ~ '^sha256:[0-9a-f]{64}$'),
    input_digest TEXT NOT NULL CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
    employee_ref TEXT NOT NULL,
    employee_revision TEXT NOT NULL,
    employee_digest TEXT NOT NULL CHECK (employee_digest ~ '^sha256:[0-9a-f]{64}$'),
    binding_ref TEXT NOT NULL,
    binding_revision TEXT NOT NULL,
    binding_digest TEXT NOT NULL CHECK (binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    agent_ref TEXT NOT NULL,
    agent_revision TEXT NOT NULL,
    agent_digest TEXT NOT NULL CHECK (agent_digest ~ '^sha256:[0-9a-f]{64}$'),

    runtime_snapshot JSON NOT NULL CHECK (json_typeof(runtime_snapshot) = 'object'),
    runtime_digest TEXT NOT NULL CHECK (runtime_digest ~ '^sha256:[0-9a-f]{64}$'),
    claimed_at TIMESTAMPTZ NOT NULL,

    terminal_status TEXT CHECK (terminal_status IN ('completed', 'failed', 'cancelled')),
    completed_at TIMESTAMPTZ,
    output_digest TEXT CHECK (output_digest IS NULL OR output_digest ~ '^sha256:[0-9a-f]{64}$'),
    result_snapshot JSON CHECK (result_snapshot IS NULL OR json_typeof(result_snapshot) = 'object'),
    terminal_error TEXT,
    finalized_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (
        btrim(work_order_ref) <> '' AND btrim(work_order_revision) <> '' AND
        btrim(employee_ref) <> '' AND btrim(employee_revision) <> '' AND
        btrim(binding_ref) <> '' AND btrim(binding_revision) <> '' AND
        btrim(agent_ref) <> '' AND btrim(agent_revision) <> ''
    ),
    CHECK (
        (terminal_status IS NULL AND completed_at IS NULL AND output_digest IS NULL AND
         result_snapshot IS NULL AND terminal_error IS NULL AND finalized_at IS NULL)
        OR
        (terminal_status IS NOT NULL AND completed_at IS NOT NULL AND finalized_at IS NOT NULL)
    )
);
