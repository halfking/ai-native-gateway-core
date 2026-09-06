-- =====================================================================
-- 历史问题数据注入脚本（修正版：使用真实生成的 provider_models.id）
-- 生成日期：2026-09-07
-- 目的：模拟历史遗留问题，验证 P0/P1 修复后的三层恢复机制
--       能及时准确地将节点状态从异常翻转为正常
-- =====================================================================

BEGIN;

-- 1. 注入测试凭据
INSERT INTO credentials (id, provider_id, label, manual_disabled, lifecycle_status, status,
                        availability_state, availability_recover_at, quota_state, quota_recover_at,
                        circuit_state, cooling_until, consecutive_failures)
VALUES
  (100, 2, 'test-cred-cooldown-expired', FALSE, 'active', 'active',
   'cooling', now() - INTERVAL '15 minutes', 'ok', NULL,
   'open', now() - INTERVAL '5 minutes', 5),
  (101, 2, 'test-cred-rate-limited-expired', FALSE, 'active', 'active',
   'rate_limited', now() - INTERVAL '20 minutes', 'ok', NULL,
   'closed', NULL, 3),
  (102, 3, 'test-cred-quota-periodic-due', FALSE, 'active', 'active',
   'ready', NULL, 'periodic_exhausted', now() - INTERVAL '5 minutes',
   'closed', NULL, 0),
  (103, 4, 'test-cred-all-broken', FALSE, 'active', 'active',
   'cooling', now() - INTERVAL '30 minutes', 'ok', NULL,
   'open', now() - INTERVAL '20 minutes', 7),
  (104, 4, 'test-cred-partial-broken', FALSE, 'active', 'active',
   'cooling', now() - INTERVAL '15 minutes', 'ok', NULL,
   'open', now() - INTERVAL '5 minutes', 5)
ON CONFLICT (id) DO UPDATE SET
  availability_state = EXCLUDED.availability_state,
  availability_recover_at = EXCLUDED.availability_recover_at,
  quota_state = EXCLUDED.quota_state,
  quota_recover_at = EXCLUDED.quota_recover_at,
  circuit_state = EXCLUDED.circuit_state,
  consecutive_failures = EXCLUDED.consecutive_failures,
  state_updated_at = now();

-- 2. 注入测试 provider_models（用 INSERT ... RETURNING 获取真实 ID）
WITH inserted_models AS (
  INSERT INTO provider_models (provider_id, raw_model_name, canonical_raw_name, standardized_name, outbound_model_name, canonical_id, created_at)
  VALUES
    (2, 'test-claude-opus', 'test-claude-opus', 'test-claude-opus', 'test-claude-opus', NULL, now()),
    (2, 'test-claude-sonnet', 'test-claude-sonnet', 'test-claude-sonnet', 'test-claude-sonnet', NULL, now()),
    (2, 'test-claude-haiku', 'test-claude-haiku', 'test-claude-haiku', 'test-claude-haiku', NULL, now()),
    (3, 'test-gpt-56', 'test-gpt-56', 'test-gpt-56', 'test-gpt-56', NULL, now()),
    (3, 'test-gpt-56-mini', 'test-gpt-56-mini', 'test-gpt-56-mini', 'test-gpt-56-mini', NULL, now()),
    (4, 'test-baichuan-4-pro', 'test-baichuan-4-pro', 'test-baichuan-4-pro', 'test-baichuan-4-pro', NULL, now()),
    (4, 'test-baichuan-3-turbo', 'test-baichuan-3-turbo', 'test-baichuan-3-turbo', 'test-baichuan-3-turbo', NULL, now()),
    (2, 'test-deepseek-flash', 'test-deepseek-flash', 'test-deepseek-flash', 'test-deepseek-flash', NULL, now()),
    (2, 'test-deepseek-pro', 'test-deepseek-pro', 'test-deepseek-pro', 'test-deepseek-pro', NULL, now()),
    (2, 'test-glm-52', 'test-glm-52', 'test-glm-52', 'test-glm-52', NULL, now()),
    (2, 'test-grok-46', 'test-grok-46', 'test-grok-46', 'test-grok-46', NULL, now()),
    (3, 'test-qwen3-max', 'test-qwen3-max', 'test-qwen3-max', 'test-qwen3-max', NULL, now()),
    (4, 'test-mistral-large', 'test-mistral-large', 'test-mistral-large', 'test-mistral-large', NULL, now()),
    (4, 'test-cohere-r-plus', 'test-cohere-r-plus', 'test-cohere-r-plus', 'test-cohere-r-plus', NULL, now()),
    (4, 'test-gemini-25-pro', 'test-gemini-25-pro', 'test-gemini-25-pro', 'test-gemini-25-pro', NULL, now())
  ON CONFLICT (provider_id, raw_model_name) DO UPDATE
    SET canonical_raw_name = EXCLUDED.canonical_raw_name
  RETURNING id, raw_model_name
)
SELECT id, raw_model_name INTO TEMP tmp_pm_ids FROM inserted_models;

