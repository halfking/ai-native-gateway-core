-- 311_prompt_injection_detection.sql
-- 提示词注入检测增强 - 租户级策略配置

-- 1. 创建提示词注入策略表
CREATE TABLE IF NOT EXISTS prompt_injection_policies (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- 基础配置
    enabled BOOLEAN DEFAULT true,
    detection_mode VARCHAR(20) DEFAULT 'observe', -- 'observe'/'enforce'
    
    -- 检测层级启用
    enable_basic_rules BOOLEAN DEFAULT true,
    enable_advanced_rules BOOLEAN DEFAULT true,
    enable_heuristics BOOLEAN DEFAULT true,
    enable_ml_model BOOLEAN DEFAULT false,
    
    -- 分数阈值配置
    score_threshold_log INT DEFAULT 3,
    score_threshold_warn INT DEFAULT 6,
    score_threshold_sanitize INT DEFAULT 8,
    score_threshold_block INT DEFAULT 10,
    
    -- 响应动作配置
    action_on_low_risk VARCHAR(20) DEFAULT 'log',
    action_on_medium_risk VARCHAR(20) DEFAULT 'warn',
    action_on_high_risk VARCHAR(20) DEFAULT 'block',
    
    -- 白名单配置
    whitelist_patterns TEXT[],
    whitelist_users TEXT[],
    
    -- 通知配置
    notify_on_detection BOOLEAN DEFAULT false,
    notification_webhook VARCHAR(500),
    notification_email VARCHAR(255),
    
    -- 统计字段
    total_detections INT DEFAULT 0,
    total_blocks INT DEFAULT 0,
    last_detection_at TIMESTAMPTZ,
    
    -- 审计字段
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    created_by VARCHAR(255),
    updated_by VARCHAR(255),
    
    CONSTRAINT fk_prompt_injection_tenant FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE,
    CONSTRAINT unique_tenant_policy UNIQUE (tenant_id)
);

