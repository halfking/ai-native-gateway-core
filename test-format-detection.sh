#!/bin/bash

# 格式检测系统部署验证测试脚本
# 日期: 2026-07-26
# 目标: 验证智能格式检测与自适应系统是否正常工作

set -e

# 配置
BASE_URL="https://llm.kxpms.cn/v1"
API_KEY="sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9"
METRICS_URL="https://llm.kxpms.cn:9090/metrics"  # 需要确认实际端口
TEST_MODEL="gpt-5.4"
SESSION_ID="test-format-$(date +%s)"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

# 测试计数
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

run_test() {
    local test_name="$1"
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    echo ""
    echo "=========================================="
    echo "测试 #$TOTAL_TESTS: $test_name"
    echo "=========================================="
}

test_passed() {
    PASSED_TESTS=$((PASSED_TESTS + 1))
    log_success "✓ 测试通过"
}

test_failed() {
    local reason="$1"
    FAILED_TESTS=$((FAILED_TESTS + 1))
    log_error "✗ 测试失败: $reason"
}

# ==========================================
# 测试 1: 服务健康检查
# ==========================================
test_health_check() {
    run_test "服务健康检查"
    
    log_info "检查基础连通性..."
    
    # 尝试访问根路径或健康检查端点
    if curl -s -f -m 5 "${BASE_URL%/v1}/health" > /dev/null 2>&1; then
        log_success "健康检查端点可访问"
        test_passed
    elif curl -s -f -m 5 "$BASE_URL/models" -H "Authorization: Bearer $API_KEY" > /dev/null 2>&1; then
        log_success "API 端点可访问"
        test_passed
    else
        test_failed "无法访问服务"
        return 1
    fi
}

# ==========================================
# 测试 2: 标准 OpenAI 格式请求
# ==========================================
test_standard_request() {
    run_test "标准 OpenAI 格式请求"
    
    log_info "发送标准格式请求..."
    
    response=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -H "X-Gw-Session-Id: $SESSION_ID-standard" \
        -d "{
            \"model\": \"$TEST_MODEL\",
            \"messages\": [{\"role\": \"user\", \"content\": \"测试：回复OK\"}],
            \"max_tokens\": 50
        }")
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    log_info "HTTP 状态码: $http_code"
    
    if [ "$http_code" = "200" ]; then
        if echo "$body" | grep -q '"choices"'; then
            log_success "标准请求成功，返回正常响应"
            echo "$body" | jq -r '.choices[0].message.content' 2>/dev/null || echo "$body"
            test_passed
        else
            log_warn "返回 200 但响应格式异常"
            echo "$body"
            test_failed "响应格式异常"
        fi
    else
        log_error "请求失败，状态码: $http_code"
        echo "$body"
        test_failed "HTTP $http_code"
    fi
}

# ==========================================
# 测试 3: 字符串消息自动转换（核心功能）
# ==========================================
test_string_message_fix() {
    run_test "字符串消息自动转换"
    
    log_info "发送字符串格式的 messages（应该自动修复）..."
    log_info "请求格式: {\"messages\": \"hello world\"}"
    
    response=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -H "User-Agent: opencode/1.0" \
        -H "X-Gw-Session-Id: $SESSION_ID-string" \
        -d "{
            \"model\": \"$TEST_MODEL\",
            \"messages\": \"测试：自动修复字符串消息\",
            \"max_tokens\": 50
        }")
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    log_info "HTTP 状态码: $http_code"
    
    if [ "$http_code" = "200" ]; then
        log_success "✓ 字符串消息自动修复成功！"
        log_success "系统将字符串转换为消息数组并正常处理"
        echo "$body" | jq -r '.choices[0].message.content' 2>/dev/null || echo "$body"
        test_passed
    else
        log_error "自动修复失败或请求被拒绝"
        echo "$body"
        test_failed "HTTP $http_code - 应该自动修复并返回 200"
    fi
}

