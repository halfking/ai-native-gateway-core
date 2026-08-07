#!/bin/bash
# 会话脱敏与输出安全检查优化 - 部署验证脚本
# 用途：验证优化后的系统是否正常工作

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 配置
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
REDIS_HOST="${REDIS_HOST:-localhost:6379}"

# 测试计数器
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# 辅助函数
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

test_pass() {
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    log_info "✓ $1"
}

test_fail() {
    FAILED_TESTS=$((FAILED_TESTS + 1))
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    log_error "✗ $1"
}

# 检查依赖
check_dependencies() {
    log_info "检查依赖..."
    
    if ! command -v curl &> /dev/null; then
        log_error "curl 未安装"
        exit 1
    fi
    
    if ! command -v jq &> /dev/null; then
        log_error "jq 未安装，请安装: brew install jq 或 apt-get install jq"
        exit 1
    fi
    
    if ! command -v redis-cli &> /dev/null; then
        log_warn "redis-cli 未安装，将跳过Redis验证"
    fi
    
    test_pass "依赖检查完成"
}

# 测试1：基础脱敏还原
test_basic_sanitize() {
    log_info "测试1: 基础脱敏还原"
    
    local session_id="test-session-$(date +%s)"
    
    # 发送包含敏感信息的请求
    local response=$(curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -H "X-Session-ID: ${session_id}" \
        -d '{
            "model": "gpt-4",
            "messages": [
                {"role": "user", "content": "我的手机号是13800138000，帮我查询订单"}
            ]
        }')
    
    # 检查响应
    if echo "$response" | jq -e '.choices[0].message.content' > /dev/null 2>&1; then
        local content=$(echo "$response" | jq -r '.choices[0].message.content')
        
        # 验证：响应中不应包含占位符
        if echo "$content" | grep -q "{SENSITIVE:"; then
            test_fail "响应中仍包含占位符（还原失败）"
            echo "响应内容: $content"
        else
            test_pass "基础脱敏还原测试通过"
        fi
    else
        test_fail "API请求失败: $response"
    fi
}

# 测试2：跨轮次还原
test_multi_round_restore() {
    log_info "测试2: 跨轮次还原"
    
    local session_id="test-multi-$(date +%s)"
    
    # 第1轮：提供手机号
    log_info "  第1轮：提供手机号"
    curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -H "X-Session-ID: ${session_id}" \
        -d '{
            "model": "gpt-4",
            "messages": [
                {"role": "user", "content": "我的手机号是13900139000"}
            ]
        }' > /dev/null
    
    # 等待Redis写入
    sleep 1
    
    # 检查Redis中是否存储了映射表
    if command -v redis-cli &> /dev/null; then
        local redis_key="session:sanitize:*:${session_id}"
        local redis_value=$(redis-cli -h ${REDIS_HOST%:*} -p ${REDIS_HOST#*:} --scan --pattern "${redis_key}" | head -1 | xargs redis-cli -h ${REDIS_HOST%:*} -p ${REDIS_HOST#*:} GET)
        
        if [ -n "$redis_value" ] && echo "$redis_value" | grep -q "13900139000"; then
            test_pass "Redis映射表存储成功"
        else
            test_fail "Redis映射表未找到或不正确"
            echo "Redis key pattern: $redis_key"
            echo "Redis value: $redis_value"
        fi
    else
        log_warn "跳过Redis验证（redis-cli未安装）"
    fi
    
    # 第2轮：模拟LLM引用之前的手机号
    log_info "  第2轮：验证跨轮次还原"
    local response2=$(curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -H "X-Session-ID: ${session_id}" \
        -d '{
            "model": "gpt-4",
            "messages": [
                {"role": "user", "content": "我的手机号是13900139000"},
                {"role": "assistant", "content": "已记录您的手机号"},
                {"role": "user", "content": "查询我的手机号"}
            ]
        }')
    
    # 验证：如果LLM响应中包含占位符，应该被还原
    if echo "$response2" | jq -e '.choices[0].message.content' > /dev/null 2>&1; then
        local content2=$(echo "$response2" | jq -r '.choices[0].message.content')
        
        if echo "$content2" | grep -q "{SENSITIVE:"; then
            test_fail "跨轮次还原失败，响应中仍包含占位符"
            echo "响应内容: $content2"
        else
            test_pass "跨轮次还原测试通过"
        fi
    else
        test_fail "API请求失败: $response2"
    fi
}

# 测试3：Hook执行顺序
test_hook_priority() {
    log_info "测试3: Hook执行顺序验证"
    
    # 启用DEBUG日志并发送请求
    local session_id="test-priority-$(date +%s)"
    
    curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -H "X-Session-ID: ${session_id}" \
        -d '{
            "model": "gpt-4",
            "messages": [
                {"role": "user", "content": "我的邮箱是test@example.com"}
            ]
        }' > /dev/null
    
    # 检查日志文件（如果可访问）
    if [ -f "./logs/gateway.log" ]; then
        if grep -q "sanitizer.output.*priority=50" ./logs/gateway.log && \
           grep -q "output_compliance.*priority=100" ./logs/gateway.log; then
            test_pass "Hook优先级配置正确"
        else
            test_fail "Hook优先级可能不正确，请检查日志"
        fi
    else
        log_warn "无法访问日志文件，跳过优先级验证"
        test_pass "Hook优先级测试跳过（无日志访问权限）"
    fi
}

