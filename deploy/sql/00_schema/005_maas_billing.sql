-- ============================================================================
-- llm-gateway-go MaaS计费相关表结构
-- ============================================================================

-- ----------------------------------------------------------------------------
-- MaaS Settings 表 - MaaS设置
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.maas_settings (
    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    cents_per_credit NUMERIC(10, 4) NOT NULL DEFAULT 0.1,
    base_credits_per_1m BIGINT NOT NULL DEFAULT 10000,
    currency_display VARCHAR(8) NOT NULL DEFAULT 'CNY',
    base_credits_per_1m_out BIGINT,
    base_credits_per_1m_cache_in BIGINT,
    base_credits_per_1m_cache_out BIGINT,
    global_discount NUMERIC(6, 4) NOT NULL DEFAULT 1.0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ----------------------------------------------------------------------------
-- Model Credit Rates 表 - 模型积分费率
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_credit_rates (
    canonical_id INT PRIMARY KEY REFERENCES models_canonical(id) ON DELETE CASCADE,
    credits_per_1m_in BIGINT,
    credits_per_1m_out BIGINT,
    credits_per_1m_cache_in BIGINT,
    credits_per_1m_cache_out BIGINT,
    manual_in BOOLEAN NOT NULL DEFAULT FALSE,
    manual_out BOOLEAN NOT NULL DEFAULT FALSE,
    manual_cache_in BOOLEAN NOT NULL DEFAULT FALSE,
    manual_cache_out BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ----------------------------------------------------------------------------
-- Pricing Plans 表 - 定价计划
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.pricing_plans (
    id SERIAL PRIMARY KEY,
    plan_code TEXT NOT NULL UNIQUE,
    plan_name TEXT NOT NULL,
    plan_type TEXT NOT NULL DEFAULT 'subscription'
        CHECK (plan_type IN ('subscription', 'pay_as_you_go', 'enterprise')),
    tenant_id TEXT,
    status VARCHAR(32) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'suspended', 'expired')),
    base_price NUMERIC(12, 2),
    base_credits BIGINT,
    included_tokens BIGINT,
    price_per_1m_input NUMERIC(10, 4),
    price_per_1m_output NUMERIC(10, 4),
    features JSONB DEFAULT '{}',
    started_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pricing_plans_code ON pricing_plans(plan_code);
CREATE INDEX IF NOT EXISTS idx_pricing_plans_tenant ON pricing_plans(tenant_id);

-- ----------------------------------------------------------------------------
-- Credit Ledger 表（分区表）- 积分账本
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.credit_ledger (
    id BIGSERIAL,
    tenant_id TEXT NOT NULL,
    transaction_type VARCHAR(32) NOT NULL
        CHECK (transaction_type IN ('credit', 'debit', 'refund', 'adjustment', 'transfer')),
    amount BIGINT NOT NULL,
    balance_after BIGINT NOT NULL,
    source VARCHAR(64),
    reference_id TEXT,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- 创建当前月和未来几个月的分区
-- 注意：生产环境需要根据实际月份创建分区
CREATE TABLE IF NOT EXISTS public.credit_ledger_2026_06
    PARTITION OF public.credit_ledger
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE IF NOT EXISTS public.credit_ledger_2026_07
    PARTITION OF public.credit_ledger
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

CREATE TABLE IF NOT EXISTS public.credit_ledger_2026_08
    PARTITION OF public.credit_ledger
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

-- 旧账本表（保留用于历史数据）
CREATE TABLE IF NOT EXISTS public.credit_ledger_old (
    LIKE public.credit_ledger INCLUDING ALL
);

-- ----------------------------------------------------------------------------
-- Usage Ledger 表（分区表）- 使用量账本
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.usage_ledger (
    id BIGSERIAL,
    tenant_id TEXT NOT NULL,
    api_key_id BIGINT,
    canonical_id INT,
    input_tokens INT NOT NULL DEFAULT 0,
    output_tokens INT NOT NULL DEFAULT 0,
    cache_read_tokens INT NOT NULL DEFAULT 0,
    cache_write_tokens INT NOT NULL DEFAULT 0,
    credits_charged BIGINT NOT NULL DEFAULT 0,
    cost_usd NUMERIC(14, 8),
    usage_date DATE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, usage_date)
) PARTITION BY RANGE (usage_date);

CREATE TABLE IF NOT EXISTS public.usage_ledger_2026_06
    PARTITION OF public.usage_ledger
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE IF NOT EXISTS public.usage_ledger_2026_07
    PARTITION OF public.usage_ledger
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

CREATE TABLE IF NOT EXISTS public.usage_ledger_2026_08
    PARTITION OF public.usage_ledger
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

CREATE TABLE IF NOT EXISTS public.usage_ledger_old (
    LIKE public.usage_ledger INCLUDING ALL
);

-- ----------------------------------------------------------------------------
-- Billing Orders 表 - 计费订单
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.billing_orders (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    order_type VARCHAR(32) NOT NULL
        CHECK (order_type IN ('subscription', 'topup', 'refund', 'adjustment')),
    amount NUMERIC(12, 2) NOT NULL,
    currency VARCHAR(8) NOT NULL DEFAULT 'CNY',
    credits_amount BIGINT,
    payment_method VARCHAR(32),
    payment_status VARCHAR(32) NOT NULL DEFAULT 'pending'
        CHECK (payment_status IN ('pending', 'paid', 'failed', 'refunded', 'cancelled')),
    external_order_id TEXT,
    description TEXT,
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_billing_orders_tenant ON billing_orders(tenant_id);
CREATE INDEX IF NOT EXISTS idx_billing_orders_created ON billing_orders(created_at DESC);

-- ----------------------------------------------------------------------------
-- Tenant Credit Wallets 表 - 租户积分钱包
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tenant_credit_wallets (
    tenant_id TEXT PRIMARY KEY,
    balance BIGINT NOT NULL DEFAULT 0,
    reserved BIGINT NOT NULL DEFAULT 0,
    currency VARCHAR(8) NOT NULL DEFAULT 'CNY',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ----------------------------------------------------------------------------
-- Pricing Refresh Log 表 - 定价刷新日志
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.pricing_refresh_log (
    id SERIAL PRIMARY KEY,
    run_id TEXT NOT NULL,
    run_ts TIMESTAMPTZ NOT NULL,
    trigger VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,
    before_summary JSONB,
    after_summary JSONB,
    diff_count INT,
    artifacts_path TEXT,
    feishu_sent BOOLEAN DEFAULT FALSE,
    duration_seconds INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pricing_refresh_log_run_id ON pricing_refresh_log(run_id);