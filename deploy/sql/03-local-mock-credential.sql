-- ====================================================================
-- 本地测试 seed: 注入一条指向 llm-mock-upstream 的 credential
-- ====================================================================
-- 仅用于本地 R1.12 测试环境 (docker-compose.local-r112.yml)。
-- 让 cmd/gateway (v1 生产入口) 的 /v1/chat/completions 能真正转发到
-- 真 OpenAI 兼容 mock (r112_llm_mock_upstream:18080)。
--
-- 前置条件:
--   - 01-schema.sql + 02-seed.sql + db/migrations/*.sql 已应用
--   - 03-local-mock-credential.sql 由 local-r112-migrate.sh 在末尾加载
--
-- 配对的加密 key (写在 docker-compose gateway.environment):
--   LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=AwoRGB8mLTQ7QklQV15lbHN6gYiPlp2kq7K5wMfO1dw
--   (32-byte deterministic key, base64url. LOCAL ONLY.)
--
-- 幂等: 使用固定 ID + DELETE-then-INSERT, 重复执行不报错。
-- (provider_models / credential_model_bindings 无 pkey, 用固定 id 清理)
-- ====================================================================

BEGIN;

-- ── 清理旧 seed 行 (幂等) ──
DELETE FROM public.credential_model_bindings WHERE id IN (9001, 9002);
DELETE FROM public.provider_models        WHERE id IN (9001, 9002) OR (provider_id = 9001);
DELETE FROM public.credentials            WHERE id = 9001;
DELETE FROM public.providers              WHERE id = 9001;

-- ── Provider: local-mock (指向容器内 llm-mock-upstream) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, egress_profile, domestic, enabled, manual_disabled
) VALUES (
    9001, 'default', 'local-mock', 'Local Mock Upstream',
    'cloud', 'official', 'openai-completions',
    'http://llm-mock-upstream:18080', 'direct', false, true, false
);

-- ── Provider model: gpt-4o + gpt-4o-mini ──
INSERT INTO public.provider_models (
    id, provider_id, tenant_id, raw_model_name, standardized_name,
    outbound_model_name, available
) VALUES
    (9001, 9001, 'default', 'gpt-4o',      'gpt-4o',      'gpt-4o',      true),
    (9002, 9001, 'default', 'gpt-4o-mini', 'gpt-4o-mini', 'gpt-4o-mini', true);

-- ── Credential: local-mock-key ──
-- secret_ciphertext 用 LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY 加密
-- 明文 = "sk-local-mock-not-a-real-key"
-- 密文 = "v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA"
-- (由 secret.EncryptAESGCM 生成, kid="legacy")
INSERT INTO public.credentials (
    id, provider_id, tenant_id, label, secret_ciphertext, secret_kid,
    trust_level, status, lifecycle_status, availability_state,
    quota_state, health_status, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
) VALUES (
    9001, 9001, 'default', 'local-mock-key',
    -- bytea 列, 存 ASCII envelope 字符串 (DecryptAny 按 string 解析)
    E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
    'legacy',
    'trusted', 'active', 'active', 'ready',
    'ok', 'unknown', 'closed', false,
    -- fp_slot_limit 必须 <= concurrency_limit (CHECK 约束)
    5, 10
);

-- ── Credential ↔ ProviderModel 绑定 ──
INSERT INTO public.credential_model_bindings (
    id, credential_id, provider_model_id, routing_tier, weight,
    manual_priority, available
) VALUES
    (9001, 9001, 9001, 1, 100, 99, true),
    (9002, 9001, 9002, 1, 100, 99, true);

COMMIT;

-- ── 验证: 确认 seed 行的 is_routable = true ──
SELECT cred.id, cred.label, p.base_url, pm.raw_model_name,
       v.is_routable, v.unavailable_reason
FROM credentials cred
JOIN provider_models pm ON pm.provider_id = cred.provider_id
JOIN providers p ON p.id = cred.provider_id
JOIN v_routable_credential_models v
  ON v.credential_id = cred.id AND v.provider_model_id = pm.id
WHERE cred.id = 9001;
