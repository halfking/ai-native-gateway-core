#!/usr/bin/env bash
# verify-637-638-639.sh — 在本地 252 同步副本(llm-gateway-pg)上验证迁移 637/638/639 的 up/down
#
# 前提：副本 schema 已被上一会话应用过 637/638/639 但 schema_migrations 只记到 636
# （checksum ledger 无 637-639）。因此本脚本执行完整循环：
#   down(639→638→637) → 断言前置态(625/626 形态) → up(637→638→639) → 行为验证
#   → ledger 记账(模拟 deploy_apply_pending_migrations) → 二次 up 幂等 → down → up 恢复
#
# 行为数据（hot 行、当日 promote、retention=0、credential 聚合）均在事务内
# ROLLBACK，不污染副本。结构性变更（视图/函数/索引）是迁移本身的内容，验证
# 结束后停在 up 完成态。
set -uo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"

PW="$(~/workspace/ai-native-tools/envs/loader.sh query COMMON_PG_SUPERUSER_PASS 2>/dev/null | tail -1)"
export PGPASSWORD="$PW"
PSQL=(psql -X -h 127.0.0.1 -p 5432 -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 -q)

PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "  ✓ $1"; }
bad() { FAIL=$((FAIL+1)); echo "  ✗ $1"; }
chk() { # chk <desc> <expected> <actual>
  if [[ "$2" == "$3" ]]; then ok "$1 (=$3)"; else bad "$1: expected=$2 got=$3"; fi
}

q() { psql -X -h 127.0.0.1 -p 5432 -U llm_gateway -d llm_gateway -tAq "$@" 2>&1; }

M=sql/migrations/startup
echo "== 阶段0：前置快照 =="
q -c "SELECT version FROM schema_migrations WHERE version ~ '^[0-9]+$' AND version::int >= 636 ORDER BY version::int" | tr '\n' ' '
echo "(schema_migrations 数字版本)"
LEDGER637=$(q -c "SELECT count(*) FROM llm_gateway_migration_checksums WHERE version='637'")
echo "  checksum ledger 637 rows: $LEDGER637 (rerun scenario)"

echo
echo "== 阶段1：down 639→638→637（回到 625/626 形态）=="
"${PSQL[@]}" -f "$M/639_provider_error_details_credential.down.sql" && ok "639 down applied" || bad "639 down failed"
"${PSQL[@]}" -f "$M/638_session_bodies_promote_guard.down.sql" && ok "638 down applied" || bad "638 down failed"
"${PSQL[@]}" -f "$M/637_session_bodies_unified_today_visible.down.sql" && ok "637 down applied" || bad "637 down failed"

echo "-- 断言前置态（625 视图带过滤 / 626 函数无 guard / 无 credential_id）--"
PRE_FILTER=$(q -c "SELECT count(*) FROM pg_get_viewdef('public.session_bodies_unified'::regclass, true) WHERE pg_get_viewdef ~ 'CURRENT_DATE'")
chk "625 视图恢复 partition_date 过滤" "1" "$PRE_FILTER"
PRE_GUARD=$(q -c "SELECT count(*) FROM pg_get_functiondef('public.promote_session_bodies_hot_to_partition(interval,integer)'::regprocedure) WHERE pg_get_functiondef LIKE '%retention_window must be positive%'")
chk "626 函数恢复（无 guard）" "0" "$PRE_GUARD"
PRE_COL=$(q -c "SELECT count(*) FROM information_schema.columns WHERE table_name='provider_error_details' AND column_name='credential_id'")
chk "credential_id 列已删除" "0" "$PRE_COL"
PRE_IDX=$(q -c "SELECT count(*) FROM pg_indexes WHERE indexname='idx_provider_error_details_tenant_fingerprint'")
chk "620 旧唯一索引恢复" "1" "$PRE_IDX"

echo
echo "== 阶段2：up 637→638→639 =="
"${PSQL[@]}" -f "$M/637_session_bodies_unified_today_visible.sql" && ok "637 up applied" || bad "637 up failed"
"${PSQL[@]}" -f "$M/638_session_bodies_promote_guard.sql" && ok "638 up applied" || bad "638 up failed"
"${PSQL[@]}" -f "$M/639_provider_error_details_credential.sql" && ok "639 up applied" || bad "639 up failed"

echo
echo "== 阶段3：行为验证 =="
echo "-- 3a. 637：当日写入+当日 promote 行在 unified 视图可见 --"
V3A=$(q <<'SQL'
BEGIN;
INSERT INTO session_bodies_hot (id, session_id, turn_no, tenant_id, request_id,
  request_delta, response_delta, outbound_body, request_attachments, response_attachments,
  ts, partition_date)
VALUES (990000000001, 'sess-verify-637', 1, 'default', 'req-verify-637',
  '{"v":1}', '{"v":1}', NULL, NULL, NULL,
  now() - interval '9 hours', CURRENT_DATE);
-- promote 只搬 ts < cutoff 的行；9h 前 > 8h retention，会被搬走
SELECT public.promote_session_bodies_hot_to_partition(interval '8 hours', 5000);
-- 视图必须能看到这条当日 partition_date 行（625 过滤版看不见）
SELECT count(*) FROM session_bodies_unified WHERE id = 990000000001;
ROLLBACK;
SQL
)
V3A=$(printf "%s" "$V3A" | grep -E "^[0-9]+\|?[0-9]*$" | tail -1)
chk "637 当日 promote 行经 unified 视图可见" "1" "$V3A"

