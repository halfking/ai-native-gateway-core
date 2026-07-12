-- fix-third-party-relay.sql
-- 修复第三方中转节点配置
--
-- 问题描述：
--   - apiclaude 和 apigpt provider 存在但没有凭证和模型
--   - 需要添加凭证和模型配置
--
-- 修复方案：
--   1. 在 models_canonical 表中添加缺失的模型
--   2. 在 provider_models 表中添加模型记录
--   3. 在 credentials 表中添加凭证
--   4. 在 credential_model_bindings 表中添加绑定

BEGIN;

-- 1. 添加 claude 系列模型到 models_canonical
INSERT INTO models_canonical (canonical_name, family, source, status, notes, display_name, context_window)
VALUES 
    ('claude-fable-5', 'anthropic-claude', 'seed', 'active', 'Anthropic Claude 5 Fable', 'Claude Fable 5', 200000),
    ('claude-sonnet-4-6', 'anthropic-claude', 'seed', 'active', 'Anthropic Claude 4.6 Sonnet', 'Claude Sonnet 4.6', 200000),
    ('claude-sonnet-5', 'anthropic-claude', 'seed', 'active', 'Anthropic Claude 5 Sonnet', 'Claude Sonnet 5', 200000)
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    display_name = EXCLUDED.display_name,
    updated_at = NOW();

-- 2. 添加 gpt 系列模型到 models_canonical
INSERT INTO models_canonical (canonical_name, family, source, status, notes, display_name, context_window)
VALUES 
    ('gpt-5.6-sola', 'openai-gpt', 'seed', 'active', 'OpenAI GPT-5.6 Sola', 'GPT-5.6 Sola', 128000),
    ('gpt-5.6-luna', 'openai-gpt', 'seed', 'active', 'OpenAI GPT-5.6 Luna', 'GPT-5.6 Luna', 128000),
    ('gpt-5.6-terra', 'openai-gpt', 'seed', 'active', 'OpenAI GPT-5.6 Terra', 'GPT-5.6 Terra', 128000),
    ('gpt-5.4', 'openai-gpt', 'seed', 'active', 'OpenAI GPT-5.4', 'GPT-5.4', 128000)
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    display_name = EXCLUDED.display_name,
    updated_at = NOW();

-- 3. 为 apiclaude provider 添加模型
INSERT INTO provider_models (provider_id, tenant_id, raw_model_name, canonical_id, standardized_name, outbound_model_name, available)
VALUES 
    (587, 'default', 'claude-fable-5', (SELECT id FROM models_canonical WHERE canonical_name = 'claude-fable-5'), 'claude-fable-5', 'claude-fable-5', true),
    (587, 'default', 'claude-opus-4-8', (SELECT id FROM models_canonical WHERE canonical_name = 'claude-opus-4-8'), 'claude-opus-4-8', 'claude-opus-4-8', true),
    (587, 'default', 'claude-sonnet-4-6', (SELECT id FROM models_canonical WHERE canonical_name = 'claude-sonnet-4-6'), 'claude-sonnet-4-6', 'claude-sonnet-4-6', true),
    (587, 'default', 'claude-sonnet-5', (SELECT id FROM models_canonical WHERE canonical_name = 'claude-sonnet-5'), 'claude-sonnet-5', 'claude-sonnet-5', true)
ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET
    canonical_id = EXCLUDED.canonical_id,
    available = EXCLUDED.available,
    updated_at = NOW();

-- 4. 为 apigpt provider 添加模型
INSERT INTO provider_models (provider_id, tenant_id, raw_model_name, canonical_id, standardized_name, outbound_model_name, available)
VALUES 
    (2451, 'default', 'gpt-5.6-sola', (SELECT id FROM models_canonical WHERE canonical_name = 'gpt-5.6-sola'), 'gpt-5.6-sola', 'gpt-5.6-sola', true),
    (2451, 'default', 'gpt-5.6-luna', (SELECT id FROM models_canonical WHERE canonical_name = 'gpt-5.6-luna'), 'gpt-5.6-luna', 'gpt-5.6-luna', true),
    (2451, 'default', 'gpt-5.6-terra', (SELECT id FROM models_canonical WHERE canonical_name = 'gpt-5.6-terra'), 'gpt-5.6-terra', 'gpt-5.6-terra', true),
    (2451, 'default', 'gpt-5.4', (SELECT id FROM models_canonical WHERE canonical_name = 'gpt-5.4'), 'gpt-5.4', 'gpt-5.4', true)
ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET
    canonical_id = EXCLUDED.canonical_id,
    available = EXCLUDED.available,
    updated_at = NOW();

-- 5. 添加 apiclaude 凭证
-- 注意：实际的 secret_ciphertext 需要使用加密 key 加密
-- 这里先添加占位符，实际部署时需要替换
INSERT INTO credentials (
    id, provider_id, tenant_id, label, secret_ciphertext,
    status, lifecycle_status, availability_state,
    quota_state, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
) VALUES (
    587, 587, 'default', 'apiclaude-key',
    'v1:legacy:placeholder',  -- 需要替换为实际加密后的值
    'active', 'active', 'ready',
    'ok', 'closed', false,
    20, 50  -- fp_slot_limit <= concurrency_limit
)
ON CONFLICT (id) DO UPDATE SET
    label = EXCLUDED.label,
    updated_at = NOW();

-- 6. 添加 apigpt 凭证
INSERT INTO credentials (
    id, provider_id, tenant_id, label, secret_ciphertext,
    status, lifecycle_status, availability_state,
    quota_state, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
) VALUES (
    2451, 2451, 'default', 'apigpt-key',
    'v1:legacy:placeholder',  -- 需要替换为实际加密后的值
    'active', 'active', 'ready',
    'ok', 'closed', false,
    20, 50  -- fp_slot_limit <= concurrency_limit
)
ON CONFLICT (id) DO UPDATE SET
    label = EXCLUDED.label,
    updated_at = NOW();

-- 7. 添加 apiclaude 凭证模型绑定
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, billing_mode)
SELECT 
    587,  -- credential_id
    pm.id,  -- provider_model_id
    true,  -- available
    'per_token'  -- billing_mode
FROM provider_models pm
WHERE pm.provider_id = 587
ON CONFLICT DO NOTHING;

-- 8. 添加 apigpt 凭证模型绑定
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, billing_mode)
SELECT 
    2451,  -- credential_id
    pm.id,  -- provider_model_id
    true,  -- available
    'per_token'  -- billing_mode
FROM provider_models pm
WHERE pm.provider_id = 2451
ON CONFLICT DO NOTHING;

-- 9. 验证配置
SELECT 
    'models_canonical' as table_name,
    COUNT(*) as count
FROM models_canonical
WHERE canonical_name IN ('claude-fable-5', 'claude-opus-4-8', 'claude-sonnet-4-6', 'claude-sonnet-5', 'gpt-5.6-sola', 'gpt-5.6-luna', 'gpt-5.6-terra', 'gpt-5.4')
UNION ALL
SELECT 
    'provider_models' as table_name,
    COUNT(*) as count
FROM provider_models
WHERE provider_id IN (587, 2451)
UNION ALL
SELECT 
    'credentials' as table_name,
    COUNT(*) as count
FROM credentials
WHERE id IN (587, 2451)
UNION ALL
SELECT 
    'credential_model_bindings' as table_name,
    COUNT(*) as count
FROM credential_model_bindings
WHERE credential_id IN (587, 2451);

COMMIT;
