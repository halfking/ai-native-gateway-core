-- 764 down: 撤回 request_logs 分区家族 tenant_ts 索引。
--
-- ⚠️ 撤回后 tenant 维度 days>7 聚合回到每分区全表扫（252-dev 实测 3 行租户
--    6.5s / default 21.5s）。本 down 仅供回滚演练，不要在生产执行。

DROP INDEX IF EXISTS public.idx_request_logs_tenant_ts;
