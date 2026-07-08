-- ========================================
-- 完整压测拓扑：10 供应商 × 5 模型
-- ========================================
-- 简化版本：不创建 API key（使用默认 test-key）
-- 10 个 mock provider，每个支持 5 个模型
-- ========================================

BEGIN;

-- ══════════════════════════════════════
-- 1. 清理旧数据
-- ══════════════════════════════════════
DELETE FROM credential_model_bindings WHERE credential_id >= 3001 AND credential_id <= 3010;
DELETE FROM provider_models WHERE provider_id >= 3001 AND provider_id <= 3010;
DELETE FROM credentials WHERE id >= 3001 AND id <= 3010;
DELETE FROM providers WHERE id >= 3001 AND id <= 3010;

-- ══════════════════════════════════════
-- 2. 创建 10 个 Provider (mock-provider-01 ~ mock-provider-10)
-- ══════════════════════════════════════
INSERT INTO providers (id, tenant_id, code, display_name, kind, category, protocol, base_url, egress_profile, domestic, enabled, manual_disabled)
SELECT 
  3000 + n,
  'default',
  'mock-provider-' || lpad(n::text, 2, '0'),
  'Mock Provider ' || lpad(n::text, 2, '0'),
  'cloud',
  'official',
  'openai-completions',
  'http://llm-mock-upstream:18080',
  'direct',
  false,
  true,
  false
FROM generate_series(1, 10) n;

-- ══════════════════════════════════════
-- 3. 创建 10 个 Credential (每个 provider 1 个)
-- ══════════════════════════════════════
INSERT INTO credentials (id, provider_id, tenant_id, label, fp_slot_limit, concurrency_limit, health_status, lifecycle_status, availability_state, quota_state)
SELECT 
  3000 + n,
  3000 + n,
  'default',
  'mock-credential-' || lpad(n::text, 2, '0'),
  5,
  10,
  'healthy',
  'active',
  'ready',
  'ok'
FROM generate_series(1, 10) n;

-- ══════════════════════════════════════
-- 4. 创建 5 个模型 × 10 个 provider = 50 个 provider_models
-- ══════════════════════════════════════
INSERT INTO provider_models (provider_id, tenant_id, raw_model_name, available)
SELECT 
  3000 + p.n,
  'default',
  m.model_name,
  true
FROM generate_series(1, 10) p(n)
CROSS JOIN (
  VALUES 
    ('gpt-4o'),
    ('gpt-4o-mini'),
    ('claude-3-opus'),
    ('claude-3-sonnet'),
    ('gemini-pro')
) m(model_name);

-- ══════════════════════════════════════
-- 5. 创建 credential_model_bindings (每个 credential × 5 个模型)
-- ══════════════════════════════════════
WITH pm AS (
  SELECT id AS provider_model_id, provider_id, raw_model_name
  FROM provider_models
  WHERE provider_id >= 3001 AND provider_id <= 3010
)
INSERT INTO credential_model_bindings (credential_id, provider_model_id, routing_tier, weight)
SELECT 
  c.id,
  pm.provider_model_id,
  2,
  100
FROM credentials c
JOIN pm ON pm.provider_id = c.provider_id
WHERE c.id >= 3001 AND c.id <= 3010;

COMMIT;

-- ══════════════════════════════════════
-- 验证
-- ══════════════════════════════════════
\echo '=== Providers (10 total) ==='
SELECT id, code, display_name FROM providers WHERE id >= 3001 AND id <= 3010 ORDER BY id;

\echo ''
\echo '=== Credentials (10 total) ==='
SELECT id, label, health_status FROM credentials WHERE id >= 3001 AND id <= 3010 ORDER BY id;

\echo ''
\echo '=== Provider Models (50 total: 10 providers × 5 models) ==='
SELECT provider_id, COUNT(*) AS model_count, array_agg(raw_model_name ORDER BY raw_model_name) AS models
FROM provider_models 
WHERE provider_id >= 3001 AND provider_id <= 3010
GROUP BY provider_id
ORDER BY provider_id;

\echo ''
\echo '=== Credential Model Bindings (50 total) ==='
SELECT COUNT(*) AS total_bindings FROM credential_model_bindings WHERE credential_id >= 3001 AND credential_id <= 3010;
