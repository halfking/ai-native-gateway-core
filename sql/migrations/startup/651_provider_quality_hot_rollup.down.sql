-- Rollback for 645_provider_quality_hot_rollup.sql
--
-- 只回滚本迁移新建的支撑索引。ADD COLUMN IF NOT EXISTS 保护的列在所有
-- 标准环境中先于本迁移存在并承载实时写入数据，回滚时不删除，避免数据丢失。
-- 若确需在专用环境移除这些列，请人工评估后执行。

DROP INDEX IF EXISTS public.idx_request_logs_hot_provider_ts;
