-- ====================================================================
-- 184 Mock Test Credentials — 12 个 mock 供应商
-- ====================================================================
-- 用于 184 网关的定时健康探测。与生产数据隔离：
--   - tenant_id = 'default'（视图 v_routable 硬编码 WHERE tenant_id='default'，
--     非 default 租户的 credential 不进视图，无法路由。故必须用 default）
--   - provider_id 段 9010-9021（与生产 provider 不冲突）
--   - ciphertext 用项目测试 key 加密（与 gateway CREDENTIAL_ENCRYPTION_KEY 一致）
--
-- 前置：12 个 mock 进程运行在 184 的 127.0.0.1:19080-19091
-- 清理：scripts/cleanup-184-mocks.sh（按 provider_id BETWEEN 9010 AND 9021 精确删除）
-- ====================================================================

BEGIN;

-- 清理旧 loadtest 数据（幂等，按 provider_id 段精确删除，不影响生产）
DELETE FROM public.credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9099;
DELETE FROM public.provider_models        WHERE provider_id BETWEEN 9010 AND 9099;
DELETE FROM public.credentials            WHERE id BETWEEN 9010 AND 9099;
DELETE FROM public.providers              WHERE id BETWEEN 9010 AND 9099;

-- 12 个 Provider（base_url 指向 184 宿主机 mock，用 Pod 可达的 172.31.0.4）
-- 注意：mock 必须监听 0.0.0.0（非 127.0.0.1），否则 K8s Pod 访问不到宿主机 mock
INSERT INTO public.providers (id, tenant_id, code, display_name, kind, category, protocol, base_url, enabled, manual_disabled)
SELECT
  9010 + g, 'default',
  '184mock' || lpad(g::text, 2, '0'),
  '184 Mock ' || lpad(g::text, 2, '0'),
  'cloud', 'official', 'openai-completions',
  'http://172.31.0.4:' || (19080 + g), true, false
FROM generate_series(0, 11) AS g;

-- Provider Models
INSERT INTO public.provider_models (id, provider_id, tenant_id, raw_model_name, standardized_name, outbound_model_name, available)
SELECT 9010 + g, 9010 + g, 'default', 'gpt-4o', 'gpt-4o', 'gpt-4o', true
FROM generate_series(0, 11) AS g;

-- Credentials（fp_slot_limit=50 <= concurrency_limit=100 满足 CHECK）
INSERT INTO public.credentials (
    id, provider_id, tenant_id, label, secret_ciphertext,
    status, lifecycle_status, availability_state,
    quota_state, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
)
SELECT
  9010 + g, 9010 + g, 'default',
  '184mock' || lpad(g::text, 2, '0') || '-key',
  'v1:legacy:vjWiHSUIEOxTTk1sajTWejxfYsaGnu18MMcag5N5Mebvy6WQ8ewAtOyVcuo',
  'active', 'active', 'ready', 'ok', 'closed', false, 50, 100
FROM generate_series(0, 11) AS g;

-- Credential ↔ ProviderModel 绑定
INSERT INTO public.credential_model_bindings (id, credential_id, provider_model_id, available)
SELECT 9010 + g, 9010 + g, 9010 + g, true
FROM generate_series(0, 11) AS g;

COMMIT;

-- 验证
SELECT 'providers' AS obj, count(*)::text AS n FROM providers WHERE id BETWEEN 9010 AND 9099
UNION ALL SELECT 'routable', count(*)::text FROM v_routable_credential_models WHERE credential_id BETWEEN 9010 AND 9099 AND raw_model_name='gpt-4o' AND is_routable;
