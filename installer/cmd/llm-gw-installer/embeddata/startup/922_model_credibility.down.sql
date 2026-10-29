-- ===========================================================================
-- File:          sql/migrations/startup/922_model_credibility.down.sql
-- Migration:     922 (down)
-- Database:      llm_gateway
-- Purpose:       回滚模型可信度体系的两张新表。
--
-- ★ 注意：provider_quality_profiles 的 reliability_score / calculated_at
--   **不回滚** —— 它们是 435 遗漏、pg_dump 基线里本来就有的列，补齐属于
--   修缺陷而不是本轮新增的结构。回滚它们会让只跑迁移的库退回到
--   "INSERT 必 42703" 的状态。
-- ===========================================================================

SET lock_timeout = '2s';
SET statement_timeout = '30min';

DROP TABLE IF EXISTS public.provider_model_credibility_runs;
DROP TABLE IF EXISTS public.provider_model_credibility;
