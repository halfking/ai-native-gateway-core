-- =====================================================================
-- 历史问题数据注入脚本（v2: 完整守卫场景）
-- 生成日期：2026-09-07
-- 目的：模拟 8 类不同恢复场景，验证 RecoverExpired 真实 SQL 的 5 个守卫
--       1. NOT LIKE 'manual%' （manual_disabled 场景）
--       2. <> 'model_probe_broken' （probe_broken 模型不应盲重试）
--       3. admin_protected = FALSE
--       4. unavailable_recover_at < now()
--       5. broken_confirmed 守卫（凭据级）
-- =====================================================================

BEGIN;

-- 1. 注入测试凭据（6 个，覆盖所有场景）
INSERT INTO credentials (id, provider_id, label, manual_disabled, lifecycle_status, status,
                        availability_state, availability_recover_at, quota_state, quota_recover_at,
                        circuit_state, cooling_until, consecutive_failures)
VALUES
  -- 凭据 100：典型冷却期满 + 应被 RecoverExpired 恢复
  (100, 2, 'recoverable-cooldown', FALSE, 'active', 'active',
   'cooling', now() - INTERVAL '15 minutes', 'ok', NULL,
   'closed', now() - INTERVAL '5 minutes', 5),
  -- 凭据 101：rate_limited 过期 + 应被恢复
  (101, 2, 'recoverable-rate-limited', FALSE, 'active', 'active',
   'rate_limited', now() - INTERVAL '20 minutes', 'ok', NULL,
   'closed', NULL, 3),
  -- 凭据 102：周期性配额到期 + 应被恢复
  (102, 3, 'recoverable-quota-periodic', FALSE, 'active', 'active',
   'ready', NULL, 'periodic_exhausted', now() - INTERVAL '5 minutes',
   'closed', NULL, 0),
  -- 凭据 103：所有模型 broken_confirmed + 应被阻止（broken_confirmed 守卫）
  (103, 4, 'all-broken-guard-test', FALSE, 'active', 'active',
   'cooling', now() - INTERVAL '30 minutes', 'ok', NULL,
   'open', now() - INTERVAL '20 minutes', 7),
  -- 凭据 104：1 broken + 1 健康 + 应被恢复（per-model 守卫放宽）
  (104, 4, 'partial-broken-recoverable', FALSE, 'active', 'active',
   'cooling', now() - INTERVAL '15 minutes', 'ok', NULL,
   'open', now() - INTERVAL '5 minutes', 5),
  -- 凭据 105：含 model_probe_broken 绑定 + 该绑定应被 RecoverExpired 跳过
  (105, 2, 'probe-broken-skip-test', FALSE, 'active', 'active',
   'cooling', now() - INTERVAL '15 minutes', 'ok', NULL,
   'closed', now() - INTERVAL '5 minutes', 4),
  -- 凭据 106：含 manual_disabled 绑定 + 该绑定应被 RecoverExpired 跳过
  (106, 2, 'manual-skip-test', FALSE, 'active', 'active',
   'cooling', now() - INTERVAL '15 minutes', 'ok', NULL,
   'closed', now() - INTERVAL '5 minutes', 4)
ON CONFLICT (id) DO UPDATE SET
  availability_state = EXCLUDED.availability_state,
  availability_recover_at = EXCLUDED.availability_recover_at,
  quota_state = EXCLUDED.quota_state,
  quota_recover_at = EXCLUDED.quota_recover_at,
  circuit_state = EXCLUDED.circuit_state,
  consecutive_failures = EXCLUDED.consecutive_failures,
  state_updated_at = now();

