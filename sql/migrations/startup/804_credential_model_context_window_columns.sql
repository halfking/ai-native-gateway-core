-- ===========================================================================
-- File:          sql/migrations/startup/804_credential_model_context_window_columns.sql
-- Migration:     804
-- Database:      llm_gateway
-- Purpose:       credential_model_bindings 补 context_window_source /
--                context_window_updated_at（682 视图重建的前置列集对账）
--
-- Status:        active
-- Idempotent:    YES (ADD COLUMN IF NOT EXISTS，存量库零行为变化)
-- Dependencies:  469_context_window_override.sql（models_canonical 同款三列）
--
-- Background:
--   credential_model_bindings 的 context_window 三列由 523 引入
--   （override/source/updated_at）。baseline 01-schema 只带了 override 一列，
--   source/updated_at 两列从未有过 canonical 创建者——deploy 链（V359 血统）
--   的库由 523 补齐，canonical 新装链却拿不到：682 重建 model_offers 视图时
--   按 cmb.context_window_source/_updated_at 显式引用，fresh-install e2e
--   实证 42703 中断（2026-10-01）。
--
--   523 本体无法直接注册进 fresh 序列：其 CREATE OR REPLACE VIEW 是
--   2026-07 形态，比 baseline 里 dump 的 model_offers 列集更窄，
--   CREATE OR REPLACE VIEW 不允许缩列（cannot drop columns from view，
--   e2e 实证）。本迁移只收编列集（类型/默认值与生产实测逐列一致），
--   视图与触发器由 baseline 和 678/682 的 DROP+CREATE 重建负责。
-- ===========================================================================

BEGIN;

ALTER TABLE IF EXISTS public.credential_model_bindings
    ADD COLUMN IF NOT EXISTS context_window_override integer,
    ADD COLUMN IF NOT EXISTS context_window_source text DEFAULT 'catalog',
    ADD COLUMN IF NOT EXISTS context_window_updated_at timestamp with time zone;

COMMIT;
