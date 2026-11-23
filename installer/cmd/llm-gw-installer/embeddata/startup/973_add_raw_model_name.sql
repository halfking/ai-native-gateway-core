-- ===========================================================================
-- File:          sql/migrations/startup/973_add_raw_model_name.sql
-- Migration:     973
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
-- ★★ 编号说明（2026-11-24 12h 修订审计轮）：本迁移原名 994_add_raw_model_name，
--   与 994_session_analytics_owner_key_and_close_path **撞号**（迁移号唯一性门红）
--   且**任何投递通道都没有登记它**（embeddata / StartupFiles / tsv / 部署通道全缺）
--   ⇒ raw_model_name 这一列在生产上根本不存在 ⇒ 写 request_metrics 的那条 SQL
--   （internal/metricsstore/writer.go）必然 42703 ⇒ 整条统计管线静默丢数。
--   按仓内规矩「**新代码必须换新号**」改为 1000，并补齐九面登记。
--
-- Affected Rows: 0 (DDL操作)
-- Breaking Change: NO (纯新增字段)
-- Rollback Script: 973_add_raw_model_name.down.sql
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

-- ============================================================
-- 台账自登记（≥952 双账本纪律）
-- ============================================================
-- ★ 缺此段 ⇒ 任何通道应用后运行时台账无行 ⇒ 连续性门/漂移报告假红
-- ★ 本迁移原先**没有**这一段 ⇒ 即使被投递也不会在运行时台账留行
INSERT INTO public.schema_migrations (version, description)
VALUES ('973', 'add_raw_model_name: request_metrics 补记原始模型名列')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
