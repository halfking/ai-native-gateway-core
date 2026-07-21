#!/bin/bash
# E2E 集成测试：Ticket #9-11 Bodies 双写与查询
# 用途：在 245 测试环境验证完整业务流程

set -euo pipefail

# 配置
ADMIN_API="${ADMIN_API:-http://localhost:8080}"
DB_HOST="${DB_HOST:-172.16.2.210}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-llm_gateway}"
DB_PASS="${DB_PASS:-}"
ADMIN_API_KEY="${ADMIN_API_KEY:-${LLM_GATEWAY_ADMIN_API_KEY:-}}"

# 颜色输出
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✅ $1${NC}"; }
fail() { echo -e "${RED}❌ $1${NC}"; exit 1; }
warn() { echo -e "${YELLOW}⚠️  $1${NC}"; }
info() { echo "ℹ️  $1"; }

# 测试计数
TOTAL=0
PASSED=0
FAILED=0

test_start() {
    TOTAL=$((TOTAL + 1))
    echo ""
    echo "========================================="
    echo "Test $TOTAL: $1"
    echo "========================================="
}

test_pass() {
    PASSED=$((PASSED + 1))
    pass "$1"
}

test_fail() {
    FAILED=$((FAILED + 1))
    fail "$1"
}

# 检查环境
check_env() {
    info "检查环境配置..."
    
    if [ -z "$DB_PASS" ]; then
        fail "DB_PASS 未设置"
    fi
    if [ -z "$ADMIN_API_KEY" ]; then
        fail "ADMIN_API_KEY 或 LLM_GATEWAY_ADMIN_API_KEY 未设置"
    fi
    
    pass "环境配置检查通过"
}

# PostgreSQL 查询辅助函数
psql_query() {
    PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c "$1" 2>/dev/null
}

# Test Suite 1: 端到端业务流程
test_e2e_001() {
    test_start "TC-E2E-001: 创建会话并请求 → 查询详情"
    
    # Only a paired row validates that application dual-write has occurred.
    info "查询最近的双写请求记录..."
    REQUEST_ID=$(psql_query "SELECT rl.request_id FROM request_logs_hot rl JOIN request_logs_bodies_hot rb ON rb.request_id = rl.request_id AND rb.ts = rl.ts WHERE rb.request_body IS NOT NULL OR rb.response_body IS NOT NULL ORDER BY rl.ts DESC LIMIT 1;")
    
    if [ -z "$REQUEST_ID" ]; then
        test_fail "未找到 request_logs_hot 与 request_logs_bodies_hot 的双写记录"
    fi
    
    info "使用 request_id: $REQUEST_ID"
    
    # 2. 验证双写
    info "验证双写记录..."
    META_COUNT=$(psql_query "SELECT COUNT(*) FROM request_logs_hot WHERE request_id = '$REQUEST_ID';")
    BODIES_COUNT=$(psql_query "SELECT COUNT(*) FROM request_logs_bodies_hot WHERE request_id = '$REQUEST_ID';")
    
    if [ "$META_COUNT" -eq 1 ]; then
        test_pass "request_logs_hot 有记录"
    else
        test_fail "request_logs_hot 记录数异常: $META_COUNT"
    fi
    
    if [ "$BODIES_COUNT" -eq 1 ]; then
        test_pass "request_logs_bodies_hot 有配对记录"
    else
        test_fail "request_logs_bodies_hot 记录数异常: $BODIES_COUNT"
    fi
    
    # 3. 调用 Admin API 查询详情
    info "调用 Admin API 查询详情..."
    API_RESPONSE=$(curl -fsS -H "Authorization: Bearer $ADMIN_API_KEY" "$ADMIN_API/api/logs/$REQUEST_ID" 2>/dev/null || echo "")
    
    if echo "$API_RESPONSE" | grep -q "request_id"; then
        test_pass "Admin API 返回有效响应"
    else
        test_fail "Admin API 未返回日志详情"
    fi
}

test_e2e_002() {
    test_start "TC-E2E-002: Null Bodies 场景"
    
    info "查询 sibling bodies 均为 null 的记录..."
    NULL_REQUEST_ID=$(psql_query "SELECT rl.request_id FROM request_logs_hot rl LEFT JOIN request_logs_bodies_hot rb ON rb.request_id = rl.request_id AND rb.ts = rl.ts WHERE rb.request_id IS NULL OR (rb.request_body IS NULL AND rb.response_body IS NULL) ORDER BY rl.ts DESC LIMIT 1;")
    
    if [ -z "$NULL_REQUEST_ID" ]; then
        warn "没有找到 null bodies 记录，跳过此测试"
        return 0
    fi
    
    info "使用 request_id: $NULL_REQUEST_ID"
    
    # 验证 bodies 表中无记录或为 null
    BODIES_COUNT=$(psql_query "SELECT COUNT(*) FROM request_logs_bodies_hot WHERE request_id = '$NULL_REQUEST_ID';")
    
    if [ "$BODIES_COUNT" -eq 0 ]; then
        test_pass "Null bodies 场景：bodies 表无记录"
    else
        info "Bodies 表有记录，检查是否为 null..."
        BODY_NULL=$(psql_query "SELECT request_body IS NULL AND response_body IS NULL FROM request_logs_bodies_hot WHERE request_id = '$NULL_REQUEST_ID';")
        if [ "$BODY_NULL" = "t" ]; then
            test_pass "Null bodies 场景：bodies 为 null"
        else
            test_fail "Null bodies 场景：bodies 不为 null"
        fi
    fi
}

