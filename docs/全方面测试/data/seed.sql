-- docs/全方面测试/data/seed.sql
--
-- 测试 fixture：60 provider + 60 credential (1-to-1) + 60 provider_model + 60 cmb + 8 api_key
-- 真实 schema 镜像（用 \d 验证列名后写入）。
--
-- 用法：psql -h localhost -p 5432 -U <user> -d llm_gateway -f seed.sql

BEGIN;

-- 清理（idempotent — 测试残留清理）
DELETE FROM credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9069;
DELETE FROM api_keys WHERE key_hash LIKE 'sk-loadtest%';
DELETE FROM provider_models WHERE id BETWEEN 9100 AND 9159;
DELETE FROM credentials WHERE id BETWEEN 9010 AND 9069;
DELETE FROM providers WHERE id BETWEEN 9010 AND 9069;

-- ── 60 providers ─────────────────────────────────────────────────
-- 一对一：每 provider = 1 mock_supplier 进程 (port 19080-19139)

INSERT INTO providers (id, code, display_name, base_url, kind, enabled,
                       manual_disabled, protocol, egress_profile, quality_fix_mode, category,
                       created_at, updated_at)
SELECT
    9010 + i,
    'loadtest-' || LPAD((9010+i)::text, 4, '0'),
    'Loadtest ' || (9010+i),
    'http://' || COALESCE(NULLIF(:'loadtest_host', ''), 'host.docker.internal') || ':' || (19080 + i),
    'cloud',
    TRUE,
    FALSE,
    'openai',
    'direct',
    'off',
    'official',
    NOW(),
    NOW()
FROM generate_series(0, 59) AS i;

-- ── 60 credentials (1-to-1 with provider) ────────────────────────
-- 用 placeholder ciphertext (网关通常按 secret_ciphertext = E'\x00...' 跳过)
-- secret_kid 留 'k1' (default encryption key)

INSERT INTO credentials (id, provider_id, tenant_id, label,
                       secret_ciphertext, secret_kid, status, trust_level,
                       health_status, plan_consumed_json, tags,
                       effective_concurrency, concurrency_limit, fp_slot_limit,
                       created_at, updated_at)
SELECT
    9010 + i,
    9010 + i,
    'default',
    'cred-' || (9010+i),
    NULL,  -- 2026-07-12 main 分支新逻辑不识别 '\x00' placeholder，设为 NULL
    'k1',
    'active',
    'trusted',
    'healthy',
    '{}'::jsonb,
    '[]'::jsonb,
    100,   -- effective_concurrency (避免 C/D fp_slot 卡)
    100,   -- concurrency_limit
    50,    -- fp_slot_limit
    NOW(),
    NOW()
FROM generate_series(0, 59) AS i;

-- ── 60 provider_models ────────────────────────────────────────────
-- 15 个 base model × 4 instance/组 = 60
-- raw_model_name 决定路由：所有 raw_model_name 相同的 provider 进入同 candidates 列表

WITH models(name) AS (
    VALUES ('mini-alpha'), ('mini-beta'), ('mini-gamma'),
           ('standard-alpha'), ('standard-beta'), ('standard-gamma'),
           ('pro-alpha'), ('pro-beta'), ('pro-gamma'),
           ('ultra-alpha'), ('ultra-beta'), ('ultra-gamma'),
           ('vision-alpha'), ('vision-beta'), ('vision-gamma')
)
INSERT INTO provider_models (id, provider_id, raw_model_name, outbound_model_name,
                             canonical_raw_name, standardized_name, created_at)
SELECT
    9100 + i,
    9010 + i,
    'loadtest-' || (SELECT name FROM models OFFSET (i % 15) LIMIT 1),
    'loadtest-' || (SELECT name FROM models OFFSET (i % 15) LIMIT 1),
    'loadtest-' || (SELECT name FROM models OFFSET (i % 15) LIMIT 1),
    'loadtest-' || (SELECT name FROM models OFFSET (i % 15) LIMIT 1),
    NOW()
FROM generate_series(0, 59) AS i;

-- ── 60 cmb (每个 credential 绑自己的 provider_model) ───────────────
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available,
                                       created_at, updated_at)
SELECT
    9010 + i,  -- credential_id
    9100 + i,  -- provider_model_id
    TRUE,
    NOW(),
    NOW()
FROM generate_series(0, 59) AS i;

