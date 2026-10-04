-- ===========================================================================
-- File:          sql/migrations/startup/827_modality_verification_progress_view.down.sql
-- Migration:     827 (down)
-- Database:      llm_gateway
-- Purpose:        删掉 827 建的只读视图。
--
-- 本迁移 up 侧**没有改任何表、没有加任何列、没删任何数据**，
-- 所以 down 侧也只删视图。回滚它不会丢核实结论 ——
-- 那些结论在 825 建的 model_modality_verification 里，与本视图无关。
--
-- 幂等：DROP VIEW IF EXISTS，可安全重放。
-- ===========================================================================
BEGIN;

DROP VIEW IF EXISTS public.v_model_modality_verification_rollup;

DROP VIEW IF EXISTS public.v_model_modality_verification_progress;

COMMIT;
