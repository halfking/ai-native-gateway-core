-- Migration 363: Create security_detector_config table and views
--
-- Purpose:
--   安全检测器配置表：统一管理提示词注入检测和会话审计的配置。
--   将硬编码配置迁移到数据库，支持租户级定制和热更新。
--
-- Design notes:
--   - tenant_id = NULL 表示平台级默认配置
--   - 复用于提示词注入检测（promptinjection）和会话审计（sessionaudit）
--   - JSONB存储灵活的规则配置（敏感词、正则模式等）
--   - 支持分层阈值配置（warn/approval/block）
--
-- Date: 2026-07-08

CREATE TABLE IF NOT EXISTS security_detector_config (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT,  -- NULL=平台级，非NULL=租户级
    
    -- 配置名称和描述
    config_name TEXT NOT NULL DEFAULT 'default',
    description TEXT,
    
    -- 敏感词配置
    sensitive_words JSONB NOT NULL DEFAULT '[]',
    -- 示例：["政变", "六四", "法轮功", "色情", "暴力", "毒品", "枪支"]
    
    -- 提示词注入检测规则
    injection_patterns JSONB NOT NULL DEFAULT '[]',
    -- 示例：[
    --   {"pattern": "(?i)ignore\\s+(previous|all|above)\\s+instructions?", "severity": 9, "description": "忽略指令"},
    --   {"pattern": "(?i)you\\s+are\\s+now\\s+a\\s+different", "severity": 10, "description": "角色切换"}
    -- ]
    
    -- PII（个人信息）检测规则
    pii_patterns JSONB NOT NULL DEFAULT '[]',
    -- 示例：[
    --   {"pattern": "\\b\\d{4}[\\s-]?\\d{4}[\\s-]?\\d{4}[\\s-]?\\d{4}\\b", "type": "credit_card", "severity": 9},
    --   {"pattern": "\\b\\d{17}[\\dXx]\\b", "type": "id_card", "severity": 9},
    --   {"pattern": "\\b1[3-9]\\d{9}\\b", "type": "phone", "severity": 7}
    -- ]
    
    -- 越狱（Jailbreak）检测规则
    jailbreak_patterns JSONB NOT NULL DEFAULT '[]',
    -- 示例：[
    --   {"pattern": "(?i)\\bDAN\\b", "severity": 10, "description": "DAN越狱"},
    --   {"pattern": "(?i)jailbreak", "severity": 9, "description": "越狱关键词"},
    --   {"pattern": "(?i)no\\s+restrictions?", "severity": 8, "description": "无限制模式"}
    -- ]
    
    -- 内容长度限制
    max_content_len INT NOT NULL DEFAULT 50000,
    -- 超长内容截断（防止DoS攻击）
    
    -- 决策阈值配置
    score_threshold_log INT NOT NULL DEFAULT 3 CHECK (score_threshold_log >= 0 AND score_threshold_log <= 10),
    -- 评分>=此值：记录日志
    
    score_threshold_warn INT NOT NULL DEFAULT 5 CHECK (score_threshold_warn >= 0 AND score_threshold_warn <= 10),
    -- 评分>=此值：警告
    
    score_threshold_approval INT NOT NULL DEFAULT 8 CHECK (score_threshold_approval >= 0 AND score_threshold_approval <= 10),
    -- 评分>=此值：需要人工审批
    
    score_threshold_block INT NOT NULL DEFAULT 10 CHECK (score_threshold_block >= 0 AND score_threshold_block <= 10),
    -- 评分>=此值：直接阻断
    
    severity_threshold_approval INT NOT NULL DEFAULT 8 CHECK (severity_threshold_approval >= 0 AND severity_threshold_approval <= 10),
    -- 任一威胁严重度>=此值：需要人工审批
    
    -- 会话审计特定配置
    audit_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    -- 是否启用会话审计
    
    audit_sampling_rate FLOAT NOT NULL DEFAULT 1.0 CHECK (audit_sampling_rate >= 0 AND audit_sampling_rate <= 1),
    -- 审计采样率（0-1）：1.0=全量审计，0.1=10%采样
    
    auto_approval_whitelist JSONB DEFAULT '[]',
    -- 自动通过白名单（用户ID、IP等）
    -- 示例：{"user_ids": ["admin@example.com"], "ip_cidrs": ["10.0.0.0/8"], "tenant_ids": ["trusted_tenant"]}
    
    -- 状态
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    version INT NOT NULL DEFAULT 1,  -- 配置版本号（用于缓存失效）
    
    -- 时间戳
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 约束
    CONSTRAINT security_detector_config_unique_tenant UNIQUE (tenant_id, config_name)
);

-- 快速查询租户配置
CREATE INDEX IF NOT EXISTS idx_security_config_tenant 
    ON security_detector_config (tenant_id) 
    WHERE enabled = TRUE;

-- 版本管理（用于缓存失效）
CREATE INDEX IF NOT EXISTS idx_security_config_version 
    ON security_detector_config (version DESC, updated_at DESC);

-- 表注释
COMMENT ON TABLE security_detector_config IS
    '安全检测器配置 — 统一管理提示词注入检测和会话审计配置，支持租户级定制和热更新';

COMMENT ON COLUMN security_detector_config.sensitive_words IS
    '敏感词列表（JSONB数组）：["政变", "六四", "色情", "暴力"]';

COMMENT ON COLUMN security_detector_config.injection_patterns IS
    '提示词注入检测规则（JSONB）：[{"pattern":"regex","severity":9,"description":"说明"}]';

COMMENT ON COLUMN security_detector_config.pii_patterns IS
    'PII检测规则（JSONB）：[{"pattern":"regex","type":"credit_card","severity":9}]';

