-- Migration Rollback: 977_pi_error_analysis_semantics
-- Purpose: 回滚 977 的四语义订正（把语义面恢复到 971 的形态）
-- Author: System
-- Date: 2026-11-17
--
-- ★ 回滚口径：**恢复 971 的语义**，不是「删对象」——
--   977 是纯语义订正（零建表 / 零列变更 / 零删表）⇒ 它的「回滚」就是把语义面
--   改回 971 当时在役的形态。
-- ★ ★ 回滚代价如实登记（977 存在的**全部意义**就是修掉这四件事）：
--   ① `cleanup_old_error_analysis` 回到 971 的函数体 ⇒ **两个缺陷一并回来**：
--        · `RETURNING * INTO` 一个标量 ⇒ 删到 ≥2 行时 PL/pgSQL 抛
--          「query returned more than one row」⇒ 清理事务整体回滚；
--        · 判据依赖 `auto_applied = TRUE AND human_verified = TRUE`，而全仓
--          Go 代码**从不写这两列** ⇒ 清理器恒 no-op。
--      ⇒ 若只想撤销**结构面**（唯一索引）而保住语义面，**手工跳过第 1 段**即可。
--   ② `v_error_analysis_stats` 回到 971 的定义 ⇒ 统计重新按 (队列 × 结果)
--      的连接行计数 ⇒ 有一对多结果时 total_errors / total_tokens_used 虚高。
--   ③ 唯一索引被删 ⇒ queue_id 重新允许多条结果。
--      ★ **已去重的行不会回来**：去重是**数据订正**、不可逆（本 down 不做任何
--        数据恢复）—— 971→977 的去重把同一 queue_id 的多余分析行删掉了。
--   ④ 三条 L2 规则的 pattern 回到种子形态 ⇒ "root cause" / "/root" /
--      "the operating system as a whole (see below)" 等普通文本重新命中
--      severity 9/10。
--
-- ★ 幂等：全部 IF EXISTS / CREATE OR REPLACE / ON CONFLICT ⇒ 可重放。

-- 1. 函数体回到 971 形态（★ 代价见文件头；想只回滚结构面就跳过本段）
CREATE OR REPLACE FUNCTION public.cleanup_old_error_analysis(retention_days INTEGER DEFAULT 90)
RETURNS TABLE(deleted_queue_count BIGINT, deleted_result_count BIGINT) AS $$
DECLARE
    v_deleted_results BIGINT;
    v_deleted_queue BIGINT;
BEGIN
    -- 删除超过保留期的已分析结果（保留未解决的）
    DELETE FROM public.error_analysis_results
    WHERE created_at < NOW() - (retention_days || ' days')::INTERVAL
      AND auto_applied = TRUE
      AND human_verified = TRUE
    RETURNING * INTO v_deleted_results;

    GET DIAGNOSTICS v_deleted_results = ROW_COUNT;

    -- 删除对应的队列项（级联删除）
    DELETE FROM public.error_analysis_queue
    WHERE id NOT IN (SELECT queue_id FROM public.error_analysis_results)
      AND status IN ('analyzed', 'skipped')
      AND created_at < NOW() - (retention_days || ' days')::INTERVAL;

    GET DIAGNOSTICS v_deleted_queue = ROW_COUNT;

    deleted_result_count := v_deleted_results;
    deleted_queue_count := v_deleted_queue;

    RETURN NEXT;
END;
$$ LANGUAGE plpgsql;

-- 2. 统计视图回到 971 的定义（★ 代价见文件头）
CREATE OR REPLACE VIEW public.v_error_analysis_stats AS
SELECT
    q.error_type,
    COUNT(*) as total_errors,
    COUNT(CASE WHEN q.status = 'analyzed' THEN 1 END) as analyzed_count,
    COUNT(CASE WHEN q.status = 'pending' THEN 1 END) as pending_count,
    COUNT(CASE WHEN r.is_false_positive = TRUE THEN 1 END) as false_positive_count,
    AVG(r.confidence_score) as avg_confidence,
    AVG(r.analysis_duration_ms) as avg_analysis_time_ms,
    SUM(r.llm_tokens_used) as total_tokens_used,
    MAX(q.created_at) as last_error_at
FROM public.error_analysis_queue q
LEFT JOIN public.error_analysis_results r ON q.id = r.queue_id
GROUP BY q.error_type;

-- 3. 唯一索引回到 971 的形态（queue_id 重新允许多条结果）
DROP INDEX IF EXISTS public.idx_error_analysis_results_queue_uniq;

-- 4. 三条 L2 规则的 pattern 回到种子形态（★ 代价见文件头）
--    ★ 种子文件 315 / 364 不动（改它们＝改已 applied 迁移的字节 ⇒ 触发内容
--      指纹重放 ⇒ 部署 die）⇒ 回滚也只能以 UPDATE 的形式落。
--    ★ 与 up 同理先判表在不在场（315 不在两条投递通道的清单里 ⇒ 规则表不保证
--      存在）⇒ 裸 UPDATE 会 42P01 ⇒ 回滚脚本失败。
DO $$
BEGIN
    IF to_regclass('public.prompt_injection_rules') IS NULL THEN
        RAISE NOTICE '977 down: public.prompt_injection_rules 不在场 ⇒ 跳过 L2 规则 pattern 回退（无规则可改）';
        RETURN;
    END IF;

    UPDATE public.prompt_injection_rules
    SET pattern = '(?i)(grant|give).*admin|sudo|root'
    WHERE rule_name = 'privilege_escalation';

    UPDATE public.prompt_injection_rules
    SET pattern = '(?i)(exec|system|eval|shell).*\('
    WHERE rule_name = 'command_injection';

    UPDATE public.prompt_injection_rules
    SET pattern = '(?i)(exec|eval|system|subprocess|os\.system|child_process).*(\(|call|invoke)'
    WHERE rule_name = 'tool_abuse_exec';

END;
$$;

-- 5. 注释回到 971 形态
COMMENT ON FUNCTION public.cleanup_old_error_analysis IS '清理超过指定天数的已分析错误记录';
COMMENT ON VIEW public.v_error_analysis_stats IS '错误分析统计视图，用于监控和报告';

-- 6. 台账行清理（与 up 的 schema_migrations 自登记对称）
DELETE FROM public.schema_migrations WHERE version = '977';

-- Rollback complete
