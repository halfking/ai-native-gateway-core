-- Migration: 979_pi_analysis_applied_actions
-- Purpose: PI 错误分析「一键应用修复」的审计留痕列
-- Phase: PI 分析管理面优化轮（2026-11-18）
-- Author: System
-- Date: 2026-11-18
--
-- ★ 背景：971 建出的 error_analysis_results 自带
--     auto_applied BOOLEAN / applied_at TIMESTAMPTZ / applied_by TEXT
--   三列，但**全仓 Go 代码零写入方**（grep auto_applied --include=*.go
--   在写面 0 处命中）⇒「分析建议 → 落地修复」这条通道从未存在。
--   本轮把「点击应用修复」接活（admin/pi_analysis_fix_apply.go），
--   应用时需要把**实际执行了哪些动作 + 每条规则的旧值快照**留痕，
--   否则：
--     · 出了问题无法回答「当时到底改了什么」；
--     · 回滚（revert）没有旧值可恢复。
--   ⇒ 新增 applied_actions JSONB 一列承载这份明细。
--
-- ★ 形状（由 admin/pi_analysis_fix_apply.go 写入）：
--     [
--       {
--         "action": "disable_rule" | "adjust_severity" | "update_pattern",
--         "rule_name": "<规则名>",
--         "old": { "enabled": true, "severity": 9, "pattern": "..." },
--         "new": { "enabled": false },
--         "reason": "<来自 LLM 建议>"
--       }, ...
--     ]
--
-- ★ 锁面：单列 ADD COLUMN（NULLable、无默认值回填）在 PG 11+ 是
--   元数据级操作（不重写表）；error_analysis_results 行数量级 10^0~10^3。
--   仍声明 5s lock_timeout / 10min statement_timeout（与 977/978 同口径）
--   ⇒ 抢不到锁就快速失败，而不是半应用。
--
-- ★ 幂等：ADD COLUMN IF NOT EXISTS + 台账 ON CONFLICT DO UPDATE
--   ⇒ 内容指纹重放零冲突。
--
-- ★ 表在场守卫：error_analysis_results 由 971 建出。「没跑过 971」的库
--   （异常形态）上裸 ALTER 会 42P01 ⇒ 迁移失败 ⇒ 部署 die。
--   正常安装链下 971 一定先于 979（本清单有序），守卫只是防御性口径
--   （与 977 对 prompt_injection_rules 的守卫同款）。

BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

DO $$
BEGIN
    IF to_regclass('public.error_analysis_results') IS NULL THEN
        RAISE NOTICE '979: public.error_analysis_results 不在场（971 未应用）⇒ 跳过 applied_actions 加列';
        RETURN;
    END IF;

    ALTER TABLE public.error_analysis_results
        ADD COLUMN IF NOT EXISTS applied_actions JSONB;
END;
$$;

-- ★ 双账本纪律：211 的 public.schema_migrations 与 public.gateway_db_revision_sequences
--   都必须有 979 的行，否则「账本说没做」⇒ 后续按账本判断的人会踩空。
--   （public.gateway_db_revision_sequences 那行由 deploy 通道自己按内容指纹写；
--   本件只负责 public.schema_migrations 这一本，与 971/977 的形态一致。）
INSERT INTO public.schema_migrations (version, description)
VALUES ('979', 'pi_analysis_applied_actions: 应用修复的动作明细与旧值快照列（audit trail for apply/revert）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
