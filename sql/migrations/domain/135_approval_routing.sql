-- Migration 135: Approval Routing Rules & Notification Channels
-- 审批路由规则表 + 通知渠道凭证表，支撑多租户 / 多渠道审批通知

-- 审批路由规则表
CREATE TABLE IF NOT EXISTS approval_routing_rules (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    risk_level VARCHAR(16) NOT NULL,                       -- low/medium/high/critical
    channel_type VARCHAR(16) NOT NULL,                    -- lark/dingtalk/wechat
    approver_ids JSONB NOT NULL DEFAULT '[]'::jsonb,      -- [{"user_id":"xxx","name":"xxx","lark_open_id":"xxx","dingtalk_user_id":"xxx","wechat_user_id":"xxx"}]
    priority INT NOT NULL DEFAULT 0,                       -- 同 (tenant, risk) 多条规则时按 priority 升序
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_routing_risk_level CHECK (risk_level IN ('low','medium','high','critical')),
    CONSTRAINT chk_routing_channel CHECK (channel_type IN ('lark','dingtalk','wechat'))
);

CREATE INDEX IF NOT EXISTS idx_approval_routing_tenant_enabled
    ON approval_routing_rules(tenant_id, enabled);
CREATE INDEX IF NOT EXISTS idx_approval_routing_risk
    ON approval_routing_rules(tenant_id, risk_level) WHERE enabled = true;
CREATE INDEX IF NOT EXISTS idx_approval_routing_updated_at
    ON approval_routing_rules(updated_at DESC);

-- 自动更新 updated_at
CREATE OR REPLACE FUNCTION update_approval_routing_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_approval_routing_updated_at ON approval_routing_rules;
CREATE TRIGGER trg_approval_routing_updated_at
    BEFORE UPDATE ON approval_routing_rules
    FOR EACH ROW
    EXECUTE FUNCTION update_approval_routing_updated_at();


-- 通知渠道凭证配置表
-- 凭证字段存 JSONB（app_secret / encrypt_key 等），不输出到日志
CREATE TABLE IF NOT EXISTS notification_channels (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    channel_type VARCHAR(16) NOT NULL,                    -- lark/dingtalk/wechat
    config JSONB NOT NULL DEFAULT '{}'::jsonb,            -- {"app_id":"xxx","app_secret":"xxx","webhook_url":"xxx",...}
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_notification_channel UNIQUE (tenant_id, channel_type),
    CONSTRAINT chk_notification_channel CHECK (channel_type IN ('lark','dingtalk','wechat'))
);

CREATE INDEX IF NOT EXISTS idx_notification_channels_tenant_enabled
    ON notification_channels(tenant_id, enabled);
CREATE INDEX IF NOT EXISTS idx_notification_channels_updated_at
    ON notification_channels(updated_at DESC);

CREATE OR REPLACE FUNCTION update_notification_channels_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_notification_channels_updated_at ON notification_channels;
CREATE TRIGGER trg_notification_channels_updated_at
    BEFORE UPDATE ON notification_channels
    FOR EACH ROW
    EXECUTE FUNCTION update_notification_channels_updated_at();


-- 通知发送审计表（轻量，仅记录 channel/approval_id/状态，便于排查失败原因）
CREATE TABLE IF NOT EXISTS notification_send_log (
    id BIGSERIAL PRIMARY KEY,
    approval_id VARCHAR(64),
    tenant_id VARCHAR(64) NOT NULL,
    channel_type VARCHAR(16) NOT NULL,
    recipients_count INT NOT NULL DEFAULT 0,
    success BOOLEAN NOT NULL,
    error_message TEXT,
    latency_ms INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notification_send_log_approval
    ON notification_send_log(approval_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_send_log_tenant_created
    ON notification_send_log(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_send_log_failure
    ON notification_send_log(created_at DESC) WHERE success = false;


-- 注释
COMMENT ON TABLE approval_routing_rules IS '审批路由规则，按 (tenant_id, risk_level) 映射接收人';
COMMENT ON COLUMN approval_routing_rules.approver_ids IS 'JSONB 数组：审批人列表，含三渠道 OpenID';
COMMENT ON COLUMN approval_routing_rules.priority IS '同 (tenant, risk) 多条规则时按升序拼接';
COMMENT ON COLUMN approval_routing_rules.enabled IS 'false 时路由时不参与匹配';

COMMENT ON TABLE notification_channels IS '通知渠道凭证配置，config JSONB 包含 app_secret 等敏感字段';
COMMENT ON TABLE notification_send_log IS '通知发送审计表，用于失败原因排查';
