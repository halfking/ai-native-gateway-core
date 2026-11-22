#!/usr/bin/env bash
# maestro/run-analytics-tenant-test.sh — /admin/analytics/tenant/default 两层验证实跑入口
# 端口占用顺延；profile 带时间戳；跑完回收 Chrome（仓内 runner 纪律）
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BASE_URL="${1:-https://llm.itestu.cn}"

# 凭据：优先仓内文档记录的种子账号（docs/211-user-auth-final-verification.md）
export ADMIN_USER="${ADMIN_USER:-demoadmin}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-Demo@admin123}"

CHROME=""
for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
         "$(command -v google-chrome 2>/dev/null)" \
         "$(command -v chromium 2>/dev/null)"; do
  [ -n "$c" ] && [ -x "$c" ] && CHROME="$c" && break
done
[ -z "$CHROME" ] && { echo "❌ 未找到 Chrome"; exit 2; }

# 端口占用顺延（9222 起找第一个空闲）
PORT=9222
while lsof -ti:$PORT >/dev/null 2>&1; do PORT=$((PORT+1)); done
PROFILE="/tmp/llmgw-analytics-profile-$(date +%s)"

cleanup() {
  [ -n "${CHROME_PID:-}" ] && kill "$CHROME_PID" 2>/dev/null
  rm -rf "$PROFILE" 2>/dev/null
}
trap cleanup EXIT INT TERM

echo "=== Analytics Tenant 页面 Maestro 验证 ==="
echo "目标: $BASE_URL/admin/analytics/tenant/default?days=7"
echo "Chrome: $CHROME (port $PORT)"
echo

mkdir -p "$PROFILE" maestro/reports
"$CHROME" --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage \
  --disable-extensions --remote-debugging-port=$PORT \
  --user-data-dir="$PROFILE" --window-size=1440,2200 \
  about:blank >maestro/reports/analytics-chrome.log 2>&1 &
CHROME_PID=$!

for i in $(seq 1 30); do
  curl -s "http://127.0.0.1:$PORT/json/version" >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { echo "❌ Chrome 启动超时"; exit 2; }
  sleep 0.5
done
echo "✓ Chrome 就绪 (PID $CHROME_PID)"
echo

cd "$SCRIPT_DIR"
node tools/assert-analytics-tenant.mjs "$BASE_URL" "http://127.0.0.1:$PORT"
RC=$?

echo
[ $RC -eq 0 ] && echo "✅ 验证通过" || echo "❌ 验证失败 (rc=$RC)"
exit $RC
