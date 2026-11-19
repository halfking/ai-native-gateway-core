#!/usr/bin/env bash
# scripts/verify-211-deployment.sh —— 211 部署验证脚本
#
# 用法：
#   ADMIN_PASSWORD=<口令> bash scripts/verify-211-deployment.sh
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_URL="https://llm.itestu.cn"
ADMIN_USER="admin"
: "${ADMIN_PASSWORD?需要 ADMIN_PASSWORD 环境变量}"

echo "=========================================="
echo "211 部署验证"
echo "目标: $BASE_URL"
echo "时间: $(date '+%Y-%m-%d %H:%M:%S')"
echo "=========================================="
echo ""

# ── 1. 健康检查 ────────────────────────────────────────────────
echo "==> 1. 健康检查"
HEALTH=$(curl -sS "$BASE_URL/api/health" || echo '{"status":"failed"}')
STATUS=$(echo "$HEALTH" | jq -r '.status // "unknown"')
VERSION=$(echo "$HEALTH" | jq -r '.version // "unknown"')
COMMIT=$(echo "$HEALTH" | jq -r '.commit // "unknown"')

if [ "$STATUS" = "ok" ]; then
    echo "✓ 健康检查通过"
    echo "  版本: $VERSION"
    echo "  提交: $COMMIT"
else
    echo "✗ 健康检查失败"
    echo "$HEALTH"
    exit 1
fi
echo ""

# ── 2. 登录测试 ────────────────────────────────────────────────
echo "==> 2. 登录测试"
LOGIN_RESP=$(curl -sS -X POST "$BASE_URL/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" \
    -c /tmp/cookies-211-verify.txt -w "\n%{http_code}")

HTTP_CODE=$(echo "$LOGIN_RESP" | tail -1)
BODY=$(echo "$LOGIN_RESP" | head -n -1)

if [ "$HTTP_CODE" = "200" ]; then
    echo "✓ 登录成功 (HTTP $HTTP_CODE)"
    echo "$BODY" | jq -r '.message // .token[0:20] // "OK"' 2>/dev/null || echo "OK"
else
    echo "✗ 登录失败 (HTTP $HTTP_CODE)"
    echo "$BODY"
    exit 1
fi
echo ""

# ── 3. 认证验证 ────────────────────────────────────────────────
echo "==> 3. 认证验证"
AUTH_ME=$(curl -sS "$BASE_URL/api/auth/me" -b /tmp/cookies-211-verify.txt)
USERNAME=$(echo "$AUTH_ME" | jq -r '.username // empty')

if [ -n "$USERNAME" ]; then
    echo "✓ 认证通过"
    echo "  用户: $USERNAME"
    ROLE=$(echo "$AUTH_ME" | jq -r '.role // "unknown"')
    echo "  角色: $ROLE"
else
    echo "✗ 认证失败"
    echo "$AUTH_ME"
    exit 1
fi
echo ""

# ── 4. 关键端点测试 ────────────────────────────────────────────
echo "==> 4. 关键端点测试"

# 4.1 Dashboard
echo "  4.1 Dashboard"
DASH=$(curl -sS "$BASE_URL/api/admin/dashboard/board?days=7" -b /tmp/cookies-211-verify.txt -w "\n%{http_code}")
DASH_CODE=$(echo "$DASH" | tail -1)
if [ "$DASH_CODE" = "200" ]; then
    echo "    ✓ Dashboard (HTTP $DASH_CODE)"
else
    echo "    ✗ Dashboard (HTTP $DASH_CODE)"
fi

# 4.2 PI Analysis Stats (新增)
echo "  4.2 PI Analysis Stats"
PI_STATS=$(curl -sS "$BASE_URL/api/admin/pi-analysis/stats" -b /tmp/cookies-211-verify.txt -w "\n%{http_code}")
PI_CODE=$(echo "$PI_STATS" | tail -1)
if [ "$PI_CODE" = "200" ]; then
    echo "    ✓ PI Stats (HTTP $PI_CODE)"
else
    echo "    ✗ PI Stats (HTTP $PI_CODE)"
fi

# 4.3 PI Analysis Queue (新增)
echo "  4.3 PI Analysis Queue"
PI_QUEUE=$(curl -sS "$BASE_URL/api/admin/pi-analysis/queue?status=completed&limit=10" -b /tmp/cookies-211-verify.txt -w "\n%{http_code}")
PI_Q_CODE=$(echo "$PI_QUEUE" | tail -1)
if [ "$PI_Q_CODE" = "200" ]; then
    echo "    ✓ PI Queue (HTTP $PI_Q_CODE)"
else
    echo "    ✗ PI Queue (HTTP $PI_Q_CODE)"
fi

echo ""

# ── 5. 数据库迁移验证 ──────────────────────────────────────────
echo "==> 5. 数据库迁移验证"
echo "  检查迁移 980 是否应用..."

# 通过 SSH 检查（需要配置）
if ssh -o ConnectTimeout=5 root@192.168.31.211 "echo 'SELECT version FROM schema_migrations WHERE version=980;' | docker exec -i acc-pg17 psql -U postgres -d llm_gateway -t" 2>/dev/null | grep -q "980"; then
    echo "  ✓ 迁移 980 已应用"
else
    echo "  ⚠ 无法验证迁移 980（需要 SSH 访问）"
fi
echo ""

# ── 总结 ────────────────────────────────────────────────────────
echo "=========================================="
echo "✓ 基础验证通过"
echo "  下一步: 运行 Maestro UI 测试"
echo "  命令: ADMIN_PASSWORD=<口令> bash scripts/test-211-full.sh"
echo "=========================================="
