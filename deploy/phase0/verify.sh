#!/bin/bash
# ============================================================================
# File: deploy/phase0/verify.sh
# Purpose: Verify Phase 0 optimization effectiveness
# Usage: bash deploy/phase0/verify.sh --target=<local|kaixuan-1>
# ============================================================================

set -euo pipefail

TARGET="${1:-local}"

echo "========================================"
echo "  Phase 0 验证脚本"
echo "========================================"
echo "目标: $TARGET"
echo ""

case "$TARGET" in
  local)
    HOST="localhost:8781"
    CMD_PREFIX=""
    ;;
  kaixuan-1)
    HOST="192.168.31.28:8781"
    CMD_PREFIX="ssh root@192.168.31.28"
    ;;
  *)
    echo "❌ 未知目标: $TARGET"
    exit 1
    ;;
esac

# ============================================================================
# 1. Health Check
# ============================================================================
echo "=== 1. 健康检查 ==="
if [ -z "$CMD_PREFIX" ]; then
  HEALTH=$(curl -fsS http://$HOST/healthz 2>/dev/null || echo "FAIL")
else
  HEALTH=$($CMD_PREFIX "curl -fsS http://localhost:8781/healthz" 2>/dev/null || echo "FAIL")
fi

if [[ "$HEALTH" == "FAIL" ]]; then
  echo "❌ 健康检查失败"
  exit 1
else
  echo "✅ 服务运行正常"
fi

# ============================================================================
# 2. Configuration Verification
# ============================================================================
echo ""
echo "=== 2. 配置验证 ==="

if [ -n "$CMD_PREFIX" ]; then
  echo "检查环境变量..."
  $CMD_PREFIX "systemctl show llm-gateway | grep -E 'MAX_IDLE_CONNS|HTTP2'" || echo "⚠️  无法读取配置"
fi

# ============================================================================
# 3. Metrics Check
# ============================================================================
echo ""
echo "=== 3. 指标检查 ==="

if [ -z "$CMD_PREFIX" ]; then
  METRICS=$(curl -fsS http://$HOST/metrics 2>/dev/null || echo "")
else
  METRICS=$($CMD_PREFIX "curl -fsS http://localhost:8781/metrics" 2>/dev/null || echo "")
fi

if [ -n "$METRICS" ]; then
  echo "关键指标："
  echo "$METRICS" | grep -E "go_goroutines|process_resident_memory_bytes|http_requests_total" | head -5
  echo "✅ Metrics 可访问"
else
  echo "⚠️  无法获取 metrics"
fi

# ============================================================================
# 4. Connection Pool Stats
# ============================================================================
echo ""
echo "=== 4. 连接池统计 ==="

if [ -n "$METRICS" ]; then
  echo "$METRICS" | grep -i "pool" | head -10 || echo "⚠️  无 pool metrics"
else
  echo "⚠️  Metrics 不可用"
fi

# ============================================================================
# 5. Quick Performance Test
# ============================================================================
echo ""
echo "=== 5. 快速性能测试 ==="

echo "发送 10 个测试请求..."
TOTAL_TIME=0
SUCCESS_COUNT=0

for i in {1..10}; do
  if [ -z "$CMD_PREFIX" ]; then
    RESPONSE_TIME=$(curl -w "%{time_total}" -o /dev/null -s http://$HOST/healthz 2>/dev/null || echo "0")
  else
    RESPONSE_TIME=$($CMD_PREFIX "curl -w '%{time_total}' -o /dev/null -s http://localhost:8781/healthz" 2>/dev/null || echo "0")
  fi
  
  if [[ "$RESPONSE_TIME" != "0" ]]; then
    TOTAL_TIME=$(echo "$TOTAL_TIME + $RESPONSE_TIME" | bc)
    SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
  fi
  
  sleep 0.1
done

if [ $SUCCESS_COUNT -gt 0 ]; then
  AVG_TIME=$(echo "scale=3; $TOTAL_TIME / $SUCCESS_COUNT" | bc)
  echo "✅ 平均响应时间: ${AVG_TIME}s (${SUCCESS_COUNT}/10 成功)"
else
  echo "❌ 所有请求失败"
fi

echo ""
echo "========================================"
echo "  验证完成"
echo "========================================"
echo ""
echo "持续监控命令:"
echo "  watch -n 5 'curl -s http://localhost:8781/metrics | grep -E \"ttfb|pool\"'"
echo ""
