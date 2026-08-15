-- 349_base_seed_and_migrate.up.sql — seed 5 个公司基地 + 把 33 agent 的 custom_env 基地映射迁移到正式列(不复制员工)。
INSERT INTO base (workspace_id, code, name, device, machine_title)
SELECT w.id, v.code, v.name, v.device, v.machine_title
FROM (SELECT id FROM workspace WHERE slug = 'hivecosm') w,
     (VALUES
       ('BASE-01','中枢基地','Mac mini','HiveCosm Mac mini'),
       ('BASE-02','工程基地','MBP M5X','HiveCrew MBP M5X'),
       ('BASE-03','产品基地','MBP M4','HiveCrew MBP M4'),
       ('BASE-04','质量基地','MBA M4','HiveCrew MBA M4'),
       ('BASE-05','研究基地','MB M2','HiveCrew MB M2')
     ) AS v(code, name, device, machine_title)
ON CONFLICT (workspace_id, machine_title) DO NOTHING;

UPDATE agent a
SET home_base_id = b.id
FROM base b
WHERE a.workspace_id = b.workspace_id
  AND a.custom_env->>'HIVECOSM_HOME_BASE' = b.machine_title
  AND a.home_base_id IS NULL;

UPDATE agent a
SET fallback_base_id = b.id
FROM base b
WHERE a.workspace_id = b.workspace_id
  AND a.custom_env->>'HIVECOSM_FALLBACK_BASE' = b.machine_title
  AND a.fallback_base_id IS NULL;
