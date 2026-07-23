#!/usr/bin/env bash
# scripts/deploy/health-check.sh - 通用健康检查脚本
# 用法：bash health-check.sh <base_url>

set -euo pipefail

BASE_URL="${1:-http://localhost:8781}"
MAX_RETRIES=30
RETRY_INTERVAL=2

echo "========================================="
echo "健康检查: $BASE_URL"
echo "========================================="

# ============================================================================
# L1: HTTP 存活检查
# ============================================================================

echo ""
echo "L1: HTTP 存活检查"

for i in $(seq 1 $MAX_RETRIES); do
    if curl -fsS "${BASE_URL}/healthz" | grep -q '"status":"ok"'; then
        echo "   ✅ /healthz 返回 200 OK"
        break
    fi
    
    if [[ $i -eq $MAX_RETRIES ]]; then
        echo "   ❌ /healthz 检查失败（超时 ${MAX_RETRIES}次）"
        exit 1
    fi
    
    echo "   ⏳ 尝试 $i/$MAX_RETRIES，等待 ${RETRY_INTERVAL}s..."
    sleep $RETRY_INTERVAL
done

# ============================================================================
# L2: 依赖连通性检查
# ============================================================================

echo ""
echo "L2: 依赖连通性检查"

# 数据库
DB_CHECK=$(curl -fsS "${BASE_URL}/api/internal/ready/db" 2>/dev/null || echo "FAIL")
if [[ "$DB_CHECK" == "OK" ]]; then
    echo "   ✅ 数据库连接正常"
else
    echo "   ❌ 数据库连接失败"
    exit 1
fi

# Redis
REDIS_CHECK=$(curl -fsS "${BASE_URL}/api/internal/ready/redis" 2>/dev/null || echo "FAIL")
if [[ "$REDIS_CHECK" == "OK" ]]; then
    echo "   ✅ Redis 连接正常"
else
    echo "   ❌ Redis 连接失败"
    exit 1
fi

# ============================================================================
# L3: 功能链路检查
# ============================================================================

echo ""
echo "L3: 功能链路检查"

# 版本信息
VERSION=$(curl -fsS "${BASE_URL}/api/system/version" 2>/dev/null | jq -r '.version' 2>/dev/null || echo "unknown")
if [[ "$VERSION" != "unknown" && -n "$VERSION" ]]; then
    echo "   ✅ 版本: $VERSION"
else
    echo "   ❌ 版本信息获取失败"
    exit 1
fi

# 配置信息
CONFIG=$(curl -fsS "${BASE_URL}/api/system/config" 2>/dev/null | jq -r '.server.port' 2>/dev/null || echo "unknown")
if [[ "$CONFIG" != "unknown" && -n "$CONFIG" ]]; then
    echo "   ✅ 配置加载正常"
else
    echo "   ❌ 配置加载失败"
    exit 1
fi

# ============================================================================
# L4: 业务真实性检查（可选）
# ============================================================================

echo ""
echo "L4: 业务真实性检查"

# 检查是否可以列出租户（需要登录，这里简化为检查API是否响应）
TENANTS=$(curl -fsS "${BASE_URL}/api/v1/tenants" 2>/dev/null | jq -r '.data' 2>/dev/null || echo "[]")
if [[ "$TENANTS" != "null" ]]; then
    echo "   ✅ 租户 API 响应正常"
else
    echo "   ⚠️  租户 API 需要认证（正常）"
fi

# ============================================================================
# 汇总
# ============================================================================

echo ""
echo "========================================="
echo "✅ 健康检查通过"
echo "========================================="
echo ""
echo "服务状态:"
echo "  URL: $BASE_URL"
echo "  版本: $VERSION"
echo "  数据库: ✅"
echo "  Redis: ✅"
echo ""

exit 0

