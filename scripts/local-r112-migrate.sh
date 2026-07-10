#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# R1.12 本地 PG migrations
#
# 流程:
#   1. 等待 r112_postgres 启动
#   2. 重建 llm_gateway 库（本地测试需要干净基线）
#   3. 加载 sql/schema/00-prereqs.sql + 01-schema.sql + 02-seed.sql
#   4. 按文件名顺序应用 sql/migrations/startup/*.sql (增量迁移)
#   4. 每个 migration 单独 try/catch, 失败时精确定位
#
# 用法:
#   ./scripts/local-r112-migrate.sh
#   ./scripts/local-r112-migrate.sh --reset   # DROP + 重建库 (慎用)
#
# 验证:
#   bash -n scripts/local-r112-migrate.sh
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MIGRATIONS_DIR="$ROOT_DIR/sql/migrations/startup"
BASE_SCHEMA_SQL="$ROOT_DIR/sql/schema/01-schema.sql"

PG_CONTAINER="r112_postgres"
PG_USER="kxuser"
PG_PASS="kxpass"
TARGET_DB="llm_gateway"
ADMIN_DB="postgres"   # CREATE DATABASE 必须在 postgres 库下执行

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }

# ── 解析参数 ──
RESET=0
case "${1:-}" in
  --reset) RESET=1 ;;
  "")      RESET=0 ;;
  *)       err "未知参数: $1"; echo "用法: $0 [--reset]"; exit 1 ;;
esac

# ── 前置检查 ──
[ -d "$MIGRATIONS_DIR" ] || { err "migrations 目录不存在: $MIGRATIONS_DIR"; exit 1; }

if ! docker ps --format '{{.Names}}' | grep -q "^${PG_CONTAINER}\$"; then
  err "容器 $PG_CONTAINER 未运行"
  err "  修复: ./scripts/local-up.sh  (会先启动 postgres)"
  exit 1
fi

# ── 工具函数 ──
pg_exec() {
  # 用 admin 库 (postgres) 执行 SQL, 不指定 -d
  PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
    "$PG_CONTAINER" psql -U "$PG_USER" -d "$ADMIN_DB" -v ON_ERROR_STOP=1 -tAc "$1"
}

pg_exec_db() {
  # 用目标库 (llm_gateway) 执行 SQL 文件
  PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
    -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$TARGET_DB" -v ON_ERROR_STOP=1 "$@"
}

# ── 等待 postgres 就绪 ──
info "等待 postgres..."
for i in $(seq 1 60); do
  if docker exec "$PG_CONTAINER" pg_isready -U "$PG_USER" -d "$ADMIN_DB" >/dev/null 2>&1; then
    ok "postgres ready (after ${i}s)"
    break
  fi
  sleep 1
done

# ── 重建库 (可选) ──
if [ "$RESET" -eq 1 ]; then
  info "--reset: 重建 $TARGET_DB 库..."
  pg_exec "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$TARGET_DB' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
  pg_exec "DROP DATABASE IF EXISTS $TARGET_DB;" >/dev/null
  ok "DROP DATABASE $TARGET_DB"
fi

# ── 创建库 ──
info "重建 $TARGET_DB 库（本地测试使用干净基线）..."
pg_exec "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$TARGET_DB' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
pg_exec "DROP DATABASE IF EXISTS $TARGET_DB;" >/dev/null 2>&1 || true
pg_exec "CREATE DATABASE $TARGET_DB;"
ok "CREATE DATABASE $TARGET_DB"

# ── 启用 Citus (citusdata/citus 镜像需要) ──
info "启用 Citus 扩展..."
pg_exec_db -c "CREATE EXTENSION IF NOT EXISTS citus;" >/dev/null 2>&1 || {
  # 单节点模式, 部分迁移不依赖 citus, 失败不阻断
  info "  (citus 扩展不可用, 单节点模式继续)"
}

info "加载基线 SQL: 00-prereqs.sql / 01-schema.sql / 02-seed.sql..."
for BASE_SQL in "$ROOT_DIR/sql/schema/00-prereqs.sql" "$BASE_SCHEMA_SQL" "$ROOT_DIR/sql/schema/02-seed.sql"; do
  BASE_NAME="$(basename "$BASE_SQL")"
  printf "  [%s] %s ... " "$BASE_NAME" "loading"
  if [[ "$BASE_NAME" == "02-seed.sql" ]]; then
    LOAD_CMD=(awk '!/__REDACTED_/' "$BASE_SQL")
  else
    LOAD_CMD=(cat "$BASE_SQL")
  fi
  if ! "${LOAD_CMD[@]}" | PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
       -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$TARGET_DB" \
       -v ON_ERROR_STOP=1 -f - >/tmp/r112_base_$$.log 2>&1; then
    echo -e "${RED}FAIL${NC}"
    err "  SQL 错误输出 (前 20 行):"
    head -20 /tmp/r112_base_$$.log | sed 's/^/    /' >&2
    rm -f /tmp/r112_base_$$.log
    exit 1
  fi
  echo -e "${GREEN}OK${NC}"
done
rm -f /tmp/r112_base_$$.log

info "应用 migrations (目录: $MIGRATIONS_DIR)..."

# 排序: 001-052 (主序列) + 291-300 (补丁序列)
# 用 sort -V 自动处理
# 排除 .down.sql 回滚脚本 (正向迁移不执行回滚)
mapfile -t MIGRATION_FILES < <(find "$MIGRATIONS_DIR" -maxdepth 1 -name "*.sql" ! -name "*.down.sql" -type f | sort)

