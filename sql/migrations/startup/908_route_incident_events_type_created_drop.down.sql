-- 908 down：重建 idx_route_incident_events_type_created。
--
-- 与 718 的「文档化 no-op」不同，本条给**真重建**：903 删的是一个
-- 无行为依赖、但可能被未来查询（按事件类型的时间线过滤）重新需要
-- 的索引；down 给出逐字同形的重建路径，回退成本最小化。
--
-- dbinit:no-transaction —— CREATE INDEX CONCURRENTLY 不能进事务块
-- （同正本；表在写路径上，普通 CREATE 会阻塞 failure_observed 写入）。
-- 会话级 SET + RESET（非事务通道，SET LOCAL 不生效，871 头注同款）。

SET lock_timeout = '60s';
SET statement_timeout = '10min';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_route_incident_events_type_created
    ON public.route_incident_events (event_type, created_at DESC);

RESET statement_timeout;
RESET lock_timeout;
