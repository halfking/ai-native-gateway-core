-- 882 down: 删除 idx_request_logs_cost_negative（父表 DROP 级联全部分区侧子索引）。
-- 仅回滚索引本身，不动 860 的改名与任何数据。
DROP INDEX IF EXISTS public.idx_request_logs_cost_negative;