echo "-- 3b. 637：promote 后 hot 行确实删掉（move 语义，无双份）--"
V3B=$(q <<'SQL'
BEGIN;
INSERT INTO session_bodies_hot (id, session_id, turn_no, tenant_id, request_id,
  request_delta, response_delta, outbound_body, request_attachments, response_attachments,
  ts, partition_date)
VALUES (990000000002, 'sess-verify-637', 2, 'default', 'req-verify-637',
  '{"v":1}', '{"v":1}', NULL, NULL, NULL,
  now() - interval '9 hours', CURRENT_DATE);
SELECT public.promote_session_bodies_hot_to_partition(interval '8 hours', 5000);
SELECT (SELECT count(*) FROM session_bodies_unified WHERE id=990000000002)
    || '|' || (SELECT count(*) FROM session_bodies_hot WHERE id=990000000002);
ROLLBACK;
SQL
)
V3B=$(printf "%s" "$V3B" | grep -E "^[0-9]+\|[0-9]+$" | tail -1)
chk "637 move 语义：视图恰好1行/hot 0行" "1|0" "$V3B"

echo "-- 3c. 638：retention_window=0 必须报错 --"
V3C=$(q <<'SQL'
BEGIN;
SELECT public.promote_session_bodies_hot_to_partition(interval '0 seconds', 100);
SELECT 'NO_ERROR';
ROLLBACK;
SQL
)
if [[ "$V3C" == *"retention_window must be positive"* ]]; then ok "638 retention=0 报错"; else bad "638 retention=0 未报错: $V3C"; fi

echo "-- 3d. 638：retention_window NULL 必须报错 --"
V3D=$(q <<'SQL'
BEGIN;
SELECT public.promote_session_bodies_hot_to_partition(NULL::interval, 100);
SELECT 'NO_ERROR';
ROLLBACK;
SQL
)
if [[ "$V3D" == *"retention_window must be positive"* ]]; then ok "638 retention NULL 报错"; else bad "638 retention NULL 未报错: $V3D"; fi

echo "-- 3e. 638：batch_size=0 必须报错 --"
V3E=$(q <<'SQL'
BEGIN;
SELECT public.promote_session_bodies_hot_to_partition(interval '8 hours', 0);
SELECT 'NO_ERROR';
ROLLBACK;
SQL
)
if [[ "$V3E" == *"batch_size must be >= 1"* ]]; then ok "638 batch_size=0 报错"; else bad "638 batch_size=0 未报错: $V3E"; fi

echo "-- 3f. 638：合法调用仍正常工作（guard 不破坏正常路径）--"
V3F=$(q <<'SQL'
BEGIN;
INSERT INTO session_bodies_hot (id, session_id, turn_no, tenant_id, request_id,
  request_delta, response_delta, outbound_body, request_attachments, response_attachments,
  ts, partition_date)
VALUES (990000000003, 'sess-verify-638', 1, 'default', 'req-verify-638',
  '{"v":1}', '{"v":1}', NULL, NULL, NULL,
  now() - interval '9 hours', CURRENT_DATE);
SELECT public.promote_session_bodies_hot_to_partition(interval '8 hours', 5000);
ROLLBACK;
SQL
)
if [[ "$V3F" == *"1"* ]]; then ok "638 合法调用 moved=1"; else bad "638 合法调用异常: $V3F"; fi

echo "-- 3g. 639：聚合器 conflict 目标索引存在且含 credential_id --"
V3G=$(q -c "SELECT count(*) FROM pg_indexes WHERE indexname='idx_provider_error_details_tenant_cred_fingerprint' AND indexdef LIKE '%COALESCE(credential_id%'")
chk "639 唯一索引含 credential_id 表达式" "1" "$V3G"
V3G2=$(q -c "SELECT count(*) FROM pg_indexes WHERE indexname='idx_ped_tenant_credential'")
chk "639 凭据详情查询索引存在" "1" "$V3G2"

