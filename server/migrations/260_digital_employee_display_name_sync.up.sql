-- Sync seven HiveCrew workforce agent display names to the approved Notion
--对照表 (2026-09-08). employee_ref / agent UUID / IdentityBinding refs stay
-- unchanged; only local agent.name and description (former-name note) move.
--
-- Order matters for agent_workspace_name_unique: Finn (performance) → Bolt
-- before Willow → Finn.
--
-- Source of truth: scripts/digital-employee-display-name-renames.json
-- Verify: node scripts/sync-digital-employee-display-names.mjs verify

-- KT-046 Finn → Bolt (performance optimization)
UPDATE agent
SET
    name = 'Bolt｜性能优化工程师',
    description = CASE
        WHEN description IS NULL OR btrim(description) = '' THEN 'Formerly: Finn｜性能优化工程师 (KT-046).'
        WHEN strpos(description, 'Formerly: Finn｜性能优化工程师') = 0
            THEN left(description || ' Formerly: Finn｜性能优化工程师 (KT-046).', 255)
        ELSE description
    END,
    updated_at = NOW()
WHERE id = '7f1d98a5-307f-40d8-893b-82d9fe09f33e'::uuid
  AND archived_at IS NULL;

-- KT-058 Willow → Finn (frontend & interaction)
UPDATE agent
SET
    name = 'Finn｜前端与交互工程师',
    description = CASE
        WHEN description IS NULL OR btrim(description) = '' THEN 'Formerly: Willow｜前端与交互工程师 (KT-058).'
        WHEN strpos(description, 'Formerly: Willow｜前端与交互工程师') = 0
            THEN left(description || ' Formerly: Willow｜前端与交互工程师 (KT-058).', 255)
        ELSE description
    END,
    updated_at = NOW()
WHERE id = '876e2514-ee7c-4fc3-85f2-861baa464a45'::uuid
  AND archived_at IS NULL;

-- KT-013 Emma → Vera
UPDATE agent
SET
    name = 'Vera｜风险评估专员',
    description = CASE
        WHEN description IS NULL OR btrim(description) = '' THEN 'Formerly: Emma｜风险评估专员 (KT-013).'
        WHEN strpos(description, 'Formerly: Emma｜风险评估专员') = 0
            THEN left(description || ' Formerly: Emma｜风险评估专员 (KT-013).', 255)
        ELSE description
    END,
    updated_at = NOW()
WHERE id = 'c5ab7e4f-8c34-4fee-90e7-634d42c4edee'::uuid
  AND archived_at IS NULL;

-- EXT-001 Emory → Harbor
UPDATE agent
SET
    name = 'Harbor｜外部 IDE 协作专员',
    description = CASE
        WHEN description IS NULL OR btrim(description) = '' THEN 'Formerly: Emory｜外部 IDE 协作专员 (EXT-001).'
        WHEN strpos(description, 'Formerly: Emory｜外部 IDE 协作专员') = 0
            THEN left(description || ' Formerly: Emory｜外部 IDE 协作专员 (EXT-001).', 255)
        ELSE description
    END,
    updated_at = NOW()
WHERE id = 'bf6f658f-b8d6-45d8-8f57-e9444ce9b4e4'::uuid
  AND archived_at IS NULL;

-- KT-023 Quinn → Gate (engineering quality; content-review Quinn is Grok-only)
UPDATE agent
SET
    name = 'Gate｜质量守护者',
    description = CASE
        WHEN description IS NULL OR btrim(description) = '' THEN 'Formerly: Quinn｜质量守护者 (KT-023). Content-review Quinn remains Grok-only; engineering quality uses Gauss.'
        WHEN strpos(description, 'Formerly: Quinn｜质量守护者') = 0
            THEN left(description || ' Formerly: Quinn｜质量守护者 (KT-023). Content-review Quinn remains Grok-only; engineering quality uses Gauss.', 255)
        ELSE description
    END,
    updated_at = NOW()
WHERE id = '708a49ba-7e7d-4363-8b9b-e6a4aeb5d980'::uuid
  AND archived_at IS NULL;

-- KT-018 Nova → Elsa
UPDATE agent
SET
    name = 'Elsa｜UI/UX 设计总监',
    description = CASE
        WHEN description IS NULL OR btrim(description) = '' THEN 'Formerly: Nova｜UI/UX 设计总监 (KT-018).'
        WHEN strpos(description, 'Formerly: Nova｜UI/UX 设计总监') = 0
            THEN left(description || ' Formerly: Nova｜UI/UX 设计总监 (KT-018).', 255)
        ELSE description
    END,
    updated_at = NOW()
WHERE id = 'bfea5417-eb5e-4e67-99a8-5609824c663d'::uuid
  AND archived_at IS NULL;

-- HC-020 Sage → Will
UPDATE agent
SET
    name = 'Will｜首席架构师',
    description = CASE
        WHEN description IS NULL OR btrim(description) = '' THEN 'Formerly: Sage｜首席架构师 (HC-020).'
        WHEN strpos(description, 'Formerly: Sage｜首席架构师') = 0
            THEN left(description || ' Formerly: Sage｜首席架构师 (HC-020).', 255)
        ELSE description
    END,
    updated_at = NOW()
WHERE id = '3f6fb05c-cf49-4446-8906-13e1f17794a3'::uuid
  AND archived_at IS NULL;
