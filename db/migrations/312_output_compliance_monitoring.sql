-- 312_output_compliance_monitoring.sql
-- 输出合规监控 - 检测 LLM 输出中的敏感信息、有害内容

-- 1. 创建输出合规策略表
CREATE TABLE IF NOT EXISTS output_compliance_policies (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- 基础配置
    enabled BOOLEAN DEFAULT true,
    enforcement_mode VARCHAR(20) DEFAULT 'observe', -- 'observe'/'enforce'
    
    -- 检测类型启用
    check_pii BOOLEAN DEFAULT true,                 -- PII 检测（电话/邮箱/身份证）
    check_toxicity BOOLEAN DEFAULT true,            -- 毒性检测（辱骂/仇恨言论）
    check_bias BOOLEAN DEFAULT false,               -- 偏见检测（性别/种族歧视）
    check_hallucination BOOLEAN DEFAULT false,      -- 幻觉提示检测
    
    -- 阈值配置
    pii_threshold DECIMAL(3,2) DEFAULT 0.7,         -- PII 检测阈值 0.0-1.0
    toxicity_threshold DECIMAL(3,2) DEFAULT 0.7,    -- 毒性阈值
    bias_threshold DECIMAL(3,2) DEFAULT 0.6,        -- 偏见阈值
    hallucination_threshold DECIMAL(3,2) DEFAULT 0.7, -- 幻觉提示阈值
    
    -- 响应动作
    action_on_pii VARCHAR(20) DEFAULT 'redact',     -- 'log'/'warn'/'redact'/'block'
    action_on_toxicity VARCHAR(20) DEFAULT 'warn',  -- 'log'/'warn'/'redact'/'block'
    action_on_bias VARCHAR(20) DEFAULT 'log',       -- 'log'/'warn'
    action_on_hallucination VARCHAR(20) DEFAULT 'log', -- 'log'/'warn'
    
    -- 自动脱敏配置
    auto_redact BOOLEAN DEFAULT true,               -- 自动脱敏 PII
    redact_email BOOLEAN DEFAULT true,              -- 脱敏邮箱
    redact_phone BOOLEAN DEFAULT true,              -- 脱敏电话
    redact_id_card BOOLEAN DEFAULT true,            -- 脱敏身份证
    redact_credit_card BOOLEAN DEFAULT true,        -- 脱敏信用卡
    
    -- 严格模式
    strict_mode BOOLEAN DEFAULT false,              -- 严格模式（有问题立即阻断）
    log_all_outputs BOOLEAN DEFAULT false,          -- 记录所有输出（审计用）
    
    -- 白名单
    whitelist_patterns TEXT[],                      -- 白名单正则（不检测）
    
    -- 统计
    total_checks INT DEFAULT 0,
    total_issues INT DEFAULT 0,
    total_redactions INT DEFAULT 0,
    last_check_at TIMESTAMPTZ,
    
    -- 审计字段
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    created_by VARCHAR(255),
    updated_by VARCHAR(255),
    
    CONSTRAINT fk_output_compliance_tenant FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE,
    CONSTRAINT unique_output_compliance_tenant UNIQUE (tenant_id)
);

-- 2. 创建输出合规审计表
CREATE TABLE IF NOT EXISTS output_compliance_audit (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- 请求信息
    request_id VARCHAR(255) NOT NULL,
    session_key VARCHAR(255),
    
    -- 检测信息
    detected_at TIMESTAMPTZ DEFAULT NOW(),
    issue_type VARCHAR(50) NOT NULL,               -- 'pii'/'toxic'/'bias'/'hallucination'
    issue_subtype VARCHAR(50),                     -- 'email'/'phone'/'racial'/'gender' 等
    severity INT NOT NULL CHECK (severity >= 1 AND severity <= 10),
    
    -- 检测结果
    evidence TEXT,                                 -- 脱敏后的证据
    location VARCHAR(100),                         -- 位置信息（char:120-145）
    score DECIMAL(5,4),                           -- 检测置信度 0.0-1.0
    
    -- 执行的动作
    action_taken VARCHAR(20) NOT NULL,            -- 'logged'/'warned'/'redacted'/'blocked'
    redacted BOOLEAN DEFAULT false,
    blocked BOOLEAN DEFAULT false,
    
    -- 原始输出（可选，仅 log_all_outputs=true 时）
    original_output TEXT,
    redacted_output TEXT,
    
    -- 上下文
    model VARCHAR(100),
    client_ip VARCHAR(45),
    
    -- 索引
    INDEX idx_output_audit_tenant_time (tenant_id, detected_at DESC),
    INDEX idx_output_audit_request (request_id),
    INDEX idx_output_audit_session (session_key),
    INDEX idx_output_audit_issue (tenant_id, issue_type, severity DESC)
);

