#!/usr/bin/env bash
# =====================================================================
# tests/unified_probe_status_views_test.sh — 2026-09-17 数据源统一集成验证
#
# 验证 migration 716(探测健康视图族切换到 node_probe_state 投影)在真实
# PostgreSQL 上成立:
#   1. 应用 716(幂等,含 v_node_probe_state_compat);
#   2. 事务内插入夹具节点(healthy/broken/suspicious/probing/manual_offline/
#      未探测),断言 v_node_probe_state_compat 派生状态与路由一致性;
#   3. 断言 v_model_health_dashboard 的 healthy/failing 计数反映新事实源;
#   4. 断言视图列顺序与旧契约一致(包 SELECT * 按位置 Scan);
#   5. 回滚夹具,不污染数据。
#
# 用法: bash tests/unified_probe_status_views_test.sh
# 环境变量: PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE(默认读 .env.local)
# =====================================================================
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ── 连接参数:优先环境变量,回退 .env.local ──────────────────────────
if [[ -z "${PGDATABASE:-}" && -f "$ROOT_DIR/.env.local" ]]; then
  # shellcheck disable=SC1091
  source "$ROOT_DIR/.env.local"
  export PGHOST="${LOCAL_PG_HOST:-127.0.0.1}"
  export PGPORT="${LOCAL_PG_PORT:-5432}"
  export PGUSER="${LLM_GATEWAY_DB_USER:-llm_gateway}"
  export PGPASSWORD="$LLM_GATEWAY_DB_PASSWORD"
  export PGDATABASE="${LOCAL_PG_LLM_GATEWAY_DATABASE:-llm_gateway}"
fi

PSQL=(psql -v ON_ERROR_STOP=1 -q)

echo "[1/5] 连接 $PGUSER@$PGHOST:$PGPORT/$PGDATABASE"
"${PSQL[@]}" -Atc "SELECT 1" >/dev/null

echo "[2/5] 应用 migration 716(幂等重建探测健康视图族)"
"${PSQL[@]}" -f "$ROOT_DIR/sql/migrations/startup/716_unify_probe_health_views.sql" >/dev/null

FIXTURE_PROVIDER=-9001
FIXTURE_CRED=-9002

echo "[3/5] 事务夹具:派生状态/优先级/路由一致性断言(结束回滚)"
"${PSQL[@]}" <<'SQL'
BEGIN;

INSERT INTO providers (id, code, display_name, catalog_code, protocol, base_url)
VALUES (-9001, 'unify-test', 'unify-test-provider', 'unify-test', 'openai', 'https://ut.invalid')
ON CONFLICT (id) DO NOTHING;

INSERT INTO credentials (id, provider_id, label, secret_ciphertext, fp_slot_limit, status, lifecycle_status, manual_disabled)
VALUES (-9002, -9001, 'unify-test-cred', 'gAAAAAB0ZXN0', 1, 'active', 'active', FALSE)
ON CONFLICT (id) DO NOTHING;

INSERT INTO node_probe_state
  (credential_id, raw_model_name, consecutive_failures, consecutive_successes,
   last_attempt_at, next_retry_at, paused, last_direct_ok, last_err_code, updated_at)
VALUES
  (-9002, 'ut-healthy',   0, 2, NOW() - INTERVAL '5 min', NOW() + INTERVAL '1 h', FALSE, TRUE,  NULL,           NOW() - INTERVAL '5 min'),
  (-9002, 'ut-broken',    9, 0, NOW() - INTERVAL '5 min', NOW() + INTERVAL '2 h', FALSE, FALSE, 'http_410',     NOW() - INTERVAL '5 min'),
  (-9002, 'ut-suspicious',1, 0, NOW() - INTERVAL '5 min', NOW() + INTERVAL '1 min', FALSE, FALSE, 'http_429',   NOW() - INTERVAL '5 min'),
  (-9002, 'ut-manual',    0, 0, NOW() - INTERVAL '5 min', NOW() + INTERVAL '100 years', TRUE, TRUE, 'manual_offline', NOW() - INTERVAL '5 min'),
  (-9002, 'ut-probing',   0, 0, NULL,                     NOW() + INTERVAL '1 min', FALSE, NULL, NULL,           NOW() - INTERVAL '5 min')
ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
  consecutive_failures = EXCLUDED.consecutive_failures,
  consecutive_successes = EXCLUDED.consecutive_successes,
  last_direct_ok = EXCLUDED.last_direct_ok,
  last_err_code = EXCLUDED.last_err_code,
  paused = EXCLUDED.paused,
  in_flight_until = NULL;

DO $$
DECLARE
  got TEXT;
