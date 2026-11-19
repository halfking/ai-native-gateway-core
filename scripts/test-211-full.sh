#!/usr/bin/env bash
# scripts/test-211-full.sh —— 211 全面测试验证脚本
#
# 用法：
#   ADMIN_PASSWORD=<口令> bash scripts/test-211-full.sh
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# ── 配置 ────────────────────────────────────────────────────────
BASE_URL="https://llm.itestu.cn"
ADMIN_USER="admin"
: "${ADMIN_PASSWORD:?需要 ADMIN_PASSWORD 环境变量}"

echo "=========================================="
echo "211 全面测试验证"
echo "目标: $BASE_URL"
echo "时间: $(date '+%Y-%m-%d %H:%M:%S')"
echo "=========================================="
echo ""

# ── 1. 健康检查 ────────────────────────────────────────────────
echo "==> 1. 健康检查"
HEALTH=$(curl -s "$BASE_URL/api/health" || echo "FAILED")
if echo "$HEALTH" | grep -q '"status":"ok"'; then
    echo "✓ 健康检查通过"
    echo "$HEALTH" | jq . 2>/dev/null || echo "$HEALTH"
else
    echo "✗ 健康检查失败: $HEALTH"
    exit 1
fi
echo ""

# ── 2. 版本信息 ────────────────────────────────────────────────
echo "==> 2. 版本信息"
VERSION=$(curl -s "$BASE_URL/api/health" | jq -r '.version // "unknown"')
COMMIT=$(curl -s "$BASE_URL/api/health" | jq -r '.commit // "unknown"')
echo "版本: $VERSION"
echo "提交: $COMMIT"
echo ""

# ── 3. 登录测试 ────────────────────────────────────────────────
echo "==> 3. 登录测试"
LOGIN_RESP=$(curl -s -X POST "$BASE_URL/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" \
    -c /tmp/cookies-211.txt)

if echo "$LOGIN_RESP" | grep -q '"success":true'; then
    echo "✓ 登录成功"
else
    echo "✗ 登录失败: $LOGIN_RESP"
    exit 1
fi
echo ""

# ── 4. 认证测试 ────────────────────────────────────────────────
echo "==> 4. 认证测试"
AUTH_ME=$(curl -s "$BASE_URL/api/auth/me" -b /tmp/cookies-211.txt)
if echo "$AUTH_ME" | grep -q '"username"'; then
    echo "✓ 认证通过"
    echo "$AUTH_ME" | jq . 2>/dev/null || echo "$AUTH_ME"
else
    echo "✗ 认证失败: $AUTH_ME"
    exit 1
fi
echo ""

# ── 5. Maestro 测试 ────────────────────────────────────────────
echo "==> 5. Maestro 测试"
echo ""

# 检查 maestro 是否可用
if ! command -v maestro &> /dev/null; then
    echo "⚠ maestro 未安装，跳过 UI 测试"
    echo "   安装: brew tap mobile-dev-inc/tap && brew install maestro"
    exit 0
fi

# 创建输出目录
OUT_DIR="/tmp/maestro-211-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT_DIR"

# 导出环境变量
export BASE_URL ADMIN_USER ADMIN_PASSWORD

# Flow 11: 简易模式总览页
echo "==> 5.1 Flow 11: 简易模式总览页"
if [ -f maestro/run-simple-overview.sh ]; then
    bash maestro/run-simple-overview.sh "$OUT_DIR/flow-11" || {
        echo "✗ Flow 11 失败"
        ls -lh "$OUT_DIR/flow-11" 2>/dev/null || true
    }
else
    echo "⚠ maestro/run-simple-overview.sh 不存在"
fi
echo ""

# Flow 12: Live Stage Tree
echo "==> 5.2 Flow 12: Live Stage Tree"
if [ -f maestro/run-stage-tree.sh ]; then
    bash maestro/run-stage-tree.sh "$OUT_DIR/flow-12" || {
        echo "✗ Flow 12 失败"
        ls -lh "$OUT_DIR/flow-12" 2>/dev/null || true
    }
else
    echo "⚠ maestro/run-stage-tree.sh 不存在"
fi
echo ""

# Flow 13: Board Operational (新增)
echo "==> 5.3 Flow 13: Board Operational"
if [ -f maestro/flows/13-board-operational.yaml ]; then
    # 需要解析动态 ID
    if [ -f maestro/tools/resolve-ids.mjs ]; then
        node maestro/tools/resolve-ids.mjs --base-url "$BASE_URL" \
            --admin-user "$ADMIN_USER" --admin-password "$ADMIN_PASSWORD" \
            > /tmp/resolved-ids-211.json || {
            echo "⚠ ID 解析失败，使用默认值"
        }
    fi
    
    maestro test --driver-host-port 9222 --headless --no-ansi \
        --output "$OUT_DIR/flow-13" \
        -e BASE_URL="$BASE_URL" \
        -e ADMIN_USER="$ADMIN_USER" \
        -e "ADMIN_PASSWORD=$ADMIN_PASSWORD" \
        maestro/flows/13-board-operational.yaml || {
        echo "✗ Flow 13 失败"
        ls -lh "$OUT_DIR/flow-13" 2>/dev/null || true
    }
else
    echo "⚠ maestro/flows/13-board-operational.yaml 不存在"
fi
echo ""

# ── 6. 总结 ────────────────────────────────────────────────────
echo "=========================================="
echo "测试完成"
echo "输出目录: $OUT_DIR"
echo "=========================================="
ls -lh "$OUT_DIR" 2>/dev/null || true
