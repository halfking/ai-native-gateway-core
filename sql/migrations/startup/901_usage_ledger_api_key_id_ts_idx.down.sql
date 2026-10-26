-- Migration 901 down: 删除 usage_ledger 的按 key 聚合索引。
-- 回滚后 /api/usage/{id}* 三端点回到 ts 首列索引 + api_key_id Filter 的
-- 全窗扫描形态（7 天空窗实测 6,983 ms）。

BEGIN;

DROP INDEX IF EXISTS public.idx_usage_ledger_api_key_id_ts;

COMMIT;
