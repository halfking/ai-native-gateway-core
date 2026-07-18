#!/bin/bash
# ============================================================================
# File: deploy/phase0/test-local-docker.sh
# Purpose: Test Phase 0 optimization in local Docker environment
# Usage: bash deploy/phase0/test-local-docker.sh
# ============================================================================

set -euo pipefail

BASELINE_METRICS="/tmp/baseline-metrics.json"
OPTIMIZED_METRICS="/tmp/optimized-metrics.json"

echo "========================================"
echo "  Phase 0 本地 Docker 测试"
echo "========================================"
echo ""

# ============================================================================
# Step 1: Build binary
# ============================================================================
echo "=== Step 1: 编译二进制 ==="
go build -o bin/gateway cmd/gateway/main.go
echo "✅ 编译完成"

# ============================================================================
# Step 2: Run baseline test (without optimization)
# ============================================================================
echo ""
echo "=== Step 2: 基准测试（优化前）==="

# Export minimal config (baseline)
export LLM_GATEWAY_MAX_IDLE_CONNS_PER_HOST=16
export LLM_GATEWAY_MAX_CONNS_PER_HOST=64
export LLM_GATEWAY_IDLE_CONN_TIMEOUT=90s
export LLM_GATEWAY_HTTP2_MAX_CONCURRENT_STREAMS=100
export LLM_GATEWAY_CONNECT_TIMEOUT=10s
export LLM_GATEWAY_TCP_KEEPALIVE=30s

# Start service in background
./bin/gateway &
GATEWAY_PID=$!
echo "Gateway PID: $GATEWAY_PID"

# Wait for startup
echo "等待服务启动..."
sleep 10

# Health check
if curl -fsS http://localhost:8781/healthz > /dev/null 2>&1; then
    echo "✅ 基准服务启动成功"
else
    echo "❌ 基准服务启动失败"
    kill $GATEWAY_PID 2>/dev/null || true
    exit 1
fi

# Run performance test
echo "运行基准性能测试（100次请求）..."
BASELINE_AVG_TIME=0
BASELINE_SUCCESS=0

for i in {1..100}; do
    RESPONSE_TIME=$(curl -w "%{time_total}" -o /dev/null -s http://localhost:8781/healthz 2>/dev/null || echo "0")
    if [[ "$RESPONSE_TIME" != "0" ]]; then
        BASELINE_AVG_TIME=$(echo "$BASELINE_AVG_TIME + $RESPONSE_TIME" | bc)
        BASELINE_SUCCESS=$((BASELINE_SUCCESS + 1))
    fi
    sleep 0.05
done

BASELINE_AVG=$(echo "scale=4; $BASELINE_AVG_TIME / $BASELINE_SUCCESS" | bc)
echo "基准平均响应时间: ${BASELINE_AVG}s (${BASELINE_SUCCESS}/100 成功)"

# Save baseline metrics
cat > "$BASELINE_METRICS" <<EOJ
{
  "avg_response_time": $BASELINE_AVG,
  "success_count": $BASELINE_SUCCESS,
  "config": {
    "max_idle_conns_per_host": 16,
    "max_conns_per_host": 64,
    "idle_conn_timeout": "90s",
    "http2_max_concurrent_streams": 100
  }
}
EOJ

# Stop baseline service
kill $GATEWAY_PID
wait $GATEWAY_PID 2>/dev/null || true
sleep 5

# ============================================================================
# Step 3: Run optimized test
# ============================================================================
echo ""
echo "=== Step 3: 优化测试（Phase 0）==="

# Export optimized config
export $(cat deploy/phase0/optimization.env | grep -v '^#' | xargs)

# Start optimized service
./bin/gateway &
GATEWAY_PID=$!
echo "Gateway PID: $GATEWAY_PID"

# Wait for startup
echo "等待服务启动..."
sleep 10

# Health check
if curl -fsS http://localhost:8781/healthz > /dev/null 2>&1; then
    echo "✅ 优化服务启动成功"
else
    echo "❌ 优化服务启动失败"
    kill $GATEWAY_PID 2>/dev/null || true
    exit 1
fi

# Run performance test
echo "运行优化性能测试（100次请求）..."
OPTIMIZED_AVG_TIME=0
OPTIMIZED_SUCCESS=0

for i in {1..100}; do
    RESPONSE_TIME=$(curl -w "%{time_total}" -o /dev/null -s http://localhost:8781/healthz 2>/dev/null || echo "0")
    if [[ "$RESPONSE_TIME" != "0" ]]; then
        OPTIMIZED_AVG_TIME=$(echo "$OPTIMIZED_AVG_TIME + $RESPONSE_TIME" | bc)
        OPTIMIZED_SUCCESS=$((OPTIMIZED_SUCCESS + 1))
    fi
    sleep 0.05
done

OPTIMIZED_AVG=$(echo "scale=4; $OPTIMIZED_AVG_TIME / $OPTIMIZED_SUCCESS" | bc)
echo "优化平均响应时间: ${OPTIMIZED_AVG}s (${OPTIMIZED_SUCCESS}/100 成功)"

# Save optimized metrics
cat > "$OPTIMIZED_METRICS" <<EOJ
{
  "avg_response_time": $OPTIMIZED_AVG,
  "success_count": $OPTIMIZED_SUCCESS,
  "config": {
    "max_idle_conns_per_host": 64,
    "max_conns_per_host": 256,
    "idle_conn_timeout": "180s",
    "http2_max_concurrent_streams": 250
  }
}
EOJ

# Stop optimized service
kill $GATEWAY_PID
wait $GATEWAY_PID 2>/dev/null || true

# ============================================================================
# Step 4: Compare results
# ============================================================================
echo ""
echo "=== Step 4: 结果对比 ==="
echo ""
echo "基准配置:"
echo "  - MAX_IDLE_CONNS_PER_HOST: 16"
echo "  - MAX_CONNS_PER_HOST: 64"
echo "  - 平均响应时间: ${BASELINE_AVG}s"
echo ""
echo "优化配置:"
echo "  - MAX_IDLE_CONNS_PER_HOST: 64 (↑ 4x)"
echo "  - MAX_CONNS_PER_HOST: 256 (↑ 4x)"
echo "  - 平均响应时间: ${OPTIMIZED_AVG}s"
echo ""

# Calculate improvement
IMPROVEMENT=$(echo "scale=2; ($BASELINE_AVG - $OPTIMIZED_AVG) / $BASELINE_AVG * 100" | bc)
echo "性能提升: ${IMPROVEMENT}%"

if (( $(echo "$IMPROVEMENT > 0" | bc -l) )); then
    echo "✅ 优化生效"
else
    echo "⚠️  未观察到明显提升（可能需要更高负载测试）"
fi

echo ""
echo "========================================"
echo "  测试完成"
echo "========================================"
echo ""
echo "详细指标:"
echo "  基准: $BASELINE_METRICS"
echo "  优化: $OPTIMIZED_METRICS"
echo ""
echo "下一步: 部署到 245 服务器"
echo "  bash deploy/phase0/deploy-245.sh"
