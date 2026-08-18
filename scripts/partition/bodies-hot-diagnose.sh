#!/bin/bash
# ========================================
# 修订历史 / Revision History
# ========================================
# | 版本 | 日期       | 变更                            | 作者   |
# |------|------------|---------------------------------|--------|
# | 1.0  | 2026-08-18 | 初始版本（P1 TOAST bloat 诊断）| Infra  |
# ========================================
#
# bodies-hot-diagnose.sh — request_logs_bodies_hot TOAST/bloat 只读诊断
#
# 背景：2026-08-18 审计发现 252 上该表 ~35GB（几乎全在 TOAST），
# live rows 仅 ~1.8k，但累计 ins 22.8w / upd 18.8w / del 31.3w，
# 典型的 upsert 高频更新 + TOAST 膨胀。
#
# 用法：
#   ./scripts/partition/bodies-hot-diagnose.sh --env=local
#   ./scripts/partition/bodies-hot-diagnose.sh --env=252
#   ./scripts/partition/bodies-hot-diagnose.sh --env=252 --table=request_logs_bodies_hot
#
# 全部查询只读（仅 [4] 尝试 CREATE EXTENSION IF NOT EXISTS pgstattuple，幂等），
# 可在任意时间执行。依赖：psql；--env=252 需先建立 SSH 隧道（localhost:15432）。

set -euo pipefail

ENV_ARG="local"
TABLE="request_logs_bodies_hot"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env=*) ENV_ARG="${1#--env=}" ;;
    --table=*) TABLE="${1#--table=}" ;;
    -h|--help) sed -n '3,21p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
  shift
done

# 表名白名单校验后嵌入（防注入；本脚本只面向本仓库已知 hot 表）
[[ "$TABLE" =~ ^[a-z_][a-z0-9_]*$ ]] || { echo "invalid table name: $TABLE" >&2; exit 1; }

if [[ "$ENV_ARG" == "local" ]]; then
  run_sql() { docker exec -i llm-gateway-pg psql -X -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 "$@"; }
elif [[ "$ENV_ARG" == "252" ]]; then
  # shellcheck disable=SC1091
  source "$HOME/workspace/ai-native-tools/envs/loader.sh" --all --project llm-gateway-go --server 115.29.212.252 --mode plain >/dev/null 2>&1 || {
    echo "envs loader 失败（252 凭据）" >&2; exit 1; }
  run_sql() { PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" psql -X -h 127.0.0.1 -p 15432 -U "$COMMON_PG_SUPERUSER" -d llm_gateway -v ON_ERROR_STOP=1 "$@"; }
else
  echo "unknown --env: $ENV_ARG (local|252)" >&2; exit 1
fi

echo "=== [1] 对象尺寸（total / heap / indexes / TOAST） env=$ENV_ARG table=$TABLE ==="
run_sql -c "
SELECT c.relname,
       pg_size_pretty(pg_total_relation_size(c.oid))          AS total,
       pg_size_pretty(pg_relation_size(c.oid))                 AS heap,
       pg_size_pretty(pg_indexes_size(c.oid))                  AS indexes,
       pg_size_pretty(pg_total_relation_size(c.reltoastrelid)) AS toast,
       pg_total_relation_size(c.reltoastrelid)                 AS toast_bytes
FROM pg_class c WHERE c.relname = '$TABLE';"

echo; echo "=== [2] pg_stat 统计（live/dead/ins/upd/del + 最近 vacuum） ==="
run_sql -c "
SELECT relname, n_live_tup, n_dead_tup, n_tup_ins, n_tup_upd, n_tup_del,
       n_mod_since_analyze, last_vacuum, last_autovacuum, last_analyze
FROM pg_stat_user_tables WHERE relname = '$TABLE';"

echo; echo "=== [3] 真实行数（权威值，非估计）+ 重复 request_id 检查 ==="
run_sql -c "SELECT count(*) AS actual_rows FROM public.$TABLE;"
run_sql -c "
SELECT request_id, count(*) AS dup
FROM public.$TABLE GROUP BY request_id HAVING count(*) > 1
ORDER BY dup DESC LIMIT 10;"

echo; echo "=== [4] 死区粗估（pgstattuple_approx；扩展缺失则跳过） ==="
run_sql -c "CREATE EXTENSION IF NOT EXISTS pgstattuple;" >/dev/null 2>&1 || true
if run_sql -Atc "SELECT 1 FROM pg_extension WHERE extname='pgstattuple';" | grep -q 1; then
  run_sql -c "SELECT * FROM pgstattuple_approx('public.$TABLE'::regclass);"
else
  echo "pgstattuple 不可用，跳过（不影响其余诊断）"
fi

echo; echo "=== [5] 表级 autovacuum 调优 + 全局关键参数 ==="
run_sql -c "SELECT relname, reloptions FROM pg_class WHERE relname = '$TABLE';"
run_sql -c "
SELECT name, setting, unit FROM pg_settings
WHERE name IN ('autovacuum_vacuum_scale_factor','autovacuum_vacuum_threshold',
               'autovacuum_vacuum_insert_scale_factor','autovacuum_vacuum_cost_limit',
               'vacuum_cost_limit','autovacuum_max_workers');"

echo; echo "=== [6] 判定参考 ==="
run_sql -At -F$'\t' -c "
WITH s AS (SELECT n_live_tup, n_dead_tup FROM pg_stat_user_tables WHERE relname='$TABLE'),
     t AS (SELECT pg_total_relation_size(c.reltoastrelid) AS toast_bytes
           FROM pg_class c WHERE c.relname='$TABLE')
SELECT CASE
  WHEN t.toast_bytes > 10::bigint*1024*1024*1024 AND s.n_live_tup < 10000
       THEN 'P1: 巨量 TOAST + 极少 live rows → 建议 swap 重写（见 runbook）'
  WHEN s.n_dead_tup > greatest(s.n_live_tup,1)*2
       THEN 'P2: 死元组比例高 → 先普通 VACUUM，必要时 swap'
  ELSE 'OK: 未见明显膨胀'
END AS verdict FROM s, t;"
