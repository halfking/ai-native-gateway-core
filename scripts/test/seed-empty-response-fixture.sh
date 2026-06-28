#!/usr/bin/env bash
# seed-empty-response-fixture.sh
#
# 灌入端到端测试数据，模拟 commit 2b2fc911 修复前的污染：
#   - credential A 提供 claude-opus-4-8，被误标 unavailable (continuous_failure)
#   - model_probe_state 中 opus-4-8 被误推 unavailable (empty_response)
#   - request_logs 里有 5 条 zero_tokens_few_chunks 误判
#
# 同时给一个对照组：
#   - credential B 提供 claude-sonnet-4-6，无误判
#   - request_logs 里 5 条正常成功
#
# 灌入后用 SELECT 验证；recover-empty-response-misclassification.sh 应只动
# credential A 和 opus-4-8 的 probe 状态，不动 credential B / sonnet-4-6。

set -euo pipefail

PG_CONTAINER="${PG_CONTAINER:-r112_postgres}"
PG_USER="${PG_USER:-kxuser}"
PG_PASS="${PG_PASS:-kxpass}"
TARGET_DB="${TARGET_DB:-llm_gateway}"
ADMIN_DB="postgres"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

run_sql() {
  PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
    "$PG_CONTAINER" psql -U "$PG_USER" -d "$TARGET_DB" -v ON_ERROR_STOP=1 -tAc "$1"
}

run_sql_file() {
  PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
    -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$TARGET_DB" -v ON_ERROR_STOP=1 -f -
}

# 抹掉上一次测试残留
run_sql "TRUNCATE request_logs, credential_model_bindings, model_probe_state RESTART IDENTITY CASCADE;" >/dev/null
run_sql "DELETE FROM credentials;" >/dev/null
run_sql "DELETE FROM model_offers WHERE raw_model_name IN ('claude-opus-4-8', 'claude-sonnet-4-6');" >/dev/null
run_sql "DELETE FROM provider_models WHERE raw_model_name IN ('claude-opus-4-8', 'claude-sonnet-4-6');" >/dev/null
run_sql "DELETE FROM providers WHERE code IN ('anthropic-test', 'anthropic-test-2');" >/dev/null

cat <<'SQL' | run_sql_file

-- 1) providers / provider_models (基础数据)
INSERT INTO providers (code, display_name, kind, protocol, base_url, enabled) VALUES
    ('anthropic-test',   'Anthropic (test A)', 'cloud', 'anthropic', 'https://api.example.test', TRUE),
    ('anthropic-test-2', 'Anthropic (test B)', 'cloud', 'anthropic', 'https://api.example.test', TRUE);

INSERT INTO provider_models (provider_id, raw_model_name, tenant_id, available)
SELECT id, 'claude-opus-4-8',   'default', TRUE FROM providers WHERE code = 'anthropic-test'
UNION ALL
SELECT id, 'claude-sonnet-4-6', 'default', TRUE FROM providers WHERE code = 'anthropic-test-2';

-- 2) credentials
INSERT INTO credentials (provider_id, label, secret_ciphertext, status, created_at, updated_at)
SELECT id, 'cred-A-opus',   E'\\x00'::bytea, 'active', NOW(), NOW() FROM providers WHERE code='anthropic-test'
UNION ALL
SELECT id, 'cred-B-sonnet', E'\\x00'::bytea, 'active', NOW(), NOW() FROM providers WHERE code='anthropic-test-2';

-- 3) credential_model_bindings：credential A 被误标 unavailable，credential B 正常
INSERT INTO credential_model_bindings
    (credential_id, provider_model_id, weight, manual_priority, success_rate, available,
     unavailable_reason, unavailable_at, unavailable_recover_at, consecutive_failures,
     created_at, updated_at)
SELECT c.id, pm.id, 100, 50, 0.95, FALSE,
       'continuous_failure', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 hour', 5,
       NOW() - INTERVAL '2 days', NOW() - INTERVAL '1 hour'
FROM credentials c JOIN provider_models pm
  ON pm.provider_id = c.provider_id
 WHERE c.label = 'cred-A-opus';

INSERT INTO credential_model_bindings
    (credential_id, provider_model_id, weight, manual_priority, success_rate, available,
     consecutive_failures, created_at, updated_at)
SELECT c.id, pm.id, 100, 50, 0.98, TRUE, 0,
       NOW() - INTERVAL '2 days', NOW()
FROM credentials c JOIN provider_models pm
  ON pm.provider_id = c.provider_id
 WHERE c.label = 'cred-B-sonnet';

