#!/usr/bin/env bash
# Mock Provider 快速验证脚本 - 修复版

set -e

GATEWAY="http://127.0.0.1:8782"
MOCK_BASE="http://localhost:18080"

echo "========================================="
echo "Mock Provider 快速验证"
echo "========================================="

# 1. 确保 mock 处于 healthy 模式
echo "[1] 重置 mock 到 healthy 模式..."
curl -s -X POST "$MOCK_BASE/admin/state" \
    -H "Content-Type: application/json" \
    -d '{"mode":"healthy","latency_min_ms":200,"latency_max_ms":500}' >/dev/null
sleep 1
MODE=$(curl -s "$MOCK_BASE/admin/state" | jq -r '.mode')
echo "✓ Mock 当前状态: $MODE"

# 2. 并发测试 - 修复版（使用循环而非 xargs）
echo ""
echo "[2] 并发测试 (20 并发 × 5 请求 = 100 总请求)..."
TMP_RESULTS="/tmp/concurrent-test-fixed-$$.txt"
rm -f "$TMP_RESULTS"

START_TIME=$(date +%s)

# 启动20个后台进程，每个发送5个请求
for i in $(seq 1 20); do
    (
        for j in $(seq 1 5); do
            if curl -s -X POST "$MOCK_BASE/v1/chat/completions" \
                -H "Content-Type: application/json" \
                -H "Authorization: Bearer test" \
                -d "{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"c${i}-r${j}\"}]}" \
                >/dev/null 2>&1; then
                echo "ok"
            else
                echo "fail"
            fi
        done
    ) >> "$TMP_RESULTS" 2>&1 &
done

# 等待所有后台任务完成
wait

END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

# 统计结果
SUCCESS=$(grep -c "ok" "$TMP_RESULTS" 2>/dev/null || echo 0)
FAILED=$(grep -c "fail" "$TMP_RESULTS" 2>/dev/null || echo 0)
TOTAL=$((SUCCESS + FAILED))
SUCCESS_RATE=$((SUCCESS * 100 / TOTAL))

echo "✓ 完成: $SUCCESS/$TOTAL 成功 (${SUCCESS_RATE}%), 耗时 ${ELAPSED}s"
rm -f "$TMP_RESULTS"

# 3. Rate Limited 测试
echo ""
echo "[3] Rate Limited 场景测试..."
curl -s -X POST "$MOCK_BASE/admin/state" \
    -H "Content-Type: application/json" \
    -d '{"mode":"rate_limited","ttl_seconds":20}' >/dev/null
sleep 1

RESPONSE=$(curl -s -X POST "$MOCK_BASE/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer test" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}')

ERROR_TYPE=$(echo "$RESPONSE" | jq -r '.error.type' 2>/dev/null || echo "none")
if [ "$ERROR_TYPE" = "rate_limit_exceeded" ]; then
    echo "✓ Rate limited 返回正确: $ERROR_TYPE"
else
    echo "⚠ Rate limited 返回异常: $ERROR_TYPE"
fi

# 4. Server Error 测试
echo ""
echo "[4] Server Error 场景测试..."
curl -s -X POST "$MOCK_BASE/admin/state" \
    -H "Content-Type: application/json" \
    -d '{"mode":"server_error","ttl_seconds":20}' >/dev/null
sleep 1

HTTP_CODE=$(curl -s -w "%{http_code}" -o /dev/null \
    -X POST "$MOCK_BASE/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer test" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}')

if [ "$HTTP_CODE" = "500" ] || [ "$HTTP_CODE" = "503" ]; then
    echo "✓ Server error 返回正确: HTTP $HTTP_CODE"
else
    echo "⚠ Server error 返回异常: HTTP $HTTP_CODE"
fi

# 5. Flaky 测试
echo ""
echo "[5] Flaky 场景测试 (50%成功率)..."
curl -s -X POST "$MOCK_BASE/admin/state" \
    -H "Content-Type: application/json" \
    -d '{"mode":"flaky","ttl_seconds":20,"success_rate":0.5}' >/dev/null
sleep 1

SUCCESS_COUNT=0
for i in $(seq 1 10); do
    if curl -s -X POST "$MOCK_BASE/v1/chat/completions" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer test" \
        -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' \
        | jq -e '.choices' >/dev/null 2>&1; then
        SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
    fi
done

ACTUAL_RATE=$((SUCCESS_COUNT * 10))
echo "✓ Flaky 模式实际成功率: ${ACTUAL_RATE}% (预期 ~50%)"

# 6. 恢复健康模式
echo ""
echo "[6] 恢复 mock 到 healthy 模式..."
curl -s -X POST "$MOCK_BASE/admin/state" \
    -H "Content-Type: application/json" \
    -d '{"mode":"healthy"}' >/dev/null
sleep 1
FINAL_MODE=$(curl -s "$MOCK_BASE/admin/state" | jq -r '.mode')
echo "✓ Mock 最终状态: $FINAL_MODE"

# 7. 状态历史
echo ""
echo "[7] Mock 状态变更历史:"
curl -s "$MOCK_BASE/admin/state" | jq -r '.history[] | "  \(.at | split(".")[0]): \(.from) → \(.to)"' 2>/dev/null | tail -5

echo ""
echo "========================================="
echo "验证完成"
echo "========================================="
