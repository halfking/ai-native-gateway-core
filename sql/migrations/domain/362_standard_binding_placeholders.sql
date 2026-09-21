-- 362_standard_binding_placeholders.sql
-- Phase: 标准 credential_model_bindings 占位行（xai / moonshot / google-gemini）
-- Idempotent: ON CONFLICT DO NOTHING for placeholder rows;
--             真实 credential 接入后由管理后台/CLI 转为正常行（available=true）。
--
-- 背景：
--   路由过滤（sql/schema/01-schema.sql:17947 等）要求 cmb.available=TRUE 且
--   cmb.unavailable_reason 不以 'manual' 开头，否则该 binding 不参与路由。
--   model_offers 视图把 cmb 行 join provider_models；占位行以 available=false
--   出现，不会被任何调用方误用。
--
-- 设计原则：
--   - 占位行使用 credential_id = 0（"未关联凭据"哨兵），与任何真实 credentials.id
--     都不同；管理后台显示 provider_models 时可一眼识别占位
--   - unavailable_reason = 'placeholder_pending_credential' 是路由过滤黑名单前缀之外
--     的明确值；后续 onboarding 流程可按该 reason 找到全部占位并升级为真绑定
--   - 权重 weight=100 / routing_tier=2 与 361 的 provider_models 默认一致，
--     这样占位一旦 available 翻转即可直接参与路由
--   - 不预填 pricing（unit_price_*, currency, billing_mode 全部 NULL），
--     等模型发现流程拿到真实价格再回填
--   - 不预填 credentials 行（缺 API key；handoff §3 已记录此阻塞）
--
-- 真实 credential 接入后的"激活"流程（不在本迁移内）：
--   1) INSERT INTO credentials (provider code='xai'/'moonshot'/'google-gemini', ...) — 含真实 secret
--   2) UPDATE credential_model_bindings
--        SET credential_id = <new_cred_id>, available = true,
--            unavailable_reason = NULL, unavailable_at = NULL
--      WHERE credential_id = 0 AND provider_model_id IN (...)

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- xai (providers.code='xai'): grok-4.6 → 占位 binding (credential_id=0)
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO credential_model_bindings (
    credential_id, provider_model_id, routing_tier, weight,
    available, unavailable_reason, unavailable_at,
    plan_meta, currency, billing_mode, created_at, updated_at
)
SELECT 0, pm.id, 2, 100,
       false, 'placeholder_pending_credential', NOW(),
       '{"source":"migration-362","state":"pending_credential"}'::jsonb,
       'USD', 'per_token', NOW(), NOW()
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id AND p.tenant_id = pm.tenant_id
WHERE p.code = 'xai'
  AND p.tenant_id = 'default'
  AND pm.tenant_id = 'default'
  AND pm.raw_model_name = 'grok-4.6'
ON CONFLICT (credential_id, provider_model_id) DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────
-- moonshot (providers.code='moonshot'): kimi-k3 / kimi-k2.6 / kimi-k2.7-code(-highspeed)
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO credential_model_bindings (
    credential_id, provider_model_id, routing_tier, weight,
    available, unavailable_reason, unavailable_at,
    plan_meta, currency, billing_mode, created_at, updated_at
)
SELECT 0, pm.id, 2, 100,
       false, 'placeholder_pending_credential', NOW(),
       '{"source":"migration-362","state":"pending_credential"}'::jsonb,
       'CNY', 'per_token', NOW(), NOW()
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id AND p.tenant_id = pm.tenant_id
WHERE p.code = 'moonshot'
  AND p.tenant_id = 'default'
  AND pm.tenant_id = 'default'
  AND pm.raw_model_name IN (
    'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed'
  )
ON CONFLICT (credential_id, provider_model_id) DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────
-- google-gemini (providers.code='google-gemini'): gemini-3.* (10 个 ID)
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO credential_model_bindings (
    credential_id, provider_model_id, routing_tier, weight,
    available, unavailable_reason, unavailable_at,
    plan_meta, currency, billing_mode, created_at, updated_at
)
SELECT 0, pm.id, 2, 100,
       false, 'placeholder_pending_credential', NOW(),
       '{"source":"migration-362","state":"pending_credential"}'::jsonb,
       'USD', 'per_token', NOW(), NOW()
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id AND p.tenant_id = pm.tenant_id
WHERE p.code = 'google-gemini'
  AND p.tenant_id = 'default'
  AND pm.tenant_id = 'default'
  AND pm.raw_model_name LIKE 'gemini-3%'
ON CONFLICT (credential_id, provider_model_id) DO NOTHING;

COMMIT;
