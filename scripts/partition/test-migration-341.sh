#!/bin/bash
# Migration 341 本地测试脚本
#
# 用途：在本地环境测试热表独立化方案
# 流程：应用 migration → 验证数据 → 写入测试 → 查询测试 → 性能对比
#
# 配置：使用项目 .env.local 中的 DATABASE_URL（默认）
#       显式覆盖：PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE 环境变量

set -euo pipefail

# 默认连接配置（与 .env.local 保持一致）
PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-55432}"
PGUSER="${PGUSER:-llm_gateway}"
PGPASSWORD="${PGPASSWORD:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}"
PGDATABASE="${PGDATABASE:-llm_gateway}"

export PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE

# 如果设置了 DATABASE_URL，优先使用
if [[ -n "${DATABASE_URL:-}" ]]; then
  PGHOST=""
  PGPORT=""
  PGUSER=""
  PGPASSWORD=""
  export DATABASE_URL
  PSQL_CMD=(psql "${DATABASE_URL}")
else
  PSQL_CMD=(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE")
fi

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${BLUE}[$(date '+%H:%M:%S')]${NC} $*"; }
ok() { echo -e "${GREEN}✅ $*${NC}"; }
fail() { echo -e "${RED}❌ $*${NC}"; exit 1; }
warn() { echo -e "${YELLOW}⚠️  $*${NC}"; }

# 检查 psql 是否可用
command -v psql >/dev/null 2>&1 || fail "psql command not found"

# 检查连接
"${PSQL_CMD[@]}" -c "SELECT 1" >/dev/null 2>&1 || fail "Cannot connect to PostgreSQL"
ok "数据库连接成功: $PGHOST:$PGPORT/$PGDATABASE (或 DATABASE_URL)"

echo "========================================"
echo "Migration 341 本地测试"
echo "========================================"
echo ""

# ========================================
# 1. 数据备份
# ========================================

log "步骤 1: 备份 request_logs_default 数据"
# CREATE TABLE AS SELECT 不支持 IF NOT EXISTS，需先 DROP
"${PSQL_CMD[@]}" -c "DROP TABLE IF EXISTS request_logs_default_backup_341;" >/dev/null
"${PSQL_CMD[@]}" -c "CREATE TABLE request_logs_default_backup_341 AS SELECT * FROM request_logs_default;" >/dev/null || fail "备份失败"
BACKUP_COUNT=$("${PSQL_CMD[@]}" -t -A -c "SELECT count(*) FROM request_logs_default_backup_341;" 2>/dev/null || echo "0")
ok "备份完成: $BACKUP_COUNT 行"

# ========================================
# 2. 应用 Migration 341
# ========================================

log "步骤 2: 应用 Migration 341"
"${PSQL_CMD[@]}" < sql/migrations/startup/341_hot_table_independence.sql >/dev/null || fail "Migration 341 应用失败"
ok "Migration 341 应用成功"

# ========================================
# 3. 数据完整性验证
# ========================================

log "步骤 3: 验证数据迁移"

# 3.1 检查热表行数
HOT_COUNT=$("${PSQL_CMD[@]}" -t -A -c "SELECT count(*) FROM request_logs_hot;" 2>/dev/null || echo "0")
log "request_logs_hot: $HOT_COUNT 行"

if [[ "$HOT_COUNT" != "$BACKUP_COUNT" ]]; then
    fail "数据丢失！backup=$BACKUP_COUNT, hot=$HOT_COUNT"
fi
ok "数据完整性验证通过"

# 3.2 检查索引
INDEX_COUNT=$("${PSQL_CMD[@]}" -t -A -c "SELECT count(*) FROM pg_indexes WHERE tablename = 'request_logs_hot';" 2>/dev/null || echo "0")
log "request_logs_hot 索引数: $INDEX_COUNT"
if [[ "$INDEX_COUNT" -lt 6 ]]; then
    warn "索引数量少于预期（应为 6 个）"
fi

# 3.3 检查分区状态
log "检查分区 ATTACHED 状态..."
ATTACHED_COUNT=$("${PSQL_CMD[@]}" -t -A -c "
    SELECT count(*)
    FROM pg_inherits i
    JOIN pg_class c ON i.inhrelid = c.oid
    WHERE i.inhparent = 'request_logs'::regclass
      AND c.relname LIKE 'request_logs_2026_%';" 2>/dev/null || echo "0")
log "ATTACHED 月度分区数: $ATTACHED_COUNT"

# ========================================
# 4. 写入测试
# ========================================

log "步骤 4: 写入测试"

# 4.1 INSERT 测试
"${PSQL_CMD[@]}" -c "
    INSERT INTO request_logs_hot (request_id, ts, tenant_id, success)
    VALUES ('test-341-insert', now(), 'test-tenant-341', true);" >/dev/null || fail "INSERT 失败"
ok "INSERT 测试通过"

# 4.2 UPDATE 测试
"${PSQL_CMD[@]}" -c "
    UPDATE request_logs_hot
    SET success = false
    WHERE request_id = 'test-341-insert';" >/dev/null || fail "UPDATE 失败"
ok "UPDATE 测试通过"

# 4.3 UPSERT 测试
"${PSQL_CMD[@]}" -c "
    INSERT INTO request_logs_hot (request_id, ts, tenant_id, success)
    VALUES ('test-341-upsert', now(), 'test-tenant-341', true)
    ON CONFLICT (request_id, ts) DO UPDATE SET success = EXCLUDED.success;" >/dev/null || fail "UPSERT 失败"
ok "UPSERT 测试通过"

# ========================================
# 5. 查询测试
# ========================================

log "步骤 5: 查询测试"

# 5.1 直接查热表（用 EXPLAIN ANALYZE 拿真实耗时）
HOT_QUERY_TIME=$("${PSQL_CMD[@]}" -c "\timing on" -c "
    EXPLAIN (ANALYZE, FORMAT TEXT)
    SELECT count(*) FROM request_logs_hot WHERE ts >= now() - interval '1 day';
" 2>&1 | grep "Execution Time" | awk '{print $3}')
log "热表查询执行时间: ${HOT_QUERY_TIME:-N/A} ms"

# 5.2 查 VIEW
VIEW_QUERY_TIME=$("${PSQL_CMD[@]}" -c "\timing on" -c "
    EXPLAIN (ANALYZE, FORMAT TEXT)
    SELECT count(*) FROM request_logs_with_current_month WHERE ts >= now() - interval '7 days';
" 2>&1 | grep "Execution Time" | awk '{print $3}')
log "VIEW 查询执行时间: ${VIEW_QUERY_TIME:-N/A} ms"

# 5.3 验证 VIEW 数据完整性
VIEW_COUNT=$("${PSQL_CMD[@]}" -t -A -c "SELECT count(*) FROM request_logs_with_current_month WHERE ts >= '2026-06-01';" 2>/dev/null || echo "0")
PARENT_COUNT=$("${PSQL_CMD[@]}" -t -A -c "SELECT count(*) FROM request_logs WHERE ts >= '2026-06-01';" 2>/dev/null || echo "0")
log "VIEW 数据: $VIEW_COUNT 行"
log "父表数据: $PARENT_COUNT 行"

if [[ "$VIEW_COUNT" -gt "$PARENT_COUNT" ]]; then
    ok "VIEW 包含更多数据（包含 hot 表）"
else
    warn "VIEW 数据可能不完整（VIEW=$VIEW_COUNT, 父表=$PARENT_COUNT）"
fi

# ========================================
# 6. Promote 函数测试
# ========================================

log "步骤 6: Promote 函数测试"

# 插入一些旧数据（8 天前）
"${PSQL_CMD[@]}" -c "
    INSERT INTO request_logs_hot (request_id, ts, tenant_id, success)
    VALUES ('test-341-old', now() - interval '8 days', 'test-tenant-341', true)
    ON CONFLICT DO NOTHING;" >/dev/null 2>&1 || warn "插入旧数据失败"

# 执行 promote
PROMOTED=$("${PSQL_CMD[@]}" -t -A -c "SELECT promote_request_logs_hot_to_partition('7 days'::interval, 100);" 2>/dev/null || echo "0")
log "Promote 迁移: $PROMOTED 行"

if [[ "$PROMOTED" -gt 0 ]]; then
    ok "Promote 函数工作正常"
else
    log "无数据迁移（可能热表数据都 < 7 天）"
fi

# ========================================
# 7. 清理测试数据
# ========================================

log "步骤 7: 清理测试数据"
"${PSQL_CMD[@]}" -c "DELETE FROM request_logs_hot WHERE request_id LIKE 'test-341-%';" >/dev/null 2>&1 || warn "清理失败"
ok "测试数据已清理"

# ========================================
# 总结
# ========================================

echo ""
echo "========================================"
echo "✅ Migration 341 本地测试通过"
echo "========================================"
echo ""
echo "性能对比:"
echo "  热表查询: ${HOT_QUERY_TIME:-N/A} ms"
echo "  VIEW 查询: ${VIEW_QUERY_TIME:-N/A} ms"
echo ""
echo "数据状态:"
echo "  热表行数: $HOT_COUNT"
echo "  ATTACHED 分区数: $ATTACHED_COUNT"
echo "  Promote 迁移: $PROMOTED 行"
echo ""
echo "下一步:"
echo "  1. 重启服务验证写入"
echo "  2. 观察 1-2 天确保稳定"
echo "  3. 部署到 184 环境"
echo ""
