-- Migration 364 Down: Revert Prompt Injection Detection Enhancement
-- Date: 2026-07-09
-- Purpose: 回滚 364_prompt_injection_enhanced 的所有变更

-- ============================================================
-- 1. 删除 RLS 策略
-- ============================================================
DROP POLICY IF EXISTS llm_engines_tenant ON prompt_injection_llm_engines;
DROP POLICY IF EXISTS severity_matrix_tenant ON severity_action_matrix;
DROP POLICY IF EXISTS canary_tokens_tenant ON canary_tokens;
DROP POLICY IF EXISTS attack_vectors_tenant ON injection_attack_vectors;
DROP POLICY IF EXISTS llm_engines_super_admin ON prompt_injection_llm_engines;
DROP POLICY IF EXISTS severity_matrix_super_admin ON severity_action_matrix;
DROP POLICY IF EXISTS canary_tokens_super_admin ON canary_tokens;
DROP POLICY IF EXISTS attack_vectors_super_admin ON injection_attack_vectors;

-- ============================================================
-- 2. 禁用 RLS
-- ============================================================
ALTER TABLE IF EXISTS prompt_injection_llm_engines DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS severity_action_matrix DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS canary_tokens DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS injection_attack_vectors DISABLE ROW LEVEL SECURITY;

-- ============================================================
-- 3. 删除触发器
-- ============================================================
DROP TRIGGER IF EXISTS update_canary_tokens_modtime ON canary_tokens;
DROP TRIGGER IF EXISTS update_severity_action_matrix_modtime ON severity_action_matrix;
DROP TRIGGER IF EXISTS update_prompt_injection_llm_engines_modtime ON prompt_injection_llm_engines;

-- ============================================================
-- 4. 删除视图
-- ============================================================
DROP VIEW IF EXISTS prompt_injection_approval_stats;
DROP VIEW IF EXISTS prompt_injection_stats_enhanced;

-- ============================================================
-- 5. 删除新创建的表（按依赖顺序）
-- ============================================================
DROP TABLE IF EXISTS injection_attack_vectors CASCADE;
DROP TABLE IF EXISTS canary_tokens CASCADE;
DROP TABLE IF EXISTS severity_action_matrix CASCADE;
DROP TABLE IF EXISTS prompt_injection_llm_engines CASCADE;

-- ============================================================
-- 6. 删除 prompt_injection_policies 新增的列
-- ============================================================
ALTER TABLE prompt_injection_policies
    DROP COLUMN IF EXISTS detection_timeout_ms,
    DROP COLUMN IF EXISTS auto_learn_enabled,
    DROP COLUMN IF EXISTS max_input_length,
    DROP COLUMN IF EXISTS content_replacement_strategy,
    DROP COLUMN IF EXISTS vector_similarity_threshold,
    DROP COLUMN IF EXISTS enable_vector_similarity,
    DROP COLUMN IF EXISTS enable_canary_detection,
    DROP COLUMN IF EXISTS enable_llm_detection,
    DROP COLUMN IF EXISTS llm_engine_id;

-- ============================================================
-- 7. 删除 prompt_injection_rules 新增的列
-- ============================================================
ALTER TABLE prompt_injection_rules
    DROP COLUMN IF EXISTS false_positive_rate,
    DROP COLUMN IF EXISTS examples,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS is_system,
    DROP COLUMN IF EXISTS action_override,
    DROP COLUMN IF EXISTS category_new;

-- ============================================================
-- 8. 删除 prompt_injection_detections 新增的列
-- ============================================================
ALTER TABLE prompt_injection_detections
    DROP COLUMN IF EXISTS original_content_hash,
    DROP COLUMN IF EXISTS replaced_content,
    DROP COLUMN IF EXISTS approval_id,
    DROP COLUMN IF EXISTS similar_attack_id,
    DROP COLUMN IF EXISTS canary_token_leaked,
    DROP COLUMN IF EXISTS categories,
    DROP COLUMN IF EXISTS llm_reason,
    DROP COLUMN IF EXISTS llm_confidence,
    DROP COLUMN IF EXISTS llm_engine_id;

-- ============================================================
-- 9. 删除新创建的索引
-- ============================================================
DROP INDEX IF EXISTS idx_detections_categories;
DROP INDEX IF EXISTS idx_detections_approval;
DROP INDEX IF EXISTS idx_llm_engines_tenant;
DROP INDEX IF EXISTS idx_canary_tokens_tenant;
DROP INDEX IF EXISTS idx_attack_vectors_tenant;
DROP INDEX IF EXISTS idx_attack_vectors_categories;
DROP INDEX IF EXISTS idx_attack_vectors_embedding;

-- ============================================================
-- 10. 删除枚举类型
-- ============================================================
DROP TYPE IF EXISTS injection_action CASCADE;
DROP TYPE IF EXISTS injection_category CASCADE;

-- ============================================================
-- 11. 删除触发器函数
-- ============================================================
DROP FUNCTION IF EXISTS update_modified_column();

-- ============================================================
-- 12. 删除 364 插入的预定义规则（只删除 364 新增的）
-- ============================================================
DELETE FROM prompt_injection_rules WHERE rule_name IN (
    'instruction_override_ignore',
    'instruction_override_new_rules',
    'instruction_override_system_prompt',
    'data_exfiltration_http',
    'data_exfiltration_tool',
    'resource_exhaustion_repeat',
    'resource_exhaustion_long',
    'social_engineering_urgency',
    'social_engineering_authority',
    'unicode_homoglyph',
    'unicode_rtl_override',
    'unicode_zero_width',
    'tool_abuse_exec',
    'tool_abuse_import',
    'tool_abuse_file',
    'multi_turn_setup',
    'multi_turn_reference',
    'payload_smuggling_base64',
    'payload_smuggling_markdown',
    'context_manipulation_role',
    'context_manipulation_persona'
);
