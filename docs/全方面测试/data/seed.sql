-- docs/全方面测试/data/seed.sql
--
-- 测试 fixture：12 组 × 5 实例 = 60 个 provider + 每组 1 个 credential + 15 个 model。
-- 参考 docs/全方面测试/04-数据准备方案.md（原始 schema 镜像）。
--
-- 用法：PGPASSWORD=<pw> psql -h <host> -p 5432 -U <user> -d llm_gateway -f seed.sql
--
-- 注意：本 seed 只保留 9010-9099 范围；执行前请先清理旧数据。

BEGIN;

-- 0. 清理残留（如要）
-- DELETE FROM credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9099;
-- DELETE FROM provider_models WHERE provider_id BETWEEN 9010 AND 9099;
-- DELETE FROM credentials WHERE id BETWEEN 9010 AND 9099;
-- DELETE FROM providers WHERE id BETWEEN 9010 AND 9099;

-- ── providers ────────────────────────────────────────────────────────────────
-- 60 个 provider (=12 groups × 5 instances)，provider_id 9010-9069

INSERT INTO providers (id, code, display_name, base_url, kind, enabled,
                       manual_disabled, created_at, updated_at)
SELECT
    g.id,
    'loadtest-' || LPAD(g.id::text, 4, '0'),
    'Loadtest ' || g.id,
    'http://127.0.0.1:' || (19080 + (g.id - 9010) * 5 + g.instance),
    'cloud',
    TRUE,
    FALSE,
    NOW(),
    NOW()
FROM (
    SELECT n + 9010 AS id, i AS instance
    FROM generate_series(0, 59) AS n,
         LATERAL (SELECT n % 5 AS i) AS _
) AS g
ON CONFLICT (id) DO NOTHING;

-- ── credentials ─────────────────────────────────────────────────────────────
-- 每组共享 1 个 credential（每个 provider 用同一个 credential label "default"）
-- 这样 12 组 × 1 credential = 12 个 credentials（id 9010-9021）

INSERT INTO credentials (id, provider_id, tenant_id, label, status, health_status,
                         availability_state, consecutive_failures, circuit_state,
                         fp_slot_limit, effective_concurrency, concurrency_limit,
                         plan_type, balance_usd, created_at, updated_at)
SELECT
    9010 + (g.id_offset),
    NULL,
    'default',
    'default',
    'active',
    'healthy',
    'ready',
    0,
    'closed',
    -- 按 group 配置（来自 mock_orchestrator.GROUP_PROFILE_DEFAULT）
    CASE
        WHEN g.grp = 'A' THEN 50 WHEN g.grp = 'B' THEN 50
        WHEN g.grp = 'C' THEN 5  WHEN g.grp = 'D' THEN 10
        WHEN g.grp = 'E' THEN 30 WHEN g.grp = 'F' THEN 20
        WHEN g.grp = 'G' THEN 30 WHEN g.grp = 'H' THEN 3
        WHEN g.grp = 'I' THEN 20 WHEN g.grp = 'J' THEN 40
        WHEN g.grp = 'K' THEN 20 WHEN g.grp = 'L' THEN 30
    END,
    CASE
        WHEN g.grp IN ('A','B','H') THEN 100
        WHEN g.grp = 'C' THEN 10
        WHEN g.grp = 'D' THEN 20
        WHEN g.grp = 'E' THEN 50
        WHEN g.grp = 'F' THEN 30
        WHEN g.grp = 'G' THEN 60
        WHEN g.grp = 'I' THEN 20
        WHEN g.grp = 'J' THEN 80
        WHEN g.grp = 'K' THEN 40
        WHEN g.grp = 'L' THEN 60
    END,
    CASE
        WHEN g.grp IN ('A','B') THEN 100
        WHEN g.grp = 'C' THEN 10
        WHEN g.grp = 'D' THEN 20
        WHEN g.grp = 'E' THEN 50
        WHEN g.grp = 'F' THEN 30
        WHEN g.grp = 'G' THEN 60
        WHEN g.grp = 'H' THEN 100
        WHEN g.grp = 'I' THEN 20
        WHEN g.grp = 'J' THEN 80
        WHEN g.grp = 'K' THEN 40
        WHEN g.grp = 'L' THEN 60
    END,
    CASE
        WHEN g.grp = 'C' THEN 'tokenplan'
        WHEN g.grp = 'D' THEN 'codeplan'
        WHEN g.grp = 'F' THEN 'free'
        ELSE 'payg'
    END,
    CASE
        WHEN g.grp = 'C' THEN 1000  -- TokenPlan 1000 USD
        WHEN g.grp = 'D' THEN 1000
        WHEN g.grp = 'F' THEN 0
        ELSE 500
    END,
    NOW(),
    NOW()
FROM (
    SELECT
        n AS id_offset,
        CASE n
            WHEN 0 THEN 'A' WHEN 1 THEN 'B' WHEN 2 THEN 'C' WHEN 3 THEN 'D'
            WHEN 4 THEN 'E' WHEN 5 THEN 'F' WHEN 6 THEN 'G' WHEN 7 THEN 'H'
            WHEN 8 THEN 'I' WHEN 9 THEN 'J' WHEN 10 THEN 'K' WHEN 11 THEN 'L'
        END AS grp
    FROM generate_series(0, 11) AS n
) AS g
ON CONFLICT (id) DO NOTHING;

-- ── provider_models ─────────────────────────────────────────────────────────
-- 15 个 model：每个 model 走 60 个 provider 中的 12 个（一个 group 一个代表）
-- model_id 用 9010 + (model index × 100)（避开 production id 空间）

