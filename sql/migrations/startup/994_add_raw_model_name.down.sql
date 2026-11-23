-- Rollback script for 994_add_raw_model_name.sql

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '30s';

-- 删除索引
DROP INDEX IF EXISTS public.idx_request_metrics_raw_model;

-- 删除字段
ALTER TABLE public.request_metrics DROP COLUMN IF EXISTS raw_model_name;

COMMIT;
