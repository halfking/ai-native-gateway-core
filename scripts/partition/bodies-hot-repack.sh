#!/bin/bash
# ========================================
# 修订历史 / Revision History
# ========================================
# | 版本 | 日期       | 变更                                 | 作者   |
# |------|------------|--------------------------------------|--------|
# | 1.0  | 2026-08-18 | 初始版本（swap/vacuum-full/pg-repack）| Infra |
# ========================================
#
# bodies-hot-repack.sh — request_logs_bodies_hot TOAST 膨胀重排执行器
#
# 三种模式（按推荐顺序）：
#   --mode=swap        【推荐】活行复制 + 原子换名。旧表保留为 *_bloat_backup_<ts>，
#                      不扫描死 TOAST，锁定时长 ≈ 活行复制时长；回滚 = 换名还原
#                      （见 bodies-hot-repack-rollback.sh）。
#   --mode=vacuum-full VACUUM (FULL, ANALYZE)。全表重写含死 TOAST，锁时长 ≈ 全量重写
#                      时长（35GB 级别可能到分钟级），仅在 swap 不可用时使用。
#   --mode=pg-repack   需要 pg_repack 二进制且服务端扩展可用；本仓库环境未预装，
#                      脚本会先探测，缺失即退出。
#
# 安全设计：
#   * 默认仅允许 02:00–04:59（Asia/Shanghai 本机时区）执行破坏性模式，
#    窗口外需显式 --force。
#   * --mode=dry-run（默认）：打印将执行的 SQL，不执行。
#   * swap 前后做行数一致性校验，不一致立即回滚事务。
#   * 旧表不 DROP，由 --drop-backup-after-days 提示人工/后续 cron 清理。
#
# 用法：
#   ./scripts/partition/bodies-hot-repack.sh --env=252 --mode=dry-run
#   ./scripts/partition/bodies-hot-repack.sh --env=252 --mode=swap            # 窗口内
#   ./scripts/partition/bodies-hot-repack.sh --env=252 --mode=swap --force    # 窗口外（需运维确认）
#
# 依赖：psql；--env=252 需 SSH 隧道（localhost:15432）。

set -euo pipefail

ENV_ARG="local"
MODE="dry-run"
FORCE=false
TABLE="request_logs_bodies_hot"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env=*)   ENV_ARG="${1#--env=}" ;;
    --mode=*)  MODE="${1#--mode=}" ;;
    --table=*) TABLE="${1#--table=}" ;;
    --force)   FORCE=true ;;
    -h|--help) sed -n '3,33p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
  shift
done

[[ "$TABLE" =~ ^[a-z_][a-z0-9_]*$ ]] || { echo "invalid table name: $TABLE" >&2; exit 1; }
[[ "$MODE" =~ ^(dry-run|swap|vacuum-full|pg-repack)$ ]] || { echo "invalid mode: $MODE" >&2; exit 1; }

if [[ "$ENV_ARG" == "local" ]]; then
  run_sql()  { docker exec -i llm-gateway-pg psql -X -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 "$@"; }
  run_sql_tx() { docker exec -i llm-gateway-pg psql -X -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 -q; }
elif [[ "$ENV_ARG" == "252" ]]; then
  # shellcheck disable=SC1091
  source "$HOME/workspace/ai-native-tools/envs/loader.sh" --project llm-gateway-go >/dev/null 2>&1 || {
    echo "envs loader 失败（252 凭据）" >&2; exit 1; }
  export PG_PASS_252="${PG_PASS_252:-${COMMON_PG_SUPERUSER_PASS:?COMMON_PG_SUPERUSER_PASS not loaded}}"
  # shellcheck disable=SC1091
  source "$(cd "$(dirname "$0")/../.." && pwd)/configs/env-252.sh"
  # shellcheck disable=SC1091
  source "$(cd "$(dirname "$0")/../.." && pwd)/scripts/lib/252-db-tunnel.sh"
  db252_tunnel_ensure || { echo "managed 252 tunnel unavailable" >&2; exit 1; }
  trap db252_tunnel_teardown EXIT
  run_sql()  { PGOPTIONS='-c statement_timeout=0 -c idle_in_transaction_session_timeout=0' PGPASSWORD="$PG_PASS" "$PG_PSQL_BIN" -X -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 "$@"; }
  run_sql_tx() { PGOPTIONS='-c statement_timeout=0 -c idle_in_transaction_session_timeout=0' PGPASSWORD="$PG_PASS" "$PG_PSQL_BIN" -X -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -q; }
else
  echo "unknown --env: $ENV_ARG (local|252)" >&2; exit 1
fi

