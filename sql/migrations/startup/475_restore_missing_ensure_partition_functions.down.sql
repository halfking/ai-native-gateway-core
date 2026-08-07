-- Migration 475 down: drop the ensure_* partition functions restored by 475.
--
-- 注意：只删函数，不删分区表。475 的 up 除了建函数还会立即为当月/下月
-- 建分区；那些分区是生产数据的写入目标，回滚绝不能删（会丢数据）。
--
-- 回滚后果：这 5 张表重新失去自动分区能力（回到 475 之前的状态），
-- 需要重新依赖手工迁移（如 473）补分区，否则跨月写入会失败。

BEGIN;

DROP FUNCTION IF EXISTS public.ensure_cache_metrics_partition(date);
DROP FUNCTION IF EXISTS public.ensure_credit_ledger_partition(timestamp with time zone);
DROP FUNCTION IF EXISTS public.ensure_tool_usage_stats_partition(timestamp with time zone);
DROP FUNCTION IF EXISTS public.ensure_session_module_executions_partition(date);
DROP FUNCTION IF EXISTS public.ensure_dashboard_events_partition(date);

COMMIT;