-- 2. 注入 provider_models（先获取真实 ID）
WITH inserted_models AS (
  INSERT INTO provider_models (provider_id, raw_model_name, canonical_raw_name, standardized_name, outbound_model_name, canonical_id, created_at)
  VALUES
    (2, 'recov-claude-opus', 'recov-claude-opus', 'recov-claude-opus', 'recov-claude-opus', NULL, now()),
    (2, 'recov-claude-sonnet', 'recov-claude-sonnet', 'recov-claude-sonnet', 'recov-claude-sonnet', NULL, now()),
    (2, 'recov-claude-haiku', 'recov-claude-haiku', 'recov-claude-haiku', 'recov-claude-haiku', NULL, now()),
    (3, 'recov-gpt-56', 'recov-gpt-56', 'recov-gpt-56', 'recov-gpt-56', NULL, now()),
    (3, 'recov-gpt-56-mini', 'recov-gpt-56-mini', 'recov-gpt-56-mini', 'recov-gpt-56-mini', NULL, now()),
    (4, 'recov-baichuan-4-pro', 'recov-baichuan-4-pro', 'recov-baichuan-4-pro', 'recov-baichuan-4-pro', NULL, now()),
    (4, 'recov-baichuan-3-turbo', 'recov-baichuan-3-turbo', 'recov-baichuan-3-turbo', 'recov-baichuan-3-turbo', NULL, now())
  ON CONFLICT (provider_id, raw_model_name) DO UPDATE
    SET canonical_raw_name = EXCLUDED.canonical_raw_name
  RETURNING id, raw_model_name
)
SELECT id, raw_model_name INTO TEMP tmp_pm_v2 FROM inserted_models;

-- 3. 注入 bindings
-- 3a. 凭据 100/101 应被 RecoverExpired 恢复（连续失败/速率限制）
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 100, pm.id, FALSE, 'continuous_failure', now() - INTERVAL '15 minutes', now() - INTERVAL '5 minutes'
FROM tmp_pm_v2 pm WHERE pm.raw_model_name IN ('recov-claude-opus', 'recov-claude-sonnet')
UNION ALL
SELECT 101, pm.id, FALSE, 'rate_limit', now() - INTERVAL '20 minutes', now() - INTERVAL '10 minutes'
FROM tmp_pm_v2 pm WHERE pm.raw_model_name = 'recov-claude-haiku'
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = EXCLUDED.available,
  unavailable_reason = EXCLUDED.unavailable_reason,
  unavailable_at = EXCLUDED.unavailable_at,
  unavailable_recover_at = EXCLUDED.unavailable_recover_at;

-- 3b. 凭据 102 周期配额绑定（连续失败 + 已过期）
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 102, pm.id, FALSE, 'continuous_failure', now() - INTERVAL '45 minutes', now() - INTERVAL '5 minutes'
FROM tmp_pm_v2 pm WHERE pm.raw_model_name IN ('recov-gpt-56', 'recov-gpt-56-mini')
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = EXCLUDED.available,
  unavailable_reason = EXCLUDED.unavailable_reason,
  unavailable_at = EXCLUDED.unavailable_at,
  unavailable_recover_at = EXCLUDED.unavailable_recover_at;

-- 3c. 凭据 103 全部 broken（应被守卫阻止）
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 103, pm.id, FALSE, 'probe_broken', now() - INTERVAL '60 minutes', now() - INTERVAL '30 minutes'
FROM tmp_pm_v2 pm WHERE pm.raw_model_name IN ('recov-baichuan-4-pro', 'recov-baichuan-3-turbo')
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = FALSE,
  unavailable_reason = 'probe_broken',
  unavailable_recover_at = now() - INTERVAL '30 minutes';

-- 3d. 凭据 104 部分 broken（per-model 放宽 → 应被恢复）
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 104, pm.id, FALSE, 'probe_broken', now() - INTERVAL '60 minutes', now() - INTERVAL '15 minutes'
FROM tmp_pm_v2 pm WHERE pm.raw_model_name = 'recov-baichuan-4-pro'
UNION ALL
SELECT 104, pm.id, FALSE, 'continuous_failure', now() - INTERVAL '15 minutes', now() - INTERVAL '5 minutes'
FROM tmp_pm_v2 pm WHERE pm.raw_model_name = 'recov-baichuan-3-turbo'
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = EXCLUDED.available,
  unavailable_reason = EXCLUDED.unavailable_reason,
  unavailable_at = EXCLUDED.unavailable_at,
  unavailable_recover_at = EXCLUDED.unavailable_recover_at;

