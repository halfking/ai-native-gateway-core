#!/usr/bin/env bash
# run-live-gateway-core.sh —— /live 网关核心显示验证
#
# 用法：
#   bash maestro/run-live-gateway-core.sh
#
# 环境变量：
#   BASE_URL         目标站点（默认 https://llm.itestu.cn）
#   ADMIN_USER       管理员账号（默认 demoadmin）
#   ADMIN_PASSWORD   管理员密码（默认 Demo@admin123）
#   ADMIN_DISPLAY_NAME 管理员显示名（默认 **演示管理员** —— 必须与 ADMIN_USER 配套！
#     ★ 2026-11-23 实测修：原默认「系统管理员」是 **admin 种子账号**的 display_name，
#       而本 runner 默认账号是 **demoadmin**（display_name=「演示管理员」）
#       ⇒ 登录成功、弹窗已关，但共享登录流判据②③（顶栏出现 ${ADMIN_DISPLAY_NAME}）
#         **必然红** ⇒ 整条 flow 报红，量具红而非产品红。
#     ★ 生产实测的口令-账号配对（211，2026-11-23）：
#         demoadmin / Demo@admin123 → 200 ✓
#         admin     / Demo@admin123 → 401（env 的 admin 口令未生效，走 users 表记录）
#         maestro_test / * → 401（本仓无口令记录）
#     ⇒ 用其它账号时，调用方必须同时 -e ADMIN_USER/-e ADMIN_DISPLAY_NAME 覆盖两者。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

# ────────────────────────────────────────────────────────────────────────────
# 环境变量
# ────────────────────────────────────────────────────────────────────────────
BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
ADMIN_USER="${ADMIN_USER:-demoadmin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-Demo@admin123}"
ADMIN_DISPLAY_NAME="${ADMIN_DISPLAY_NAME:-演示管理员}"

REPORT_ROOT="$SCRIPT_DIR/reports"
mkdir -p "$REPORT_ROOT"

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
FLOW_LOG="$REPORT_ROOT/live-gateway-core-flow-${TIMESTAMP}.log"
CDP_LOG="$REPORT_ROOT/live-gateway-core-cdp-${TIMESTAMP}.log"
CHROME_LOG="$REPORT_ROOT/live-gateway-core-chrome-${TIMESTAMP}.log"

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🔍 /live 网关核心显示验证"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "目标站点: $BASE_URL"
echo "Flow 日志: $FLOW_LOG"
echo "CDP 日志:  $CDP_LOG"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo

# ────────────────────────────────────────────────────────────────────────────
# ① Maestro Flow 测试（浏览器驱动层）
# ────────────────────────────────────────────────────────────────────────────
echo "▶️  第一层：Maestro Flow 测试..."
FLOW_EXIT=0
maestro test \
  --screen-size 1440x900 \
  -e BASE_URL="$BASE_URL" \
  -e ADMIN_USER="$ADMIN_USER" \
  -e ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  -e ADMIN_DISPLAY_NAME="$ADMIN_DISPLAY_NAME" \
  "$SCRIPT_DIR/flows/17-live-gateway-core-display.yaml" \
  2>&1 | tee "$FLOW_LOG" || FLOW_EXIT=$?

if [ $FLOW_EXIT -eq 0 ]; then
  echo "✅ Flow 测试通过"
else
  echo "❌ Flow 测试失败 (exit=$FLOW_EXIT)"
fi
echo

# ────────────────────────────────────────────────────────────────────────────
# ② CDP 页面内验证（DOM 结构与布局）
# ────────────────────────────────────────────────────────────────────────────
echo "▶️  第二层：CDP 页面内验证..."
CDP_EXIT=0
node "$SCRIPT_DIR/tools/assert-live-gateway-core.mjs" "$BASE_URL" \
  2>&1 | tee "$CDP_LOG" || CDP_EXIT=$?

if [ $CDP_EXIT -eq 0 ]; then
  echo "✅ CDP 验证通过"
else
  echo "❌ CDP 验证失败 (exit=$CDP_EXIT)"
fi
echo

# ────────────────────────────────────────────────────────────────────────────
# 总结
# ────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📊 验证结果汇总"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

TOTAL_EXIT=0

if [ $FLOW_EXIT -eq 0 ]; then
  echo "✅ Maestro Flow: PASS"
else
  echo "❌ Maestro Flow: FAIL (exit=$FLOW_EXIT)"
  TOTAL_EXIT=1
fi

if [ $CDP_EXIT -eq 0 ]; then
  echo "✅ CDP 验证: PASS"
else
  echo "❌ CDP 验证: FAIL (exit=$CDP_EXIT)"
  TOTAL_EXIT=1
fi

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if [ $TOTAL_EXIT -eq 0 ]; then
  echo "🎉 所有验证通过"
  echo
  exit 0
else
  echo "💥 验证失败，请查看日志："
  echo "   - $FLOW_LOG"
  echo "   - $CDP_LOG"
  echo
  exit 1
fi