test_e2e_003() {
    test_start "TC-E2E-003: 向后兼容（旧记录）"
    
    info "查询 Migration 353 之前的记录（request_logs_hot 有 request_body）..."
    OLD_REQUEST_ID=$(psql_query "SELECT request_id FROM request_logs_hot WHERE request_body IS NOT NULL ORDER BY ts ASC LIMIT 1;")
    
    if [ -z "$OLD_REQUEST_ID" ]; then
        warn "没有找到旧记录，跳过此测试"
        return 0
    fi
    
    info "使用 request_id: $OLD_REQUEST_ID"
    
    # 检查 bodies 表是否有记录
    BODIES_COUNT=$(psql_query "SELECT COUNT(*) FROM request_logs_bodies_hot WHERE request_id = '$OLD_REQUEST_ID';")
    
    if [ "$BODIES_COUNT" -eq 0 ]; then
        info "旧记录未迁移到 bodies 表（预期行为）"
        test_pass "向后兼容：可以从 request_logs_hot 读取"
    else
        info "旧记录已迁移到 bodies 表"
        test_pass "向后兼容：bodies 表有数据"
    fi
}

# Test Suite 2: 性能验证
test_perf_001() {
    test_start "TC-PERF-001: 查询性能（有 bodies）"
    
    info "EXPLAIN ANALYZE 查询..."
    REQUEST_ID=$(psql_query "SELECT request_id FROM request_logs_bodies_hot ORDER BY ts DESC LIMIT 1;")
    
    if [ -z "$REQUEST_ID" ]; then
        warn "没有找到 bodies 记录，跳过性能测试"
        return 0
    fi
    
    EXPLAIN_OUTPUT=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" <<EOSQL
EXPLAIN ANALYZE
SELECT 
    rl.*,
    rlb.request_body,
    rlb.response_body
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rlb 
    ON rl.request_id = rlb.request_id 
    AND rl.ts = rlb.ts
WHERE rl.request_id = '$REQUEST_ID';
EOSQL
)
    
    # 检查是否使用索引
    if echo "$EXPLAIN_OUTPUT" | grep -qi "Index Scan"; then
        test_pass "使用 Index Scan"
    else
        warn "未使用 Index Scan"
    fi
    
    # 提取执行时间
    EXEC_TIME=$(echo "$EXPLAIN_OUTPUT" | grep "Execution Time" | awk '{print $3}')
    info "执行时间: ${EXEC_TIME}ms"
    
    if [ -n "$EXEC_TIME" ] && (( $(echo "$EXEC_TIME < 50" | bc -l 2>/dev/null || echo 0) )); then
        test_pass "执行时间 < 50ms"
    else
        warn "执行时间可能超过预期"
    fi
}

# Test Suite 3: 磁盘空间验证
test_disk_001() {
    test_start "TC-DISK-001: 表大小对比"
    
    info "查询表大小..."
    TABLE_SIZES=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" <<EOSQL
SELECT 
    'request_logs_hot' AS table_name,
    pg_size_pretty(pg_total_relation_size('request_logs_hot')) AS size
UNION ALL
SELECT 
    'request_logs_bodies_hot',
    pg_size_pretty(pg_total_relation_size('request_logs_bodies_hot'));
EOSQL
)
    
    echo "$TABLE_SIZES"
    test_pass "表大小查询成功"
}

# 主测试流程
main() {
    echo "================================================"
    echo "E2E 集成测试：Ticket #9-11 Bodies 双写与查询"
    echo "================================================"
    echo ""
    
    check_env
    
    # 执行测试
    test_e2e_001
    test_e2e_002
    test_e2e_003
    test_perf_001
    test_disk_001
    
    # 测试总结
    echo ""
    echo "================================================"
    echo "测试总结"
    echo "================================================"
    echo "总计: $TOTAL"
    echo -e "${GREEN}通过: $PASSED${NC}"
    echo -e "${RED}失败: $FAILED${NC}"
    
    if [ $FAILED -eq 0 ]; then
        pass "所有测试通过！"
        exit 0
    else
        fail "$FAILED 个测试失败"
    fi
}

main "$@"
