#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# R1.12 本地 PG migrations
#
# 流程:
#   1. 等待 r112_postgres 启动
#   2. 创建 llm_gateway 库 (IF NOT EXISTS, 幂等)
#   3. 按文件名顺序应用 db/migrations/*.sql (001 → 052)
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
MIGRATIONS_DIR="$ROOT_DIR/db/migrations"

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
  err "  修复: ./scripts/local-r112-up.sh  (会先启动 postgres)"
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
info "确保 $TARGET_DB 库存在..."
EXISTS=$(pg_exec "SELECT 1 FROM pg_database WHERE datname='$TARGET_DB';" || true)
if [ "$EXISTS" != "1" ]; then
  pg_exec "CREATE DATABASE $TARGET_DB;"
  ok "CREATE DATABASE $TARGET_DB"
else
  ok "$TARGET_DB 库已存在 (skip create)"
fi

# ── 启用 Citus (citusdata/citus 镜像需要) ──
info "启用 Citus 扩展..."
pg_exec_db -c "CREATE EXTENSION IF NOT EXISTS citus;" >/dev/null 2>&1 || {
  # 单节点模式, 部分迁移不依赖 citus, 失败不阻断
  info "  (citus 扩展不可用, 单节点模式继续)"
}

# ── 应用 migrations ──
info "应用 migrations (目录: $MIGRATIONS_DIR)..."

# 排序: 001-052 (主序列) + 291-300 (补丁序列)
# 用 sort -V 自动处理
mapfile -t MIGRATION_FILES < <(find "$MIGRATIONS_DIR" -maxdepth 1 -name "*.sql" -type f | sort)

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

  # 检查幂等性: 如果文件里含 -- idempotent: skip-if-applied 标记
  # (当前迁移不依赖此机制, 留扩展点)
  printf "  [%3d/%d] %s ... " "$((APPLIED+SKIPPED+FAILED+1))" "$TOTAL" "$MIG_NAME"

  if PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
       -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$TARGET_DB" \
       -v ON_ERROR_STOP=1 -f - < "$MIG_FILE" >/tmp/r112_mig_$$.log 2>&1; then
    echo -e "${GREEN}OK${NC}"
    APPLIED=$((APPLIED+1))
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
  rm -f /tmp/r112_mig_$$.log
done

# ── 总结 ──
ok "Migrations 完成: $APPLIED applied, $SKIPPED skipped, $FAILED failed (total $TOTAL)"

# ── 验证 ──
TABLE_COUNT=$(pg_exec_db "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';" 2>/dev/null || echo "?")
ok "public schema 现有 $TABLE_COUNT 张表"
