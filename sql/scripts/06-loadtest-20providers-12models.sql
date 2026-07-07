-- ====================================================================
-- Loadtest 20 Providers × 12 Models - 本地大规模压测
-- ====================================================================
-- 用途：验证网关在 20 供应商 × 12 模型 × 150 客户端下的全场景故障归一化能力
-- 特性：
--   - 20 个 provider（provider_id 9010-9029，ports 19080-19099）
--   - 每个 provider 支持 12 个模型
--   - tenant_id = 'default'（满足 v_routable 视图过滤条件）
--   - ciphertext 用本地测试 key 加密（AAAA...）
--   - fp_slot_limit=100, concurrency_limit=200（支持 150 并发压测）
--
-- 清理：DELETE FROM ... WHERE provider_id BETWEEN 9010 AND 9029
-- ====================================================================

BEGIN;

-- ── 清理旧 loadtest 数据（幂等）────────────────────────────────────
DELETE FROM public.credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9029;
DELETE FROM public.provider_models           WHERE provider_id BETWEEN 9010 AND 9029;
DELETE FROM public.credentials               WHERE id BETWEEN 9010 AND 9029;
DELETE FROM public.providers                 WHERE id BETWEEN 9010 AND 9029;

-- ── 20 个 Provider ─────────────────────────────────────────────────
INSERT INTO public.providers (id, tenant_id, code, display_name, kind, category, protocol, base_url, enabled, manual_disabled)
SELECT
  9010 + g, 'default',
  'lclmock' || lpad(g::text, 2, '0'),
  'Local Mock ' || lpad(g::text, 2, '0'),
  'cloud', 'official', 'openai-completions',
  'http://localhost:' || (19080 + g), true, false
FROM generate_series(0, 19) AS g;

-- ── 12 个模型名（用于后续 CROSS JOIN）─────────────────────────────────
-- Model list: gpt-4o, gpt-4o-mini, claude-3-opus, claude-3-sonnet,
--             glm-4, glm-4-flash, deepseek-chat, o1-mini,
--             o1-preview, qwen-turbo, qwen-plus, mixtral-8x7b

-- ── Provider Models（20 providers × 12 models = 240 rows）───────────
INSERT INTO public.provider_models (id, provider_id, tenant_id, raw_model_name, standardized_name, outbound_model_name, available)
SELECT
  90000 + (p.id - 9010) * 12 + m.model_idx,
  p.id, 'default',
  m.model_name, m.model_name, m.model_name, true
FROM public.providers p
CROSS JOIN (VALUES
  (0, 'gpt-4o'), (1, 'gpt-4o-mini'), (2, 'claude-3-opus'), (3, 'claude-3-sonnet'),
  (4, 'glm-4'), (5, 'glm-4-flash'), (6, 'deepseek-chat'), (7, 'o1-mini'),
  (8, 'o1-preview'), (9, 'qwen-turbo'), (10, 'qwen-plus'), (11, 'mixtral-8x7b')
) AS m(model_idx, model_name)
WHERE p.id BETWEEN 9010 AND 9029;

-- ── Credentials（20 个，每个 fp_slot=100, concurrency=200）────────────
INSERT INTO public.credentials (
    id, provider_id, tenant_id, label, secret_ciphertext,
    status, lifecycle_status, availability_state,
    quota_state, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
)
SELECT
  9010 + g, 9010 + g, 'default',
  'lclmock' || lpad(g::text, 2, '0') || '-key',
  E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
  'active', 'active', 'ready', 'ok', 'closed', false, 100, 200
FROM generate_series(0, 19) AS g;

-- ── Credential ↔ ProviderModel 全量绑定 ────────────────────────────
-- 每个 credential 绑定到其 provider 的全部 12 个模型
INSERT INTO public.credential_model_bindings (id, credential_id, provider_model_id, available)
SELECT
  90000 + (p.id - 9010) * 12 + m.model_idx,
  p.id,
  (
    SELECT pm2.id FROM public.provider_models pm2
    WHERE pm2.provider_id = p.id AND pm2.raw_model_name = m.model_name
    LIMIT 1
  ),
  true
FROM public.providers p
CROSS JOIN (VALUES
  (0, 'gpt-4o'), (1, 'gpt-4o-mini'), (2, 'claude-3-opus'), (3, 'claude-3-sonnet'),
  (4, 'glm-4'), (5, 'glm-4-flash'), (6, 'deepseek-chat'), (7, 'o1-mini'),
  (8, 'o1-preview'), (9, 'qwen-turbo'), (10, 'qwen-plus'), (11, 'mixtral-8x7b')
) AS m(model_idx, model_name)
WHERE p.id BETWEEN 9010 AND 9029;

COMMIT;

-- ── 验证查询 ────────────────────────────────────────────────────────
SELECT 'providers'           AS obj, count(*)::text AS n FROM providers WHERE id BETWEEN 9010 AND 9029
UNION ALL
SELECT 'provider_models'     AS obj, count(*)::text AS n FROM provider_models WHERE provider_id BETWEEN 9010 AND 9029
UNION ALL
SELECT 'credentials'         AS obj, count(*)::text AS n FROM credentials WHERE id BETWEEN 9010 AND 9029
UNION ALL
SELECT 'bindings'           AS obj, count(*)::text AS n FROM credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9029
UNION ALL
SELECT 'routable bindings'  AS obj, count(*)::text AS n FROM v_routable_credential_models WHERE credential_id BETWEEN 9010 AND 9029 AND is_routable
UNION ALL
SELECT 'models per provider' AS obj, count(DISTINCT raw_model_name)::text AS n FROM provider_models WHERE provider_id BETWEEN 9010 AND 9029;
