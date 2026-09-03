-- Rollback for 646_system_monitor_fallback_queue.sql
--
-- 删除本迁移确保的表。若环境中该表由 V352 先创建并承载过 fallback 数据，
-- 回滚前请确认队列为空（无未 drain 的任务）再执行。

DROP TABLE IF EXISTS public.system_monitor_fallback_queue;
