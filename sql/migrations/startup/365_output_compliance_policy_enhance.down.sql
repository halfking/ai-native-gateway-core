-- Migration 365 Down: Revert Output Compliance Policy Enhancement
-- Date: 2026-07-09
-- Purpose: 回滚 365_output_compliance_policy_enhance 的所有变更

-- 1. 删除触发器
DROP TRIGGER IF EXISTS update_output_compliance_custom_keywords_modtime ON output_compliance_custom_keywords;

-- 2. 删除函数
DROP FUNCTION IF EXISTS update_output_compliance_modified_column();

-- 3. 删除 RLS 策略
DROP POLICY IF EXISTS output_compliance_custom_keywords_tenant ON output_compliance_custom_keywords;
DROP POLICY IF EXISTS output_compliance_review_queue_tenant ON output_compliance_review_queue;
DROP POLICY IF EXISTS output_compliance_feedback_tenant ON output_compliance_feedback;
DROP POLICY IF EXISTS output_compliance_custom_keywords_super_admin ON output_compliance_custom_keywords;
DROP POLICY IF EXISTS output_compliance_review_queue_super_admin ON output_compliance_review_queue;
DROP POLICY IF EXISTS output_compliance_feedback_super_admin ON output_compliance_feedback;

-- 4. 禁用 RLS
ALTER TABLE IF EXISTS output_compliance_custom_keywords DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS output_compliance_review_queue DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS output_compliance_feedback DISABLE ROW LEVEL SECURITY;

-- 5. 删除新增的表（按依赖顺序）
DROP TABLE IF EXISTS output_compliance_feedback CASCADE;
DROP TABLE IF EXISTS output_compliance_review_queue CASCADE;
DROP TABLE IF EXISTS output_compliance_custom_keywords CASCADE;

-- 6. 删除 output_compliance_audit 新增的列
ALTER TABLE output_compliance_audit
    DROP COLUMN IF EXISTS review_queue_id,
    DROP COLUMN IF EXISTS skill_suggestion,
    DROP COLUMN IF EXISTS alert_sent,
    DROP COLUMN IF EXISTS exception_scope,
    DROP COLUMN IF EXISTS exception_matched,
    DROP COLUMN IF EXISTS rule_triggered,
    DROP COLUMN IF EXISTS policy_id;

-- 7. 删除 output_compliance_policies 新增的列
ALTER TABLE output_compliance_policies
    DROP COLUMN IF EXISTS policy_name,
    DROP COLUMN IF EXISTS retention_days,
    DROP COLUMN IF EXISTS auto_threshold_tuning_enabled,
    DROP COLUMN IF EXISTS skill_generation_enabled,
    DROP COLUMN IF EXISTS feedback_loop_enabled,
    DROP COLUMN IF EXISTS auto_review_queue_enabled,
    DROP COLUMN IF EXISTS sampling_rate,
    DROP COLUMN IF EXISTS alert_aggregation_window_minutes,
    DROP COLUMN IF EXISTS realtime_alert_enabled,
    DROP COLUMN IF EXISTS notification_channels,
    DROP COLUMN IF EXISTS exception_rules,
    DROP COLUMN IF EXISTS whitelist_keywords,
    DROP COLUMN IF EXISTS redact_format_overrides,
    DROP COLUMN IF EXISTS toxic_replacement,
    DROP COLUMN IF EXISTS redact_password,
    DROP COLUMN IF EXISTS redact_jwt,
    DROP COLUMN IF EXISTS redact_bank_card,
    DROP COLUMN IF EXISTS block_message,
    DROP COLUMN IF EXISTS action_on_instruction_injection_response,
    DROP COLUMN IF EXISTS action_on_jailbreak_response,
    DROP COLUMN IF EXISTS action_on_internal_ip,
    DROP COLUMN IF EXISTS action_on_secrets,
    DROP COLUMN IF EXISTS alert_threshold_severity,
    DROP COLUMN IF EXISTS internal_ip_threshold,
    DROP COLUMN IF EXISTS secrets_threshold,
    DROP COLUMN IF EXISTS check_instruction_injection_response,
    DROP COLUMN IF EXISTS check_jailbreak_response,
    DROP COLUMN IF EXISTS check_internal_ip,
    DROP COLUMN IF EXISTS check_secrets,
    DROP COLUMN IF EXISTS toxicity_engine,
    DROP COLUMN IF EXISTS pii_engine,
    DROP COLUMN IF EXISTS llm_engine_id;

-- 8. 记录迁移历史
COMMENT ON TABLE output_compliance_policies IS '输出合规策略表 (回滚了 365 的扩展)';
