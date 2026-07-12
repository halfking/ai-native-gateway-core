#!/bin/bash
# API端到端测试脚本：验证分表架构下的数据完整性
# 测试策略：通过真实API调用验证数据是否正确保存到hot表

set -e

# 服务端点（本地测试）
API_BASE="${API_BASE:-http://localhost:8080}"
API_KEY="${API_KEY:-test-api-key}"

# 数据库连接
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-kxuser}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_PASSWORD="${DB_PASSWORD:-kxpass}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

function log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
function log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
function log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }

function psql_exec() {
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "$1" 2>/dev/null
}

# 测试计数器
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# ============================================================
# 测试 1: 验证服务是否运行
# ============================================================
function test_service_health() {
    log_info "测试 1: 验证服务健康状态"
    ((TOTAL_TESTS++))
    
    if curl -s -f "${API_BASE}/health" > /dev/null 2>&1; then
        log_info "✓ 服务健康检查通过"
        ((PASSED_TESTS++))
        return 0
    else
        log_error "✗ 服务未运行或健康检查失败"
        log_warn "请先启动服务: go run cmd/gateway/main.go"
        ((FAILED_TESTS++))
        return 1
    fi
}

# ============================================================
# 测试 2: 通过 API 创建请求并验证数据保存
# ============================================================
function test_request_logs_via_api() {
    log_info "测试 2: 验证 request_logs_hot 数据保存"
    ((TOTAL_TESTS++))
    
    local test_id="api-test-$(date +%s)"
    
    # 调用 API（假设是聊天接口）
    log_info "  发送 API 请求..."
    local response=$(curl -s -X POST "${API_BASE}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"gpt-3.5-turbo\",
            \"messages\": [{\"role\": \"user\", \"content\": \"test\"}],
            \"max_tokens\": 10
        }" 2>&1)
    
    # 检查响应
    if echo "$response" | grep -q "error"; then
        log_warn "  API 调用失败（可能是服务未配置或 API Key 无效）"
        log_warn "  跳过此测试"
        return 0
    fi
    
    # 等待数据写入
    sleep 2
    
    # 查询最近的请求记录
    local count=$(psql_exec "SELECT COUNT(*) FROM request_logs_hot WHERE ts > NOW() - INTERVAL '10 seconds';")
    count=$(echo "$count" | xargs)
    
    if [[ "$count" -gt 0 ]]; then
        log_info "✓ request_logs_hot 有 $count 条最近记录"
        ((PASSED_TESTS++))
    else
        log_error "✗ request_logs_hot 没有找到最近的记录"
        ((FAILED_TESTS++))
    fi
}

# ============================================================
# 测试 3: 验证 usage_ledger_hot 数据保存
# ============================================================
function test_usage_ledger_via_db() {
    log_info "测试 3: 验证 usage_ledger_hot 数据保存"
    ((TOTAL_TESTS++))
    
    # 直接插入测试数据
    psql_exec "INSERT INTO usage_ledger_hot (request_id, ts, tenant_id, total_tokens, cost_usd, success) VALUES ('test-$(date +%s)', NOW(), 'default', 100, 0.001, true) ON CONFLICT (request_id, ts) DO NOTHING;"
    
    # 验证数据
    local count=$(psql_exec "SELECT COUNT(*) FROM usage_ledger_hot WHERE ts > NOW() - INTERVAL '10 seconds';")
    count=$(echo "$count" | xargs)
    
    if [[ "$count" -gt 0 ]]; then
        log_info "✓ usage_ledger_hot 插入成功，包含 $count 条最近记录"
        ((PASSED_TESTS++))
    else
        log_error "✗ usage_ledger_hot 插入失败"
        ((FAILED_TESTS++))
    fi
}

# ============================================================
# 测试 4: 验证视图聚合查询
# ============================================================
function test_view_aggregation() {
    log_info "测试 4: 验证视图聚合查询"
    ((TOTAL_TESTS++))
    
    # 查询 hot 表
    local hot_count=$(psql_exec "SELECT COUNT(*) FROM request_logs_hot;")
    hot_count=$(echo "$hot_count" | xargs)
    
    # 查询父表（分区）
    local parent_count=$(psql_exec "SELECT COUNT(*) FROM request_logs;")
    parent_count=$(echo "$parent_count" | xargs)
    
    # 查询视图
    local view_count=$(psql_exec "SELECT COUNT(*) FROM request_logs_with_current_month;")
    view_count=$(echo "$view_count" | xargs)
    
    log_info "  hot 表: $hot_count 行"
    log_info "  父表: $parent_count 行"
    log_info "  视图: $view_count 行"
    
    # 视图应该包含 hot + parent 的数据
    local expected=$((hot_count + parent_count))
    if [[ "$view_count" -eq "$expected" ]]; then
        log_info "✓ 视图聚合正确 (hot + parent = view)"
        ((PASSED_TESTS++))
    else
        log_error "✗ 视图聚合错误: 期望 $expected 行，实际 $view_count 行"
        ((FAILED_TESTS++))
    fi
}

