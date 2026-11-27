#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# casdoor-e2e 本地 PG 迁移脚本
#
# 目标库：本 compose 栈的 PG 容器（127.0.0.1:15433，db=gateway_e2e，
#         镜像 kx-citus-pg17：00-prereqs 硬依赖 citus）
# 流程：DROP+CREATE 库 → 基线三件套（00-prereqs/01-schema/02-seed）
#       → 热表本地堆目标 → **tsv 有序清单**的 startup 迁移
#
# ★ 迁移集权威依据（2026-11-27 实测对拍）：
#   fresh install 权威集 = installed_startup_migrations.tsv 的有序清单
#   （installer 唯一执行序）。sql/migrations/startup/ 目录里其余 ~275 个
#   文件是 baseline-dependent（对象已在 01-schema dump 里；重放会撞
#   view/列冲突：326 改视图列、1000-1002 引用基线没有的
#   session_turns.created_at——基线时间列是 ts）。判级台账：
#   scripts/audit/asset/installer-reachability.tsv。
#
# bash 3.2 兼容（无 mapfile；幂等 SKIP 用 grep 不用 rg）
# ─────────────────────────────────────────────────────────────────────
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
MIGRATIONS_DIR="$ROOT_DIR/sql/migrations/startup"
EMBED_DIR="$ROOT_DIR/installer/cmd/llm-gw-installer/embeddata/startup"
TSV="$ROOT_DIR/sql/schema/installed_startup_migrations.tsv"

[ -f "$SCRIPT_DIR/runtime/pg_password" ] || { echo "✗ runtime/pg_password 不存在（先跑 start-e2e.sh）" >&2; exit 1; }

PG_HOST="${PGHOST:-127.0.0.1}"
PG_PORT="${PGPORT:-15433}"
PG_USER="${PGUSER:-gateway_e2e}"
TARGET_DB="${PGDATABASE:-gateway_e2e}"
ADMIN_DB="postgres"
# 密码 SSOT = runtime/pg_password（start-e2e.sh 生成）；先 cat 到中间变量再
# export，避免写 PGPASSWORD="$(cat ...)" 一行式（历史上被工具改写两次）
PG_PW="$(cat "$SCRIPT_DIR/runtime/pg_password")"
export PGPASSWORD="$PG_PW"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }

pg_exec()     { psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$ADMIN_DB" -v ON_ERROR_STOP=1 -tAc "$1"; }
pg_exec_db()  { psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$TARGET_DB" -v ON_ERROR_STOP=1 "$@"; }

info "等待 PG ($PG_HOST:$PG_PORT)..."
for i in $(seq 1 60); do
  if pg_isready -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$ADMIN_DB" >/dev/null 2>&1; then
    ok "PG ready (after ${i}s)"; break
  fi
  [ "$i" = 60 ] && { err "PG 60s 未就绪"; exit 1; }
  sleep 1
done

info "重建 $TARGET_DB 库..."
pg_exec "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$TARGET_DB' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
pg_exec "DROP DATABASE IF EXISTS $TARGET_DB;" >/dev/null 2>&1 || true
pg_exec "CREATE DATABASE $TARGET_DB;"
ok "CREATE DATABASE $TARGET_DB"

info "加载基线 SQL: 00-prereqs.sql / 01-schema.sql / 02-seed.sql..."
for BASE_SQL in "$ROOT_DIR/sql/schema/00-prereqs.sql" "$ROOT_DIR/sql/schema/01-schema.sql" "$ROOT_DIR/sql/schema/02-seed.sql"; do
  BASE_NAME="$(basename "$BASE_SQL")"
  printf "  [%s] %s ... " "$BASE_NAME" "loading"
  if [ "$BASE_NAME" = "02-seed.sql" ]; then
    awk '!/__REDACTED_/' "$BASE_SQL" | psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$TARGET_DB" \
         -v ON_ERROR_STOP=1 -f - > "/tmp/e2e_base_$$.log" 2>&1 || {
      echo "FAIL"; head -20 "/tmp/e2e_base_$$.log" | sed 's/^/    /' >&2; rm -f "/tmp/e2e_base_$$.log"; exit 1; }
  else
    psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$TARGET_DB" \
         -v ON_ERROR_STOP=1 -f "$BASE_SQL" > "/tmp/e2e_base_$$.log" 2>&1 || {
      echo "FAIL"; head -20 "/tmp/e2e_base_$$.log" | sed 's/^/    /' >&2; rm -f "/tmp/e2e_base_$$.log"; exit 1; }
  fi
  echo "OK"