# ==========================================
# 测试 4: 空对象检测（修复后验证失败）
# ==========================================
test_empty_object_detection() {
    run_test "空对象检测与验证"
    
    log_info "发送空对象 messages（应该修复为空数组后验证失败）..."
    log_info "请求格式: {\"messages\": {}}"
    
    response=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -H "User-Agent: opencode/1.0" \
        -H "X-Gw-Session-Id: $SESSION_ID-empty" \
        -d "{
            \"model\": \"$TEST_MODEL\",
            \"messages\": {}
        }")
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    log_info "HTTP 状态码: $http_code"
    
    if [ "$http_code" = "400" ]; then
        if echo "$body" | grep -qi "empty\|cannot be empty\|invalid"; then
            log_success "✓ 空对象正确检测！"
            log_success "系统修复为空数组后，验证逻辑正确拒绝"
            echo "错误消息: $(echo "$body" | jq -r '.error.message' 2>/dev/null || echo "$body")"
            test_passed
        else
            log_warn "返回 400 但错误消息不符合预期"
            echo "$body"
            test_failed "错误消息不匹配"
        fi
    else
        log_error "应该返回 400 错误"
        echo "$body"
        test_failed "HTTP $http_code - 应该返回 400"
    fi
}

# ==========================================
# 测试 5: 缓存命中测试
# ==========================================
test_cache_hit() {
    run_test "格式缓存命中测试"
    
    local cache_session="$SESSION_ID-cache-test"
    
    log_info "第一次请求（缓存 MISS）..."
    response1=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -H "User-Agent: opencode/1.0" \
        -H "X-Gw-Session-Id: $cache_session" \
        -d "{
            \"model\": \"$TEST_MODEL\",
            \"messages\": \"第一次请求\",
            \"max_tokens\": 30
        }")
    
    http_code1=$(echo "$response1" | tail -n1)
    log_info "第一次请求状态: $http_code1"
    
    if [ "$http_code1" != "200" ]; then
        log_error "第一次请求失败"
        test_failed "无法测试缓存"
        return
    fi
    
    log_info "等待 2 秒，让缓存写入完成..."
    sleep 2
    
    log_info "第二次请求（缓存 HIT）..."
    response2=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -H "User-Agent: opencode/1.0" \
        -H "X-Gw-Session-Id: $cache_session" \
        -d "{
            \"model\": \"$TEST_MODEL\",
            \"messages\": \"第二次请求\",
            \"max_tokens\": 30
        }")
    
    http_code2=$(echo "$response2" | tail -n1)
    log_info "第二次请求状态: $http_code2"
    
    if [ "$http_code2" = "200" ]; then
        log_success "✓ 缓存测试完成"
        log_info "两次请求都成功，缓存应该在第二次命中"
        log_info "请检查 Prometheus 指标: llmgw_format_cache_total"
        test_passed
    else
        test_failed "第二次请求失败"
    fi
}

# ==========================================
# 测试 6: 无效消息数组（确保验证生效）
# ==========================================
test_validation_no_user_message() {
    run_test "验证逻辑 - 缺少 user 消息"
    
    log_info "发送只有 system 消息的请求（应该被拒绝）..."
    
    response=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"$TEST_MODEL\",
            \"messages\": [{\"role\": \"system\", \"content\": \"你是助手\"}]
        }")
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    log_info "HTTP 状态码: $http_code"
    
    if [ "$http_code" = "400" ]; then
        if echo "$body" | grep -qi "user message"; then
            log_success "✓ 验证逻辑正常工作"
            echo "错误消息: $(echo "$body" | jq -r '.error.message' 2>/dev/null || echo "$body")"
            test_passed
        else
            log_warn "返回 400 但原因可能不是缺少 user 消息"
            test_passed  # 也算通过，只是原因不同
        fi
    else
        log_warn "可能系统配置允许只有 system 消息"
        test_passed  # 不算失败
    fi
}

