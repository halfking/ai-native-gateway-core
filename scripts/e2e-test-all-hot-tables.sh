#!/bin/bash
# 端到端测试脚本：验证所有热表架构
# 测试 5 张已迁移的热表：request_logs_hot, usage_ledger_hot, request_wal_hot, 
# routing_decision_log_hot, credential_model_index_hot
#
# 使用方式：
#   ./scripts/e2e-test-all-hot-tables.sh

set -e

# 数据库连接信息
DB_HOST="${DB_HOST:-10.43.118.61}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-llm_gateway}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_PASSWORD="${DB_PASSWORD:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

function log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

function log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

function log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

function psql_exec() {
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "$1"
}

# ============================================================
# 测试 1: 验证所有热表存在
# ============================================================
log_info "测试 1: 验证所有热表存在"

TABLES=(
    "request_logs_hot"
    "usage_ledger_hot"
    "request_wal_hot"
    "routing_decision_log_hot"
    "credential_model_index_hot"
)

for table in "${TABLES[@]}"; do
    exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = '$table');")
    if [[ "$exists" =~ "t" ]]; then
        log_info "  ✓ $table exists"
    else
        log_error "  ✗ $table NOT FOUND"
        exit 1
    fi
done

# ============================================================
# 测试 2: 验证所有热表是 heap 存储
# ============================================================
log_info "测试 2: 验证所有热表是 heap 存储"

for table in "${TABLES[@]}"; do
    storage=$(psql_exec "SELECT am.amname FROM pg_class c LEFT JOIN pg_am am ON c.relam = am.oid WHERE c.relname = '$table';")
    storage=$(echo "$storage" | xargs)  # trim whitespace
    if [[ "$storage" == "heap" ]]; then
        log_info "  ✓ $table storage = heap"
    else
        log_error "  ✗ $table storage = $storage (expected heap)"
        exit 1
    fi
done

# ============================================================
# 测试 3: 验证所有 _default 分区已删除
# ============================================================
log_info "测试 3: 验证所有 _default 分区已删除"

DEFAULT_TABLES=(
    "usage_ledger_default"
    "request_wal_default"
    "routing_decision_log_default"
    "credential_model_index_default"
)

for table in "${DEFAULT_TABLES[@]}"; do
    exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = '$table');")
    if [[ "$exists" =~ "f" ]]; then
        log_info "  ✓ $table deleted"
    else
        log_error "  ✗ $table STILL EXISTS (should be deleted)"
        exit 1
    fi
done

# ============================================================
# 测试 4: 验证所有 promote 函数存在
# ============================================================
log_info "测试 4: 验证所有 promote 函数存在"

PROMOTE_FUNCTIONS=(
    "promote_request_logs_hot_to_partition"
    "promote_usage_ledger_hot_to_partition"
    "promote_request_wal_hot_to_partition"
    "promote_routing_decision_log_hot_to_partition"
    "promote_credential_model_index_hot_to_partition"
)

for fn in "${PROMOTE_FUNCTIONS[@]}"; do
    exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = '$fn');")
    if [[ "$exists" =~ "t" ]]; then
        log_info "  ✓ $fn exists"
    else
        log_error "  ✗ $fn NOT FOUND"
        exit 1
    fi
done

# ============================================================
# 测试 5: 验证所有视图存在
# ============================================================
log_info "测试 5: 验证所有视图存在"

VIEWS=(
    "request_logs_with_current_month"
    "usage_ledger_with_current_month"
    "request_wal_with_current_month"
    "routing_decision_log_with_current_month"
    "credential_model_index_with_current_month"
)

for view in "${VIEWS[@]}"; do
    exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = '$view');")
    if [[ "$exists" =~ "t" ]]; then
        log_info "  ✓ $view exists"
    else
        log_error "  ✗ $view NOT FOUND"
        exit 1
    fi
done

# ============================================================
# 测试 6: 插入测试数据（request_logs_hot）
# ============================================================
log_info "测试 6: 插入测试数据到 request_logs_hot"

TEST_REQUEST_ID="e2e-test-$(date +%s)"
psql_exec "INSERT INTO request_logs_hot (request_id, ts, tenant_id, success) VALUES ('$TEST_REQUEST_ID', NOW(), 'default', true);"
log_info "  ✓ INSERT success (request_id=$TEST_REQUEST_ID)"

# ============================================================
# 测试 7: 更新测试数据
# ============================================================
log_info "测试 7: 更新测试数据"

psql_exec "UPDATE request_logs_hot SET prompt_tokens = 100, completion_tokens = 50 WHERE request_id = '$TEST_REQUEST_ID';"
log_info "  ✓ UPDATE success"

# ============================================================
# 测试 8: 查询测试数据
# ============================================================
log_info "测试 8: 查询测试数据"

tokens=$(psql_exec "SELECT prompt_tokens FROM request_logs_hot WHERE request_id = '$TEST_REQUEST_ID';")
tokens=$(echo "$tokens" | xargs)
if [[ "$tokens" == "100" ]]; then
    log_info "  ✓ SELECT success (prompt_tokens=100)"
else
    log_error "  ✗ SELECT failed (expected 100, got $tokens)"
    exit 1
fi

# ============================================================
# 测试 9: 测试 promote 函数（不实际迁移数据）
# ============================================================
log_info "测试 9: 测试 promote 函数（dry-run）"

for fn in "${PROMOTE_FUNCTIONS[@]}"; do
    # 调用 promote 函数，传入 999 天（确保不会迁移任何数据）
    result=$(psql_exec "SELECT $fn('999 days', 10);")
    result=$(echo "$result" | xargs)
    log_info "  ✓ $fn returned $result (dry-run)"
done

# ============================================================
# 测试 10: 清理测试数据
# ============================================================
log_info "测试 10: 清理测试数据"

psql_exec "DELETE FROM request_logs_hot WHERE request_id = '$TEST_REQUEST_ID';"
log_info "  ✓ DELETE success"

# ============================================================
# 测试 11: 验证热表数据量
# ============================================================
log_info "测试 11: 验证热表数据量"

for table in "${TABLES[@]}"; do
    count=$(psql_exec "SELECT COUNT(*) FROM $table;")
    count=$(echo "$count" | xargs)
    log_info "  ✓ $table contains $count rows"
done

# ============================================================
# 总结
# ============================================================
echo ""
log_info "=========================================="
log_info "✅ 所有端到端测试通过！"
log_info "=========================================="
log_info "架构验证："
log_info "  - 5 张热表全部存在且为 heap 存储"
log_info "  - 4 张 _default 分区已删除"
log_info "  - 5 个 promote 函数全部存在"
log_info "  - 5 个视图全部存在"
log_info ""
log_info "功能验证："
log_info "  - INSERT/UPDATE/DELETE 全部正常"
log_info "  - Promote 函数可正常调用"
log_info ""
log_info "下一步：部署到生产环境"