-- 4) model_probe_state：opus-4-8 被推 unavailable，sonnet 正常
INSERT INTO model_probe_state
    (credential_id, raw_model_name, state, consecutive_failures, total_attempts,
     last_attempt_at, last_state_change_at, last_status, last_unavailable_reason, last_err_code,
     next_retry_at, marked_suspicious_at, success_rate_7d, real_request_failure_count)
SELECT c.id, 'claude-opus-4-8', 'unavailable', 5, 8,
       NOW() - INTERVAL '30 minutes', NOW() - INTERVAL '30 minutes', 'empty_response',
       'zero_tokens_few_chunks', 'zero_tokens_few_chunks',
       NOW() + INTERVAL '2 hours', NOW() - INTERVAL '30 minutes', 12.50, 5
FROM credentials c WHERE c.label = 'cred-A-opus';

INSERT INTO model_probe_state
    (credential_id, raw_model_name, state, consecutive_successes, total_attempts,
     last_attempt_at, last_state_change_at, last_status,
     next_retry_at, success_rate_7d, real_request_success_count)
SELECT c.id, 'claude-sonnet-4-6', 'available', 12, 12,
       NOW() - INTERVAL '1 minute', NOW() - INTERVAL '1 minute', 'ok',
       NOW(), 100.00, 12
FROM credentials c WHERE c.label = 'cred-B-sonnet';

-- 5) request_logs：5 条 opus-4-8 empty_response 误判 + 5 条 sonnet 正常成功
-- 用 generate_series 简化 ts
INSERT INTO request_logs
    (request_id, ts, tenant_id, client_model, outbound_model, credential_id, provider_id,
     prompt_tokens, completion_tokens, success, error_kind, failure_stage, failure_detail_code,
     request_status, upstream_finish_reason, usage_source, request_mode, stream_chunk_count,
     stream_done_received, response_preview, response_checksum, transform_rule_id,
     quality_flags, quality_fix_actions)
SELECT
    md5('opus-empty-' || g)::text,
    NOW() - (g || ' minutes')::interval,
    'test-tenant',
    'claude-opus-4-8',
    'claude-opus-4-8',
    c.id,
    (SELECT id FROM providers WHERE code='anthropic-test'),
    100, 0, FALSE,
    'empty_response', 'upstream_empty_response', 'zero_tokens_few_chunks',
    'failure', NULL, 'estimated', 'agent', 2, TRUE, '', '',     'opus-fix',
    '{}'::text[], '{}'::jsonb
FROM credentials c, generate_series(1, 5) g
WHERE c.label = 'cred-A-opus';

INSERT INTO request_logs
    (request_id, ts, tenant_id, client_model, outbound_model, credential_id, provider_id,
     prompt_tokens, completion_tokens, success, request_status, upstream_finish_reason,
     usage_source, request_mode, stream_chunk_count, stream_done_received, response_preview,
     transform_rule_id, quality_flags, quality_fix_actions)
SELECT
    md5('sonnet-ok-' || g)::text,
    NOW() - (g || ' minutes')::interval,
    'test-tenant',
    'claude-sonnet-4-6',
    'claude-sonnet-4-6',
    c.id,
    (SELECT id FROM providers WHERE code='anthropic-test-2'),
    120, 80, TRUE,
    'success', 'stop', 'llm', 'agent', 8, TRUE, 'Hello world',
    'sonnet-control', '{}'::text[], '{}'::jsonb
FROM credentials c, generate_series(1, 5) g
WHERE c.label = 'cred-B-sonnet';

SQL

echo "─── fixture loaded ───"
run_sql "SELECT 'cmb' AS t, count(*) FROM credential_model_bindings
         UNION ALL SELECT 'probe', count(*) FROM model_probe_state
         UNION ALL SELECT 'request_logs', count(*) FROM request_logs
         UNION ALL SELECT 'credentials', count(*) FROM credentials;"
echo
echo "─── cmb (before recovery) ───"
run_sql "SELECT id, credential_id, available, unavailable_reason, unavailable_recover_at,
                consecutive_failures
         FROM credential_model_bindings ORDER BY id;"
echo
echo "─── probe (before recovery) ───"
run_sql "SELECT credential_id, raw_model_name, state, last_unavailable_reason, last_err_code,
                next_retry_at
         FROM model_probe_state ORDER BY credential_id, raw_model_name;"
echo
echo "─── request_logs (BEFORE — must remain unchanged after recovery) ───"
run_sql "SELECT request_id, client_model, success, error_kind, failure_detail_code,
                upstream_finish_reason
         FROM request_logs ORDER BY ts DESC, request_id LIMIT 12;"
