-- Migration 365: 输出合规策略增强
--
-- Purpose:
--   为输出合规检测模块补齐企业级配置所需的策略字段：
--   1. LLM 多模式检测引擎选择
--   2. 密钥 / 内部 IP / JWT / 越狱响应 / 指令注入响应检测开关
--   3. 身份感知例外规则（数据所有者/角色/应用级放行）
--   4. 实时告警通知通道
--   5. 人工复核、反馈闭环、安全改写/技能生成开关
--   6. 采样率、日志保留、审计字段
--
-- Date: 2026-07-09

-- 1. 扩展现有 output_compliance_policies 表
ALTER TABLE output_compliance_policies
    -- 模型/引擎选择
    ADD COLUMN IF NOT EXISTS llm_engine_id INT,                  -- 关联 prompt_injection_llm_engines.id，用于幻觉/偏见评估
    ADD COLUMN IF NOT EXISTS pii_engine VARCHAR(20) DEFAULT 'regex' CHECK (pii_engine IN ('regex', 'model', 'hybrid')),
    ADD COLUMN IF NOT EXISTS toxicity_engine VARCHAR(20) DEFAULT 'keyword' CHECK (toxicity_engine IN ('keyword', 'model', 'hybrid')),

    -- 额外的检测类型开关
    ADD COLUMN IF NOT EXISTS check_secrets BOOLEAN DEFAULT true,            -- API Key / 私钥 / Token
    ADD COLUMN IF NOT EXISTS check_internal_ip BOOLEAN DEFAULT true,        -- 内网 IP / RFC1918
    ADD COLUMN IF NOT EXISTS check_jailbreak_response BOOLEAN DEFAULT false, -- 越狱响应检测
    ADD COLUMN IF NOT EXISTS check_instruction_injection_response BOOLEAN DEFAULT false, -- 指令注入响应检测

    -- 阈值与灵敏度
    ADD COLUMN IF NOT EXISTS secrets_threshold DECIMAL(3,2) DEFAULT 0.7,
    ADD COLUMN IF NOT EXISTS internal_ip_threshold DECIMAL(3,2) DEFAULT 0.7,
    ADD COLUMN IF NOT EXISTS alert_threshold_severity INT DEFAULT 7 CHECK (alert_threshold_severity >= 1 AND alert_threshold_severity <= 10),

    -- 新增响应动作
    ADD COLUMN IF NOT EXISTS action_on_secrets VARCHAR(20) DEFAULT 'redact' CHECK (action_on_secrets IN ('log', 'warn', 'redact', 'block')),
    ADD COLUMN IF NOT EXISTS action_on_internal_ip VARCHAR(20) DEFAULT 'redact' CHECK (action_on_internal_ip IN ('log', 'warn', 'redact', 'block')),
    ADD COLUMN IF NOT EXISTS action_on_jailbreak_response VARCHAR(20) DEFAULT 'block' CHECK (action_on_jailbreak_response IN ('log', 'warn', 'block')),
    ADD COLUMN IF NOT EXISTS action_on_instruction_injection_response VARCHAR(20) DEFAULT 'block' CHECK (action_on_instruction_injection_response IN ('log', 'warn', 'block')),
    ADD COLUMN IF NOT EXISTS block_message TEXT DEFAULT '响应因合规策略被阻断',

    -- 脱敏扩展
    ADD COLUMN IF NOT EXISTS redact_bank_card BOOLEAN DEFAULT false,
    ADD COLUMN IF NOT EXISTS redact_jwt BOOLEAN DEFAULT true,
    ADD COLUMN IF NOT EXISTS redact_password BOOLEAN DEFAULT true,
    ADD COLUMN IF NOT EXISTS toxic_replacement VARCHAR(100) DEFAULT '[内容已过滤]',
    ADD COLUMN IF NOT EXISTS redact_format_overrides JSONB DEFAULT '{}',     -- {"email":"[EMAIL]", "phone":"[PHONE]"}

    -- 白名单与例外
    ADD COLUMN IF NOT EXISTS whitelist_keywords TEXT[] DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS exception_rules JSONB DEFAULT '[]'::jsonb,
    -- 例外规则结构示例：
    -- [
    --   {"scope":"owner_user", "values":["user@example.com"], "check_types":["pii","secret"], "actions":["skip_redact","skip_block"], "reason":"数据所有者"},
    --   {"scope":"role",        "values":["security_auditor"], "check_types":["pii"],          "actions":["skip_redact"],          "reason":"安全审计"}
    -- ]

    -- 通知告警
    ADD COLUMN IF NOT EXISTS notification_channels JSONB DEFAULT '[]'::jsonb,
    -- [{"type":"webhook","url":"..."}, {"type":"lark","webhook":"..."}, {"type":"email","addresses":["..."]}]
    ADD COLUMN IF NOT EXISTS realtime_alert_enabled BOOLEAN DEFAULT false,
    ADD COLUMN IF NOT EXISTS alert_aggregation_window_minutes INT DEFAULT 5 CHECK (alert_aggregation_window_minutes >= 0),

    -- 学习与优化
    ADD COLUMN IF NOT EXISTS sampling_rate DECIMAL(3,2) DEFAULT 1.0 CHECK (sampling_rate >= 0 AND sampling_rate <= 1),
    ADD COLUMN IF NOT EXISTS auto_review_queue_enabled BOOLEAN DEFAULT false,
    ADD COLUMN IF NOT EXISTS feedback_loop_enabled BOOLEAN DEFAULT false,
    ADD COLUMN IF NOT EXISTS skill_generation_enabled BOOLEAN DEFAULT false,
    ADD COLUMN IF NOT EXISTS auto_threshold_tuning_enabled BOOLEAN DEFAULT false,

    -- 审计与保留
    ADD COLUMN IF NOT EXISTS retention_days INT DEFAULT 90 CHECK (retention_days >= 0),

    -- 策略元数据
    ADD COLUMN IF NOT EXISTS policy_name VARCHAR(100) DEFAULT 'default';

