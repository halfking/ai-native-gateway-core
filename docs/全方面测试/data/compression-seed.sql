-- ============================================================================
-- 会话压缩测试数据准备
-- 用于本地环境的 Mock 供应商和模型配置
-- ============================================================================

BEGIN;

-- 1. 创建测试模型
INSERT INTO models_canonical (canonical_name, family, context_window, source, status, display_name)
VALUES 
    ('loadtest-gpt-128k', 'openai-gpt', 128000, 'test', 'active', 'LoadTest GPT-128K'),
    ('loadtest-gpt-null-ctx', 'openai-gpt', NULL, 'test', 'active', 'LoadTest GPT NULL Context'),
    ('loadtest-claude-200k', 'anthropic-claude', 200000, 'test', 'active', 'LoadTest Claude 200K')
ON CONFLICT (canonical_name) DO UPDATE 
SET 
    context_window = EXCLUDED.context_window,
    display_name = EXCLUDED.display_name,
    updated_at = NOW();

-- 2. 创建 Mock Providers
INSERT INTO providers (id, tenant_id, label, catalog_code, category, protocol, base_url, enabled, lifecycle_status, quality_fix_mode)
VALUES 
    (9000, 'default', 'mock-direct-gpt', 'mock', 'official', 'openai-completions', 'http://localhost:19200', true, 'active', 'off'),
    (9001, 'default', 'mock-relay-gpt', 'mock', 'third_party_relay', 'openai-completions', 'http://localhost:19205', true, 'active', 'off'),
    (9002, 'default', 'mock-claude', 'mock', 'official', 'anthropic-messages', 'http://localhost:19210', true, 'active', 'off')
ON CONFLICT (id) DO UPDATE 
SET 
    base_url = EXCLUDED.base_url,
    enabled = EXCLUDED.enabled,
    updated_at = NOW();

-- 3. 创建 Credentials (A组: 9200-9204 直连, B组: 9205-9209 中转, C组: 9210-9214 Claude)
INSERT INTO credentials (id, provider_id, tenant_id, label, secret_ciphertext, status, lifecycle_status, concurrency_limit, fp_slot_limit, plan_type)
SELECT 
    9200 + (gs - 1) AS id,
    CASE 
        WHEN gs <= 5 THEN 9000  -- A组直连
        WHEN gs <= 10 THEN 9001 -- B组中转
        ELSE 9002               -- C组Claude
    END AS provider_id,
    'default' AS tenant_id,
    'mock-compression-cred-' || (9200 + gs - 1) AS label,
    'v1:test:mock-key-' || (9200 + gs - 1) AS secret_ciphertext,
    'active' AS status,
    'active' AS lifecycle_status,
    50 AS concurrency_limit,
    20 AS fp_slot_limit,
    'per_token' AS plan_type
FROM generate_series(1, 15) gs
ON CONFLICT (id) DO UPDATE 
SET 
    provider_id = EXCLUDED.provider_id,
    status = 'active',
    updated_at = NOW();

-- 4. 绑定模型到凭据 (model_offers)
-- A组和B组绑定 GPT-128K
INSERT INTO model_offers (credential_id, raw_model_name, canonical_id, standardized_name, outbound_model_name, available, routing_tier, weight)
SELECT 
    c.id AS credential_id,
    'loadtest-gpt-128k' AS raw_model_name,
    mc.id AS canonical_id,
    'loadtest-gpt-128k' AS standardized_name,
    'loadtest-gpt-128k' AS outbound_model_name,
    true AS available,
    1 AS routing_tier,
    100 AS weight
FROM credentials c
CROSS JOIN models_canonical mc
WHERE c.id BETWEEN 9200 AND 9209
  AND mc.canonical_name = 'loadtest-gpt-128k'
ON CONFLICT (credential_id, raw_model_name) DO UPDATE 
SET 
    available = true,
    updated_at = NOW();

-- C组绑定 Claude
INSERT INTO model_offers (credential_id, raw_model_name, canonical_id, standardized_name, outbound_model_name, available, routing_tier, weight)
SELECT 
    c.id AS credential_id,
    'loadtest-claude-200k' AS raw_model_name,
    mc.id AS canonical_id,
    'loadtest-claude-200k' AS standardized_name,
    'loadtest-claude-200k' AS outbound_model_name,
    true AS available,
    1 AS routing_tier,
    100 AS weight
