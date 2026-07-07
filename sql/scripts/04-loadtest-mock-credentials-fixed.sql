-- ====================================================================
-- Loadtest Mock Credentials (修复版) - 适配当前数据库 schema
-- ====================================================================
BEGIN;

-- 清理旧数据
DELETE FROM public.credential_model_bindings WHERE credential_id IN (9010, 9011, 9012, 9013);
DELETE FROM public.provider_models        WHERE provider_id IN (9010, 9011, 9012, 9013);
DELETE FROM public.credentials            WHERE id IN (9010, 9011, 9012, 9013);
DELETE FROM public.providers              WHERE id IN (9010, 9011, 9012, 9013);

-- ── Provider A: mock-A (localhost:19080) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, enabled, manual_disabled
) VALUES (
    9010, 'default', 'loadtest-mock-a', 'Loadtest Mock A',
    'cloud', 'official', 'openai-completions',
    'http://localhost:19080', true, false
);

-- ── Provider B: mock-B (localhost:19081) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, enabled, manual_disabled
) VALUES (
    9011, 'default', 'loadtest-mock-b', 'Loadtest Mock B',
    'cloud', 'official', 'openai-completions',
    'http://localhost:19081', true, false
);

-- ── Provider C: mock-C (localhost:19082) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, enabled, manual_disabled
) VALUES (
    9012, 'default', 'loadtest-mock-c', 'Loadtest Mock C',
    'cloud', 'official', 'openai-completions',
    'http://localhost:19082', true, false
);

-- ── Provider D: mock-D (localhost:19083) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, enabled, manual_disabled
) VALUES (
    9013, 'default', 'loadtest-mock-d', 'Loadtest Mock D',
    'cloud', 'official', 'openai-completions',
    'http://localhost:19083', true, false
);

-- ── Provider Models (每个 provider 都提供 gpt-4o) ──
INSERT INTO public.provider_models (
    id, provider_id, raw_model_name, outbound_model_name
) VALUES
    (9010, 9010, 'gpt-4o', 'gpt-4o'),
    (9011, 9011, 'gpt-4o', 'gpt-4o'),
    (9012, 9012, 'gpt-4o', 'gpt-4o'),
    (9013, 9013, 'gpt-4o', 'gpt-4o');

-- ── Credentials (每个 mock 一个 credential) ──
INSERT INTO public.credentials (
    id, provider_id, tenant_id, label, secret_ciphertext,
    status, lifecycle_status, availability_state,
    quota_state, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
) VALUES
    (9010, 9010, 'default', 'loadtest-mock-a-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'active', 'active', 'ready',
     'ok', 'closed', false, 5, 10),
    (9011, 9011, 'default', 'loadtest-mock-b-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'active', 'active', 'ready',
     'ok', 'closed', false, 5, 10),
    (9012, 9012, 'default', 'loadtest-mock-c-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'active', 'active', 'ready',
     'ok', 'closed', false, 5, 10),
    (9013, 9013, 'default', 'loadtest-mock-d-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'active', 'active', 'ready',
     'ok', 'closed', false, 5, 10);

-- ── Credential ↔ ProviderModel 绑定 ──
INSERT INTO public.credential_model_bindings (
    id, credential_id, provider_model_id, available
) VALUES
    (9010, 9010, 9010, true),
    (9011, 9011, 9011, true),
    (9012, 9012, 9012, true),
    (9013, 9013, 9013, true);

COMMIT;

-- ── 验证 ──
SELECT cred.id, cred.label, p.base_url, pm.raw_model_name
FROM credentials cred
JOIN provider_models pm ON pm.provider_id = cred.provider_id
JOIN providers p ON p.id = cred.provider_id
WHERE cred.id IN (9010, 9011, 9012, 9013)
ORDER BY cred.id;
