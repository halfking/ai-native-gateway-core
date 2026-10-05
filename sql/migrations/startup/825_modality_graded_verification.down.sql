-- ===========================================================================
-- File:          sql/migrations/startup/825_modality_graded_verification.down.sql
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
-- ⚠ **up 里补的 models_canonical_pkey 主键故意不删**：
--    825 的 up 段给 public.models_canonical 补了 models_canonical_pkey
--    （真表原本既无主键也无唯一约束，见 up 的前置自愈段）。down 不摘它，
--    理由有二，都指向同一个方向——**删掉它可能破坏本迁移之外的东西**：
--      1. 回滚时无法分辨这条主键是「本迁移加的」还是「环境上本来就有的」
--         （up 段是幂等的，环境上原本有主键时它会短路）。删一个本来就在的
--         主键，是破坏而不是回滚。
--      2. 留着它是无害的：id 本来就是标准代理键写法（bigint NOT NULL +
--         序列默认），一张代理键表没有主键才是异常。删掉它则下一次 825
--         重跑又要靠 up 段补回来。
--    ⇒ 残留一条主键，好过删掉一个不属于本迁移的约束。
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