-- 3. 注入 bindings（使用 subquery 获取真实 pm.id）
-- 3a. 凭据 100/101 的绑定（unavailable + 有 recover_at）
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 100, pm.id, FALSE, 'continuous_failure', now() - INTERVAL '15 minutes', now() - INTERVAL '5 minutes'
FROM tmp_pm_ids pm WHERE pm.raw_model_name IN ('test-claude-opus', 'test-claude-sonnet')
UNION ALL
SELECT 101, pm.id, FALSE, 'rate_limit', now() - INTERVAL '20 minutes', now() - INTERVAL '10 minutes'
FROM tmp_pm_ids pm WHERE pm.raw_model_name = 'test-claude-haiku'
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = EXCLUDED.available,
  unavailable_reason = EXCLUDED.unavailable_reason,
  unavailable_at = EXCLUDED.unavailable_at,
  unavailable_recover_at = EXCLUDED.unavailable_recover_at;

-- 3b. 凭据 102 的绑定（NULL unavailable_recover_at - 历史遗留）
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 102, pm.id, FALSE, 'continuous_failure', now() - INTERVAL '45 minutes', NULL
FROM tmp_pm_ids pm WHERE pm.raw_model_name IN ('test-gpt-56', 'test-gpt-56-mini')
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = EXCLUDED.available,
  unavailable_reason = EXCLUDED.unavailable_reason,
  unavailable_at = EXCLUDED.unavailable_at,
  unavailable_recover_at = NULL;

-- 3c. 凭据 103 全部模型 broken_confirmed
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 103, pm.id, FALSE, 'probe_broken', now() - INTERVAL '60 minutes', now() - INTERVAL '30 minutes'
FROM tmp_pm_ids pm WHERE pm.raw_model_name IN ('test-baichuan-4-pro', 'test-baichuan-3-turbo')
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = FALSE,
  unavailable_reason = 'probe_broken',
  unavailable_recover_at = now() - INTERVAL '30 minutes';

-- 3d. 凭据 104 一个 broken + 一个连续失败
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, unavailable_reason, unavailable_at, unavailable_recover_at)
SELECT 104, pm.id, FALSE, 'probe_broken', now() - INTERVAL '60 minutes', now() - INTERVAL '15 minutes'
FROM tmp_pm_ids pm WHERE pm.raw_model_name = 'test-baichuan-4-pro'
UNION ALL
SELECT 104, pm.id, FALSE, 'continuous_failure', now() - INTERVAL '15 minutes', now() - INTERVAL '5 minutes'
FROM tmp_pm_ids pm WHERE pm.raw_model_name = 'test-baichuan-3-turbo'
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
  available = EXCLUDED.available,
  unavailable_reason = EXCLUDED.unavailable_reason,
  unavailable_at = EXCLUDED.unavailable_at,
  unavailable_recover_at = EXCLUDED.unavailable_recover_at;

-- 4. 注入 model_probe_state（broken_confirmed）
INSERT INTO model_probe_state (credential_id, raw_model_name, state, consecutive_failures, last_attempt_at, next_retry_at)
VALUES
  (103, 'test-baichuan-4-pro', 'broken_confirmed', 7, now() - INTERVAL '60 minutes', now() + INTERVAL '5 minutes'),
  (103, 'test-baichuan-3-turbo', 'broken_confirmed', 7, now() - INTERVAL '60 minutes', now() + INTERVAL '5 minutes'),
  (104, 'test-baichuan-4-pro', 'broken_confirmed', 7, now() - INTERVAL '60 minutes', now() + INTERVAL '5 minutes')
ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
  state = EXCLUDED.state,
  consecutive_failures = EXCLUDED.consecutive_failures,
  last_attempt_at = EXCLUDED.last_attempt_at;