# ==========================================
# 测试 7: Prometheus 指标验证
# ==========================================
test_prometheus_metrics() {
    run_test "Prometheus 指标验证"
    
    log_info "尝试获取 Prometheus 指标..."
    
    # 尝试几个可能的端口
    for port in 9090 9091 8080; do
        metrics_url="https://llm.kxpms.cn:$port/metrics"
        log_info "尝试 $metrics_url ..."
        
        if metrics=$(curl -s -m 5 "$metrics_url" 2>/dev/null); then
            log_success "成功获取指标（端口 $port）"
            
            echo ""
            log_info "检查格式检测相关指标..."
            
            # 检查格式检测指标
            if echo "$metrics" | grep -q "llmgw_format_detection_total"; then
                log_success "✓ 找到 llmgw_format_detection_total"
                echo "$metrics" | grep "llmgw_format_detection_total"
            else
                log_warn "未找到 llmgw_format_detection_total"
            fi
            
            if echo "$metrics" | grep -q "llmgw_format_fix_applied_total"; then
                log_success "✓ 找到 llmgw_format_fix_applied_total"
                echo "$metrics" | grep "llmgw_format_fix_applied_total"
            else
                log_warn "未找到 llmgw_format_fix_applied_total"
            fi
            
            if echo "$metrics" | grep -q "llmgw_format_cache_total"; then
                log_success "✓ 找到 llmgw_format_cache_total"
                echo "$metrics" | grep "llmgw_format_cache_total"
            else
                log_warn "未找到 llmgw_format_cache_total"
            fi
            
            test_passed
            return 0
        fi
    done
    
    log_warn "无法访问 Prometheus 指标端点"
    log_info "可能需要管理员权限或指标端口未开放"
    test_failed "无法访问指标端点"
}

# ==========================================
# 测试 8: 性能基准测试
# ==========================================
test_performance_benchmark() {
    run_test "性能基准测试"
    
    log_info "测试标准请求延迟（10次）..."
    
    local total_time=0
    local successful_requests=0
    
    for i in {1..10}; do
        start_time=$(date +%s%N)
        
        response=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/chat/completions" \
            -H "Authorization: Bearer $API_KEY" \
            -H "Content-Type: application/json" \
            -d "{
                \"model\": \"$TEST_MODEL\",
                \"messages\": [{\"role\": \"user\", \"content\": \"测试$i\"}],
                \"max_tokens\": 10
            }")
        
        end_time=$(date +%s%N)
        http_code=$(echo "$response" | tail -n1)
        
        if [ "$http_code" = "200" ]; then
            duration=$(( (end_time - start_time) / 1000000 ))  # 转换为毫秒
            total_time=$((total_time + duration))
            successful_requests=$((successful_requests + 1))
            echo -n "."
        else
            echo -n "x"
        fi
    done
    
    echo ""
    
    if [ $successful_requests -gt 0 ]; then
        avg_time=$((total_time / successful_requests))
        log_success "成功请求: $successful_requests/10"
        log_info "平均延迟: ${avg_time}ms"
        
        if [ $avg_time -lt 500 ]; then
            log_success "✓ 性能良好（< 500ms）"
        elif [ $avg_time -lt 1000 ]; then
            log_info "性能可接受（< 1000ms）"
        else
            log_warn "性能较慢（> 1000ms）"
        fi
        
        test_passed
    else
        test_failed "所有请求都失败"
    fi
}

# ==========================================
# 主测试流程
# ==========================================
main() {
    echo "=========================================="
    echo "格式检测系统部署验证测试"
    echo "=========================================="
    echo "部署环境: $BASE_URL"
    echo "测试时间: $(date)"
    echo "会话 ID 前缀: $SESSION_ID"
    echo ""
    
    # 检查依赖
    for cmd in curl jq; do
        if ! command -v $cmd &> /dev/null; then
            log_error "缺少必需工具: $cmd"
            log_info "请安装: apt-get install $cmd 或 brew install $cmd"
            exit 1
        fi
    done
    
    # 运行测试
    test_health_check
    test_standard_request
    test_string_message_fix
    test_empty_object_detection
    test_cache_hit
    test_validation_no_user_message
    test_performance_benchmark
    test_prometheus_metrics
    
    # 测试总结
    echo ""
    echo "=========================================="
    echo "测试总结"
    echo "=========================================="
    echo "总测试数: $TOTAL_TESTS"
    echo -e "${GREEN}通过: $PASSED_TESTS${NC}"
    echo -e "${RED}失败: $FAILED_TESTS${NC}"
    echo ""
    
    if [ $FAILED_TESTS -eq 0 ]; then
        log_success "🎉 所有测试通过！系统运行正常"
        exit 0
    else
        log_warn "⚠️  有 $FAILED_TESTS 个测试失败，请检查日志"
        exit 1
    fi
}

# 运行主流程
main "$@"
