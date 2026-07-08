-- Migration 364 Down: Revert Prompt Injection Detection Enhancement

-- 删除触发器
DROP TRIGGER IF EXISTS update_canary_tokens_modtime ON canary_tokens;
DROP TRIGGER IF EXISTS update_severity_action_matrix_modtime ON severity_action_matrix;
DROP TRIGGER IF EXISTS update_prompt_injection_llm_engines_modtime ON prompt_injection_llm_engines;

-- 删除视图
DROP VIEW IF EXISTS prompt_injection_approval_stats;
DROP VIEW IF EXISTS prompt_injection_stats_enhanced;

-- 删除表（按依赖顺序）
DROP TABLE IF EXISTS injection_attack_vectors;
DROP TABLE IF EXISTS canary_tokens;
DROP TABLE IF EXISTS severity_action_matrix;
DROP TABLE IF EXISTS prompt_injection_llm_engines;

-- 删除枚举类型
DROP TYPE IF EXISTS injection_action;
DROP TYPE IF EXISTS injection_category;

-- 删除触发器函数
DROP FUNCTION IF EXISTS update_modified_column();