# 本地 R1.12 只跑 schema 迁移，跳过含 demo seed / 外部依赖 / Citus 兼容性问题的迁移。
# Citus 11.3 (PG 15) 对部分索引谓词的 IMMUTABLE 检查更严, 且缺少某些函数
# (round(double,int)), 这些 migration 在生产 PG 16+ 上能跑, 本地跳过。
SKIP_MIGRATIONS=(
  "002_work_types.sql"
  "004_tuning_signals.sql"
  "005_tuning_proposals.sql"
  "021_tool_registry_and_metatools.sql"
  "029_seed_tool_registry.sql"
  "031_provider_settings.sql"
  "033_credential_model_call_history.sql"
  "036_fp_slot_limit.sql"
  "302_unified_probe_scheduler.sql"
  "304_model_health_dashboard.sql"
  "308_probe_dashboard_state_alignment.sql"
  "310_session_summaries.sql"
  "313_probe_dashboard_followup.sql"
  "315_prompt_injection_detection.sql"
  "316_output_compliance_monitoring.sql"
  "317_partition_credential_model_index.sql"
  "321_cleanup_stale_in_progress.sql"
  "340_create_partition_query_views.sql"
  "341_hot_table_independence.fix.sql"
  "342_create_other_table_views.sql"
  "343_fix_routing_decision_log_columnar.sql"
  "344_usage_ledger_hot_independence.sql"
  "345_request_wal_hot_independence.sql"
  "346_routing_decision_log_hot_independence.sql"
  "347_credential_model_index_hot_independence.sql"
  "348_tool_usage_stats_hot_independence.sql"
  "349_credit_ledger_hot_independence.sql"
  "350_session_analytics_fix.sql"
  "351_session_analytics_tables.sql"
  "353_request_logs_bodies_hot_independence.sql"
  "354_credential_model_index_hot_independence.sql"
  "355_session_analytics_indexes.sql"
  "356_session_health_columns.sql"
  "357_session_analytics_aggregation_views.sql"
  "358_session_ownership.sql"
  "364_prompt_injection_enhanced.sql"
  "365_output_compliance_policy_enhance.sql"
)

should_skip_migration() {
  local name="$1"
  local skip
  for skip in "${SKIP_MIGRATIONS[@]}"; do
    if [[ "$name" == "$skip" ]]; then
      return 0
    fi
  done
  return 1
}

if [ "${#MIGRATION_FILES[@]}" -eq 0 ]; then
  err "未找到 .sql 迁移文件"
  exit 1
fi

TOTAL=${#MIGRATION_FILES[@]}
APPLIED=0
SKIPPED=0
FAILED=0

for MIG_FILE in "${MIGRATION_FILES[@]}"; do
  MIG_NAME="$(basename "$MIG_FILE")"

  if should_skip_migration "$MIG_NAME"; then
    printf "  [%3d/%d] %s ... %s\n" "$((APPLIED+SKIPPED+FAILED+1))" "$TOTAL" "$MIG_NAME" "${YELLOW}SKIP${NC}"
    SKIPPED=$((SKIPPED+1))
    continue
  fi

  # 检查幂等性: 如果文件里含 -- idempotent: skip-if-applied 标记
  # (当前迁移不依赖此机制, 留扩展点)
  printf "  [%3d/%d] %s ... " "$((APPLIED+SKIPPED+FAILED+1))" "$TOTAL" "$MIG_NAME"

  if PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
       -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$TARGET_DB" \
       -v ON_ERROR_STOP=1 -f - < "$MIG_FILE" >/tmp/r112_mig_$$.log 2>&1; then
    echo -e "${GREEN}OK${NC}"
    APPLIED=$((APPLIED+1))
  else
    if rg -q "already exists|duplicate key value violates unique constraint|relation .* already exists|function .* already exists" /tmp/r112_mig_$$.log; then
      echo -e "${YELLOW}SKIP${NC}"
      SKIPPED=$((SKIPPED+1))
    else
      echo -e "${RED}FAIL${NC}"
      FAILED=$((FAILED+1))
      err "  SQL 错误输出 (前 20 行):"
      head -20 /tmp/r112_mig_$$.log | sed 's/^/    /' >&2
      err ""
      err "  修复建议:"
      err "    1. 检查迁移文件: $MIG_FILE"
      err "    2. 重置后重试:   $0 --reset"
      err "    3. 手动调试:     PGPASSWORD=$PG_PASS docker exec -it $PG_CONTAINER psql -U $PG_USER -d $TARGET_DB -f $MIG_FILE"
      rm -f /tmp/r112_mig_$$.log
      exit 1
    fi
  fi
  rm -f /tmp/r112_mig_$$.log
done

# ── 总结 ──
ok "Migrations 完成: $APPLIED applied, $SKIPPED skipped, $FAILED failed (total $TOTAL)"

# ── 加载本地 mock credential seed (让 v1 /v1/chat 能转发到 mock) ──
LOCAL_SEED="$ROOT_DIR/sql/scripts/03-local-mock-credential.sql"
if [ -f "$LOCAL_SEED" ]; then
  info "加载本地 mock credential seed: $(basename "$LOCAL_SEED")"
  if PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
       -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$TARGET_DB" \
       -v ON_ERROR_STOP=1 -f - < "$LOCAL_SEED" >/dev/null 2>&1; then
    ok "local mock credential seed 已加载 (provider=local-mock, model=gpt-4o)"
  else
    err "local mock credential seed 加载失败 (非致命, v1 chat 转发将不可用)"
    err "  排查: PGPASSWORD=$PG_PASS docker exec -i $PG_CONTAINER psql -U $PG_USER -d $TARGET_DB -f $LOCAL_SEED"
  fi
fi

# ── 验证 ──
TABLE_COUNT=$(pg_exec_db "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';" 2>/dev/null || echo "?")
ok "public schema 现有 $TABLE_COUNT 张表"
