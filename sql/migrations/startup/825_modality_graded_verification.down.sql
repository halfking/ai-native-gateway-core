-- ===========================================================================
-- File:          sql/migrations/startup/825_modality_graded_verification.sql.down.sql
-- Migration:     825 (down)
-- Database:      llm_gateway
--
-- 回滚顺序与 up 相反：先删视图（它依赖证据表），再删证据表，最后摘
-- models_canonical 上新加的三列与守卫约束。
--
-- ⚠ **本 down 会丢弃全部已积累的核实证据**：
--    model_modality_verification 里的分级判词（可承载 / 真能读）与
--    models_canonical.modality_source / modality_verified_at /
--    modality_evidence 一起消失。这三列从不存在，所以回滚后：
--      - 定时核实任务（bg/modality_verification.go）失去到期判据
--        （modality_verified_at 恒 NULL ⇒ 全表被当成「未核实」）；
--      - 已由语义正证据升级的模型**不会退回 text**——modality 本身
--        留在升级后的值，只是出处标记没了，于是 discovery 的规则重推
--        重新获得对它的写权限（这正是 825 up 的第 (c) 条堵掉的洞）。
--    生产回滚前先确认这些降级/升级结论已被业务接受。
--
-- 幂等：DROP ... IF EXISTS / IF EXISTS，可安全重放。
-- ===========================================================================
BEGIN;

DROP VIEW IF EXISTS public.v_model_modality_verdict;

DROP TABLE IF EXISTS public.model_modality_verification;

ALTER TABLE public.models_canonical
    DROP CONSTRAINT IF EXISTS models_canonical_modality_source_check;

ALTER TABLE public.models_canonical
    DROP COLUMN IF EXISTS modality_evidence,
    DROP COLUMN IF EXISTS modality_verified_at,
    DROP COLUMN IF EXISTS modality_source;

COMMIT;
