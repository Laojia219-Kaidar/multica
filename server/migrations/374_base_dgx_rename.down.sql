-- 374 down: 改回"计算基地"
UPDATE base
SET name = '计算基地', updated_at = now()
WHERE code = 'BASE-06' AND name = '底座基地';
