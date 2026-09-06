#!/usr/bin/env bash
# ====================================================================
# Mock Provider 综合测试 - 简化版
# ====================================================================

set -e

GATEWAY_URL="http://127.0.0.1:8782"
MOCK_START_PORT=18080
NUM_MOCKS=10
REPORT="/tmp/mock-test-$(date +%s).md"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

ok() { echo -e "${GREEN}✓${NC} $*"; }
err() { echo -e "${RED}✗${NC} $*"; }
warn() { echo -e "${YELLOW}⚠${NC} $*"; }

TESTS_RUN=0
TESTS_PASSED=0
BUGS=()

echo "========================================"
echo "Mock Provider 综合测试"
echo "========================================"
echo "Gateway: $GATEWAY_URL"
echo "Mock ports: $MOCK_START_PORT - $((MOCK_START_PORT + NUM_MOCKS - 1))"
echo ""

# 初始化报告
cat > "$REPORT" <<EOF
# Mock Provider 测试报告
生成时间: $(date)
网关: $GATEWAY_URL

## 测试结果

EOF

# 测试 1: 网关健康检查
echo "[1/6] 网关健康检查..."
TESTS_RUN=$((TESTS_RUN + 1))
if curl -s "$GATEWAY_URL/healthz" | jq -e '.status == "ok"' >/dev/null 2>&1; then
    VERSION=$(curl -s "$GATEWAY_URL/version" | jq -r '.version' 2>/dev/null)
    ok "网关运行正常 (版本: $VERSION)"
    TESTS_PASSED=$((TESTS_PASSED + 1))
    echo "- ✓ 网关健康检查通过" >> "$REPORT"
else
    err "网关未响应"
    echo "- ✗ 网关健康检查失败" >> "$REPORT"
    BUGS+=("网关healthz端点未响应")
fi

# 测试 2: 启动 Mock Providers
echo ""
echo "[2/6] 启动 $NUM_MOCKS 个 Mock Providers..."
TESTS_RUN=$((TESTS_RUN + 1))
cd "$(dirname "$0")/mocks/llm-mock-upstream"

MOCKS_STARTED=0
for i in $(seq 0 $((NUM_MOCKS - 1))); do
    PORT=$((MOCK_START_PORT + i))
    TOKEN="mock-$(printf "%02d" $i)"
    
    # 检查是否已运行
    if curl -s --max-time 1 "http://localhost:$PORT/healthz" >/dev/null 2>&1; then
        echo "  - $TOKEN (port $PORT) 已运行"
        MOCKS_STARTED=$((MOCKS_STARTED + 1))
        continue
    fi
    
    # 启动
    MOCK_PORT=$PORT MOCK_TOKEN=$TOKEN \
    python3 server-v2.py > "/tmp/mock-$PORT.log" 2>&1 &
    echo $! > "/tmp/mock-$PORT.pid"
    echo "  - 启动 $TOKEN (port $PORT)"
    MOCKS_STARTED=$((MOCKS_STARTED + 1))
done

sleep 2

# 验证
MOCKS_HEALTHY=0
for i in $(seq 0 $((NUM_MOCKS - 1))); do
    PORT=$((MOCK_START_PORT + i))
    if curl -s --max-time 1 "http://localhost:$PORT/healthz" >/dev/null 2>&1; then
        MOCKS_HEALTHY=$((MOCKS_HEALTHY + 1))
    fi
done

if [ $MOCKS_HEALTHY -ge $((NUM_MOCKS / 2)) ]; then
    ok "$MOCKS_HEALTHY/$NUM_MOCKS mock providers 可用"
    TESTS_PASSED=$((TESTS_PASSED + 1))
    echo "- ✓ Mock providers 启动成功 ($MOCKS_HEALTHY/$NUM_MOCKS)" >> "$REPORT"
else
    err "只有 $MOCKS_HEALTHY/$NUM_MOCKS 可用"
    echo "- ✗ Mock providers 启动失败 ($MOCKS_HEALTHY/$NUM_MOCKS)" >> "$REPORT"
    BUGS+=("Mock provider启动成功率低: $MOCKS_HEALTHY/$NUM_MOCKS")