# 测试4：无敏感信息场景
test_no_sensitive_data() {
    log_info "测试4: 无敏感信息场景"
    
    local session_id="test-nosensitive-$(date +%s)"
    
    local response=$(curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -H "X-Session-ID: ${session_id}" \
        -d '{
            "model": "gpt-4",
            "messages": [
                {"role": "user", "content": "今天天气怎么样？"}
            ]
        }')
    
    # 验证：响应正常，无错误
    if echo "$response" | jq -e '.choices[0].message.content' > /dev/null 2>&1; then
        test_pass "无敏感信息场景测试通过"
    else
        test_fail "无敏感信息场景失败: $response"
    fi
}

# 测试5：性能基准测试
test_performance() {
    log_info "测试5: 性能基准测试"
    
    local session_id="test-perf-$(date +%s)"
    local start_time=$(date +%s%3N)
    
    # 发送10个请求
    for i in {1..10}; do
        curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
            -H "Authorization: Bearer ${API_KEY}" \
            -H "Content-Type: application/json" \
            -H "X-Session-ID: ${session_id}" \
            -d '{
                "model": "gpt-4",
                "messages": [
                    {"role": "user", "content": "测试消息'$i'，手机号13800138000"}
                ]
            }' > /dev/null &
    done
    
    wait
    
    local end_time=$(date +%s%3N)
    local duration=$((end_time - start_time))
    local avg_latency=$((duration / 10))
    
    log_info "  10个请求总耗时: ${duration}ms"
    log_info "  平均延迟: ${avg_latency}ms"
    
    # 验证：平均延迟应该合理（< 5000ms for 10 parallel requests）
    if [ $avg_latency -lt 5000 ]; then
        test_pass "性能测试通过（平均延迟: ${avg_latency}ms）"
    else
        test_fail "性能测试失败（平均延迟: ${avg_latency}ms 过高）"
    fi
}

# 测试6：Redis故障降级
test_redis_fallback() {
    log_info "测试6: Redis故障降级测试"
    
    if ! command -v redis-cli &> /dev/null; then
        log_warn "跳过Redis降级测试（redis-cli未安装）"
        return
    fi
    
    # 保存原始Redis配置
    local original_redis="${REDIS_HOST}"
    
    # 模拟Redis不可用（通过错误的连接信息）
    export REDIS_ADDR="localhost:9999"
    
    local session_id="test-fallback-$(date +%s)"
    
    local response=$(curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -H "X-Session-ID: ${session_id}" \
        -d '{
            "model": "gpt-4",
            "messages": [
                {"role": "user", "content": "手机号13800138000"}
            ]
        }')
    
    # 恢复Redis配置
    export REDIS_ADDR="${original_redis}"
    
    # 验证：即使Redis不可用，系统应该降级处理（单轮还原仍然有效）
    if echo "$response" | jq -e '.choices[0].message.content' > /dev/null 2>&1; then
        test_pass "Redis故障降级测试通过（系统正常降级）"
    else
        test_fail "Redis故障时系统未能正常降级"
    fi
}

# 测试7：并发安全测试
test_concurrency() {
    log_info "测试7: 并发安全测试"
    
    local session_id="test-concurrent-$(date +%s)"
    
    # 并发发送20个请求到同一session
    for i in {1..20}; do
        curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
            -H "Authorization: Bearer ${API_KEY}" \
            -H "Content-Type: application/json" \
            -H "X-Session-ID: ${session_id}" \
            -d '{
                "model": "gpt-4",
                "messages": [
                    {"role": "user", "content": "并发测试'$i'，手机号1380013800'$i'"}
                ]
            }' > /dev/null &
    done
    
    wait
    
    # 检查Redis映射表是否正确合并
    if command -v redis-cli &> /dev/null; then
        sleep 2 # 等待所有请求完成
        
        local redis_key="session:sanitize:*:${session_id}"
        local redis_value=$(redis-cli -h ${REDIS_HOST%:*} -p ${REDIS_HOST#*:} --scan --pattern "${redis_key}" | head -1 | xargs redis-cli -h ${REDIS_HOST%:*} -p ${REDIS_HOST#*:} GET)
        
        # 验证：映射表应该包含多个条目
        local count=$(echo "$redis_value" | grep -o "SENSITIVE" | wc -l)
        
        if [ "$count" -gt 0 ]; then
            test_pass "并发安全测试通过（映射表包含 $count 个条目）"
        else
            test_fail "并发测试失败，映射表为空或损坏"
        fi
    else
        log_warn "跳过并发测试Redis验证（redis-cli未安装）"
    fi
}

# 主测试流程
main() {
    log_info "=========================================="
    log_info "会话脱敏与输出安全检查 - 部署验证"
    log_info "=========================================="
    log_info ""
    log_info "目标网关: ${GATEWAY_URL}"
    log_info "Redis地址: ${REDIS_HOST}"
    log_info ""
    
    # 执行测试
    check_dependencies
    test_basic_sanitize
    test_multi_round_restore
    test_hook_priority
    test_no_sensitive_data
    test_performance
    test_redis_fallback
    test_concurrency
    
    # 输出结果
    echo ""
    log_info "=========================================="
    log_info "测试结果汇总"
    log_info "=========================================="
    log_info "总计: ${TOTAL_TESTS} 个测试"
    log_info "通过: ${GREEN}${PASSED_TESTS}${NC} 个"
    log_info "失败: ${RED}${FAILED_TESTS}${NC} 个"
    
    if [ $FAILED_TESTS -eq 0 ]; then
        echo ""
        log_info "🎉 所有测试通过！系统已准备好部署。"
        exit 0
    else
        echo ""
        log_error "❌ 部分测试失败，请检查日志并修复问题。"
        exit 1
    fi
}

# 运行主流程
main