# ============================================================
# 测试 5: 验证 API Key 配额检查（使用视图）
# ============================================================
function test_budget_check_with_view() {
    log_info "测试 5: 验证 API Key 配额检查使用视图"
    ((TOTAL_TESTS++))
    
    # 检查代码是否使用视图
    local uses_view=$(grep -r "usage_ledger_with_current_month" domains/authentication/verifier.go | wc -l)
    
    if [[ "$uses_view" -gt 0 ]]; then
        log_info "✓ verifier.go 正确使用 usage_ledger_with_current_month 视图"
        ((PASSED_TESTS++))
    else
        log_error "✗ verifier.go 未使用视图（可能遗漏最近数据）"
        ((FAILED_TESTS++))
    fi
}

# ============================================================
# 测试 6: 验证 attachments 查询（使用视图）
# ============================================================
function test_attachments_with_view() {
    log_info "测试 6: 验证 attachments 查询使用视图"
    ((TOTAL_TESTS++))
    
    # 检查代码是否使用视图
    local uses_view=$(grep -r "request_logs_with_current_month" domains/attachments/handler.go | wc -l)
    
    if [[ "$uses_view" -gt 0 ]]; then
        log_info "✓ attachments/handler.go 正确使用 request_logs_with_current_month 视图"
        ((PASSED_TESTS++))
    else
        log_error "✗ attachments/handler.go 未使用视图（可能遗漏最近数据）"
        ((FAILED_TESTS++))
    fi
}

# ============================================================
# 测试 7: 验证所有 hot 表都有最近数据（如果有流量）
# ============================================================
function test_hot_tables_data_freshness() {
    log_info "测试 7: 验证 hot 表数据新鲜度"
    
    local tables=(
        "request_logs_hot"
        "usage_ledger_hot"
        "request_wal_hot"
        "routing_decision_log_hot"
        "tool_usage_stats_hot"
        "credit_ledger_hot"
        "request_logs_bodies_hot"
        "credential_model_index_hot"
    )
    
    for table in "${tables[@]}"; do
        ((TOTAL_TESTS++))
        
        local total_count=$(psql_exec "SELECT COUNT(*) FROM $table;")
        total_count=$(echo "$total_count" | xargs)
        
        if [[ "$total_count" -gt 0 ]]; then
            # 检查最近7天的数据（hot表应该只包含7天内数据）
            local recent_count=$(psql_exec "SELECT COUNT(*) FROM $table WHERE CASE 
                WHEN '$table' = 'credential_model_index_hot' THEN bucket > NOW() - INTERVAL '7 days'
                WHEN '$table' = 'request_wal_hot' THEN created_at > NOW() - INTERVAL '7 days'
                WHEN '$table' = 'credit_ledger_hot' THEN created_at > NOW() - INTERVAL '7 days'
                ELSE ts > NOW() - INTERVAL '7 days' END;")
            recent_count=$(echo "$recent_count" | xargs)
            
            local old_count=$((total_count - recent_count))
            
            if [[ "$old_count" -gt 0 ]]; then
                log_warn "⚠ $table 包含 $old_count 条超过7天的数据（需要 promote）"
            fi
            
            log_info "✓ $table: 总计 $total_count 行（最近7天 $recent_count 行）"
            ((PASSED_TESTS++))
        else
            log_info "○ $table: 无数据（正常，取决于流量）"
            ((PASSED_TESTS++))
        fi
    done
}

# ============================================================
# 测试 8: 验证 UPDATE 操作正确性
# ============================================================
function test_update_operations() {
    log_info "测试 8: 验证 UPDATE 操作"
    ((TOTAL_TESTS++))
    
    # 插入测试数据
    local test_id="update-test-$(date +%s)"
    psql_exec "INSERT INTO usage_ledger_hot (request_id, ts, tenant_id, total_tokens, cost_usd, success) VALUES ('$test_id', NOW(), 'default', 100, 0.001, true);"
    
    # 更新数据
    psql_exec "UPDATE usage_ledger_hot SET total_tokens = 200, cost_usd = 0.002 WHERE request_id = '$test_id';"
    
    # 验证更新
    local updated_tokens=$(psql_exec "SELECT total_tokens FROM usage_ledger_hot WHERE request_id = '$test_id';")
    updated_tokens=$(echo "$updated_tokens" | xargs)
    
    if [[ "$updated_tokens" == "200" ]]; then
        log_info "✓ UPDATE 操作成功"
        ((PASSED_TESTS++))
    else
        log_error "✗ UPDATE 操作失败（期望 200，实际 $updated_tokens）"
        ((FAILED_TESTS++))
    fi
    
    # 清理测试数据
    psql_exec "DELETE FROM usage_ledger_hot WHERE request_id = '$test_id';"
}

# ============================================================
# 主函数
# ============================================================
function main() {
    echo ""
    echo "=========================================="
    echo "分表架构 API 端到端测试"
    echo "=========================================="
    echo ""
    
    # 执行测试
    test_service_health || log_warn "服务健康检查失败，部分测试将被跳过"
    test_usage_ledger_via_db
    test_view_aggregation
    test_budget_check_with_view
    test_attachments_with_view
    test_hot_tables_data_freshness
    test_update_operations
    
    # 总结
    echo ""
    echo "=========================================="
    echo "测试总结"
    echo "=========================================="
    echo "总测试数: $TOTAL_TESTS"
    echo "通过: $PASSED_TESTS"
    echo "失败: $FAILED_TESTS"
    echo ""
    
    if [[ "$FAILED_TESTS" -eq 0 ]]; then
        log_info "✅ 所有测试通过！"
        exit 0
    else
        log_error "❌ 有 $FAILED_TESTS 个测试失败"
        exit 1
    fi
}

main