fi

# 测试 3: Mock 状态控制
echo ""
echo "[3/6] Mock 状态控制测试..."
TESTS_RUN=$((TESTS_RUN + 1))
PORT=$MOCK_START_PORT
BASE="http://localhost:$PORT"

# 获取初始状态
INITIAL_STATE=$(curl -s "$BASE/admin/state" | jq -r '.mode' 2>/dev/null || echo "error")
if [ "$INITIAL_STATE" != "error" ]; then
    echo "  - 初始状态: $INITIAL_STATE"
    
    # 切换到 slow 模式
    curl -s -X POST "$BASE/admin/state" \
        -H "Content-Type: application/json" \
        -d '{"mode":"slow","ttl_seconds":30,"latency_min_ms":2000,"latency_max_ms":3000}' \
        >/dev/null 2>&1
    
    sleep 1
    NEW_STATE=$(curl -s "$BASE/admin/state" | jq -r '.mode' 2>/dev/null)
    
    if [ "$NEW_STATE" = "slow" ]; then
        ok "状态切换成功: $INITIAL_STATE → slow"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        echo "- ✓ Mock 状态切换正常" >> "$REPORT"
        
        # 验证延迟
        START=$(python3 -c 'import time; print(int(time.time() * 1000))')
        curl -s -X POST "$BASE/v1/chat/completions" \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer test" \
            -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' \
            >/dev/null 2>&1
        END=$(python3 -c 'import time; print(int(time.time() * 1000))')
        LATENCY=$((END - START))
        
        if [ $LATENCY -ge 2000 ]; then
            ok "延迟验证通过: ${LATENCY}ms"
            echo "  - 延迟: ${LATENCY}ms (预期 ≥2000ms)" >> "$REPORT"
        else
            warn "延迟低于预期: ${LATENCY}ms (预期 ≥2000ms)"
            echo "  - ⚠ 延迟: ${LATENCY}ms (预期 ≥2000ms)" >> "$REPORT"
            BUGS+=("Mock slow模式延迟未生效: ${LATENCY}ms")
        fi
        
        # 恢复健康模式
        curl -s -X POST "$BASE/admin/state" \
            -H "Content-Type: application/json" \
            -d '{"mode":"healthy"}' >/dev/null 2>&1
    else
        warn "状态切换失败: $NEW_STATE"
        echo "- ⚠ Mock 状态切换异常" >> "$REPORT"
        BUGS+=("Mock状态切换到slow失败")
    fi
else
    err "无法获取 mock 状态"
    echo "- ✗ Mock 状态API不可用" >> "$REPORT"
    BUGS+=("Mock状态API返回error")
fi

# 测试 4: 并发测试
echo ""
echo "[4/6] 并发测试 (50 并发 × 10 请求)..."
TESTS_RUN=$((TESTS_RUN + 1))
PORT=$MOCK_START_PORT
BASE="http://localhost:$PORT"

TOTAL_REQ=500
SUCCESS=0

