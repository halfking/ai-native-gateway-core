-- ===========================================================================
-- File:          sql/migrations/startup/994_add_raw_model_name.sql
-- Migration:     994
-- Database:      llm_gateway
-- Purpose:       向request_metrics表添加raw_model_name字段，记录原始模型名称
--
-- Status:        active
-- Idempotent:    YES (ADD COLUMN IF NOT EXISTS)
-- Dependencies:  983 (request_metrics表)
--
-- Background:
--   当前request_metrics.outbound_model存储的是映射后的标准模型名称，
--   但缺少原始模型名称字段。为了支持：
--   1. 追溯原始请求使用的模型名称
--   2. 统计时能灵活选择按原始名或标准名聚合
--   3. 排查模型别名映射问题
--   
--   需要添加raw_model_name字段记录usage_facts.raw_model_name的原始值。
--
-- Affected Rows: 0 (DDL操作)
-- Breaking Change: NO (纯新增字段)
-- Rollback Script: 994_add_raw_model_name.down.sql
--
-- Date: 2026-11-24
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '30s';

-- 添加raw_model_name字段
ALTER TABLE public.request_metrics 
ADD COLUMN IF NOT EXISTS raw_model_name TEXT;

-- 创建索引支持按原始模型名查询
CREATE INDEX IF NOT EXISTS idx_request_metrics_raw_model 
ON public.request_metrics (raw_model_name, occurred_date);

-- 添加注释
COMMENT ON COLUMN public.request_metrics.raw_model_name IS '原始模型名称（来自usage_facts.raw_model_name）';

COMMIT;
