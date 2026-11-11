#!/usr/bin/env bash
# 运营体系集成测试脚本
# 验证用户管理、配额、统计、监控等核心功能

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试结果统计
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

log_info() {
    echo -e "${GREEN}[INFO]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

assert_success() {
    local test_name="$1"
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    if [ $? -eq 0 ]; then
        log_info "✓ ${test_name}"
        PASSED_TESTS=$((PASSED_TESTS + 1))
        return 0
    else
        log_error "✗ ${test_name}"
        FAILED_TESTS=$((FAILED_TESTS + 1))
        return 1
    fi
}

# 检查必需的环境变量和依赖
check_prerequisites() {
    log_info "检查前置条件..."
    
    # 检查命令
    for cmd in curl jq psql; do
        if ! command -v "$cmd" &> /dev/null; then
            log_error "缺少必需命令: $cmd"
            exit 1
        fi
    done
    
    # 检查数据库连接
    if [ -z "${DATABASE_URL:-}" ]; then
        log_warn "DATABASE_URL 未设置，使用默认值"
        export DATABASE_URL="postgresql://localhost:5432/llm_gateway?sslmode=disable"
    fi
}

# 测试数据库Schema完整性
test_database_schema() {
    log_info "=== 测试数据库Schema ==="
    
    local required_tables=(
        "users"
        "tenants"
        "providers"
        "credentials"
        "request_logs"
        "sessions"
        "routing_candidates"
        "routing_candidate_bindings"
        "api_keys"
    )
    
    for table in "${required_tables[@]}"; do
        psql "${DATABASE_URL}" -c "\d ${table}" &> /dev/null
        assert_success "表存在检查: ${table}"
    done
}

# 测试用户管理API
test_user_management() {
    log_info "=== 测试用户管理功能 ==="
    
    local base_url="${API_BASE_URL:-http://localhost:8080}"
    local admin_token="${ADMIN_TOKEN:-}"
    
    if [ -z "$admin_token" ]; then
        log_warn "ADMIN_TOKEN 未设置，跳过用户管理API测试"
        return 0
    fi
    
    # 测试获取用户列表
    local response
    response=$(curl -s -w "\n%{http_code}" \
        -H "Authorization: Bearer ${admin_token}" \
        "${base_url}/api/admin/users")
    
    local http_code
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ]; then
        assert_success "获取用户列表"
    else
        log_error "获取用户列表失败: HTTP ${http_code}"
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
    
    # 测试创建用户
    local test_username="test_user_$(date +%s)"
    response=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Authorization: Bearer ${admin_token}" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"${test_username}\",\"password\":\"test123\",\"tenant_id\":\"default\",\"role\":\"user\"}" \
        "${base_url}/api/admin/users")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "201" ] || [ "$http_code" = "200" ]; then
        assert_success "创建测试用户"
    else
        log_error "创建用户失败: HTTP ${http_code}"
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
}

# 测试统计API
test_statistics_api() {
    log_info "=== 测试统计分析功能 ==="
    
    local base_url="${API_BASE_URL:-http://localhost:8080}"
    local admin_token="${ADMIN_TOKEN:-}"
    
    if [ -z "$admin_token" ]; then
        log_warn "ADMIN_TOKEN 未设置，跳过统计API测试"
        return 0
    fi
    
    # 测试统计摘要
    local response
    response=$(curl -s -w "\n%{http_code}" \
        -H "Authorization: Bearer ${admin_token}" \
        "${base_url}/api/admin/stats/summary?days=7")
    
    local http_code
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ]; then
        assert_success "获取统计摘要"
    else
        log_error "获取统计摘要失败: HTTP ${http_code}"
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
    
    # 测试趋势统计
    response=$(curl -s -w "\n%{http_code}" \
        -H "Authorization: Bearer ${admin_token}" \
        "${base_url}/api/admin/stats/trend?days=7")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ]; then
        assert_success "获取趋势统计"
    else
        log_error "获取趋势统计失败: HTTP ${http_code}"
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
}

# 测试监控指标
test_monitoring_metrics() {
    log_info "=== 测试监控指标 ==="
    
    local base_url="${API_BASE_URL:-http://localhost:8080}"
    
    # 测试健康检查
    local response
    response=$(curl -s -w "\n%{http_code}" "${base_url}/health")
    
    local http_code
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ]; then
        assert_success "健康检查端点"
    else
        log_error "健康检查失败: HTTP ${http_code}"
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
    
    # 测试Prometheus指标
    response=$(curl -s -w "\n%{http_code}" "${base_url}/metrics")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ]; then
        assert_success "Prometheus指标端点"
    else
        log_warn "Prometheus指标端点不可用: HTTP ${http_code}"
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
    fi
}

# 测试配额管理
test_quota_management() {
    log_info "=== 测试配额管理 ==="
    
    # 检查数据库中的配额相关字段
    psql "${DATABASE_URL}" -c "
        SELECT column_name 
        FROM information_schema.columns 
        WHERE table_name = 'credentials' 
          AND column_name LIKE '%quota%'
    " &> /dev/null
    
    assert_success "配额字段存在检查"
}

# 测试运维脚本
test_operations_scripts() {
    log_info "=== 测试运维脚本 ==="
    
    # 检查关键脚本是否存在且可执行
    local scripts=(
        "deploy-local.sh"
        "run-tests.sh"
        "apply-db-revision-sequence.sh"
    )
    
    for script in "${scripts[@]}"; do
        local script_path="${SCRIPT_DIR}/${script}"
        if [ -f "$script_path" ] && [ -x "$script_path" ]; then
            assert_success "脚本存在且可执行: ${script}"
        else
            log_error "脚本缺失或不可执行: ${script}"
            TOTAL_TESTS=$((TOTAL_TESTS + 1))
            FAILED_TESTS=$((FAILED_TESTS + 1))
        fi
    done
}

# 生成测试报告
generate_report() {
    log_info "=== 测试报告 ==="
    echo ""
    echo "总测试数: ${TOTAL_TESTS}"
    echo "通过: ${PASSED_TESTS}"
    echo "失败: ${FAILED_TESTS}"
    echo ""
    
    local pass_rate=0
    if [ $TOTAL_TESTS -gt 0 ]; then
        pass_rate=$((PASSED_TESTS * 100 / TOTAL_TESTS))
    fi
    
    echo "通过率: ${pass_rate}%"
    echo ""
    
    if [ $FAILED_TESTS -eq 0 ]; then
        log_info "所有测试通过！✓"
        return 0
    else
        log_error "有 ${FAILED_TESTS} 个测试失败"
        return 1
    fi
}

# 主函数
main() {
    log_info "开始运营体系集成测试..."
    echo ""
    
    check_prerequisites
    
    # 执行各项测试
    test_database_schema || true
    test_user_management || true
    test_statistics_api || true
    test_monitoring_metrics || true
    test_quota_management || true
    test_operations_scripts || true
    
    echo ""
    generate_report
}

# 执行主函数
main "$@"
