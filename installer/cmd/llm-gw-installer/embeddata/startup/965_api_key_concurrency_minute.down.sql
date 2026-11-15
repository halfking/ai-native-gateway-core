-- ===========================================================================
-- File:          965_api_key_concurrency_minute.down.sql
-- Migration:     965 (down)
-- Purpose:       回退 965——删除 per-key 并发分钟观测表。
--                回滚后果如实登记：bg.KeyConcurrencyStatsWriter 的 UPSERT
--                将持续 WARN（表缺席自愈语义，不停 worker、不影响并发闸
--                执法）；已落库的历史观测数据随表删除不可恢复。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DROP TABLE IF EXISTS public.api_key_concurrency_minute;

DELETE FROM public.schema_migrations WHERE version = '965';

COMMIT;
