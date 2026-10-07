-- 841_monthly_partition_retention.down.sql
-- 回滚 841：**只删本迁移建的函数与两张表**。
--
-- ⚠️ 已被 llm_gateway_drop_expired_month_partitions() DROP 掉的分区**不可由此恢复**——
--   本文件只是「不再继续删」，已删分区需要从备份还原。
--   这不是「只能退不能进」的假回滚：它确实撤掉了机制本身
--   （配置、函数、审计表全部消失，行为回到 841 之前 = 什么都不删）。
--
-- ★ 刻意**不**删任何已存在的月度分区：那属于业务数据，不是本迁移建的东西。

BEGIN;

DROP FUNCTION IF EXISTS public.llm_gateway_drop_expired_month_partitions();
DROP FUNCTION IF EXISTS public.llm_gateway_expired_month_partitions();
DROP TABLE IF EXISTS public.llm_gateway_partition_drop_log;
DROP TABLE IF EXISTS public.llm_gateway_partition_retention;

COMMIT;
