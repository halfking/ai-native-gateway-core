-- ===========================================================================
-- File:          sql/migrations/startup/967_prompt_injection_llm_policy_columns.sql
-- Migration:     967
-- Purpose:       补齐 prompt_injection_policies 的三列（LLM 判官的成本/失败
--                模式控制列）——2026-11-16 12h 修订审计轮 P1 根修。
--
-- ── 背景（★ 一条典型的「投影加了列、迁移没跟上」＝静默全放行）────────────
--
-- 5a73df106（2026-11-15 12h 审计轮 P2#6 修复）在
-- domains/promptinjection/detector.go 的 detectorPolicySelectCols 投影里
-- 用了三列：
--
--     COALESCE(llm_detection_fail_closed, false),
--     COALESCE(llm_detection_sample_rate, 1.0),
--     COALESCE(llm_detection_timeout_sec, 30)
--
-- ★ 但这三个列名**在仓内只出现在这一个 .go 文件里**——全仓 *.sql 零命中：
--   既不在 sql/objects/tables/prompt_injection_policies.sql 的表定义里，
--   也不在任何 startup 迁移里 ⇒ **读侧 SQL 必然报 42703 column does not
--   exist**。
--
-- ★ 后果链（★ 这是本条被判 P1 的原因，不是「配置读不到」的轻量问题）：
--   getPolicy 查询报错
--     → Detect 返回 "failed to get policy"
--       → domains/hooks/promptinjection/hook.go Execute 记一行
--         env.Metadata["prompt_injection_error"] 后 **return nil**
--           → governance verdict = allow
--             → **每一次请求都放行，提示词注入检测域整体静默失效**
--   ★ 提交说明里写的「读侧 COALESCE 已防 NULL」是**误判**：COALESCE 挡的是
--     「列存在但值为 NULL」，挡不住「列根本不存在」⇒ 而不存在的列正是
--     本次缺陷的形态。
--
-- ── 修法 ────────────────────────────────────────────────────────────────
--
--   补一条 startup 正典通道迁移，用 **ADD COLUMN IF NOT EXISTS** 加三列
--   （幂等：startup 通道每次部署无脑重放是安全默认）。
--
--   ★ 为什么不改 baseline dump：deploy/sql/schemas/baseline/01-schema.sql
--     与 installer/.../embeddata/01-schema.sql 是**活库 pg_dump 的产物**
--     （scripts/checks/sync-baseline-copies.sh 头注明令「不要手改这三份
--     dump 里的任何一行」——手改会被下一次 dump 覆盖，且会让 dump 不再等于
--     真库形状）⇒ 加列只能走迁移通道。
--     ★ 全新的安装顺序是：baseline schema → startup 迁移依次执行 ⇒
--       本迁移同样覆盖全新安装面（不需要额外改 baseline）。
--
-- ── 三列语义（与 detector.go 的 Policy 字段一一对应）──────────────────────
--
--   llm_detection_fail_closed  boolean      LLM 判官不可用时是否阻断
--                                        （默认 false ＝ fail-open，保持
--                                          5a73df106 引入时的既有默认）
--   llm_detection_sample_rate  double      LLM 判官采样率 0.0~1.0
--                                        （★ 0 = **禁用判官**；
--                                          1.0 = 全量）
--   llm_detection_timeout_sec  integer     LLM 判官调用超时（秒，默认 30）
--
-- ── 幂等性与回放 ────────────────────────────────────────────────────────
--
--   · ADD COLUMN IF NOT EXISTS ×3 ⇒ 重放无副作用。
--   · 加约束用 CHECK，且用 DO $$ ... IF NOT EXISTS ... $$ 包裹
--     （ADD CONSTRAINT 没有 IF NOT EXISTS 语法）。
--   · 本文件不含不可事务化语句（CREATE INDEX CONCURRENTLY 等）⇒ 可单事务
--     执行，不需要 dbinit:no-transaction 标记。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- ── 三列（幂等）────────────────────────────────────────────────────────
ALTER TABLE public.prompt_injection_policies
    ADD COLUMN IF NOT EXISTS llm_detection_fail_closed boolean DEFAULT false;
ALTER TABLE public.prompt_injection_policies
    ADD COLUMN IF NOT EXISTS llm_detection_sample_rate double precision DEFAULT 1.0;
ALTER TABLE public.prompt_injection_policies
    ADD COLUMN IF NOT EXISTS llm_detection_timeout_sec integer DEFAULT 30;

-- ── 取值域约束（幂等：DO $$ 包裹，仅在约束缺失时创建）────────────────────
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'prompt_injection_policies_llm_sample_rate_check'
          AND conrelid = 'public.prompt_injection_policies'::regclass
    ) THEN
        ALTER TABLE public.prompt_injection_policies
            ADD CONSTRAINT prompt_injection_policies_llm_sample_rate_check
            CHECK (llm_detection_sample_rate IS NULL
                   OR (llm_detection_sample_rate >= 0.0
                       AND llm_detection_sample_rate <= 1.0));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'prompt_injection_policies_llm_timeout_sec_check'
          AND conrelid = 'public.prompt_injection_policies'::regclass
    ) THEN
        ALTER TABLE public.prompt_injection_policies
            ADD CONSTRAINT prompt_injection_policies_llm_timeout_sec_check
            CHECK (llm_detection_timeout_sec IS NULL
                   OR llm_detection_timeout_sec > 0);
    END IF;
END $$;

-- ── 注释（文档面；COMMENT 本身可重放）──────────────────────────────────
COMMENT ON COLUMN public.prompt_injection_policies.llm_detection_fail_closed IS
    'LLM judge fail-closed switch: when the judge call fails, block instead of falling back to rule score (2026-11-16 12h audit P1 fix, migration 967). Default false = fail-open.';
COMMENT ON COLUMN public.prompt_injection_policies.llm_detection_sample_rate IS
    'LLM judge sampling rate in [0,1]: 0 = judge disabled, 1.0 = every request (2026-11-16 12h audit P1 fix, migration 967).';
COMMENT ON COLUMN public.prompt_injection_policies.llm_detection_timeout_sec IS
    'LLM judge per-call timeout in seconds (2026-11-16 12h audit P1 fix, migration 967). Default 30.';

-- ── 记账（≥952 自登记纪律）────────────────────────────────────────────
INSERT INTO public.schema_migrations (version, description)
VALUES ('967', 'prompt_injection_policies: add llm_detection_fail_closed / llm_detection_sample_rate / llm_detection_timeout_sec columns. Fixes 12h-audit P1 where the detector projection referenced three columns that no migration ever created, so getPolicy failed and the whole prompt-injection domain silently failed open.')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