COMMENT ON COLUMN output_compliance_policies.llm_engine_id IS '用于幻觉/偏见评估的 LLM 引擎 ID（可关联 prompt_injection_llm_engines）';
COMMENT ON COLUMN output_compliance_policies.pii_engine IS 'PII 检测引擎：regex / model / hybrid';
COMMENT ON COLUMN output_compliance_policies.toxicity_engine IS '毒性检测引擎：keyword / model / hybrid';
COMMENT ON COLUMN output_compliance_policies.check_secrets IS '检测 API Key、私钥、Token 等凭据';
COMMENT ON COLUMN output_compliance_policies.check_internal_ip IS '检测内网 IP（RFC1918）';
COMMENT ON COLUMN output_compliance_policies.check_jailbreak_response IS '检测模型是否输出越狱/被注入后的异常响应';
COMMENT ON COLUMN output_compliance_policies.check_instruction_injection_response IS '检测模型输出是否泄露系统提示或被注入指令触发';
COMMENT ON COLUMN output_compliance_policies.exception_rules IS '身份感知例外规则（owner_user/role/application_code等）';
COMMENT ON COLUMN output_compliance_policies.notification_channels IS '告警通道配置（webhook/lark/email）';
COMMENT ON COLUMN output_compliance_policies.skill_generation_enabled IS '命中后自动生成安全改写建议/团队技能';
COMMENT ON COLUMN output_compliance_policies.retention_days IS '审计日志保留天数，0 表示永久保留';

-- 2. 扩展现有 output_compliance_audit 表，新增与策略/例外相关的字段
ALTER TABLE output_compliance_audit
    ADD COLUMN IF NOT EXISTS policy_id INT,
    ADD COLUMN IF NOT EXISTS rule_triggered VARCHAR(100),           -- 命中的具体规则/模式名
    ADD COLUMN IF NOT EXISTS exception_matched BOOLEAN DEFAULT false, -- 是否命中例外规则
    ADD COLUMN IF NOT EXISTS exception_scope VARCHAR(50),           -- owner_user / role / application_code
    ADD COLUMN IF NOT EXISTS alert_sent BOOLEAN DEFAULT false,      -- 是否已发送告警
    ADD COLUMN IF NOT EXISTS skill_suggestion TEXT,                 -- 自动生成的安全改写/技能建议
    ADD COLUMN IF NOT EXISTS review_queue_id INT;                   -- 关联复核队列

COMMENT ON COLUMN output_compliance_audit.skill_suggestion IS 'skill_generation_enabled=true 时自动生成的合规改写建议';
COMMENT ON COLUMN output_compliance_audit.exception_matched IS '是否因身份感知例外规则被跳过脱敏/阻断';

