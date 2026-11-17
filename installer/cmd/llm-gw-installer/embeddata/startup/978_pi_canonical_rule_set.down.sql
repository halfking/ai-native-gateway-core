-- Migration: 978_pi_canonical_rule_set (down)
-- Purpose: 回退「canonical 规则集补种」里**由本件引入**的那部分
-- Date: 2026-11-17
--
-- ★ 为什么只删 2 条、而不是把 25 条全删：
--   978 用 `ON CONFLICT (rule_name) DO NOTHING` 补种 ⇒ 它**可能**新增 0..25 条
--   （存量库里 23 条早已在场 ⇒ 实际只新增 2 条）。若 down 把 25 条全删，
--   在 245 这类环境上会**连带删掉 364 时代就有的 23 条** ⇒ 那是「回退超出本件
--   效果范围」的破坏性动作。
--   ⇒ 本件 down 的语义 = **把环境退回「364 时代的规则集形态」**：
--     删掉本件唯二引入的两条（privilege_escalation / command_injection），
--     其余 23 条（364 的 21 + ensureRulesTable 的 2）保持不动。
--   ⇒ 在**全新安装**上回退 978 之后，规则表仍有 23 条（= 364 时代形态）——
--     这与「未跑 978 的存量库」形态一致 ⇒ **可复算**。
--
-- ★ 表不在场 ⇒ 不报错（幂等）。
DO $$
BEGIN
    IF to_regclass('public.prompt_injection_rules') IS NULL THEN
        RAISE NOTICE '978 down: public.prompt_injection_rules 不在场 ⇒ 无需回退';
        RETURN;
    END IF;

    DELETE FROM public.prompt_injection_rules
    WHERE rule_name IN ('privilege_escalation', 'command_injection');
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '978';
