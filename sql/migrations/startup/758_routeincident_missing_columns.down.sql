-- 758 down: 撤回 diagnostic_runs / routing_audit_log 的 4 个列
--
-- ⚠️ 同样禁止包进 DO 块的 EXECUTE（续十六实测：静默无效）。
-- ⚠️ 撤回后 routeincident 的两条 INSERT 重新变成 ERROR: column ... does not exist，
--    而那条路径是**每条落库请求日志**都走的（main.go:3554）。本 down 仅供回滚演练，
--    不要在生产执行。

ALTER TABLE public.diagnostic_runs
    DROP COLUMN IF EXISTS route_key,
    DROP COLUMN IF EXISTS parameters,
    DROP COLUMN IF EXISTS result;

ALTER TABLE public.routing_audit_log
    DROP COLUMN IF EXISTS reason;