FROM credentials c
CROSS JOIN models_canonical mc
WHERE c.id BETWEEN 9210 AND 9214
  AND mc.canonical_name = 'loadtest-claude-200k'
ON CONFLICT (credential_id, raw_model_name) DO UPDATE 
SET 
    available = true,
    updated_at = NOW();

-- 5. 绑定 NULL context 测试模型
INSERT INTO model_offers (credential_id, raw_model_name, canonical_id, standardized_name, outbound_model_name, available, routing_tier, weight)
SELECT 
    9200 AS credential_id,
    'loadtest-gpt-null-ctx' AS raw_model_name,
    mc.id AS canonical_id,
    'loadtest-gpt-null-ctx' AS standardized_name,
    'loadtest-gpt-null-ctx' AS outbound_model_name,
    true AS available,
    1 AS routing_tier,
    100 AS weight
FROM models_canonical mc
WHERE mc.canonical_name = 'loadtest-gpt-null-ctx'
ON CONFLICT (credential_id, raw_model_name) DO UPDATE 
SET 
    available = true,
    updated_at = NOW();

-- 6. 设置 Provider Settings
-- B组(中转)禁用压缩，模拟生产问题
INSERT INTO provider_settings (provider_id, key, value, enabled, source)
VALUES 
    (9001, 'compression.mode', '"off"', true, 'admin')
ON CONFLICT (provider_id, key) DO UPDATE 
SET 
    value = '"off"',
    enabled = true,
    updated_at = NOW();

-- A组和C组启用智能压缩
INSERT INTO provider_settings (provider_id, key, value, enabled, source)
VALUES 
    (9000, 'compression.mode', '"smart"', true, 'admin'),
    (9002, 'compression.mode', '"smart"', true, 'admin')
ON CONFLICT (provider_id, key) DO UPDATE 
SET 
    value = '"smart"',
    enabled = true,
    updated_at = NOW();

-- 7. 创建测试 API Keys
INSERT INTO api_keys (tenant_id, key_hash, label, status, rate_limit_rpm, rate_limit_tpm, created_by)
VALUES 
    ('default', 'hash-test-compression-key', 'compression-test-key', 'active', 1000, 1000000, 'system')
ON CONFLICT (key_hash) DO UPDATE 
SET 
    status = 'active',
    updated_at = NOW();

-- 8. 创建模型别名（方便测试）
INSERT INTO model_aliases (raw_name, canonical_id, status)
SELECT 
    'compression-test-gpt',
    id,
    'active'
FROM models_canonical 
WHERE canonical_name = 'loadtest-gpt-128k'
ON CONFLICT (raw_name) DO UPDATE 
SET status = 'active';

INSERT INTO model_aliases (raw_name, canonical_id, status)
SELECT 
    'compression-test-claude',
    id,
    'active'
FROM models_canonical 
WHERE canonical_name = 'loadtest-claude-200k'
ON CONFLICT (raw_name) DO UPDATE 
SET status = 'active';

COMMIT;

-- 验证数据
SELECT 
    'Providers' AS entity,
    COUNT(*) AS count
FROM providers 
WHERE id BETWEEN 9000 AND 9002

UNION ALL

SELECT 
    'Credentials',
    COUNT(*)
FROM credentials 
WHERE id BETWEEN 9200 AND 9214

UNION ALL

SELECT 
    'Model Offers',
    COUNT(*)
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
WHERE c.id BETWEEN 9200 AND 9214

UNION ALL

SELECT 
    'Provider Settings',
    COUNT(*)
FROM provider_settings 
WHERE provider_id BETWEEN 9000 AND 9002;

-- 显示配置摘要
SELECT 
    p.id AS provider_id,
    p.label AS provider,
    p.category,
    COUNT(DISTINCT c.id) AS credential_count,
    COUNT(DISTINCT mo.raw_model_name) AS model_count,
    ps.value AS compression_mode
FROM providers p
LEFT JOIN credentials c ON c.provider_id = p.id
LEFT JOIN model_offers mo ON mo.credential_id = c.id
LEFT JOIN provider_settings ps ON ps.provider_id = p.id AND ps.key = 'compression.mode'
WHERE p.id BETWEEN 9000 AND 9002
GROUP BY p.id, p.label, p.category, ps.value
ORDER BY p.id;
