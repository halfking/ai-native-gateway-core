-- 最小化初始化脚本：创建必要的表和视图

-- 1. 创建 providers 表
CREATE TABLE IF NOT EXISTS providers (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    code TEXT NOT NULL,
    display_name TEXT,
    catalog_code TEXT,
    protocol TEXT DEFAULT 'openai-completions',
    base_url TEXT NOT NULL,
    enabled BOOLEAN DEFAULT TRUE,
    manual_disabled BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 2. 创建 credentials 表
CREATE TABLE IF NOT EXISTS credentials (
    id SERIAL PRIMARY KEY,
    provider_id INTEGER REFERENCES providers(id),
    tenant_id TEXT NOT NULL DEFAULT 'default',
    label TEXT,
    status TEXT DEFAULT 'active',
    lifecycle_status TEXT DEFAULT 'active',
    availability_state TEXT DEFAULT 'ready',
    quota_state TEXT DEFAULT 'ok',
    manual_disabled BOOLEAN DEFAULT FALSE,
    availability_recover_at TIMESTAMPTZ,
    quota_recover_at TIMESTAMPTZ,
    circuit_state TEXT DEFAULT 'closed',
    consecutive_failures INTEGER DEFAULT 0,
    cooling_until TIMESTAMPTZ,
    state_reason_code TEXT,
    state_reason_detail TEXT,
    state_updated_at TIMESTAMPTZ,
    concurrency_limit INTEGER,
    secret_ciphertext TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 3. 创建 model_offers 表
CREATE TABLE IF NOT EXISTS model_offers (
    id SERIAL PRIMARY KEY,
    credential_id INTEGER REFERENCES credentials(id),
    raw_model_name TEXT NOT NULL,
    standardized_name TEXT,
    outbound_model_name TEXT,
    available BOOLEAN DEFAULT TRUE,
    unavailable_reason TEXT,
    unavailable_at TIMESTAMPTZ,
    unavailable_recover_at TIMESTAMPTZ,
    admin_protected BOOLEAN DEFAULT FALSE,
    routing_tier INTEGER DEFAULT 2,
    weight INTEGER DEFAULT 100,
    manual_priority INTEGER DEFAULT 99,
    active_sessions INTEGER DEFAULT 0,
    consecutive_failures INTEGER DEFAULT 0,
    billing_mode TEXT DEFAULT 'token',
    unit_price_in_per_1m NUMERIC,
    unit_price_out_per_1m NUMERIC,
    currency TEXT DEFAULT 'USD',
    success_rate NUMERIC DEFAULT 0.9,
    p95_latency_ms INTEGER DEFAULT 9999,
    canonical_id INTEGER,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(credential_id, raw_model_name)
);

-- 4. 创建 credential_model_bindings 表（可选，但视图可能需要）
CREATE TABLE IF NOT EXISTS credential_model_bindings (
    id SERIAL PRIMARY KEY,
    credential_id INTEGER REFERENCES credentials(id),
    provider_model_id INTEGER,
    available BOOLEAN DEFAULT TRUE,
    unavailable_reason TEXT,
    unavailable_at TIMESTAMPTZ,
    unavailable_recover_at TIMESTAMPTZ,
    admin_protected BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 5. 创建 provider_models 表（credential_model_bindings 依赖）
CREATE TABLE IF NOT EXISTS provider_models (
    id SERIAL PRIMARY KEY,
    provider_id INTEGER REFERENCES providers(id),
    raw_model_name TEXT NOT NULL,
    outbound_model_name TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 6. 创建 v_routable_credential_models 视图
CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT 
    p.id AS provider_id,
    p.tenant_id,
    c.id AS credential_id,
    mo.raw_model_name,
    -- is_routable: 综合判断该 (credential, model) 组合是否可路由
    (
        COALESCE(p.enabled, TRUE) = TRUE
        AND COALESCE(p.manual_disabled, FALSE) = FALSE
        AND COALESCE(c.status, 'active') = 'active'
        AND COALESCE(c.lifecycle_status, 'active') = 'active'
        AND COALESCE(c.availability_state, 'ready') = 'ready'
        AND COALESCE(c.quota_state, 'ok') = 'ok'
        AND COALESCE(c.manual_disabled, FALSE) = FALSE
        AND COALESCE(mo.available, TRUE) = TRUE
        AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
        AND COALESCE(mo.admin_protected, FALSE) = FALSE
    ) AS is_routable,
    -- unavailable_reason: 如果不可路由，说明原因
    CASE 
        WHEN COALESCE(p.enabled, TRUE) != TRUE THEN 'provider_disabled'
        WHEN COALESCE(p.manual_disabled, FALSE) = TRUE THEN 'provider_manual_disabled'
        WHEN COALESCE(c.status, 'active') != 'active' THEN 'credential_status_' || COALESCE(c.status, 'unknown')
        WHEN COALESCE(c.lifecycle_status, 'active') != 'active' THEN 'lifecycle_' || COALESCE(c.lifecycle_status, 'unknown')
        WHEN COALESCE(c.availability_state, 'ready') != 'ready' THEN 'availability_' || COALESCE(c.availability_state, 'unknown')
        WHEN COALESCE(c.quota_state, 'ok') != 'ok' THEN 'quota_' || COALESCE(c.quota_state, 'unknown')
        WHEN COALESCE(c.manual_disabled, FALSE) = TRUE THEN 'credential_manual_disabled'
        WHEN COALESCE(mo.available, TRUE) != TRUE THEN 'offer_unavailable'
        WHEN COALESCE(mo.unavailable_reason, '') LIKE 'manual%' THEN mo.unavailable_reason
        WHEN COALESCE(mo.admin_protected, FALSE) = TRUE THEN 'offer_admin_protected'
        ELSE NULL
    END AS unavailable_reason
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id;

-- 7. 插入测试数据
-- 插入一个测试 provider
INSERT INTO providers (code, display_name, base_url, enabled, tenant_id)
VALUES ('test-provider', 'Test Provider', 'https://api.example.com', TRUE, 'default')
ON CONFLICT DO NOTHING;

-- 插入一个测试 credential
INSERT INTO credentials (provider_id, label, status, lifecycle_status, availability_state, quota_state, tenant_id)
SELECT id, 'Test Credential', 'active', 'active', 'ready', 'ok', 'default'
FROM providers WHERE code = 'test-provider'
ON CONFLICT DO NOTHING;

-- 插入一个测试 model offer
INSERT INTO model_offers (credential_id, raw_model_name, available)
SELECT c.id, 'gpt-4', TRUE
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE p.code = 'test-provider'
ON CONFLICT (credential_id, raw_model_name) DO NOTHING;

-- 8. 验证
SELECT '=== 表创建完成 ===' as status;
SELECT 'Providers:' as table_name, COUNT(*) as count FROM providers
UNION ALL
SELECT 'Credentials:', COUNT(*) FROM credentials
UNION ALL
SELECT 'Model Offers:', COUNT(*) FROM model_offers;

SELECT '=== 视图验证 ===' as status;
SELECT 
    COUNT(*) as total_records,
    COUNT(*) FILTER (WHERE is_routable = TRUE) as routable_count,
    COUNT(*) FILTER (WHERE is_routable = FALSE) as not_routable_count
FROM v_routable_credential_models
WHERE tenant_id = 'default';

SELECT '=== 样本数据 ===' as status;
SELECT provider_id, credential_id, raw_model_name, is_routable, unavailable_reason
FROM v_routable_credential_models
WHERE tenant_id = 'default'
LIMIT 5;