# 使用 xargs 实现并发
seq 1 $TOTAL_REQ | xargs -P 50 -I {} sh -c "
    curl -s -X POST '$BASE/v1/chat/completions' \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer test' \
        -d '{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"req-{}\"}]}' \
        >/dev/null 2>&1 && echo 'ok' || echo 'fail'
" > /tmp/concurrent-test-results.txt 2>&1

SUCCESS=$(grep -c "ok" /tmp/concurrent-test-results.txt 2>/dev/null || echo 0)
SUCCESS_RATE=$((SUCCESS * 100 / TOTAL_REQ))

if [ $SUCCESS_RATE -ge 95 ]; then
    ok "并发测试通过: $SUCCESS/$TOTAL_REQ 成功 (${SUCCESS_RATE}%)"
    TESTS_PASSED=$((TESTS_PASSED + 1))
    echo "- ✓ 并发测试: $SUCCESS/$TOTAL_REQ (${SUCCESS_RATE}%)" >> "$REPORT"
else
    warn "并发测试成功率: $SUCCESS/$TOTAL_REQ (${SUCCESS_RATE}%)"
    echo "- ⚠ 并发测试: $SUCCESS/$TOTAL_REQ (${SUCCESS_RATE}%)" >> "$REPORT"
    BUGS+=("并发成功率${SUCCESS_RATE}%低于95%")
fi

# 测试 5: 故障场景 - rate_limited
echo ""
echo "[5/6] 故障场景测试 - rate_limited..."
TESTS_RUN=$((TESTS_RUN + 1))

curl -s -X POST "$BASE/admin/state" \
    -H "Content-Type: application/json" \
    -d '{"mode":"rate_limited","ttl_seconds":20}' >/dev/null 2>&1

RESPONSE=$(curl -s -X POST "$BASE/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer test" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' 2>/dev/null)

ERROR_TYPE=$(echo "$RESPONSE" | jq -r '.error.type' 2>/dev/null || echo "none")

if [ "$ERROR_TYPE" = "rate_limit_exceeded" ]; then
    ok "rate_limited 模式正常"
    TESTS_PASSED=$((TESTS_PASSED + 1))
    echo "- ✓ rate_limited 响应正确" >> "$REPORT"
else
    warn "rate_limited 返回: $ERROR_TYPE"
    echo "- ⚠ rate_limited 返回异常: $ERROR_TYPE" >> "$REPORT"
    BUGS+=("rate_limited模式返回类型错误: $ERROR_TYPE")
fi

# 测试 6: 故障场景 - server_error
echo ""
echo "[6/6] 故障场景测试 - server_error..."
TESTS_RUN=$((TESTS_RUN + 1))

curl -s -X POST "$BASE/admin/state" \
    -H "Content-Type: application/json" \
    -d '{"mode":"server_error","ttl_seconds":20}' >/dev/null 2>&1

HTTP_CODE=$(curl -s -w "%{http_code}" -o /dev/null -X POST "$BASE/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer test" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' 2>/dev/null || echo "000")

if [ "$HTTP_CODE" = "500" ] || [ "$HTTP_CODE" = "503" ]; then
    ok "server_error 模式正常 (HTTP $HTTP_CODE)"
    TESTS_PASSED=$((TESTS_PASSED + 1))
    echo "- ✓ server_error 返回 $HTTP_CODE" >> "$REPORT"
else
    warn "server_error 返回: $HTTP_CODE"
    echo "- ⚠ server_error 返回异常: $HTTP_CODE" >> "$REPORT"
    BUGS+=("server_error模式返回状态码异常: $HTTP_CODE")
fi

# 恢复健康模式
curl -s -X POST "$BASE/admin/state" \
    -H "Content-Type: application/json" \
    -d '{"mode":"healthy"}' >/dev/null 2>&1

# 生成报告
echo "" >> "$REPORT"
echo "## 统计" >> "$REPORT"
echo "" >> "$REPORT"
echo "- 总测试: $TESTS_RUN" >> "$REPORT"
echo "- 通过: $TESTS_PASSED" >> "$REPORT"
echo "- 成功率: $((TESTS_PASSED * 100 / TESTS_RUN))%" >> "$REPORT"
echo "" >> "$REPORT"

if [ ${#BUGS[@]} -gt 0 ]; then
    echo "## 发现的问题" >> "$REPORT"
    echo "" >> "$REPORT"
    for i in "${!BUGS[@]}"; do
        echo "$((i+1)). ${BUGS[$i]}" >> "$REPORT"
    done
else
    echo "## 结论" >> "$REPORT"
    echo "" >> "$REPORT"
    echo "✓ 所有测试通过，未发现问题。" >> "$REPORT"
fi

# 输出摘要
echo ""
echo "========================================"
echo "测试完成"
echo "========================================"
echo "总测试: $TESTS_RUN"
echo "通过: $TESTS_PASSED"
echo "成功率: $((TESTS_PASSED * 100 / TESTS_RUN))%"
echo "问题数: ${#BUGS[@]}"
echo ""
echo "报告: $REPORT"
echo "========================================"

if [ ${#BUGS[@]} -eq 0 ]; then
    ok "所有测试通过！"
    exit 0
else
    warn "发现 ${#BUGS[@]} 个问题"
    exit 1
fi