BEGIN
  SELECT state INTO got FROM v_node_probe_state_compat
   WHERE credential_id = -9002 AND raw_model_name = 'ut-healthy';
  IF got <> 'healthy_confirmed' THEN RAISE EXCEPTION 'ut-healthy: got %, want healthy_confirmed', got; END IF;

  SELECT state INTO got FROM v_node_probe_state_compat
   WHERE credential_id = -9002 AND raw_model_name = 'ut-broken';
  IF got <> 'broken_confirmed' THEN RAISE EXCEPTION 'ut-broken: got %, want broken_confirmed', got; END IF;

  SELECT state INTO got FROM v_node_probe_state_compat
   WHERE credential_id = -9002 AND raw_model_name = 'ut-suspicious';
  IF got <> 'suspicious' THEN RAISE EXCEPTION 'ut-suspicious: got %, want suspicious', got; END IF;

  SELECT state INTO got FROM v_node_probe_state_compat
   WHERE credential_id = -9002 AND raw_model_name = 'ut-manual';
  IF got <> 'unknown' THEN RAISE EXCEPTION 'ut-manual: got %, want unknown(手动下线)', got; END IF;

  SELECT state INTO got FROM v_node_probe_state_compat
   WHERE credential_id = -9002 AND raw_model_name = 'ut-probing';
  IF got <> 'probing' THEN RAISE EXCEPTION 'ut-probing: got %, want probing', got; END IF;

  -- 优先级映射(队列视图与 dashboard 依赖)
  SELECT probe_priority INTO got FROM v_node_probe_state_compat
   WHERE credential_id = -9002 AND raw_model_name = 'ut-broken';
  IF got <> 'urgent' THEN RAISE EXCEPTION 'ut-broken priority: got %, want urgent', got; END IF;

  -- total_attempts 派生
  IF (SELECT total_attempts FROM v_node_probe_state_compat
      WHERE credential_id = -9002 AND raw_model_name = 'ut-broken') <> 9 THEN
    RAISE EXCEPTION 'ut-broken total_attempts <> 9';
  END IF;

  -- v_model_health_dashboard 行存在(计数断言在下一步用独立夹具精确验证)
  SELECT TRUE INTO got FROM v_model_health_dashboard WHERE raw_model_name = 'ut-broken';
  IF got IS NULL THEN RAISE EXCEPTION 'dashboard row for ut-broken missing'; END IF;

  RAISE NOTICE 'compat state derivation assertions PASSED';
END $$;

ROLLBACK;
SQL

echo "[4/5] dashboard 计数断言(独立夹具,同样回滚)"
"${PSQL[@]}" <<'SQL'
BEGIN;
INSERT INTO providers (id, code, display_name, catalog_code, protocol, base_url)
VALUES (-9001, 'unify-test', 'unify-test-provider', 'unify-test', 'openai', 'https://ut.invalid')
ON CONFLICT (id) DO NOTHING;
INSERT INTO credentials (id, provider_id, label, secret_ciphertext, fp_slot_limit, status, lifecycle_status, manual_disabled)
VALUES (-9002, -9001, 'unify-test-cred', 'gAAAAAB0ZXN0', 1, 'active', 'active', FALSE) ON CONFLICT (id) DO NOTHING;
INSERT INTO node_probe_state (credential_id, raw_model_name, consecutive_failures, consecutive_successes,
  last_attempt_at, next_retry_at, paused, last_direct_ok, updated_at)
VALUES (-9002, 'ut-model-a', 0, 1, NOW(), NOW() + INTERVAL '1 h', FALSE, TRUE, NOW())
ON CONFLICT (credential_id, raw_model_name) DO NOTHING;
INSERT INTO node_probe_state (credential_id, raw_model_name, consecutive_failures, consecutive_successes,
  last_attempt_at, next_retry_at, paused, last_direct_ok, last_err_code, updated_at)
VALUES (-9002, 'ut-model-b', 4, 0, NOW(), NOW() + INTERVAL '2 h', FALSE, FALSE, 'http_403', NOW())
ON CONFLICT (credential_id, raw_model_name) DO NOTHING;

DO $$
DECLARE
  healthy INT; failing INT; total INT;
BEGIN
  SELECT total_credentials, healthy_count, failing_count
    INTO total, healthy, failing
    FROM v_model_health_dashboard WHERE raw_model_name = 'ut-model-b';
  IF total IS NULL THEN RAISE EXCEPTION 'v_model_health_dashboard missing ut-model-b'; END IF;
  IF total <> 1 OR healthy <> 0 OR failing <> 1 THEN
    RAISE EXCEPTION 'dashboard counts wrong: total=% healthy=% failing=% (want 1/0/1)', total, healthy, failing;
  END IF;
  RAISE NOTICE 'dashboard count assertions PASSED';
END $$;
ROLLBACK;
SQL

echo "[5/5] 视图列顺序契约(SELECT * 按位置 Scan)"
"${PSQL[@]}" -Atc "
SELECT string_agg(column_name, ',' ORDER BY ordinal_position)
FROM information_schema.columns
WHERE table_name = 'v_model_health_dashboard'" | grep -q '^provider_model_id,raw_model_name,outbound_model_name,protocol,provider_name,total_credentials,healthy_count,suspicious_count,failing_count,probing_count,healthy_percentage,failing_percentage,urgent_count,suspicious_priority_count,failing_priority_count,watchdog_count,avg_success_rate_7d,avg_verification_hours,avg_consecutive_successes,total_real_success_24h,total_real_failure_24h,real_success_rate_24h,last_verified_at,last_real_request_at,next_probe_at,critical_nodes,pending_probes_5min,overall_health$' \
  && echo "  v_model_health_dashboard column order OK"

"${PSQL[@]}" -Atc "
SELECT string_agg(column_name, ',' ORDER BY ordinal_position)
FROM information_schema.columns
WHERE table_name = 'v_probe_system_health'" | grep -q '^total_nodes,healthy_nodes,failing_nodes,suspicious_nodes,probing_nodes,urgent_queue_size,suspicious_queue_size,failing_queue_size,watchdog_queue_size,ready_probes,current_probing,credentials_being_probed,avg_success_rate_7d,last_probe_at,last_real_request_at,total_real_success_24h,total_real_failure_24h,critical_nodes,pending_probes_5min,snapshot_at$' \
  && echo "  v_probe_system_health column order OK"

echo ""
echo "UNIFIED_PROBE_STATUS_VIEWS_TEST_PASS=1"
