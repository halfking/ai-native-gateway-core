-- 754 down: 撤回 archive_request_logs_default 函数 (P2 数据归档, 2026-09-26)
--
-- 仅删除函数定义；不动 request_logs_archive_* 表（这些表是 owner
-- 拍板的归档资产，业务对账可能仍依赖，按 R68 纪律「down 不丢业务
-- 数据」原则保留——若需清理归档表，手工 DROP TABLE request_logs_
-- archive_YYYY_MM）。

DROP FUNCTION IF EXISTS public.archive_request_logs_default(integer);