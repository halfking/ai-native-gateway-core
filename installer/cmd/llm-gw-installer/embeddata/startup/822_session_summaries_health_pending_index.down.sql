-- 822 down: 删除 822 建的健康分待评分部分索引。
--
-- 回退后果：bg/session_health_worker 的 sweep 捞取退回 Parallel Seq Scan +
-- Sort（252 真库 586,339 行实测 25.8-29.4s/次，每实例每 60min 一 tick），
-- 即 822 的登记病灶；除此之外无其他读方依赖该索引，删除本身安全。
-- 注意 CONCURRENTLY 不能在事务块内执行，手工回退请走非事务 psql 通道。

DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_summaries_health_pending;