-- 3. 创建 PII 检测模式表
CREATE TABLE IF NOT EXISTS pii_patterns (
    id SERIAL PRIMARY KEY,
    pattern_name VARCHAR(100) NOT NULL UNIQUE,
    pattern_type VARCHAR(50) NOT NULL,            -- 'email'/'phone'/'id_card'/'credit_card'/'ssn'
    regex_pattern TEXT NOT NULL,
    description TEXT,
    enabled BOOLEAN DEFAULT true,
    severity INT DEFAULT 7,
    redact_format VARCHAR(100),                   -- 脱敏格式，例如: "***@***.com"
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 4. 插入预定义 PII 检测模式
INSERT INTO pii_patterns (pattern_name, pattern_type, regex_pattern, description, severity, redact_format) VALUES
-- 邮箱
('email_standard', 'email', '[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}', '标准邮箱格式', 7, '***@***.com'),

-- 中国手机号
('phone_cn_mobile', 'phone', '1[3-9]\d{9}', '中国手机号（11位）', 8, '***-****-****'),
('phone_cn_formatted', 'phone', '1[3-9]\d-\d{4}-\d{4}', '中国手机号（带横杠）', 8, '***-****-****'),

-- 国际电话
('phone_intl', 'phone', '\+\d{1,3}[-\s]?\d{1,14}', '国际电话号码', 7, '+***-***-****'),

-- 中国身份证
('id_card_cn_18', 'id_card', '\d{17}[\dXx]', '中国身份证（18位）', 9, '******19******'),
('id_card_cn_15', 'id_card', '\d{15}', '中国身份证（15位，旧版）', 9, '******19***'),

-- 信用卡号
('credit_card_visa', 'credit_card', '4\d{15}', 'Visa 信用卡', 9, '****-****-****-****'),
('credit_card_mastercard', 'credit_card', '5[1-5]\d{14}', 'MasterCard 信用卡', 9, '****-****-****-****'),
('credit_card_amex', 'credit_card', '3[47]\d{13}', 'American Express 信用卡', 9, '****-******-*****'),

-- 美国社会安全号
('ssn_us', 'ssn', '\d{3}-\d{2}-\d{4}', '美国社会安全号（SSN）', 10, '***-**-****'),

-- IP 地址
('ip_address', 'ip', '\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}', 'IP 地址', 5, '***.***.***.***'),

-- 银行卡号（中国）
('bank_card_cn', 'bank_card', '\d{16,19}', '中国银行卡号', 9, '****-****-****-****');

-- 5. 创建毒性检测关键词表
CREATE TABLE IF NOT EXISTS toxic_keywords (
    id SERIAL PRIMARY KEY,
    keyword VARCHAR(100) NOT NULL,
    category VARCHAR(50) NOT NULL,               -- 'profanity'/'hate_speech'/'violence'/'sexual'
    severity INT NOT NULL CHECK (severity >= 1 AND severity <= 10),
    language VARCHAR(10) DEFAULT 'zh',           -- 'zh'/'en'
    enabled BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 6. 插入预定义毒性关键词（示例）
INSERT INTO toxic_keywords (keyword, category, severity, language) VALUES
-- 辱骂类（中文）
('傻逼', 'profanity', 8, 'zh'),
('白痴', 'profanity', 7, 'zh'),
('垃圾', 'profanity', 6, 'zh'),

-- 仇恨言论（中文）
('歧视', 'hate_speech', 9, 'zh'),
('种族主义', 'hate_speech', 10, 'zh'),

-- 暴力（中文）
('杀死', 'violence', 9, 'zh'),
('伤害', 'violence', 7, 'zh'),

-- 英文示例
('fuck', 'profanity', 9, 'en'),
('shit', 'profanity', 7, 'en'),
('kill', 'violence', 8, 'en'),
('racist', 'hate_speech', 9, 'en');

-- 7. RLS 策略
ALTER TABLE output_compliance_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE output_compliance_audit ENABLE ROW LEVEL SECURITY;

CREATE POLICY output_compliance_policies_tenant ON output_compliance_policies
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

CREATE POLICY output_compliance_audit_tenant ON output_compliance_audit
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

CREATE POLICY output_compliance_policies_super_admin ON output_compliance_policies
    USING (
        current_setting('app.current_role', true) = 'super_admin' 
        OR current_setting('app.bypass_rls', true) = 'true'
    );

CREATE POLICY output_compliance_audit_super_admin ON output_compliance_audit
    USING (
        current_setting('app.current_role', true) = 'super_admin' 
        OR current_setting('app.bypass_rls', true) = 'true'
    );

-- 8. 创建统计视图
CREATE OR REPLACE VIEW output_compliance_stats_today AS
SELECT 
    tenant_id,
    COUNT(*) as total_issues,
    COUNT(*) FILTER (WHERE redacted = true) as redacted_count,
    COUNT(*) FILTER (WHERE blocked = true) as blocked_count,
    COUNT(*) FILTER (WHERE issue_type = 'pii') as pii_count,
    COUNT(*) FILTER (WHERE issue_type = 'toxic') as toxic_count,
    COUNT(*) FILTER (WHERE issue_type = 'bias') as bias_count,
    COUNT(*) FILTER (WHERE issue_type = 'hallucination') as hallucination_count,
    AVG(severity) as avg_severity,
    MAX(severity) as max_severity
FROM output_compliance_audit
WHERE detected_at >= CURRENT_DATE
GROUP BY tenant_id;

-- 9. 创建备注
COMMENT ON TABLE output_compliance_policies IS '输出合规策略配置 - 租户级 LLM 输出检测规则';
COMMENT ON TABLE output_compliance_audit IS '输出合规审计日志 - 记录所有检测到的合规问题';
COMMENT ON TABLE pii_patterns IS 'PII 检测模式库 - 正则表达式匹配模式';
COMMENT ON TABLE toxic_keywords IS '毒性关键词库 - 辱骂/仇恨言论/暴力内容';
COMMENT ON COLUMN output_compliance_policies.enforcement_mode IS 'observe: 仅观察记录, enforce: 可阻断/脱敏';
COMMENT ON COLUMN output_compliance_policies.auto_redact IS '自动脱敏 PII（邮箱/电话/身份证等）';
COMMENT ON COLUMN output_compliance_audit.evidence IS '脱敏后的证据文本（最多 500 字符）';
COMMENT ON COLUMN output_compliance_audit.location IS '问题在输出中的位置，格式: char:120-145';
