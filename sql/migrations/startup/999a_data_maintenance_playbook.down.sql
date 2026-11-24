-- Migration: 999a_data_maintenance_playbook (down)
-- Purpose: 回滚 1000 —— 删除六条数据维护手册条目与台账行
--
-- ★ 只删本迁移自己种的数据（按 created_by='seed:999a' 精确圈定，不碰
--   995 种子、PI 分析落库条目与任何执行留痕）。手册表本体（995 建）保留。

DELETE FROM public.error_playbook_entries
WHERE created_by = 'seed:999a'
  AND error_type LIKE 'data_maintenance:%';

DELETE FROM public.schema_migrations WHERE version = '999a';