-- 2. 创建检测规则库表
CREATE TABLE IF NOT EXISTS prompt_injection_rules (
    id SERIAL PRIMARY KEY,
    rule_name VARCHAR(100) NOT NULL UNIQUE,
    rule_type VARCHAR(50) NOT NULL,
    category VARCHAR(50) NOT NULL,
    pattern TEXT NOT NULL,
    description TEXT,
    severity INT NOT NULL CHECK (severity >= 1 AND severity <= 10),
    enabled BOOLEAN DEFAULT true,
    case_sensitive BOOLEAN DEFAULT false,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 3. 插入预定义检测规则（30+ 规则）
INSERT INTO prompt_injection_rules (rule_name, rule_type, category, pattern, description, severity) VALUES
-- 角色劫持（Role Hijacking）
('role_hijack_ignore_previous', 'basic', 'role_hijack', '(?i)(ignore|forget|disregard).*(previous|above|prior).*(instruction|prompt|rule)', '尝试让模型忽略之前的指令', 9),
('role_hijack_you_are_now', 'basic', 'role_hijack', '(?i)you are now (a|an) .*(admin|root|system|god mode|developer)', '尝试切换模型角色为特权用户', 10),
('role_hijack_act_as', 'basic', 'role_hijack', '(?i)act as (a|an) .*(hacker|malicious|unrestricted)', '尝试让模型扮演恶意角色', 9),
('role_hijack_pretend', 'basic', 'role_hijack', '(?i)pretend (you are|to be) .*(jailbroken|unrestricted)', '尝试让模型假装突破限制', 9),

-- 指令泄漏（Instruction Leak）
('instruction_leak_show_prompt', 'basic', 'instruction_leak', '(?i)(show|tell|reveal|print|output).*(system prompt|instructions|rules)', '尝试泄漏系统提示词', 8),
('instruction_leak_repeat', 'basic', 'instruction_leak', '(?i)repeat.*above.*word for word', '尝试让模型重复系统指令', 8),
('instruction_leak_what_are', 'basic', 'instruction_leak', '(?i)what are (your|the) (instructions|rules|guidelines)', '询问系统指令', 7),

-- DAN 越狱（Do Anything Now）
('dan_mode', 'advanced', 'dan', '(?i)(DAN|do anything now) mode', 'DAN 越狱尝试', 10),
('dan_developer_mode', 'advanced', 'dan', '(?i)(developer|debug|admin) mode.*enabled', '开发者模式越狱', 10),
('dan_unrestricted', 'advanced', 'dan', '(?i)unrestricted mode|no restrictions', '无限制模式', 10),
('dan_jailbreak', 'advanced', 'dan', '(?i)jailbreak|bypass.*filter|ignore.*safety', '明确的越狱关键词', 9),

-- Payload 分隔符绕过
('bypass_separator_end_system', 'advanced', 'bypass', '---END SYSTEM---|---START USER---', '使用分隔符绕过', 9),
('bypass_inst_tags', 'advanced', 'bypass', '\[INST\].*\[/INST\].*\[INST\]', '使用 INST 标签绕过', 8),
('bypass_xml_tags', 'advanced', 'bypass', '<system>.*</system>.*<user>', '使用 XML 标签绕过', 8),

-- Unicode 混淆
-- 注意：Go 的 regexp (RE2) 使用 \x{XXXX} 语法，不支持 \uXXXX。
-- PostgreSQL standard_conforming_strings=on 时反斜杠为字面量，因此存储 \x{...} 字面字符供 Go 解析。
('bypass_zero_width', 'advanced', 'bypass', '[\x{200B}-\x{200D}\x{FEFF}]', '零宽字符混淆', 7),
('bypass_rtl_override', 'advanced', 'bypass', '[\x{202E}]', 'RTL 覆盖字符', 7),

-- 编码绕过
('bypass_base64', 'advanced', 'bypass', '(?i)(base64|b64).*(decode|eval)', 'Base64 编码绕过', 7),
('bypass_hex', 'advanced', 'bypass', '(?i)hex.*(decode|convert)', '十六进制编码绕过', 7),
('bypass_rot13', 'advanced', 'bypass', '(?i)rot13|rot-13', 'ROT13 编码绕过', 6),

-- 命令注入
('command_injection', 'advanced', 'bypass', '(?i)(exec|system|eval|shell).*\(', '尝试命令注入', 10),

-- 多语言绕过
('bypass_translation', 'advanced', 'bypass', '(?i)translate.*to (English|Chinese).*ignore', '翻译绕过', 7),

-- 提示词泄漏变种
('leak_base_instructions', 'basic', 'instruction_leak', '(?i)base instructions|original prompt', '询问基础指令', 7),
('leak_system_message', 'basic', 'instruction_leak', '(?i)system message|hidden instructions', '询问系统消息', 7),

-- 角色混淆
('role_confusion_assistant', 'basic', 'role_hijack', '(?i)you.re (no longer|not) (an? )?(assistant|AI)', '否认助手身份', 8),
('role_confusion_human', 'basic', 'role_hijack', '(?i)you are (now )?a (real )?human', '尝试让模型认为自己是人类', 8),

-- 权限提升
('privilege_escalation', 'advanced', 'role_hijack', '(?i)(grant|give).*admin|sudo|root', '尝试权限提升', 9),

-- 约束绕过
('bypass_restrictions', 'advanced', 'bypass', '(?i)without (any )?restrictions|ignore (all )?limitations', '尝试绕过限制', 8),
('bypass_guidelines', 'advanced', 'bypass', '(?i)ignore.*guidelines|bypass.*rules', '尝试绕过准则', 8),

-- 多轮攻击
('multi_turn_setup', 'advanced', 'bypass', '(?i)in the next (message|prompt|turn)', '多轮攻击设置', 6);

-- 4. 创建检测日志表
CREATE TABLE IF NOT EXISTS prompt_injection_detections (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- 请求信息
    request_id VARCHAR(255) NOT NULL,
    session_key VARCHAR(255),
    
    -- 检测结果
    detected_at TIMESTAMPTZ DEFAULT NOW(),
    detection_score INT NOT NULL,
    risk_level VARCHAR(20) NOT NULL,
    
    -- 匹配的规则
    matched_rules JSONB,
    matched_rules_count INT NOT NULL,
    
    -- 检测层级
    detection_layers JSONB,
    
    -- 执行的动作
    action_taken VARCHAR(20) NOT NULL,
    blocked BOOLEAN DEFAULT false,
    
    -- 证据（脱敏后）
    evidence_text TEXT,
    input_hash VARCHAR(64),
    
    -- 上下文
    client_ip VARCHAR(45),
    user_agent TEXT
);

-- 4.1 创建检测日志表索引（PostgreSQL 使用 CREATE INDEX，非 MySQL 内联 INDEX 语法）
CREATE INDEX idx_detections_tenant_time ON prompt_injection_detections (tenant_id, detected_at DESC);
CREATE INDEX idx_detections_request ON prompt_injection_detections (request_id);
CREATE INDEX idx_detections_session ON prompt_injection_detections (session_key);
CREATE INDEX idx_detections_risk ON prompt_injection_detections (tenant_id, risk_level) WHERE blocked = true;

-- 5. RLS 策略
ALTER TABLE prompt_injection_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE prompt_injection_detections ENABLE ROW LEVEL SECURITY;

CREATE POLICY prompt_injection_policies_tenant ON prompt_injection_policies
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

CREATE POLICY prompt_injection_detections_tenant ON prompt_injection_detections
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

CREATE POLICY prompt_injection_policies_super_admin ON prompt_injection_policies
    USING (
        current_setting('app.current_role', true) = 'super_admin' 
        OR current_setting('app.bypass_rls', true) = 'true'
    );

CREATE POLICY prompt_injection_detections_super_admin ON prompt_injection_detections
    USING (
        current_setting('app.current_role', true) = 'super_admin' 
        OR current_setting('app.bypass_rls', true) = 'true'
    );

-- 6. 创建获取策略的函数
CREATE OR REPLACE FUNCTION get_prompt_injection_policy(p_tenant_id VARCHAR)
RETURNS prompt_injection_policies AS $$
DECLARE
    v_policy prompt_injection_policies;
BEGIN
    SELECT * INTO v_policy
    FROM prompt_injection_policies
    WHERE tenant_id = p_tenant_id;
    
    IF NOT FOUND THEN
        v_policy.tenant_id := p_tenant_id;
        v_policy.enabled := true;
        v_policy.detection_mode := 'observe';
        v_policy.enable_basic_rules := true;
        v_policy.enable_advanced_rules := true;
        v_policy.enable_heuristics := true;
        v_policy.enable_ml_model := false;
        v_policy.score_threshold_log := 3;
        v_policy.score_threshold_warn := 6;
        v_policy.score_threshold_sanitize := 8;
        v_policy.score_threshold_block := 10;
        v_policy.action_on_low_risk := 'log';
        v_policy.action_on_medium_risk := 'warn';
        v_policy.action_on_high_risk := 'block';
    END IF;
    
    RETURN v_policy;
END;
$$ LANGUAGE plpgsql;

-- 7. 创建统计视图
CREATE OR REPLACE VIEW prompt_injection_stats_today AS
SELECT 
    tenant_id,
    COUNT(*) as total_detections,
    COUNT(*) FILTER (WHERE blocked = true) as blocked_count,
    COUNT(*) FILTER (WHERE risk_level = 'critical') as critical_count,
    COUNT(*) FILTER (WHERE risk_level = 'high') as high_count,
    COUNT(*) FILTER (WHERE risk_level = 'medium') as medium_count,
    COUNT(*) FILTER (WHERE risk_level = 'low') as low_count,
    AVG(detection_score) as avg_score,
    MAX(detection_score) as max_score
FROM prompt_injection_detections
WHERE detected_at >= CURRENT_DATE
GROUP BY tenant_id;

-- 8. 创建备注
COMMENT ON TABLE prompt_injection_policies IS '提示词注入策略配置 - 租户级检测规则和响应动作';
COMMENT ON TABLE prompt_injection_rules IS '提示词注入检测规则库 - 预定义和自定义规则';
COMMENT ON TABLE prompt_injection_detections IS '提示词注入检测日志 - 记录所有检测事件';
COMMENT ON COLUMN prompt_injection_policies.detection_mode IS 'observe: 仅观察记录, enforce: 可阻断请求';
COMMENT ON COLUMN prompt_injection_detections.detection_score IS '综合评分 0-10，基于所有匹配规则的最高严重等级';
COMMENT ON COLUMN prompt_injection_detections.risk_level IS 'low(0-5)/medium(6-7)/high(8-9)/critical(10)';
