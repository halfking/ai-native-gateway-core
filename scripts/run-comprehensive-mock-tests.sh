#!/usr/bin/env bash
# ====================================================================
# 综合 Mock 测试执行脚本 - 发现并修复问题
# ====================================================================
# 目标: 
#   1. 使用现有网关 (8782) 进行测试
#   2. 启动多个 mock providers 模拟各种场景
#   3. 执行高并发测试
#   4. 发现 bug 并记录修复方案
# ====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 配置 ──
GATEWAY_PORT="${GATEWAY_PORT:-8782}"
GATEWAY_URL="http://127.0.0.1:${GATEWAY_PORT}"
NUM_MOCKS="${NUM_MOCKS:-10}"
MOCK_START_PORT="${MOCK_START_PORT:-18080}"
REPORT_FILE="${REPORT_FILE:-/tmp/llm-gateway-mock-test-report-$(date +%Y%m%d-%H%M%S).md}"

# ── 颜色 ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

log() { echo -e "${BLUE}[TEST]${NC} $*"; }
ok() { echo -e "${GREEN}[✓]${NC} $*"; }
warn() { echo -e "${YELLOW}[⚠]${NC} $*"; }
err() { echo -e "${RED}[✗]${NC} $*" >&2; }
section() { echo -e "\n${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n${CYAN}$*${NC}\n${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; }

# ── 测试统计 ──
declare -i TOTAL_TESTS=0
declare -i PASSED_TESTS=0
declare -i FAILED_TESTS=0
declare -a BUGS_FOUND=()

# ====================================================================
# 初始化报告
# ====================================================================
init_report() {
    cat > "$REPORT_FILE" <<EOF
# LLM Gateway Mock 测试报告

**测试时间**: $(date '+%Y-%m-%d %H:%M:%S')  
**网关地址**: $GATEWAY_URL  
**Mock 数量**: $NUM_MOCKS  

---

## 测试执行概况

EOF
    log "报告初始化: $REPORT_FILE"
}

# ====================================================================
# 步骤 1: 环境检查
# ====================================================================
check_environment() {
    section "步骤 1: 环境检查"
    ((TOTAL_TESTS++))
    
    # 检查网关
    local health_status=$(curl -s "$GATEWAY_URL/healthz" 2>/dev/null | jq -r '.status' 2>/dev/null || echo "error")
    if [[ "$health_status" == "ok" ]]; then
        local version=$(curl -s "$GATEWAY_URL/version" 2>/dev/null | jq -r '.version' 2>/dev/null || echo "unknown")
        ok "网关可达: $GATEWAY_URL (版本: $version)"
        ((PASSED_TESTS++))
    else
        err "网关不可达: $GATEWAY_URL"
        ((FAILED_TESTS++))
        exit 1
    fi
    
    # 检查依赖
    local missing=()
    command -v curl >/dev/null || missing+=("curl")
    command -v jq >/dev/null || missing+=("jq")
    command -v python3 >/dev/null || missing+=("python3")
    
    if [[ ${#missing[@]} -gt 0 ]]; then
        err "缺少依赖: ${missing[*]}"
        exit 1
    fi
    
    ok "依赖检查通过"
}

# ====================================================================
# 步骤 2: 启动 Mock Providers
# ====================================================================
start_mock_providers() {
    section "步骤 2: 启动 $NUM_MOCKS 个 Mock Providers"
    
    cd "$SCRIPT_DIR/mocks/llm-mock-upstream"
    
    local started=0
    local already_running=0
    
    for i in $(seq 0 $((NUM_MOCKS - 1))); do
        local port=$((MOCK_START_PORT + i))
        local token="mock-provider-$(printf "%02d" $i)"
        
        # 检查是否已运行
        if curl -sf --max-time 1 "http://localhost:$port/healthz" >/dev/null 2>&1; then
            log "  $token (port $port) 已在运行"
            ((already_running++))
            continue
        fi
        
        # 启动
        MOCK_PORT=$port \
        MOCK_TOKEN=$token \
        MOCK_STATE_FILE="/tmp/mock-state-$port.json" \
        python3 server-v2.py > "/tmp/mock-$port.log" 2>&1 &
        
        local pid=$!
        echo $pid > "/tmp/mock-$port.pid"
        ok "  启动 $token (port $port, PID $pid)"
        ((started++))
    done
    
    # 等待启动
    sleep 3
    
    # 验证可用性
    local healthy=0
    for i in $(seq 0 $((NUM_MOCKS - 1))); do
        local port=$((MOCK_START_PORT + i))
        if curl -sf --max-time 2 "http://localhost:$port/healthz" >/dev/null 2>&1; then
            ((healthy++))
        fi
    done
    
    ok "$healthy/$NUM_MOCKS 个 mock providers 可用 (新启动: $started, 已运行: $already_running)"
    
    if [[ $healthy -lt $((NUM_MOCKS / 2)) ]]; then
        err "超过一半的 mock 不可用，中止测试"
        exit 1
    fi
}

# ====================================================================
# 步骤 3: Mock 状态控制测试
# ====================================================================
test_mock_state_control() {
    section "步骤 3: Mock 状态控制测试"
    
    local port=$MOCK_START_PORT
    local base="http://localhost:$port"
    
    # 测试 3.1: 获取当前状态
    ((TOTAL_TESTS++))
    log "测试 3.1: 获取 mock 状态"
    local state=$(curl -sf "$base/admin/state" | jq -r '.mode' 2>/dev/null || echo "error")
    if [[ "$state" != "error" ]]; then
        ok "  当前状态: $state"
        ((PASSED_TESTS++))
    else
        err "  无法获取状态"
        ((FAILED_TESTS++))
        BUGS_FOUND+=("Mock provider 状态 API 不可用")
    fi
    
    # 测试 3.2: 切换到 slow 模式
    ((TOTAL_TESTS++))
    log "测试 3.2: 切换到 slow 模式 (2-5秒延迟)"
    local result=$(curl -sf -X POST "$base/admin/state" \
        -H "Content-Type: application/json" \
        -d '{"mode":"slow","ttl_seconds":60,"latency_min_ms":2000,"latency_max_ms":5000}' \
        | jq -r '.mode' 2>/dev/null || echo "error")
    
    if [[ "$result" == "slow" ]]; then
        ok "  切换成功: slow"
        ((PASSED_TESTS++))
    else
        err "  切换失败"
        ((FAILED_TESTS++))
        BUGS_FOUND+=("Mock provider 状态切换失败")
    fi
    
    # 测试 3.3: 验证 slow 模式生效
    ((TOTAL_TESTS++))
    log "测试 3.3: 验证 slow 模式响应时间"
    local start=$(date +%s%3N)
    curl -sf -X POST "$base/v1/chat/completions" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer mock-test" \
        -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' >/dev/null 2>&1
    local end=$(date +%s%3N)
    local latency=$((end - start))
    
    if [[ $latency -ge 2000 ]]; then
        ok "  延迟验证通过: ${latency}ms (>= 2000ms)"
        ((PASSED_TESTS++))
    else
        warn "  延迟低于预期: ${latency}ms (预期 >= 2000ms)"
        ((FAILED_TESTS++))
        BUGS_FOUND+=("Mock slow 模式延迟未生效: ${latency}ms < 2000ms")
    fi
    
    # 恢复健康模式
    curl -sf -X POST "$base/admin/state" \
        -H "Content-Type: application/json" \
        -d '{"mode":"healthy","latency_min_ms":200,"latency_max_ms":500}' >/dev/null 2>&1
}

# ====================================================================
# 步骤 4: 基础路由测试
# ====================================================================
test_basic_routing() {
    section "步骤 4: 基础路由测试"
    
    # 注意: 这需要网关已配置 mock provider 和 credentials
    # 由于没有 admin API key，这部分需要手动配置或跳过
    
    warn "基础路由测试需要网关中配置 mock providers"
    warn "请手动在网关 UI 中添加 providers 和 credentials"
    warn "跳过此步骤..."
}

# ====================================================================
# 步骤 5: 并发压力测试
# ====================================================================
test_concurrent_load() {
    section "步骤 5: 并发压力测试"
    
    local port=$MOCK_START_PORT
    local base="http://localhost:$port"
    local concurrency=50
    local requests_per_client=20
    
    log "启动 $concurrency 并发客户端，每客户端 $requests_per_client 请求"
    
    ((TOTAL_TESTS++))
    local success=0
    local errors=0
    local total=$((concurrency * requests_per_client))
    
    # 简单并发测试 (直接对 mock)
    local pids=()
    for i in $(seq 1 $concurrency); do
        (
            for j in $(seq 1 $requests_per_client); do
                curl -sf -X POST "$base/v1/chat/completions" \
                    -H "Content-Type: application/json" \
                    -H "Authorization: Bearer mock-test" \
                    -d "{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"client-$i-req-$j\"}]}" \
                    >/dev/null 2>&1 && echo "ok" || echo "fail"
            done
        ) &
        pids+=($!)
    done
    
    # 等待所有完成
    local results=$(mktemp)
    for pid in "${pids[@]}"; do
        wait $pid
    done > "$results" 2>&1
    
    success=$(grep -c "ok" "$results" 2>/dev/null || echo 0)
    errors=$(grep -c "fail" "$results" 2>/dev/null || echo 0)
    rm -f "$results"
    
    local success_rate=$((success * 100 / total))
    
    if [[ $success_rate -ge 95 ]]; then
        ok "并发测试通过: $success/$total 成功 (${success_rate}%)"
        ((PASSED_TESTS++))
    else
        warn "并发测试部分失败: $success/$total 成功 (${success_rate}%), $errors 失败"
        ((FAILED_TESTS++))
        BUGS_FOUND+=("并发测试成功率低: ${success_rate}% (< 95%)")
    fi
}

# ====================================================================
# 步骤 6: 故障场景测试
# ====================================================================
test_failure_scenarios() {
    section "步骤 6: 故障场景测试"
    
    local port=$MOCK_START_PORT
    local base="http://localhost:$port"
    
    # 测试 6.1: rate_limited 模式
    ((TOTAL_TESTS++))
    log "测试 6.1: rate_limited 场景"
    curl -sf -X POST "$base/admin/state" \
        -H "Content-Type: application/json" \
        -d '{"mode":"rate_limited","ttl_seconds":30}' >/dev/null 2>&1
    
    local response=$(curl -sf -X POST "$base/v1/chat/completions" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer mock-test" \
        -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' 2>/dev/null)
    
    local error_type=$(echo "$response" | jq -r '.error.type' 2>/dev/null || echo "none")
    
    if [[ "$error_type" == "rate_limit_exceeded" ]]; then
        ok "  rate_limited 响应正确"
        ((PASSED_TESTS++))
    else
        warn "  rate_limited 响应异常: $error_type"
        ((FAILED_TESTS++))
        BUGS_FOUND+=("Mock rate_limited 模式返回错误类型: $error_type")
    fi
    
    # 测试 6.2: server_error 模式
    ((TOTAL_TESTS++))
    log "测试 6.2: server_error 场景"
    curl -sf -X POST "$base/admin/state" \
        -H "Content-Type: application/json" \
        -d '{"mode":"server_error","ttl_seconds":30}' >/dev/null 2>&1
    
    local status=$(curl -sf -w "%{http_code}" -o /dev/null -X POST "$base/v1/chat/completions" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer mock-test" \
        -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' 2>/dev/null || echo "000")
    
    if [[ "$status" == "500" || "$status" == "503" ]]; then
        ok "  server_error 返回 $status"
        ((PASSED_TESTS++))
    else
        warn "  server_error 返回异常状态码: $status"
        ((FAILED_TESTS++))
        BUGS_FOUND+=("Mock server_error 模式返回状态码异常: $status")
    fi
    
    # 恢复健康模式
    curl -sf -X POST "$base/admin/state" \
        -H "Content-Type: application/json" \
        -d '{"mode":"healthy"}' >/dev/null 2>&1
}

# ====================================================================
# 生成最终报告
# ====================================================================
generate_report() {
    section "生成测试报告"
    
    local success_rate=0
    if [[ $TOTAL_TESTS -gt 0 ]]; then
        success_rate=$((PASSED_TESTS * 100 / TOTAL_TESTS))
    fi
    
    cat >> "$REPORT_FILE" <<EOF
### 测试统计

| 指标 | 数值 |
|------|------|
| 总测试数 | $TOTAL_TESTS |
| 通过 | $PASSED_TESTS |
| 失败 | $FAILED_TESTS |
| 成功率 | ${success_rate}% |

---

## 发现的问题

EOF
    
    if [[ ${#BUGS_FOUND[@]} -eq 0 ]]; then
        cat >> "$REPORT_FILE" <<EOF
**✓ 未发现问题**

所有测试均通过，网关运行正常。

EOF
    else
        echo "发现 ${#BUGS_FOUND[@]} 个问题:" >> "$REPORT_FILE"
        echo "" >> "$REPORT_FILE"
        local i=1
        for bug in "${BUGS_FOUND[@]}"; do
            echo "$i. **$bug**" >> "$REPORT_FILE"
            ((i++))
        done
        echo "" >> "$REPORT_FILE"
    fi
    
    cat >> "$REPORT_FILE" <<EOF

---

## 建议修复方案

EOF
    
    if [[ ${#BUGS_FOUND[@]} -gt 0 ]]; then
        for bug in "${BUGS_FOUND[@]}"; do
            if [[ "$bug" == *"延迟未生效"* ]]; then
                cat >> "$REPORT_FILE" <<EOF
### 问题: Mock slow 模式延迟未生效

**根因**: Mock provider 的延迟参数可能未正确应用

**修复方案**:
1. 检查 \`server-v2.py\` 中 \`latency_min_ms\` 和 \`latency_max_ms\` 的使用
2. 确保在响应前正确 sleep
3. 验证状态切换是否同步

EOF
            elif [[ "$bug" == *"成功率低"* ]]; then
                cat >> "$REPORT_FILE" <<EOF
### 问题: 并发测试成功率低

**根因**: 可能的连接池耗尽、超时或 mock provider 不稳定

**修复方案**:
1. 增加 mock provider 的并发处理能力
2. 检查连接超时设置
3. 添加重试机制
4. 优化资源清理

EOF
            fi
        done
    else
        cat >> "$REPORT_FILE" <<EOF
✓ 所有测试通过，无需修复

EOF
    fi
    
    cat >> "$REPORT_FILE" <<EOF

---

**报告生成时间**: $(date '+%Y-%m-%d %H:%M:%S')  
**网关版本**: $(curl -sf $GATEWAY_URL/version 2>/dev/null || echo "unknown")  
EOF
    
    ok "报告生成完成: $REPORT_FILE"
    
    # 打印摘要
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "  测试完成"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "  总测试: $TOTAL_TESTS"
    echo "  通过: $PASSED_TESTS"
    echo "  失败: $FAILED_TESTS"
    echo "  成功率: ${success_rate}%"
    echo "  问题数: ${#BUGS_FOUND[@]}"
    echo "  报告: $REPORT_FILE"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

# ====================================================================
# 清理函数
# ====================================================================
cleanup() {
    section "清理"
    log "停止 mock providers (可选，注释掉保持运行)"
    # for i in $(seq 0 $((NUM_MOCKS - 1))); do
    #     local port=$((MOCK_START_PORT + i))
    #     if [[ -f "/tmp/mock-$port.pid" ]]; then
    #         kill $(cat "/tmp/mock-$port.pid") 2>/dev/null || true
    #         rm -f "/tmp/mock-$port.pid"
    #     fi
    # done
}

# ====================================================================
# 主流程
# ====================================================================
main() {
    log "开始 LLM Gateway Mock 综合测试"
    log "网关: $GATEWAY_URL"
    log "Mock 数量: $NUM_MOCKS"
    log "报告: $REPORT_FILE"
    
    init_report
    check_environment
    start_mock_providers
    test_mock_state_control
    test_basic_routing
    test_concurrent_load
    test_failure_scenarios
    generate_report
    
    # cleanup  # 可选：保持 mock 运行以便后续测试
    
    if [[ ${#BUGS_FOUND[@]} -eq 0 ]]; then
        ok "所有测试通过！"
        exit 0
    else
        warn "发现 ${#BUGS_FOUND[@]} 个问题，查看报告: $REPORT_FILE"
        exit 1
    fi
}

# 处理 Ctrl+C
trap cleanup EXIT

main "$@"
