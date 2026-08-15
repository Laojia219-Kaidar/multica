-- 371_base_dgx_nas.up.sql — 新增两个基地：DGX 重型计算基地与 NAS 存储资产基地。
-- DGX (dgx-hive-01 / spark-b398) 承载 canonical noah-ark-4 控制面、Authority BFF 与
-- hivecosm_core 正式库；NAS 承载 HiveData 存储卷与全系统数据资产。两者此前不在
-- "受管理基地注册表"内，属于基地形态的两个新类别（重型计算 / 存储资产）。
INSERT INTO base (workspace_id, code, name, device, machine_title)
SELECT w.id, v.code, v.name, v.device, v.machine_title
FROM (SELECT id FROM workspace WHERE slug = 'hivecosm') w,
     (VALUES
       ('BASE-06','计算基地','DGX','HiveCosm DGX Spark'),
       ('BASE-07','存储基地','NAS','HiveCosm NAS HiveData')
     ) AS v(code, name, device, machine_title)
ON CONFLICT (workspace_id, machine_title) DO NOTHING;
