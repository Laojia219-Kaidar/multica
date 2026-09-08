-- Revert migration 260 in reverse apply order to preserve agent_workspace_name_unique.

-- HC-020 Will → Sage
UPDATE agent
SET name = 'Sage｜首席架构师', updated_at = NOW()
WHERE id = '3f6fb05c-cf49-4446-8906-13e1f17794a3'::uuid
  AND archived_at IS NULL
  AND name = 'Will｜首席架构师';

-- KT-018 Elsa → Nova
UPDATE agent
SET name = 'Nova｜UI/UX 设计总监', updated_at = NOW()
WHERE id = 'bfea5417-eb5e-4e67-99a8-5609824c663d'::uuid
  AND archived_at IS NULL
  AND name = 'Elsa｜UI/UX 设计总监';

-- KT-023 Gate → Quinn
UPDATE agent
SET name = 'Quinn｜质量守护者', updated_at = NOW()
WHERE id = '708a49ba-7e7d-4363-8b9b-e6a4aeb5d980'::uuid
  AND archived_at IS NULL
  AND name = 'Gate｜质量守护者';

-- EXT-001 Harbor → Emory
UPDATE agent
SET name = 'Emory｜外部 IDE 协作专员', updated_at = NOW()
WHERE id = 'bf6f658f-b8d6-45d8-8f57-e9444ce9b4e4'::uuid
  AND archived_at IS NULL
  AND name = 'Harbor｜外部 IDE 协作专员';

-- KT-013 Vera → Emma
UPDATE agent
SET name = 'Emma｜风险评估专员', updated_at = NOW()
WHERE id = 'c5ab7e4f-8c34-4fee-90e7-634d42c4edee'::uuid
  AND archived_at IS NULL
  AND name = 'Vera｜风险评估专员';

-- KT-058 Finn → Willow (frontend) before Bolt → Finn (performance)
UPDATE agent
SET name = 'Willow｜前端与交互工程师', updated_at = NOW()
WHERE id = '876e2514-ee7c-4fc3-85f2-861baa464a45'::uuid
  AND archived_at IS NULL
  AND name = 'Finn｜前端与交互工程师';

-- KT-046 Bolt → Finn (performance)
UPDATE agent
SET name = 'Finn｜性能优化工程师', updated_at = NOW()
WHERE id = '7f1d98a5-307f-40d8-893b-82d9fe09f33e'::uuid
  AND archived_at IS NULL
  AND name = 'Bolt｜性能优化工程师';
