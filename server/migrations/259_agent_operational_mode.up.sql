-- Agent operational mode (HIV-332 / HIV-54 claim guard).
--
-- operational_mode is the canonical administrative setting that gates whether
-- an agent may claim new work. It is distinct from the runtime-observability
-- `status` column (idle/working/blocked/error/offline) and from the capacity
-- gate (max_concurrent_tasks):
--
--   active    — agent may claim new tasks (subject to capacity).
--   resting   — agent is intentionally idle and must not claim.
--   disabled  — agent is administratively blocked and must not claim.
--   training  — agent may only claim structurally-provable Owner direct-chat
--               or quick-create tasks (see ClaimTask gate).
--
-- Defaulting to 'active' preserves existing claim behaviour for rows created
-- before this column existed. The application-layer claim gate is fail-closed
-- for any value outside this set, so a future unknown value cannot widen the
-- door.
ALTER TABLE agent
ADD COLUMN operational_mode TEXT NOT NULL DEFAULT 'active'
    CHECK (operational_mode IN ('active', 'resting', 'disabled', 'training'));
