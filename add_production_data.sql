-- 添加生产级 Provider 和 Credential 数据
-- 注意：这些是示例配置，需要根据实际情况调整

BEGIN;

-- 1. 添加 OpenAI Provider
INSERT INTO providers (code, display_name, base_url, protocol, enabled, tenant_id, catalog_code)
VALUES 
    ('openai', 'OpenAI', 'https://api.openai.com', 'openai-completions', TRUE, 'default', 'openai')
ON CONFLICT DO NOTHING;

-- 2. 添加 Anthropic Provider
INSERT INTO providers (code, display_name, base_url, protocol, enabled, tenant_id, catalog_code)
VALUES 
    ('anthropic', 'Anthropic', 'https://api.anthropic.com', 'anthropic', TRUE, 'default', 'anthropic')
ON CONFLICT DO NOTHING;

-- 3. 添加 Azure OpenAI Provider
INSERT INTO providers (code, display_name, base_url, protocol, enabled, tenant_id, catalog_code)
VALUES 
    ('azure-openai', 'Azure OpenAI', 'https://your-resource.openai.azure.com', 'azure-openai', TRUE, 'default', 'azure-openai')
ON CONFLICT DO NOTHING;

-- 4. 添加 Google Gemini Provider
INSERT INTO providers (code, display_name, base_url, protocol, enabled, tenant_id, catalog_code)
VALUES 
    ('google-gemini', 'Google Gemini', 'https://generativelanguage.googleapis.com', 'google', TRUE, 'default', 'google')
ON CONFLICT DO NOTHING;

-- 5. 添加 Credentials (注意：secret_ciphertext 需要实际加密的 API key)
-- 这里使用 NULL 作为占位符，实际使用时需要通过应用接口添加加密的密钥

-- OpenAI Credential
INSERT INTO credentials (
    provider_id, 
    label, 
    status, 
    lifecycle_status, 
    availability_state, 
    quota_state, 
    tenant_id,
    secret_ciphertext
)
SELECT 
    id, 
    'OpenAI Production Key', 
    'active', 
    'active', 
    'ready', 
    'ok', 
    'default',
    NULL  -- 需要通过管理界面添加实际的加密密钥
FROM providers WHERE code = 'openai'
ON CONFLICT DO NOTHING;

-- Anthropic Credential
INSERT INTO credentials (
    provider_id, 
    label, 
    status, 
    lifecycle_status, 
    availability_state, 
    quota_state, 
    tenant_id,
    secret_ciphertext
)
SELECT 
    id, 
    'Anthropic Production Key', 
    'active', 
    'active', 
    'ready', 
    'ok', 
    'default',
    NULL
FROM providers WHERE code = 'anthropic'
ON CONFLICT DO NOTHING;

-- 6. 添加常见模型的 Model Offers

-- OpenAI Models
INSERT INTO model_offers (
    credential_id, 
    raw_model_name, 
    standardized_name,
    available, 
    routing_tier,
    weight,
    manual_priority,
    billing_mode,
    unit_price_in_per_1m,
    unit_price_out_per_1m,
    currency,
    success_rate
)
SELECT 
    c.id,
    model_data.name,
    model_data.std_name,
    TRUE,
    model_data.tier,
    model_data.weight,
    model_data.priority,
    model_data.billing,
    model_data.price_in,
    model_data.price_out,
    'USD',
    0.95
FROM credentials c
JOIN providers p ON p.id = c.provider_id
CROSS JOIN (
    VALUES 
        ('gpt-4-turbo', 'gpt-4-turbo', 1, 100, 10, 'token', 10.0, 30.0),
        ('gpt-4', 'gpt-4', 1, 100, 15, 'token', 30.0, 60.0),
        ('gpt-4o', 'gpt-4o', 1, 100, 5, 'token', 5.0, 15.0),
        ('gpt-4o-mini', 'gpt-4o-mini', 1, 100, 3, 'token', 0.15, 0.60),
        ('gpt-3.5-turbo', 'gpt-3.5-turbo', 2, 100, 20, 'token', 0.5, 1.5),
        ('gpt-3.5-turbo-16k', 'gpt-3.5-turbo-16k', 2, 100, 25, 'token', 3.0, 4.0)
) AS model_data(name, std_name, tier, weight, priority, billing, price_in, price_out)
WHERE p.code = 'openai'
ON CONFLICT (credential_id, raw_model_name) DO UPDATE
SET available = TRUE;