TS=$(date +%Y%m%d_%H%M%S)
BACKUP="${TABLE}_bloat_backup_${TS}"
NEW="${TABLE}_repacked_${TS}"
TABLE_REL_OPTIONS=$(run_sql -Atc "SELECT coalesce(array_to_string(reloptions, ', '), '') FROM pg_class WHERE oid = 'public.$TABLE'::regclass;")
REL_OPTIONS_SQL=""
if [[ -n "$TABLE_REL_OPTIONS" ]]; then
  REL_OPTIONS_SQL="ALTER TABLE public.$TABLE SET ($TABLE_REL_OPTIONS);"
fi
INDEX_NAME_NORMALIZE_SQL=$(cat <<SQL
DO \$\$
DECLARE
  source_index record;
  live_index_name text;
  backup_index_name text;
BEGIN
  FOR source_index IN
    SELECT source_idx.relname AS index_name,
           source.indisunique, source.indisprimary, source.indkey,
           source.indclass, source.indcollation, source.indoption,
           source.indpred, source.indexprs
    FROM pg_index source
    JOIN pg_class source_idx ON source_idx.oid = source.indexrelid
    WHERE source.indrelid = 'public.$BACKUP'::regclass
    ORDER BY source_idx.oid
  LOOP
    SELECT live_idx.relname INTO live_index_name
    FROM pg_index live
    JOIN pg_class live_idx ON live_idx.oid = live.indexrelid
    WHERE live.indrelid = 'public.$TABLE'::regclass
      AND live.indisunique = source_index.indisunique
      AND live.indisprimary = source_index.indisprimary
      AND live.indkey = source_index.indkey
      AND live.indclass = source_index.indclass
      AND live.indcollation = source_index.indcollation
      AND live.indoption = source_index.indoption
      AND live.indpred IS NOT DISTINCT FROM source_index.indpred
      AND live.indexprs IS NOT DISTINCT FROM source_index.indexprs
    LIMIT 1;
    IF live_index_name IS NULL THEN
      RAISE EXCEPTION 'cannot match copied index % on public.$TABLE', source_index.index_name;
    END IF;
    backup_index_name := left(source_index.index_name, 35)
      || '_bak_' || left(md5(source_index.index_name), 4) || '_$TS';
    EXECUTE format('ALTER INDEX public.%I RENAME TO %I', source_index.index_name, backup_index_name);
    EXECUTE format('ALTER INDEX public.%I RENAME TO %I', live_index_name, source_index.index_name);
  END LOOP;
END \$\$;
SQL
)
VIEW_REFRESH_SQL=""