done
rm -f "/tmp/e2e_base_$$.log"

info "Ensuring local hot-table prerequisites..."
# r112 范式的 _request_logs 等下划线前缀名在本基线不存在（01-schema 建的是无前缀名）；
# 各表独立容错，存在的才建 hot 副本
for HT in "_request_logs_hot:_request_logs" "usage_ledger_hot:usage_ledger" "request_wal_hot:request_wal" "request_logs_hot:request_logs"; do
  HOT="${HT%%:*}"; BASE="${HT##*:}"
  pg_exec_db -c "CREATE TABLE IF NOT EXISTS $HOT (LIKE $BASE INCLUDING ALL);" >/dev/null 2>&1 || true
done
ok "local hot-table prerequisites ready"

# ── 迁移前置 seed ──
# 02-seed.sql 的 models_canonical 数据在本地被 __REDACTED_ 过滤/不足额，
# 而 956 号迁移有「canonical 必须存在且 active」的 fail-closed 守卫 ⇒ 显式补种。
# featured 列由 tsv 序 371 的 953 号迁移添加 ⇒ seed 只写基线已有列
info "Seed models_canonical (956 前置)..."
pg_exec_db -c "INSERT INTO models_canonical (canonical_name, status) VALUES ('minimax-m3.1-flash', 'active') ON CONFLICT (canonical_name) DO NOTHING;" >/dev/null
ok "models_canonical seed 就绪"

# ── 应用 startup 迁移（fresh install 权威集 = tsv 有序清单）─────────────
[ -f "$TSV" ] || { err "找不到 $TSV"; exit 1; }

# tsv 两列：序号 \t 文件名；取全部 .sql 行
MIG_LIST="$(awk -F'\t' '($0 !~ /^#/) && (NF>=2) && ($2 ~ /\.sql$/) {print $2}' "$TSV")"
TOTAL="$(printf '%s\n' "$MIG_LIST" | grep -c .)"
[ "$TOTAL" -eq 0 ] && { err "tsv 清单为空"; exit 1; }

info "应用 migrations（${TOTAL} 个文件, 按 tsv 有序清单）..."
APPLIED=0; SKIPPED=0; FAILED=0; IDX=0
while IFS= read -r MIG_NAME; do
  [ -z "$MIG_NAME" ] && continue
  IDX=$((IDX+1))
  MIG_FILE="$MIGRATIONS_DIR/$MIG_NAME"
  [ -f "$MIG_FILE" ] || MIG_FILE="$EMBED_DIR/$MIG_NAME"
  if [ ! -f "$MIG_FILE" ]; then
    printf "  [%3d/%s] %s ... %s\n" "$IDX" "$TOTAL" "$MIG_NAME" "${RED}MISSING${NC}"
    FAILED=$((FAILED+1)); continue
  fi
  printf "  [%3d/%s] %s ... " "$IDX" "$TOTAL" "$MIG_NAME"
  if psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$TARGET_DB" \
       -v ON_ERROR_STOP=1 -f - < "$MIG_FILE" > "/tmp/e2e_mig_$$.log" 2>&1; then
    echo "OK"; APPLIED=$((APPLIED+1))
  else
    if grep -Eq "already exists|duplicate key value violates unique constraint" "/tmp/e2e_mig_$$.log"; then
      echo "SKIP"; SKIPPED=$((SKIPPED+1))
    else
      echo "FAIL"; FAILED=$((FAILED+1))
      err "  SQL 错误 (前 20 行):"; head -20 "/tmp/e2e_mig_$$.log" | sed 's/^/    /' >&2
      rm -f "/tmp/e2e_mig_$$.log"; exit 1
    fi
  fi
  rm -f "/tmp/e2e_mig_$$.log"
done <<MIG_EOF
$MIG_LIST
MIG_EOF

ok "Migrations 完成: $APPLIED applied, $SKIPPED skipped, $FAILED failed (total $TOTAL)"

TABLE_COUNT=$(pg_exec_db "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';" 2>/dev/null || echo "?")
ok "public schema tables: ${TABLE_COUNT}"