-- ── Set baseline metrics on every cmb ───────────────────────────
-- Without these, the candidate query treats every credential as 9999ms
-- (COALESCE of NULL p95_latency_ms) which neutralises the latency-aware
-- penalty that S05/S06/S12 rely on. Setting group-appropriate baselines
-- lets P2C prefer fast suppliers even on the first request.
--
-- Group layout (from tools/start_suppliers.sh):
--   A=9010-9014  tier=1 healthy   p95= 50ms
--   B=9015-9019  tier=1 healthy   p95= 60ms
--   C=9020-9024  tier=1 healthy   p95= 70ms (token_plan cost=0)
--   D=9025-9029  tier=1 healthy   p95= 80ms (code_plan cost=0)
--   E=9030-9034  tier=2 healthy   p95=120ms
--   F=9035-9039  tier=2 healthy   p95=150ms
--   G=9040-9044  tier=2 SLOW      p95=3500ms (2-4s delay default)
--   H=9045-9049  tier=1 fp_slot=3 p95= 80ms
--   I=9050-9054  tier=1 healthy   p95=100ms
--   J=9055-9059  tier=1 FLAKY     p95=300ms (intermittent slow)
--   K=9060-9064  tier=3 healthy   p95=110ms (rate-limited by default)
--   L=9065-9069  tier=2 healthy   p95=140ms
UPDATE credential_model_bindings cmb
SET p95_latency_ms = CASE
        WHEN cmb.credential_id BETWEEN 9010 AND 9014 THEN  50
        WHEN cmb.credential_id BETWEEN 9015 AND 9019 THEN  60
        WHEN cmb.credential_id BETWEEN 9020 AND 9024 THEN  70
        WHEN cmb.credential_id BETWEEN 9025 AND 9029 THEN  80
        WHEN cmb.credential_id BETWEEN 9030 AND 9034 THEN 120
        WHEN cmb.credential_id BETWEEN 9035 AND 9039 THEN 150
        WHEN cmb.credential_id BETWEEN 9040 AND 9044 THEN 3500  -- G slow default
        WHEN cmb.credential_id BETWEEN 9045 AND 9049 THEN  80
        WHEN cmb.credential_id BETWEEN 9050 AND 9054 THEN 100
        WHEN cmb.credential_id BETWEEN 9055 AND 9059 THEN 300   -- J flaky
        WHEN cmb.credential_id BETWEEN 9060 AND 9064 THEN 110
        ELSE 140
    END,
    success_rate = CASE
        WHEN cmb.credential_id BETWEEN 9055 AND 9059 THEN 0.85  -- J flaky
        ELSE 0.97
    END,
    billing_mode = CASE
        WHEN cmb.credential_id BETWEEN 9020 AND 9024 THEN 'token_plan'
        WHEN cmb.credential_id BETWEEN 9025 AND 9029 THEN 'code_plan'
        ELSE 'per_token'
    END
WHERE cmb.credential_id BETWEEN 9010 AND 9069;

-- ── 8 api_keys ───────────────────────────────────────────────────
-- application_id 引用真实 application，应用 ID 8001-8008 在真实表里，我们硬编码 8001
INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, owner_user,
                     data_sensitivity, rate_limit_rpm, enabled, status, created_at)
SELECT
    8001,
    'default',
    'sk-loadtest-' || LPAD(n::text, 2, '0') || '-hash-' || LPAD(n::text, 20, '0'),
    'sk-loadtest-' || LPAD(n::text, 2, '0'),
    'loadtest-user-' || n,
    'internal',
    6000,
    TRUE,
    'active',
    NOW()
FROM generate_series(1, 8) AS n
ON CONFLICT (key_hash) DO NOTHING;

-- ── Verify ────────────────────────────────────────────────────────
SELECT 'seed OK:' AS info;
SELECT 'providers'        AS tbl, COUNT(*) FROM providers WHERE id BETWEEN 9010 AND 9069
UNION ALL SELECT 'credentials', COUNT(*) FROM credentials WHERE id BETWEEN 9010 AND 9069
UNION ALL SELECT 'provider_models', COUNT(*) FROM provider_models WHERE id BETWEEN 9100 AND 9159
UNION ALL SELECT 'cmb', COUNT(*) FROM credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9069
UNION ALL SELECT 'api_keys', COUNT(*) FROM api_keys WHERE key_hash LIKE 'sk-loadtest%';

COMMIT;