# ALTER TABLE ... RENAME preserves dependent views' old relation OIDs. For the
# known bodies view, recreate its rule after the new table takes the live name.
# Any unrecognized dependent view blocks the swap rather than silently serving
# the retained backup table.
if [[ "$TABLE" == "request_logs_bodies_hot" ]]; then
  DEPENDENT_VIEWS=$(run_sql -Atc "
    SELECT DISTINCT n.nspname || '.' || c.relname
    FROM pg_depend d
    JOIN pg_rewrite r ON r.oid = d.objid
    JOIN pg_class c ON c.oid = r.ev_class
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE d.refobjid = 'public.$TABLE'::regclass
      AND c.relkind IN ('v', 'm')
    ORDER BY 1;" || true)
  if [[ -n "$DEPENDENT_VIEWS" && "$DEPENDENT_VIEWS" != "public.request_logs_bodies_with_current_month" ]]; then
    echo "✗ 检测到未配置的依赖 VIEW/MATVIEW，拒绝 swap 以避免 OID 漂移：$DEPENDENT_VIEWS" >&2
    exit 1
  fi
  if [[ "$DEPENDENT_VIEWS" == "public.request_logs_bodies_with_current_month" ]]; then
    VIEW_REFRESH_SQL=$(cat <<'SQL'
CREATE OR REPLACE VIEW public.request_logs_bodies_with_current_month AS
SELECT request_logs_bodies_hot.request_id,
       request_logs_bodies_hot.ts,
       request_logs_bodies_hot.request_body,
       request_logs_bodies_hot.outbound_body,
       request_logs_bodies_hot.response_body
FROM public.request_logs_bodies_hot
UNION ALL
SELECT request_logs_bodies.request_id,
       request_logs_bodies.ts,
       request_logs_bodies.request_body,
       request_logs_bodies.outbound_body,
       request_logs_bodies.response_body
FROM public.request_logs_bodies;
SQL
)
  fi
fi

# ── 维护窗口守卫（破坏性模式） ─────────────────────────────────────────────
if [[ "$MODE" != "dry-run" && "$FORCE" != true ]]; then
  HOUR=$(date +%H)
  if (( 10#$HOUR < 2 || 10#$HOUR >= 5 )); then
    echo "✗ 当前 $(date '+%F %T') 不在 02:00–04:59 维护窗口内。" >&2
    echo "  窗口外执行需运维显式确认后加 --force。" >&2
    exit 1
  fi
fi

# ── 前置检查 ───────────────────────────────────────────────────────────────
echo "== 前置检查 env=$ENV_ARG table=$TABLE mode=$MODE =="
run_sql -c "SELECT pg_size_pretty(pg_total_relation_size('public.$TABLE')) AS before_size,
                   (SELECT count(*) FROM public.$TABLE) AS before_rows;"

case "$MODE" in
  dry-run)
    echo
    echo "== DRY-RUN：以下为 swap 模式将执行的事务（未执行） =="
    cat <<SQL
BEGIN;
SET LOCAL statement_timeout = '0';
SET LOCAL idle_in_transaction_session_timeout = '0';
LOCK TABLE public.$TABLE IN ACCESS EXCLUSIVE MODE;
CREATE TABLE public.$NEW (LIKE public.$TABLE INCLUDING ALL);
INSERT INTO public.$NEW SELECT * FROM public.$TABLE;
-- 行数一致性校验（不等则抛错回滚）
DO \$\$ DECLARE a bigint; b bigint;
BEGIN
  SELECT count(*) INTO a FROM public.$TABLE;
  SELECT count(*) INTO b FROM public.$NEW;
  IF a <> b THEN RAISE EXCEPTION 'row count mismatch % <> %', a, b; END IF;
END \$\$;
ALTER TABLE public.$TABLE RENAME TO $BACKUP;
ALTER TABLE public.$NEW RENAME TO $TABLE;
$REL_OPTIONS_SQL
$INDEX_NAME_NORMALIZE_SQL
$VIEW_REFRESH_SQL
COMMIT;
ANALYZE public.$TABLE;
SQL
    echo
    echo "回滚（如需）：./scripts/partition/bodies-hot-repack-rollback.sh --env=$ENV_ARG --backup=$BACKUP"
    ;;

  swap)
    echo
    echo "== 执行 swap 重写（旧表保留为 ${BACKUP}） =="
    run_sql_tx <<SQL
BEGIN;
SET LOCAL statement_timeout = '0';
SET LOCAL idle_in_transaction_session_timeout = '0';
LOCK TABLE public.$TABLE IN ACCESS EXCLUSIVE MODE;
CREATE TABLE public.$NEW (LIKE public.$TABLE INCLUDING ALL);
INSERT INTO public.$NEW SELECT * FROM public.$TABLE;
DO \$\$ DECLARE a bigint; b bigint;
BEGIN
  SELECT count(*) INTO a FROM public.$TABLE;
  SELECT count(*) INTO b FROM public.$NEW;
  IF a <> b THEN RAISE EXCEPTION 'row count mismatch % <> %', a, b; END IF;
END \$\$;
ALTER TABLE public.$TABLE RENAME TO $BACKUP;
ALTER TABLE public.$NEW RENAME TO $TABLE;
$REL_OPTIONS_SQL
$INDEX_NAME_NORMALIZE_SQL
$VIEW_REFRESH_SQL
COMMIT;
ANALYZE public.$TABLE;
SQL
    echo "== 完成。后置校验："
    run_sql -c "SELECT pg_size_pretty(pg_total_relation_size('public.$TABLE')) AS new_size,
                       (SELECT count(*) FROM public.$TABLE) AS new_rows;"
    echo "备份表：public.${BACKUP}（确认稳定后可 DROP 释放 ~35GB；回滚用 rollback 脚本）"
    ;;

  vacuum-full)
    echo
    echo "== 执行 VACUUM (FULL, ANALYZE)（AccessExclusiveLock 至结束） =="
    run_sql -c "VACUUM (FULL, ANALYZE, VERBOSE) public.$TABLE;"
    run_sql -c "SELECT pg_size_pretty(pg_total_relation_size('public.$TABLE')) AS new_size,
                       (SELECT count(*) FROM public.$TABLE) AS new_rows;"
    ;;

  pg-repack)
    if ! command -v pg_repack >/dev/null 2>&1; then
      echo "✗ pg_repack 未安装（本机与容器均未探测到）。请改用 --mode=swap，或先安装匹配 PG17 的 pg_repack。" >&2
      exit 1
    fi
    echo "== 执行 pg_repack（在线） =="
    if [[ "$ENV_ARG" == "local" ]]; then
      pg_repack -h 127.0.0.1 -p 5432 -U llm_gateway -d llm_gateway -t public.$TABLE
    else
      PGPASSWORD="$PG_PASS" pg_repack -h "$PG_HOST" -p "$PG_PORT" \
        -U "$PG_USER" -d "$PG_DB" -t "public.$TABLE"
    fi
    ;;
esac
