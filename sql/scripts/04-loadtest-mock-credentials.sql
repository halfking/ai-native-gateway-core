-- ====================================================================
-- Loadtest Mock Credentials: 4 个 mock upstream credential
-- ====================================================================
-- 用于验证 gateway 的 routing/failover/sticky 行为。
-- 4 个 provider 分别指向 4 个 mock 实例 (19080-19083)。
--
-- 使用方式:
--   psql -f sql/scripts/04-loadtest-mock-credentials.sql
--
-- 前置条件:
--   - 01-schema.sql + 02-seed.sql 已应用
--   - mock 进程运行在 localhost:19080-19083
-- ====================================================================

BEGIN;

-- ── 清理旧 seed 行 (幂等) ──
DELETE FROM public.credential_model_bindings WHERE credential_id IN (9010, 9011, 9012, 9013);
DELETE FROM public.provider_models        WHERE provider_id IN (9010, 9011, 9012, 9013);
DELETE FROM public.credentials            WHERE id IN (9010, 9011, 9012, 9013);
DELETE FROM public.providers              WHERE id IN (9010, 9011, 9012, 9013);

-- ── Provider A: mock-A (localhost:19080) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, egress_profile, domestic, enabled, manual_disabled
) VALUES (
    9010, 'default', 'loadtest-mock-a', 'Loadtest Mock A',
    'cloud', 'official', 'openai-completions',
    'http://localhost:19080', 'direct', false, true, false
);

-- ── Provider B: mock-B (localhost:19081) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, egress_profile, domestic, enabled, manual_disabled
) VALUES (
    9011, 'default', 'loadtest-mock-b', 'Loadtest Mock B',
    'cloud', 'official', 'openai-completions',
    'http://localhost:19081', 'direct', false, true, false
);

-- ── Provider C: mock-C (localhost:19082) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, egress_profile, domestic, enabled, manual_disabled
) VALUES (
    9012, 'default', 'loadtest-mock-c', 'Loadtest Mock C',
    'cloud', 'official', 'openai-completions',
    'http://localhost:19082', 'direct', false, true, false
);

-- ── Provider D: mock-D (localhost:19083) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, egress_profile, domestic, enabled, manual_disabled
) VALUES (
    9013, 'default', 'loadtest-mock-d', 'Loadtest Mock D',
    'cloud', 'official', 'openai-completions',
    'http://localhost:19083', 'direct', false, true, false
);

-- ── Provider Models (每个 provider 都提供 gpt-4o) ──
INSERT INTO public.provider_models (
    id, provider_id, tenant_id, raw_model_name, standardized_name,
    outbound_model_name, available
) VALUES
    (9010, 9010, 'default', 'gpt-4o', 'gpt-4o', 'gpt-4o', true),
    (9011, 9011, 'default', 'gpt-4o', 'gpt-4o', 'gpt-4o', true),
    (9012, 9012, 'default', 'gpt-4o', 'gpt-4o', 'gpt-4o', true),
    (9013, 9013, 'default', 'gpt-4o', 'gpt-4o', 'gpt-4o', true);

-- ── Credentials (每个 mock 一个 credential) ──
-- secret_ciphertext 使用同一加密 key (LOCAL ONLY)
INSERT INTO public.credentials (
    id, provider_id, tenant_id, label, secret_ciphertext, secret_kid,
    trust_level, status, lifecycle_status, availability_state,
    quota_state, health_status, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
) VALUES
    (9010, 9010, 'default', 'loadtest-mock-a-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'legacy', 'trusted', 'active', 'active', 'ready',
     'ok', 'unknown', 'closed', false, 5, 10),
    (9011, 9011, 'default', 'loadtest-mock-b-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'legacy', 'trusted', 'active', 'active', 'ready',
     'ok', 'unknown', 'closed', false, 5, 10),
    (9012, 9012, 'default', 'loadtest-mock-c-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'legacy', 'trusted', 'active', 'active', 'ready',
     'ok', 'unknown', 'closed', false, 5, 10),
    (9013, 9013, 'default', 'loadtest-mock-d-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'legacy', 'trusted', 'active', 'active', 'ready',
     'ok', 'unknown', 'closed', false, 5, 10);

-- ── Credential ↔ ProviderModel 绑定 (同权重, 同优先级) ──
INSERT INTO public.credential_model_bindings (
    id, credential_id, provider_model_id, routing_tier, weight,
    manual_priority, available
) VALUES
    (9010, 9010, 9010, 1, 100, 50, true),
    (9011, 9011, 9011, 1, 100, 50, true),
    (9012, 9012, 9012, 1, 100, 50, true),
    (9013, 9013, 9013, 1, 100, 50, true);

COMMIT;

-- ── 验证: 4 个 mock credential 全部 routable ──
SELECT cred.id, cred.label, p.base_url, pm.raw_model_name,
       v.is_routable, v.unavailable_reason
FROM credentials cred
JOIN provider_models pm ON pm.provider_id = cred.provider_id
JOIN providers p ON p.id = cred.provider_id
JOIN v_routable_credential_models v
  ON v.credential_id = cred.id AND v.provider_model_id = pm.id
WHERE cred.id IN (9010, 9011, 9012, 9013)
ORDER BY cred.id;