-- 5 个 tier × 3 suffix (alpha/beta/gamma) = 15 个
WITH tier_models AS (
    SELECT * FROM (VALUES
        ('loadtest-mini-alpha',    'loadtest-mini-alpha',    100),
        ('loadtest-mini-beta',     'loadtest-mini-beta',     101),
        ('loadtest-mini-gamma',    'loadtest-mini-gamma',    102),
        ('loadtest-standard-alpha','loadtest-standard-alpha',200),
        ('loadtest-standard-beta', 'loadtest-standard-beta', 201),
        ('loadtest-standard-gamma','loadtest-standard-gamma',202),
        ('loadtest-pro-alpha',     'loadtest-pro-alpha',     300),
        ('loadtest-pro-beta',      'loadtest-pro-beta',      301),
        ('loadtest-pro-gamma',     'loadtest-pro-gamma',     302),
        ('loadtest-ultra-alpha',   'loadtest-ultra-alpha',   400),
        ('loadtest-ultra-beta',    'loadtest-ultra-beta',    401),
        ('loadtest-ultra-gamma',   'loadtest-ultra-gamma',   402),
        ('loadtest-vision-alpha',  'loadtest-vision-alpha',  500),
        ('loadtest-vision-beta',   'loadtest-vision-beta',   501),
        ('loadtest-vision-gamma',  'loadtest-vision-gamma',  502)
    ) AS t(model_code, raw_model_name, model_seed_offset)
)
INSERT INTO provider_models (id, raw_model_name, outbound_model_name, provider_id,
                             billing_mode, standardized_name, created_at)
SELECT
    9010 + tm.model_seed_offset + (g.id_offset),
    tm.raw_model_name,
    tm.raw_model_name,
    9010 + g.id_offset,
    CASE WHEN g.grp IN ('C','D') THEN 'plan' ELSE 'payg' END,
    tm.raw_model_name,
    NOW()
FROM tier_models tm
CROSS JOIN (
    SELECT n AS id_offset,
           CASE n
               WHEN 0 THEN 'A' WHEN 1 THEN 'B' WHEN 2 THEN 'C' WHEN 3 THEN 'D'
               WHEN 4 THEN 'E' WHEN 5 THEN 'F' WHEN 6 THEN 'G' WHEN 7 THEN 'H'
               WHEN 8 THEN 'I' WHEN 9 THEN 'J' WHEN 10 THEN 'K' WHEN 11 THEN 'L'
           END AS grp
    FROM generate_series(0, 11) AS n
) AS g
ON CONFLICT DO NOTHING;

-- ── credential_model_bindings ─────────────────────────────────────────────
-- 每个 credential (代表一组) 绑定到该 group 对应的 60 个 model instances (15 模型 × 5 实例)

INSERT INTO credential_model_bindings (credential_id, provider_model_id, available,
                                       admin_protected, created_at, updated_at)
SELECT
    9010 + (g.id_offset),
    pm.id,
    TRUE,
    FALSE,
    NOW(),
    NOW()
FROM (
    SELECT n AS id_offset,
           CASE n
               WHEN 0 THEN 'A' WHEN 1 THEN 'B' WHEN 2 THEN 'C' WHEN 3 THEN 'D'
               WHEN 4 THEN 'E' WHEN 5 THEN 'F' WHEN 6 THEN 'G' WHEN 7 THEN 'H'
               WHEN 8 THEN 'I' WHEN 9 THEN 'J' WHEN 10 THEN 'K' WHEN 11 THEN 'L'
           END AS grp
    FROM generate_series(0, 11) AS n
) AS g
JOIN LATERAL (
    SELECT id FROM provider_models
    WHERE id BETWEEN 9010 + (g.id_offset) + 100 AND 9010 + (g.id_offset) + 102
    UNION ALL
    SELECT id FROM provider_models
    WHERE id BETWEEN 9010 + (g.id_offset) + 200 AND 9010 + (g.id_offset) + 202
    UNION ALL
    SELECT id FROM provider_models
    WHERE id BETWEEN 9010 + (g.id_offset) + 300 AND 9010 + (g.id_offset) + 302
    UNION ALL
    SELECT id FROM provider_models
    WHERE id BETWEEN 9010 + (g.id_offset) + 400 AND 9010 + (g.id_offset) + 402
    UNION ALL
    SELECT id FROM provider_models
    WHERE id BETWEEN 9010 + (g.id_offset) + 500 AND 9010 + (g.id_offset) + 502
) AS pm ON TRUE
ON CONFLICT DO NOTHING;

-- ── api_keys ────────────────────────────────────────────────────────────────
-- 8 个测试 API key，全部 router-eligible
INSERT INTO api_keys (key_hash, key_prefix, owner_user, rate_limit_rpm,
                      rate_limit_concurrent, rate_limit_tpm, created_at, updated_at)
SELECT
    'sk-loadtest-' || LPAD(n::text, 2, '0') || '-hash-' || LPAD(n::text, 8, '0'),
    'sk-loadtest-' || LPAD(n::text, 2, '0'),
    'loadtest-user-' || n,
    6000,
    100,
    1000000,
    NOW(),
    NOW()
FROM generate_series(1, 8) AS n
ON CONFLICT (key_hash) DO NOTHING;

-- Verify
SELECT 'seed complete:' AS info;
SELECT 'providers'  AS tbl, COUNT(*) FROM providers WHERE id BETWEEN 9010 AND 9069
UNION ALL
SELECT 'credentials', COUNT(*) FROM credentials WHERE id BETWEEN 9010 AND 9021
UNION ALL
SELECT 'provider_models', COUNT(*) FROM provider_models WHERE id BETWEEN 9110 AND 9542
UNION ALL
SELECT 'cmb', COUNT(*) FROM credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9021
UNION ALL
SELECT 'api_keys', COUNT(*) FROM api_keys WHERE key_hash LIKE 'sk-loadtest-%';

COMMIT;