-- 3. 创建自定义敏感词库表（租户级）
CREATE TABLE IF NOT EXISTS output_compliance_custom_keywords (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL DEFAULT 'default',
    keyword VARCHAR(200) NOT NULL,
    category VARCHAR(50) NOT NULL DEFAULT 'custom',  -- custom / profanity / hate_speech / violence / sexual
    severity INT NOT NULL DEFAULT 7 CHECK (severity >= 1 AND severity <= 10),
    action VARCHAR(20) DEFAULT 'warn' CHECK (action IN ('log', 'warn', 'redact', 'block')),
    enabled BOOLEAN DEFAULT true,
    description TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    created_by VARCHAR(255),
    updated_by VARCHAR(255),

    CONSTRAINT unique_output_compliance_keyword UNIQUE (tenant_id, keyword)
);

COMMENT ON TABLE output_compliance_custom_keywords IS '输出合规自定义敏感词库 - 租户级';

-- 4. 创建复核队列表
CREATE TABLE IF NOT EXISTS output_compliance_review_queue (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    audit_id BIGINT NOT NULL,
    request_id VARCHAR(255) NOT NULL,
    session_key VARCHAR(255),
    issue_type VARCHAR(50) NOT NULL,
    issue_subtype VARCHAR(50),
    severity INT NOT NULL,
    status VARCHAR(20) DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    reviewer VARCHAR(255),
    review_comment TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    reviewed_at TIMESTAMPTZ,

    CONSTRAINT fk_review_queue_audit FOREIGN KEY (audit_id) REFERENCES output_compliance_audit(id) ON DELETE CASCADE,
    CONSTRAINT fk_review_queue_tenant FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_output_compliance_review_status ON output_compliance_review_queue (tenant_id, status, created_at DESC);

COMMENT ON TABLE output_compliance_review_queue IS '输出合规模块人工复核队列 - 命中阈值时进入待复核';

-- 5. 创建误报/漏报反馈表
CREATE TABLE IF NOT EXISTS output_compliance_feedback (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    audit_id BIGINT NOT NULL,
    feedback_type VARCHAR(20) NOT NULL CHECK (feedback_type IN ('false_positive', 'false_negative', 'correct')),
    reporter VARCHAR(255),
    comment TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),

    CONSTRAINT fk_feedback_audit FOREIGN KEY (audit_id) REFERENCES output_compliance_audit(id) ON DELETE CASCADE,
    CONSTRAINT fk_feedback_tenant FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_output_compliance_feedback_tenant ON output_compliance_feedback (tenant_id, feedback_type, created_at DESC);

COMMENT ON TABLE output_compliance_feedback IS '输出合规检测结果反馈 - 用于阈值调优和模型优化';

-- 6. 扩展 PII 模式类型以支持新检测类型
-- 现有 pii_patterns.pattern_type 为 VARCHAR(50)，无需修改类型；
-- 但业务代码将识别：email/phone/id_card/credit_card/ssn/ip/bank_card/secret/jwt/password

-- 7. RLS 策略
ALTER TABLE output_compliance_custom_keywords ENABLE ROW LEVEL SECURITY;
ALTER TABLE output_compliance_review_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE output_compliance_feedback ENABLE ROW LEVEL SECURITY;

CREATE POLICY output_compliance_custom_keywords_tenant ON output_compliance_custom_keywords
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

CREATE POLICY output_compliance_review_queue_tenant ON output_compliance_review_queue
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

CREATE POLICY output_compliance_feedback_tenant ON output_compliance_feedback
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

CREATE POLICY output_compliance_custom_keywords_super_admin ON output_compliance_custom_keywords
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

CREATE POLICY output_compliance_review_queue_super_admin ON output_compliance_review_queue
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

CREATE POLICY output_compliance_feedback_super_admin ON output_compliance_feedback
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

-- 8. 更新时间戳触发器
CREATE OR REPLACE FUNCTION update_output_compliance_modified_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
    CREATE TRIGGER update_output_compliance_custom_keywords_modtime
        BEFORE UPDATE ON output_compliance_custom_keywords
        FOR EACH ROW EXECUTE FUNCTION update_output_compliance_modified_column();
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;