COMMENT ON COLUMN security_detector_config.jailbreak_patterns IS
    '越狱检测规则（JSONB）：[{"pattern":"regex","severity":10,"description":"DAN越狱"}]';

COMMENT ON COLUMN security_detector_config.audit_sampling_rate IS
    '审计采样率（0-1）：1.0=全量，0.1=10%采样。用于高流量场景降低存储压力';

COMMENT ON COLUMN security_detector_config.auto_approval_whitelist IS
    '自动通过白名单（JSONB）：{"user_ids":[...],"ip_cidrs":[...],"tenant_ids":[...]}';

-- 插入平台级默认配置
INSERT INTO security_detector_config (
    tenant_id,
    config_name,
    description,
    sensitive_words,
    injection_patterns,
    pii_patterns,
    jailbreak_patterns,
    max_content_len,
    score_threshold_log,
    score_threshold_warn,
    score_threshold_approval,
    score_threshold_block,
    severity_threshold_approval,
    audit_enabled,
    audit_sampling_rate,
    auto_approval_whitelist
) VALUES (
    NULL,
    'default',
    '平台级默认安全检测配置',
    '["政变", "六四", "法轮功", "色情", "暴力", "血腥", "毒品", "枪支", "炸药"]'::jsonb,
    '[
        {"pattern": "(?i)ignore\\s+(previous|all|above)\\s+instructions?", "severity": 9, "description": "忽略之前的指令"},
        {"pattern": "(?i)disregard\\s+(previous|all)\\s+(instructions?|prompts?)", "severity": 9, "description": "忽略提示词"},
        {"pattern": "(?i)you\\s+are\\s+now\\s+a\\s+different", "severity": 10, "description": "角色切换"},
        {"pattern": "(?i)system:\\s*", "severity": 8, "description": "系统提示注入"},
        {"pattern": "(?i)<\\|im_start\\|>", "severity": 9, "description": "特殊标记注入"},
        {"pattern": "(?i)__SYSTEM__", "severity": 9, "description": "系统标记"}
    ]'::jsonb,
    '[
        {"pattern": "\\b\\d{4}[\\s-]?\\d{4}[\\s-]?\\d{4}[\\s-]?\\d{4}\\b", "type": "credit_card", "severity": 9, "description": "信用卡号"},
        {"pattern": "\\b\\d{17}[\\dXx]\\b", "type": "id_card", "severity": 9, "description": "身份证号"},
        {"pattern": "\\b1[3-9]\\d{9}\\b", "type": "phone", "severity": 7, "description": "手机号"},
        {"pattern": "\\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Z|a-z]{2,}\\b", "type": "email", "severity": 5, "description": "邮箱"}
    ]'::jsonb,
    '[
        {"pattern": "(?i)\\bDAN\\b", "severity": 10, "description": "DAN越狱"},
        {"pattern": "(?i)jailbreak", "severity": 9, "description": "越狱关键词"},
        {"pattern": "(?i)no\\s+restrictions?", "severity": 8, "description": "无限制模式"},
        {"pattern": "(?i)pretend\\s+you\\s+(are|can)", "severity": 8, "description": "假装模式"},
        {"pattern": "(?i)developer\\s+mode", "severity": 9, "description": "开发者模式"}
    ]'::jsonb,
    50000,
    3,
    5,
    8,
    10,
    8,
    true,
    1.0,
    '{"user_ids": [], "ip_cidrs": [], "tenant_ids": []}'::jsonb
) ON CONFLICT (tenant_id, config_name) DO NOTHING;

-- 创建效果评估视图
CREATE OR REPLACE VIEW intent_classification_metrics AS
SELECT 
    tenant_id,
    DATE_TRUNC('day', created_at) AS date,
    predicted_intent,
    
    -- 准确率指标
    COUNT(*) AS total_classifications,
    SUM(CASE WHEN is_correct THEN 1 ELSE 0 END) AS correct_count,
    AVG(CASE WHEN is_correct THEN 1.0 ELSE 0.0 END) AS accuracy,
    
    -- 置信度分析
    AVG(predicted_confidence) AS avg_confidence,
    STDDEV(predicted_confidence) AS confidence_stddev,
    
    -- 用户行为信号
    AVG(CASE WHEN user_accepted_model THEN 1.0 ELSE 0.0 END) AS model_acceptance_rate,
    AVG(user_retry_count) AS avg_retry_count,
    
    -- 会话质量
    AVG(session_duration_sec) AS avg_session_duration,
    AVG(user_satisfaction_score) AS avg_satisfaction_score
FROM intent_classification_feedback
WHERE annotated_at IS NOT NULL  -- 只统计已标注的数据
GROUP BY tenant_id, DATE_TRUNC('day', created_at), predicted_intent;

COMMENT ON VIEW intent_classification_metrics IS
    '意图分类效果指标 — 按天、按租户、按意图类型统计准确率、置信度和用户行为';

-- 创建配置调整效果视图
CREATE OR REPLACE VIEW intent_adjustment_effectiveness AS
SELECT 
    tenant_id,
    adjustment_type,
    target_intent,
    status,
    COUNT(*) AS adjustment_count,
    AVG(effectiveness_score) AS avg_effectiveness,
    AVG(after_accuracy - before_accuracy) AS avg_accuracy_improvement,
    SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END) AS active_count,
    SUM(CASE WHEN status = 'rolled_back' THEN 1 ELSE 0 END) AS rollback_count
FROM intent_analysis_adjustments
WHERE effectiveness_score IS NOT NULL
GROUP BY tenant_id, adjustment_type, target_intent, status;

COMMENT ON VIEW intent_adjustment_effectiveness IS
    '配置调整效果分析 — 统计各类调整的平均效果、准确率提升和回滚率';
