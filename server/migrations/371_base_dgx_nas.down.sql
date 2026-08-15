-- 371_base_dgx_nas.down.sql — 移除 DGX 与 NAS 基地注册（不触碰 agent FK，两基地当前无员工绑定）。
DELETE FROM base
WHERE workspace_id = (SELECT id FROM workspace WHERE slug = 'hivecosm')
  AND code IN ('BASE-06','BASE-07');
