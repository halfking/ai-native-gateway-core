-- Rollback script for 973_add_raw_model_name.sql

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '30s';

-- 删除索引
DROP INDEX IF EXISTS public.idx_request_metrics_raw_model;

-- 删除字段
ALTER TABLE public.request_metrics DROP COLUMN IF EXISTS raw_model_name;

-- ============================================================
-- 台账回退（与 up 的自登记对偶）
-- ============================================================
-- down 只退自己引入的行
DELETE FROM public.schema_migrations WHERE version = '973';

COMMIT;
