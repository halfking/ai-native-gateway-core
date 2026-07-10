-- Migration: feishu_bot_routing_rules
-- 飞书机器人路由规则表（管理 OpenID 白名单 + 审批人映射）
--
-- 场景：
--   1. 简单场景（<10 用户）：使用 feishu_bot.allowed_users（settings_kv，逗号分隔）
--   2. 复杂场景：DB 路由规则（按 tenant_id、risk_level、severity 匹配）
--
-- 本表为场景 2 提供持久化存储：
--   - open_id 飞书 OpenID（必填）
--   - display_name 用户显示名（可选）
--   - user_role admin/member（决定是否需要 @ 提及）
--   - tenant_id 租户隔离
--   - risk_filter low/medium/high/critical（空数组 = 全部适用）
--   - enabled 是否启用
--   - priority 数字越小优先级越高（多条规则匹配时取最高优先级）

CREATE TABLE IF NOT EXISTS feishu_bot_routing_rules (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    open_id VARCHAR(128) NOT NULL,
    display_name VARCHAR(128),
    user_role VARCHAR(16) NOT NULL DEFAULT 'member',  -- admin | member | auditor
    risk_levels JSONB NOT NULL DEFAULT '["low","medium","high","critical"]'::jsonb,
    priority INT NOT NULL DEFAULT 100,
    enabled BOOLEAN NOT NULL DEFAULT true,
    note TEXT,
    created_by VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_feishu_route_openid UNIQUE (tenant_id, open_id),
    CONSTRAINT chk_feishu_route_role CHECK (user_role IN ('admin', 'member', 'auditor'))
);

CREATE INDEX IF NOT EXISTS idx_feishu_route_tenant_enabled
    ON feishu_bot_routing_rules(tenant_id, enabled);
CREATE INDEX IF NOT EXISTS idx_feishu_route_priority
    ON feishu_bot_routing_rules(tenant_id, priority) WHERE enabled = true;
CREATE INDEX IF NOT EXISTS idx_feishu_route_updated_at
    ON feishu_bot_routing_rules(updated_at DESC);

-- 自动更新 updated_at
CREATE OR REPLACE FUNCTION update_feishu_route_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_feishu_route_updated_at ON feishu_bot_routing_rules;
CREATE TRIGGER trg_feishu_route_updated_at
    BEFORE UPDATE ON feishu_bot_routing_rules
    FOR EACH ROW
    EXECUTE FUNCTION update_feishu_route_updated_at();


-- 飞书机器人发送审计表（轻量）
-- 用于追踪每条告警/审批消息的发送状态
CREATE TABLE IF NOT EXISTS feishu_bot_send_log (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    event_type VARCHAR(32) NOT NULL,                    -- alert / approval / command
    event_id VARCHAR(64),                              -- approval_id / alert_fingerprint
    recipients_count INT NOT NULL DEFAULT 0,
    success BOOLEAN NOT NULL,
    error_code INT,
    error_message TEXT,
    latency_ms INT,
    deduped BOOLEAN NOT NULL DEFAULT false,            -- 是否被去重拦截
    rate_limited BOOLEAN NOT NULL DEFAULT false,       -- 是否被限流
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_feishu_send_log_tenant_created
    ON feishu_bot_send_log(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feishu_send_log_event
    ON feishu_bot_send_log(event_id, created_at DESC) WHERE event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_feishu_send_log_failure
    ON feishu_bot_send_log(created_at DESC) WHERE success = false;


COMMENT ON TABLE feishu_bot_routing_rules IS '飞书机器人路由规则：按 (tenant_id, risk_levels) 映射飞书 OpenID 白名单';
COMMENT ON COLUMN feishu_bot_routing_rules.open_id IS '飞书 OpenID，机器人发送消息的目标用户';
COMMENT ON COLUMN feishu_bot_routing_rules.user_role IS 'admin: 可执行命令；member: 普通接收人；auditor: 审计人';
COMMENT ON COLUMN feishu_bot_routing_rules.risk_levels IS '适用的风险级别列表，空 = 不参与匹配';
COMMENT ON COLUMN feishu_bot_routing_rules.priority IS '数字越小优先级越高，同 (tenant, risk) 多个匹配时按升序拼接';

COMMENT ON TABLE feishu_bot_send_log IS '飞书机器人发送审计表：记录每次发送的成功/失败/去重/限流状态';
COMMENT ON COLUMN feishu_bot_send_log.deduped IS 'true = 被 dedup 拦截，未实际发送';
COMMENT ON COLUMN feishu_bot_send_log.rate_limited IS 'true = 被 rate limit 拦截，未实际发送';