-- Anthropic Models
INSERT INTO model_offers (
    credential_id, 
    raw_model_name, 
    standardized_name,
    available, 
    routing_tier,
    weight,
    manual_priority,
    billing_mode,
    unit_price_in_per_1m,
    unit_price_out_per_1m,
    currency,
    success_rate
)
SELECT 
    c.id,
    model_data.name,
    model_data.std_name,
    TRUE,
    model_data.tier,
    model_data.weight,
    model_data.priority,
    model_data.billing,
    model_data.price_in,
    model_data.price_out,
    'USD',
    0.95
FROM credentials c
JOIN providers p ON p.id = c.provider_id
CROSS JOIN (
    VALUES 
        ('claude-3-5-sonnet-20241022', 'claude-3.5-sonnet', 1, 100, 8, 'token', 3.0, 15.0),
        ('claude-3-opus-20240229', 'claude-3-opus', 1, 100, 12, 'token', 15.0, 75.0),
        ('claude-3-sonnet-20240229', 'claude-3-sonnet', 1, 100, 10, 'token', 3.0, 15.0),
        ('claude-3-haiku-20240307', 'claude-3-haiku', 2, 100, 5, 'token', 0.25, 1.25)
) AS model_data(name, std_name, tier, weight, priority, billing, price_in, price_out)
WHERE p.code = 'anthropic'
ON CONFLICT (credential_id, raw_model_name) DO UPDATE
SET available = TRUE;

COMMIT;

-- 7. 验证添加的数据
\echo ''
\echo '=== 生产数据添加完成 ==='
\echo ''

SELECT '--- Providers ---' as section;
SELECT 
    id,
    code,
    display_name,
    enabled,
    base_url
FROM providers
WHERE tenant_id = 'default'
ORDER BY id;

\echo ''
SELECT '--- Credentials ---' as section;
SELECT 
    c.id,
    p.display_name as provider,
    c.label,
    c.status,
    c.availability_state,
    CASE WHEN c.secret_ciphertext IS NOT NULL THEN '已配置' ELSE '⚠️ 未配置' END as api_key_status
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE c.tenant_id = 'default'
ORDER BY c.id;

\echo ''
SELECT '--- Model Offers ---' as section;
SELECT 
    mo.id,
    p.display_name as provider,
    mo.raw_model_name,
    mo.available,
    mo.routing_tier as tier,
    mo.manual_priority as priority
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE c.tenant_id = 'default'
ORDER BY p.display_name, mo.manual_priority;

\echo ''
SELECT '--- 可路由节点统计 ---' as section;
SELECT 
    COUNT(*) as 总节点数,
    COUNT(*) FILTER (WHERE is_routable = TRUE) as 可路由节点,
    COUNT(*) FILTER (WHERE is_routable = FALSE) as 不可路由节点,
    COUNT(DISTINCT credential_id) as 唯一凭据,
    COUNT(DISTINCT raw_model_name) as 唯一模型
FROM v_routable_credential_models
WHERE tenant_id = 'default';

\echo ''
SELECT '--- 不可路由原因（如果有）---' as section;
SELECT 
    unavailable_reason,
    COUNT(*) as count
FROM v_routable_credential_models
WHERE tenant_id = 'default' 
AND is_routable = FALSE
GROUP BY unavailable_reason
ORDER BY count DESC;

\echo ''
\echo '⚠️  注意：Credentials 的 API Key 需要通过管理界面配置！'
\echo '当前所有 credentials 的 secret_ciphertext 都是 NULL。'
\echo ''
\echo '配置步骤：'
\echo '1. 启动应用'
\echo '2. 访问管理界面'
\echo '3. 为每个 credential 添加实际的 API Key'
\echo ''