-- 3e. 凭据 105 含 model_probe_broken 绑定（核心测试：守卫 <> 'model_probe_broken'）
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 105, pm.id, FALSE, 'model_probe_broken', now() - INTERVAL '60 minutes', now() - INTERVAL '5 minutes'
FROM tmp_pm_v2 pm WHERE pm.raw_model_name = 'recov-claude-opus'
UNION ALL
SELECT 105, pm.id, FALSE, 'continuous_failure', now() - INTERVAL '15 minutes', now() - INTERVAL '5 minutes'
FROM tmp_pm_v2 pm WHERE pm.raw_model_name = 'recov-claude-sonnet'
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = EXCLUDED.available,
  unavailable_reason = EXCLUDED.unavailable_reason,
  unavailable_at = EXCLUDED.unavailable_at,
  unavailable_recover_at = EXCLUDED.unavailable_recover_at;

-- 3f. 凭据 106 含 manual_disabled 绑定（核心测试：守卫 NOT LIKE 'manual%'）
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at, admin_protected)
SELECT 106, pm.id, FALSE, 'manual_disabled_by_admin', now() - INTERVAL '15 minutes', now() - INTERVAL '5 minutes', TRUE
FROM tmp_pm_v2 pm WHERE pm.raw_model_name = 'recov-claude-haiku'
UNION ALL
SELECT 106, pm.id, FALSE, 'continuous_failure', now() - INTERVAL '15 minutes', now() - INTERVAL '5 minutes', FALSE
FROM tmp_pm_v2 pm WHERE pm.raw_model_name = 'recov-gpt-56-mini'
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = EXCLUDED.available,
  unavailable_reason = EXCLUDED.unavailable_reason,
  unavailable_at = EXCLUDED.unavailable_at,
  unavailable_recover_at = EXCLUDED.unavailable_recover_at,
  admin_protected = EXCLUDED.admin_protected;

-- 4. 注入 model_probe_state（broken_confirmed）
INSERT INTO model_probe_state (credential_id, raw_model_name, state, consecutive_failures, last_attempt_at, next_retry_at)
VALUES
  (103, 'recov-baichuan-4-pro', 'broken_confirmed', 7, now() - INTERVAL '60 minutes', now() + INTERVAL '5 minutes'),
  (103, 'recov-baichuan-3-turbo', 'broken_confirmed', 7, now() - INTERVAL '60 minutes', now() + INTERVAL '5 minutes'),
  (104, 'recov-baichuan-4-pro', 'broken_confirmed', 7, now() - INTERVAL '60 minutes', now() + INTERVAL '5 minutes')
ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
  state = EXCLUDED.state,
  consecutive_failures = EXCLUDED.consecutive_failures,
  last_attempt_at = EXCLUDED.last_attempt_at;

DROP TABLE tmp_pm_v2;

COMMIT;

-- 验证注入结果
SELECT '=== 注入验证 ===' AS section;
SELECT 'credentials_injected' AS metric, count(*) FROM credentials WHERE id BETWEEN 100 AND 106
UNION ALL
SELECT 'total_bindings', count(*) FROM credential_model_bindings WHERE credential_id BETWEEN 100 AND 106
UNION ALL
SELECT 'unavailable_bindings', count(*) FROM credential_model_bindings
WHERE credential_id BETWEEN 100 AND 106 AND available = FALSE
UNION ALL
SELECT 'manual_protected_bindings', count(*) FROM credential_model_bindings
WHERE credential_id BETWEEN 100 AND 106 AND unavailable_reason LIKE 'manual%'
UNION ALL
SELECT 'model_probe_broken_bindings', count(*) FROM credential_model_bindings
WHERE credential_id BETWEEN 100 AND 106 AND unavailable_reason = 'model_probe_broken'
UNION ALL
SELECT 'broken_confirmed_models', count(*) FROM model_probe_state
WHERE credential_id BETWEEN 100 AND 106 AND state = 'broken_confirmed';
