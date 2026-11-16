-- ===========================================================================
-- File:          sql/migrations/startup/967_prompt_injection_llm_policy_columns.down.sql
-- Migration:     967 (down)
-- Purpose:       回退 967——删除 prompt_injection_policies 的三列（LLM 判官的
--                成本/失败模式控制列）。
--
-- ★ 回滚后果如实登记（★ 2026-11-16 12h 审计轮 0820 **订正**：本段原写
--   「回退即回到本条 P1 的形态 ⇒ 整个注入检测域静默失效 ⇒ 回滚必须与
--   『回退 detector.go 投影』同批进行」——**该结论与代码事实矛盾**，
--   是 6d93373f1 同一笔提交里的两处注释互相打架，本轮以代码为准订正）：
--
--   事实：同笔提交 6d93373f1 在 detector.go 里引入了**降级读面**
--     （detectorPolicySelectColsLegacy + getPolicy 遇42703 自动降级重读
--     + 三列回落默认值 + WARN 痕迹）。
--     ⇒ 三列被删后，getPolicy 的主投影会报 42703，但**降级读面会兜住**：
--       用旧投影重读 ⇒ 规则层照常检测，只有三个判官旋钮取默认值
--       （fail-open / 全量 / 30s）⇒ **检测能力不丢**，且留 WARN痕迹（不静默）。
--     ⇒ 所以「回滚 967 ⇒ 注入检测域静默全放行」是**不成立的**。
--
--   真正的回滚后果（这才是本段该写的）：
--     ① 三个判官旋钮**失去持久化**：llm_detection_fail_closed / sample_rate /
--        timeout_sec 的配置随列删除而消失 ⇒ 下次967 重新应用时回落默认值
--        （fail-open / 全量 / 30s）⇒ 运维此前的「关掉判官省钱」等配置丢失。
--     ② 行为差异：降级读面下 sample_rate 恒为 1.0（全量）⇒ LLM 判官**成本
--        回到全量**；fail_closed 恒为 false ⇒ 判官故障时**放行**（fail-open）。
--        ⇒ 回滚不是「无影响」，是「配置与成本模型回到迁移前」。
--     ③ ⚠️ down 会执行 `DELETE FROM public.schema_migrations WHERE version='967'`
--        ⇒ 回滚**不是幂等 no-op**，需按部署序列重放该迁移才能恢复记账。
--
--   ⇒ 订正后的结论：**可以单独回滚 967**（检测域不会静默失效），但必须
--     接受上述①②③ 的后果；不需要与「回退 detector.go 投影」同批进行。
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
