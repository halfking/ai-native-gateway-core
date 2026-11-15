-- ===========================================================================
-- File:          sql/migrations/startup/967_prompt_injection_llm_policy_columns.down.sql
-- Migration:     967 (down)
-- Purpose:       回退 967——删除 prompt_injection_policies 的三列（LLM 判官的
--                成本/失败模式控制列）。
--                回滚后果如实登记：回退后 **detector.go 的投影会再次引用
--                不存在的列** ⇒ getPolicy 报 42703 ⇒ Detect 报错 ⇒
--                hook 降级为 verdict=allow ⇒ 整个提示词注入检测域静默失效
--                （= 回退即回到本条 P1 的形态）⇒ ★ 回滚必须与「回退
--                detector.go 投影」同批进行，不得单独回滚本迁移。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE public.prompt_injection_policies
    DROP CONSTRAINT IF EXISTS prompt_injection_policies_llm_sample_rate_check;
ALTER TABLE public.prompt_injection_policies
    DROP CONSTRAINT IF EXISTS prompt_injection_policies_llm_timeout_sec_check;

ALTER TABLE public.prompt_injection_policies
    DROP COLUMN IF EXISTS llm_detection_fail_closed;
ALTER TABLE public.prompt_injection_policies
    DROP COLUMN IF EXISTS llm_detection_sample_rate;
ALTER TABLE public.prompt_injection_policies
    DROP COLUMN IF EXISTS llm_detection_timeout_sec;

DELETE FROM public.schema_migrations WHERE version = '967';

COMMIT;
