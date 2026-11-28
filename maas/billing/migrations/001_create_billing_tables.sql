-- Migration: 001_create_billing_tables
-- Description: 创建分层计费系统所需的数据库表
-- Author: AI Assistant
-- Date: 2024-11-29

-- ============================================================================
-- 1. 计费配置表
-- ============================================================================
CREATE TABLE IF NOT EXISTS billing_configs (
    id                bigserial PRIMARY KEY,
    tenant_id         text NOT NULL,
    user_id           text,
    billing_mode      text NOT NULL CHECK (billing_mode IN ('pay_as_you_go', 'token_plan', 'booster_pack', 'custom')),
    plan_id           bigint,
    engine_config     jsonb NOT NULL DEFAULT '{}',
    discount_rules    jsonb DEFAULT '[]',
    quota_limits      jsonb,
    cache_priority    int DEFAULT 5 CHECK (cache_priority BETWEEN 1 AND 10),
    cache_ttl_seconds int DEFAULT 300 CHECK (cache_ttl_seconds > 0),
    preload_hint      boolean DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, COALESCE(user_id, ''))
);

CREATE INDEX idx_billing_configs_tenant ON billing_configs(tenant_id);
CREATE INDEX idx_billing_configs_mode ON billing_configs(billing_mode);
CREATE INDEX idx_billing_configs_preload ON billing_configs(preload_hint) WHERE preload_hint = true;

COMMENT ON TABLE billing_configs IS '计费配置表，存储租户和用户的计费模式配置';
COMMENT ON COLUMN billing_configs.billing_mode IS '计费模式: pay_as_you_go, token_plan, booster_pack, custom';
COMMENT ON COLUMN billing_configs.engine_config IS '引擎特定配置（JSON格式）';
COMMENT ON COLUMN billing_configs.cache_priority IS '缓存优先级（1-10），10最高';

-- ============================================================================
-- 2. API Key计费映射表
-- ============================================================================
CREATE TABLE IF NOT EXISTS api_key_billing_map (
    api_key_id        bigint PRIMARY KEY,
    billing_config_id bigint NOT NULL REFERENCES billing_configs(id) ON DELETE CASCADE,
    last_used_at      timestamptz,
    request_count_1h  bigint DEFAULT 0 CHECK (request_count_1h >= 0),
    cache_warm        boolean DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_hot_keys ON api_key_billing_map(request_count_1h DESC, last_used_at DESC);
CREATE INDEX idx_billing_config ON api_key_billing_map(billing_config_id);

COMMENT ON TABLE api_key_billing_map IS 'API Key到计费配置的映射，支持热数据识别';
COMMENT ON COLUMN api_key_billing_map.request_count_1h IS '最近1小时请求数（用于热数据识别）';
COMMENT ON COLUMN api_key_billing_map.cache_warm IS '是否已预加载到缓存';

-- ============================================================================
-- 3. 套餐定义表
-- ============================================================================
CREATE TABLE IF NOT EXISTS billing_plans (
    plan_id           bigserial PRIMARY KEY,
    plan_name         text NOT NULL,
    description       text,
    monthly_quota     bigint NOT NULL CHECK (monthly_quota > 0),
    base_price_cents  bigint NOT NULL CHECK (base_price_cents >= 0),
    overage_rate      numeric(10,4) NOT NULL CHECK (overage_rate >= 0),
    currency          text NOT NULL DEFAULT 'CNY',
    is_active         boolean DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_plans_active ON billing_plans(is_active) WHERE is_active = true;
CREATE UNIQUE INDEX idx_plans_name ON billing_plans(plan_name) WHERE is_active = true;

COMMENT ON TABLE billing_plans IS '套餐定义表，存储包月套餐配置';
COMMENT ON COLUMN billing_plans.monthly_quota IS '月度配额（tokens或credits）';
COMMENT ON COLUMN billing_plans.base_price_cents IS '基础月费（分）';
COMMENT ON COLUMN billing_plans.overage_rate IS '超额费率（分/单位）';

-- ============================================================================
-- 4. 增值包表
-- ============================================================================
CREATE TABLE IF NOT EXISTS booster_packs (
    pack_id           bigserial PRIMARY KEY,
    tenant_id         text NOT NULL,
    pack_type         text NOT NULL,
    quota_amount      bigint NOT NULL CHECK (quota_amount > 0),
    remaining_amount  bigint NOT NULL CHECK (remaining_amount >= 0),
    price_cents       bigint NOT NULL CHECK (price_cents >= 0),
    currency          text NOT NULL,
    purchased_at      timestamptz NOT NULL,
    expires_at        timestamptz,
    status            text NOT NULL CHECK (status IN ('active', 'depleted', 'expired')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CHECK (remaining_amount <= quota_amount),
    CHECK (expires_at IS NULL OR expires_at > purchased_at)
);

CREATE INDEX idx_tenant_packs ON booster_packs(tenant_id, status);
CREATE INDEX idx_packs_expires ON booster_packs(expires_at) WHERE expires_at IS NOT NULL AND status = 'active';

COMMENT ON TABLE booster_packs IS '增值包表，存储预付费增值包信息';
COMMENT ON COLUMN booster_packs.status IS '状态: active, depleted, expired';

-- ============================================================================
-- 5. 触发器：自动更新updated_at
-- ============================================================================
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE TRIGGER update_billing_configs_updated_at BEFORE UPDATE ON billing_configs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_api_key_billing_map_updated_at BEFORE UPDATE ON api_key_billing_map
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_billing_plans_updated_at BEFORE UPDATE ON billing_plans
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_booster_packs_updated_at BEFORE UPDATE ON booster_packs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================================
-- 6. 初始数据
-- ============================================================================

-- 插入默认套餐
INSERT INTO billing_plans (plan_name, description, monthly_quota, base_price_cents, overage_rate, currency) VALUES
    ('Standard', '标准套餐 - 100万tokens/月', 1000000, 100000, 0.015, 'CNY'),
    ('Professional', '专业套餐 - 500万tokens/月', 5000000, 450000, 0.012, 'CNY'),
    ('Enterprise', '企业套餐 - 2000万tokens/月', 20000000, 1600000, 0.010, 'CNY')
ON CONFLICT DO NOTHING;
