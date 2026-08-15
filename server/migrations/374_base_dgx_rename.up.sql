-- 374_base_dgx_rename
-- Owner 决策（2026-08-15）：DGX 定位为"所有基地开发对象的母库存放地 + 本地 27B 私有推理
-- + 未来合同/财务等敏感 agent 的运行地"，"计算基地"不再反映真实角色，更名为"底座基地"。
-- BASE-06 编码与 machine_title 不变，仅改 name。
UPDATE base
SET name = '底座基地', updated_at = now()
WHERE code = 'BASE-06' AND name = '计算基地';
