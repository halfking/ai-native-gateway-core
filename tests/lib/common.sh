#!/usr/bin/env bash
# E2E测试通用函数库

# 颜色定义
export RED='\033[0;31m'
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export NC='\033[0m'

# 测试统计
export TESTS_TOTAL=0
export TESTS_PASSED=0
export TESTS_FAILED=0
export TESTS_SKIPPED=0

# 打印函数
print_test_header() {
    echo ""
    echo "========================================="
    echo -e "${YELLOW}测试场景: $1${NC}"
    echo "========================================="
}

print_step() {
    echo ""
    echo -e "${YELLOW}▶ $1${NC}"
}

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_info() {
    echo "   $1"
}

# 记录测试结果
record_test_result() {
    local test_name="$1"
    local status="$2"
    local duration="$3"
    
    TESTS_TOTAL=$((TESTS_TOTAL + 1))
    
    case "$status" in
        "PASS")
            TESTS_PASSED=$((TESTS_PASSED + 1))
            echo -e "${GREEN}✅ PASS${NC} - $test_name (${duration}s)"
            ;;
        "FAIL")
            TESTS_FAILED=$((TESTS_FAILED + 1))
            echo -e "${RED}❌ FAIL${NC} - $test_name (${duration}s)"
            ;;
        "SKIP")
            TESTS_SKIPPED=$((TESTS_SKIPPED + 1))
            echo -e "${YELLOW}⏭️  SKIP${NC} - $test_name"
            ;;
    esac
}

# 执行测试用例
run_test_case() {
    local test_name="$1"
    local test_command="$2"
    
    print_step "执行: $test_name"
    
    local start_time=$(date +%s)
    
    if eval "$test_command" > /tmp/test_output_$$.log 2>&1; then
        local end_time=$(date +%s)
        local duration=$((end_time - start_time))
        record_test_result "$test_name" "PASS" "$duration"
        cat /tmp/test_output_$$.log | head -20
        rm -f /tmp/test_output_$$.log
        return 0
    else
        local end_time=$(date +%s)
        local duration=$((end_time - start_time))
        record_test_result "$test_name" "FAIL" "$duration"
        cat /tmp/test_output_$$.log | head -20
        rm -f /tmp/test_output_$$.log
        return 1
    fi
}

# 跳过测试
skip_test_case() {
    local test_name="$1"
    local reason="$2"
    print_info "跳过: $test_name ($reason)"
    record_test_result "$test_name" "SKIP" "0"
}

# 等待服务就绪
wait_for_service() {
    local url="$1"
    local max_retries="${2:-10}"
    local retry_interval="${3:-2}"
    
    print_step "等待服务: $url"
    
    for i in $(seq 1 $max_retries); do
        if curl -fsS "$url" > /dev/null 2>&1; then
            print_success "服务就绪"
            return 0
        fi
        
        if [ $i -eq $max_retries ]; then
            print_error "服务未就绪（超时）"
            return 1
        fi
        
        sleep $retry_interval
    done
}

# 数据库查询
db_query() {
    local query="$1"
    local db_host="${DB_HOST:-172.16.2.210}"
    local db_port="${DB_PORT:-5432}"
    local db_name="${DB_NAME:-maintain}"
    local db_user="${DB_USER:-llm_gateway}"
    local db_pass="${DB_PASS:-}"
    
    if [ -z "$db_pass" ]; then
        db_pass=$(bash ~/workspace/ai-native-tools/envs/loader.sh query COMMON_PG_SUPERUSER_PASS 2>/dev/null || echo "")
    fi
    
    if [ -z "$db_pass" ]; then
        return 1
    fi
    
    ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$db_pass' psql -h $db_host -p $db_port -U $db_user -d $db_name -t -c \"$query\"" 2>/dev/null
}

# 输出测试总结
print_test_summary() {
    echo ""
    echo "========================================="
    echo "测试总结"
    echo "========================================="
    echo "总测试数: $TESTS_TOTAL"
    echo -e "${GREEN}通过: $TESTS_PASSED${NC}"
    echo -e "${RED}失败: $TESTS_FAILED${NC}"
    echo -e "${YELLOW}跳过: $TESTS_SKIPPED${NC}"
    
    if [ $TESTS_FAILED -eq 0 ]; then
        echo ""
        echo -e "${GREEN}🎉 所有测试通过！${NC}"
        return 0
    else
        echo ""
        local success_rate=$((TESTS_PASSED * 100 / TESTS_TOTAL))
        echo -e "${RED}❌ 有 $TESTS_FAILED 个测试失败 (通过率: ${success_rate}%)${NC}"
        return 1
    fi
}

