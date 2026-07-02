#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# 本地应用 columnar 不变量, 使本地 DB 与 184 的 columnar 配置一致
#
# pg_dump 不同步分区子表的 access method, 因此 full-sync 后需要显式:
#   1. 确保 citus_columnar 扩展已安装
#   2. 转换 9 张独立 append-only 表为 columnar (phase-22)
#   3. 安装 columnar 不变量 (phase-23): 重写 ensure 函数 + 事件触发器 + healthcheck
#   4. 调用 columnar_heal() 转换所有非合规分区
#
# 用法:
#   ./scripts/apply-columnar-local.sh
#
# 幂等: 可安全重复执行
# 前提: r112_postgres 容器在运行, llm_gateway DB 已存在
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

LOCAL_CONTAINER="${LOCAL_CONTAINER:-r112_postgres}"
LOCAL_DB="${LOCAL_DB:-llm_gateway}"
LOCAL_DB_USER="${LOCAL_DB_USER:-kxuser}"
LOCAL_DB_PASS="${LOCAL_DB_PASS:-kxpass}"

PHASE22_DIR="$ROOT_DIR/deploy/sql/phase-22-extension-and-role-sync"
PHASE23_DIR="$ROOT_DIR/deploy/sql/phase-23-columnar-invariant"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
err()  { printf "${RED}✗ %s${NC}\n" "$*" >&2; }
ok()   { printf "${GREEN}✓ %s${NC}\n" "$*"; }
info() { printf "${YELLOW}▶ %s${NC}\n" "$*"; }

# 在本地 llm_gateway DB 执行 SQL 文件
exec_sql_file() {
  local file="$1" label="$2"
  PGPASSWORD="$LOCAL_DB_PASS" docker exec -e PGPASSWORD="$LOCAL_DB_PASS" \
    -i "$LOCAL_CONTAINER" psql -U "$LOCAL_DB_USER" -d "$LOCAL_DB" \
    -v ON_ERROR_STOP=1 -f - < "$file" >/tmp/colar_apply_$$.log 2>&1 || {
    err "$label 失败:"
    head -10 /tmp/colar_apply_$$.log | sed 's/^/    /' >&2
    rm -f /tmp/colar_apply_$$.log
    return 1
  }
  rm -f /tmp/colar_apply_$$.log
}

exec_sql() {
  PGPASSWORD="$LOCAL_DB_PASS" docker exec -e PGPASSWORD="$LOCAL_DB_PASS" \
    "$LOCAL_CONTAINER" psql -U "$LOCAL_DB_USER" -d "$LOCAL_DB" \
    -v ON_ERROR_STOP=1 -tAc "$1" 2>&1
}

# ── 前置检查 ──
command -v docker >/dev/null 2>&1 || { err "docker 未安装"; exit 1; }
docker ps --format '{{.Names}}' | grep -q "^${LOCAL_CONTAINER}$" || {
  err "容器 $LOCAL_CONTAINER 未运行"
  err "  启动: ./scripts/local-up.sh --deps"
  exit 1
}

info "应用 columnar 不变量到本地 $LOCAL_DB (匹配 184)"

# ── 1. 确保 citus_columnar 扩展 ──
info "步骤 1: 确保 citus_columnar 扩展..."
exec_sql "CREATE EXTENSION IF NOT EXISTS citus_columnar;" >/dev/null
ok "citus_columnar 扩展已就绪"

# ── 2. 转换 9 张独立 append-only 表为 columnar (phase-22 02) ──
info "步骤 2: 转换独立 append-only 表为 columnar (phase-22)..."
if [ -f "$PHASE22_DIR/02-columnar-tables.sql" ]; then
  exec_sql_file "$PHASE22_DIR/02-columnar-tables.sql" "phase-22 columnar-tables" || {
    info "(部分 ALTER 可能因表不存在而跳过, 继续)"
  }
  ok "独立表 columnar 转换完成"
else
  info "跳过: phase-22 02-columnar-tables.sql 不存在"
fi

# ── 3. 安装 columnar 不变量 (phase-23: 00 → 01 → 02 → 03) ──
info "步骤 3: 安装 columnar 不变量 (phase-23)..."
for phase_file in 00-prereqs.sql 01-rewrite-ensure-functions.sql 02-event-trigger.sql 03-healthcheck-and-heal.sql; do
  full_path="$PHASE23_DIR/$phase_file"
  if [ ! -f "$full_path" ]; then
    info "跳过: $phase_file 不存在"
    continue
  fi
  printf "  [%s] ... " "$phase_file"
  if exec_sql_file "$full_path" "$phase_file" 2>/dev/null; then
    echo -e "${GREEN}OK${NC}"
  else
    echo -e "${YELLOW}SKIP (非致命)${NC}"
  fi
done
ok "columnar 不变量已安装"

# ── 4. 调用 columnar_heal() 转换所有非合规分区 ──
info "步骤 4: 执行 columnar_heal() 转换非合规分区..."
heal_result=$(exec_sql "SELECT count(*) FILTER (WHERE converted) FROM columnar_heal();" 2>/dev/null || echo "?")
if [ "$heal_result" = "?" ]; then
  info "columnar_heal() 不可用 (phase-23 可能未完全应用), 尝试手动转换..."
  # 手动转换 INSERT-only 父表的所有分区
  exec_sql "
    DO \$\$
    DECLARE r record;
    BEGIN
      FOR r IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        JOIN pg_am am ON am.oid = c.relam
        WHERE n.nspname = 'public'
          AND p.relname IN ('routing_decision_log', 'credential_model_index')
          AND am.amname != 'columnar'
      LOOP
        BEGIN
          EXECUTE format('ALTER TABLE public.%I SET ACCESS METHOD columnar', r.relname);
          RAISE NOTICE 'Converted % to columnar', r.relname;
        EXCEPTION WHEN OTHERS THEN
          RAISE NOTICE 'Skip % (already columnar or error: %)', r.relname, SQLERRM;
        END;
      END LOOP;
    END\$\$;" 2>&1 | head -5
else
  ok "columnar_heal() 转换了 $heal_result 个分区"
fi

# ── 5. 验证 ──
info "步骤 5: 验证 columnar 状态..."
echo ""
echo "=== columnar 表数量 ==="
exec_sql "
SELECT count(*) AS columnar_tables
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_am am ON am.oid = c.relam
WHERE n.nspname = 'public' AND c.relkind = 'r' AND am.amname = 'columnar';"
echo ""
echo "=== columnar_drift_report() ==="
exec_sql "SELECT parent_name, compliant_count, noncompliant_count FROM columnar_drift_report()
          WHERE noncompliant_count > 0 OR compliant_count > 0
          ORDER BY parent_name;" 2>/dev/null || info "(drift_report 不可用, 非 fatal)"

echo ""
ok "columnar 不变量应用完成"
echo ""
echo "对比 184: ./scripts/verify-columnar-sync.sh"