-- 5. 注入 node_probe_state（部分缺失、部分过期）
-- 5a. 凭据 100/101/103/104 应该有 probe_state
INSERT INTO node_probe_state (credential_id, raw_model_name, next_retry_at, next_retry_seconds, paused, consecutive_failures, last_err_code, in_flight_until)
SELECT 100, pm.raw_model_name, now() + INTERVAL '5 minutes', 300, FALSE, 2, NULL, NULL::timestamptz
FROM tmp_pm_ids pm WHERE pm.raw_model_name IN ('test-claude-opus', 'test-claude-sonnet')
UNION ALL
SELECT 101, pm.raw_model_name, now() + INTERVAL '5 minutes', 300, FALSE, 1, NULL, NULL::timestamptz
FROM tmp_pm_ids pm WHERE pm.raw_model_name = 'test-claude-haiku'
UNION ALL
SELECT 103, pm.raw_model_name, now() + INTERVAL '5 minutes', 300, FALSE, 7, NULL, NULL::timestamptz
FROM tmp_pm_ids pm WHERE pm.raw_model_name IN ('test-baichuan-4-pro', 'test-baichuan-3-turbo')
UNION ALL
SELECT 104, pm.raw_model_name, now() + INTERVAL '5 minutes', 300, FALSE, 7, NULL, NULL::timestamptz
FROM tmp_pm_ids pm WHERE pm.raw_model_name IN ('test-baichuan-4-pro', 'test-baichuan-3-turbo')
ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
  next_retry_at = EXCLUDED.next_retry_at,
  next_retry_seconds = EXCLUDED.next_retry_seconds,
  paused = EXCLUDED.paused,
  consecutive_failures = EXCLUDED.consecutive_failures,
  last_err_code = EXCLUDED.last_err_code;

-- 5b. 11 个过期探测任务（next_retry_at 过期 30 分钟到 1500 分钟）
-- 这些凭据 100/101/103/104 实际没有这些模型名，纯粹为了探测过期统计
INSERT INTO node_probe_state (credential_id, raw_model_name, next_retry_at, next_retry_seconds, paused, consecutive_failures, last_err_code, in_flight_until)
VALUES
  (100, 'overdue-1', now() - INTERVAL '30 minutes', 1800, FALSE, 3, 'timeout', NULL::timestamptz),
  (100, 'overdue-2', now() - INTERVAL '60 minutes', 3600, FALSE, 4, 'network', NULL::timestamptz),
  (100, 'overdue-3', now() - INTERVAL '120 minutes', 7200, FALSE, 5, 'rate_limit', NULL::timestamptz),
  (100, 'overdue-4', now() - INTERVAL '180 minutes', 14400, FALSE, 6, 'stream_timeout', NULL::timestamptz),
  (100, 'overdue-5', now() - INTERVAL '240 minutes', 14400, FALSE, 6, 'model_not_found', NULL::timestamptz),
  (100, 'overdue-6', now() - INTERVAL '360 minutes', 28800, FALSE, 7, 'upstream_fail', NULL::timestamptz),
  (100, 'overdue-7', now() - INTERVAL '500 minutes', 86400, TRUE, 7, 'model_deprecated', NULL::timestamptz),
  (101, 'overdue-8', now() - INTERVAL '45 minutes', 3600, FALSE, 4, 'network', NULL::timestamptz),
  (101, 'overdue-9', now() - INTERVAL '720 minutes', 86400, FALSE, 5, 'transient', NULL::timestamptz),
  (103, 'overdue-10', now() - INTERVAL '1000 minutes', 86400, FALSE, 6, 'rate_limit', NULL::timestamptz),
  (104, 'overdue-11', now() - INTERVAL '1500 minutes', 86400, FALSE, 7, 'context_length_exceeded', NULL::timestamptz)
ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
  next_retry_at = EXCLUDED.next_retry_at,
  next_retry_seconds = EXCLUDED.next_retry_seconds,
  paused = EXCLUDED.paused,
  consecutive_failures = EXCLUDED.consecutive_failures,
  last_err_code = EXCLUDED.last_err_code;

DROP TABLE tmp_pm_ids;

COMMIT;

-- 统计注入结果
SELECT '=== 注入结果统计 ===' AS section;
SELECT 'credentials_injected' AS metric, count(*) FROM credentials WHERE id BETWEEN 100 AND 104
UNION ALL
SELECT 'bindings_injected', count(*) FROM credential_model_bindings WHERE credential_id BETWEEN 100 AND 104
UNION ALL
SELECT 'bindings_with_null_recover_at', count(*) FROM credential_model_bindings
WHERE credential_id BETWEEN 100 AND 104 AND available = FALSE AND unavailable_recover_at IS NULL
UNION ALL
SELECT 'model_probe_state_broken_confirmed', count(*) FROM model_probe_state
WHERE credential_id BETWEEN 100 AND 104 AND state = 'broken_confirmed'
UNION ALL
SELECT 'node_probe_state_total', count(*) FROM node_probe_state WHERE credential_id BETWEEN 100 AND 104
UNION ALL
SELECT 'node_probe_state_overdue', count(*) FROM node_probe_state
WHERE credential_id BETWEEN 100 AND 104
  AND next_retry_at < now() - INTERVAL '5 minutes'
  AND paused = FALSE;