echo "-- 3h. 639：历史行 NULL credential_id 保持原 620 冲突语义 --"
V3H=$(q <<'SQL'
BEGIN;
-- 同一 620 身份插入两行（credential_id 均 NULL）→ 必须触发唯一冲突
INSERT INTO provider_error_details (provider_id, model_name, endpoint, error_type,
  error_code, error_message, request_id, tenant_id, aggregation_bucket, context,
  occurrences, first_seen_at, last_seen_at, resolved, credential_id)
VALUES (990001, 'm', 'unknown', 'kind_x', '', 'verify-msg', NULL, NULL,
  TIMESTAMPTZ '2026-09-01 10:00+08', NULL, 1, now(), now(), FALSE, NULL);
INSERT INTO provider_error_details (provider_id, model_name, endpoint, error_type,
  error_code, error_message, request_id, tenant_id, aggregation_bucket, context,
  occurrences, first_seen_at, last_seen_at, resolved, credential_id)
VALUES (990001, 'm', 'unknown', 'kind_x', '', 'verify-msg', NULL, NULL,
  TIMESTAMPTZ '2026-09-01 10:00+08', NULL, 1, now(), now(), FALSE, NULL);
SELECT 'INSERTED_DUPLICATE';
ROLLBACK;
SQL
)
if [[ "$V3H" != *"INSERTED_DUPLICATE"* ]]; then ok "639 NULL credential_id 行间互相冲突（语义保持）"; else bad "639 NULL 行未冲突"; fi

echo "-- 3i. 639：不同 credential_id 同指纹不冲突（新粒度生效）--"
V3I=$(q <<'SQL'
BEGIN;
INSERT INTO provider_error_details (provider_id, model_name, endpoint, error_type,
  error_code, error_message, request_id, tenant_id, aggregation_bucket, context,
  occurrences, first_seen_at, last_seen_at, resolved, credential_id)
VALUES (990001, 'm', 'unknown', 'kind_x', '', 'verify-msg', NULL, NULL,
  TIMESTAMPTZ '2026-09-01 10:00+08', NULL, 1, now(), now(), FALSE, '101'),
       (990001, 'm', 'unknown', 'kind_x', '', 'verify-msg', NULL, NULL,
  TIMESTAMPTZ '2026-09-01 10:00+08', NULL, 1, now(), now(), FALSE, '202');
SELECT 'BOTH_INSERTED';
ROLLBACK;
SQL
)
if [[ "$V3I" == *"BOTH_INSERTED"* ]]; then ok "639 不同凭据同指纹可并存"; else bad "639 不同凭据插入失败: $V3I"; fi

echo
echo "== 阶段4：模拟 deploy_apply_pending_migrations 记账 637/638/639 =="
for entry in "637 session_bodies_unified_today_visible" "638 session_bodies_promote_guard" "639 provider_error_details_credential"; do
  set -- $entry; ver=$1; name=$2
  f="$M/${ver}_${name}.sql"
  sum=$(shasum -a 256 "$f" | awk '{print $1}')
  q -c "BEGIN; SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:schema_migrations', 0));
    INSERT INTO schema_migrations (version, description) SELECT '$ver', '$name' WHERE NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version='$ver');
    INSERT INTO llm_gateway_migration_checksums (version, migration_name, checksum) VALUES ('$ver','${ver}_${name}.sql','$sum')
    ON CONFLICT (version) DO UPDATE SET migration_name=EXCLUDED.migration_name, checksum=EXCLUDED.checksum; COMMIT;" && ok "ledger 记账 $ver" || bad "ledger 记账 $ver 失败"
done
chk "schema_migrations 含 637-639" "3" "$(q -c "SELECT count(*) FROM schema_migrations WHERE version IN ('637','638','639')")"

echo
echo "== 阶段5：二次 up 幂等性（deploy 重放场景）=="
"${PSQL[@]}" -f "$M/637_session_bodies_unified_today_visible.sql" 2>&1 && ok "637 重放幂等" || bad "637 重放失败"
"${PSQL[@]}" -f "$M/638_session_bodies_promote_guard.sql" 2>&1 && ok "638 重放幂等" || bad "638 重放失败"
"${PSQL[@]}" -f "$M/639_provider_error_details_credential.sql" 2>&1 && ok "639 重放幂等" || bad "639 重放失败"

echo
echo "== 阶段6：down 后立即 up（回滚-重应用循环，最终态=up）=="
"${PSQL[@]}" -f "$M/639_provider_error_details_credential.down.sql" && ok "639 down#2" || bad "639 down#2 failed"
"${PSQL[@]}" -f "$M/639_provider_error_details_credential.sql" && ok "639 up#2" || bad "639 up#2 failed"
chk "639 后 credential_id 列存在" "1" "$(q -c "SELECT count(*) FROM information_schema.columns WHERE table_name='provider_error_details' AND column_name='credential_id'")"
# 注意：down#2 后 idx_provider_error_details_tenant_fingerprint 重建；up#2 只 IF NOT EXISTS 方式处理自身索引，需检查是否残留旧索引
RESIDUAL=$(q -c "SELECT count(*) FROM pg_indexes WHERE indexname='idx_provider_error_details_tenant_fingerprint'")
if [[ "$RESIDUAL" == "0" ]]; then ok "down→up 无旧索引残留"; else bad "旧索引 idx_provider_error_details_tenant_fingerprint 残留 ($RESIDUAL)"; fi

echo
echo "======================================"
echo "结果: PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]] && echo "✅ 迁移 637/638/639 验证全部通过" || echo "❌ 存在失败项"
exit $FAIL
