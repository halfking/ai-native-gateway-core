-- ===========================================================================
-- scripts/seed_mock_providers.sql
-- 注册多个 mock 厂商（使用不存在的名字，方便测试路由稳定性）
-- 用于高并发 + 多供应商路由测试
-- ===========================================================================

BEGIN;

-- 添加 minimax 厂商
INSERT INTO providers (code, display_name, base_url, protocol, enabled, tenant_id, catalog_code)
VALUES ('minimax', 'MiniMax', 'https://api.minimax.chat', 'openai-completions', TRUE, 'default', 'minimax')
ON CONFLICT DO NOTHING;

-- 添加 5 个 mock 厂商（用于多供应商测试）
INSERT INTO providers (code, display_name, base_url, protocol, enabled, tenant_id, catalog_code)
VALUES
    ('mock-provider-alpha', 'Mock Provider Alpha', 'https://api.mock-alpha.local', 'openai-completions', TRUE, 'default', 'mock-alpha'),
    ('mock-provider-beta',  'Mock Provider Beta',  'https://api.mock-beta.local',  'openai-completions', TRUE, 'default', 'mock-beta'),
    ('mock-provider-gamma', 'Mock Provider Gamma', 'https://api.mock-gamma.local', 'openai-completions', TRUE, 'default', 'mock-gamma'),
    ('mock-provider-delta', 'Mock Provider Delta', 'https://api.mock-delta.local', 'openai-completions', TRUE, 'default', 'mock-delta'),
    ('mock-provider-epsilon', 'Mock Provider Epsilon', 'https://api.mock-epsilon.local', 'openai-completions', TRUE, 'default', 'mock-epsilon')
ON CONFLICT DO NOTHING;

-- 为每个 mock 厂商创建凭据（secret_ciphertext 留空，路由时会被 fallback 处理）
INSERT INTO credentials (provider_id, label, status, availability_state, secret_ciphertext, concurrency_limit)
SELECT p.id, 'Mock Key ' || p.display_name, 'active', 'ready', 'PLACEHOLDER_MOCK_KEY', 50
FROM providers p
WHERE p.code IN ('minimax', 'mock-provider-alpha', 'mock-provider-beta', 'mock-provider-gamma', 'mock-provider-delta', 'mock-provider-epsilon')
ON CONFLICT DO NOTHING;

-- 在 model_offers 中注册模型
-- minimax-m2.7 + minimax-m3 + minimax-m2.5 + minimax-abab
INSERT INTO model_offers (credential_id, raw_model_name, standardized_name, outbound_model_name, available, routing_tier, weight)
SELECT c.id, m.model_name, m.model_name, m.model_name, TRUE, m.tier, m.weight
FROM credentials c
JOIN providers p ON p.id = c.provider_id
CROSS JOIN (
    VALUES
        ('minimax-m2.7', 1, 100),
        ('minimax-m3',   1, 90),
        ('minimax-m2.5', 2, 80),
        ('minimax-abab', 2, 70)
) AS m(model_name, tier, weight)
WHERE p.code = 'minimax'
ON CONFLICT (credential_id, raw_model_name) DO NOTHING;

-- 为 mock 厂商注册不同的模型名（让每个厂商都至少有 3 个模型）
INSERT INTO model_offers (credential_id, raw_model_name, standardized_name, outbound_model_name, available, routing_tier, weight)
SELECT c.id, m.model_name, m.model_name, m.model_name, TRUE, m.tier, m.weight
FROM credentials c
JOIN providers p ON p.id = c.provider_id
CROSS JOIN (
    VALUES
        ('gpt-4', 1, 100),
        ('gpt-4o', 1, 95),
        ('claude-3-5-sonnet', 2, 90)
) AS m(model_name, tier, weight)
WHERE p.code LIKE 'mock-provider-%'
ON CONFLICT (credential_id, raw_model_name) DO NOTHING;

-- 报告注册结果
SELECT '=== Providers ===' AS section, p.code, p.display_name FROM providers p ORDER BY p.id;
SELECT '=== Credentials ===' AS section, p.code, c.label, c.status FROM credentials c JOIN providers p ON p.id = c.provider_id ORDER BY c.id;
SELECT '=== Model Offers 统计 ===' AS section,
       COUNT(*) AS total_offers,
       COUNT(DISTINCT raw_model_name) AS distinct_models,
       COUNT(DISTINCT credential_id) AS distinct_credentials
FROM model_offers;

SELECT '=== 可路由节点 (前 30) ===' AS section, provider_id, credential_id, raw_model_name, is_routable
FROM v_routable_credential_models
ORDER BY provider_id, credential_id, raw_model_name
LIMIT 30;

SELECT '=== 可路由节点总数 ===' AS section, COUNT(*) AS routable_count
FROM v_routable_credential_models;

COMMIT;