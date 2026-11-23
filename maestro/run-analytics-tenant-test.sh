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
#
# ★ 只看 **LISTEN**（★ 2026-11-23 审计修正）：
#   `lsof -ti:PORT` 匹配的是**所有涉及该端口的 socket**（★ 含以该端口为
#   **客户端端点**的连接）⇒ ★ 任何与本机 9222 有连接关系的进程都会让探测
#   误判为「被占用」⇒ ★ 端口一路顺延到高位、★ 甚至撞上别的服务。
#   ⇒ 正确姿势（★ 与仓内 server-inventory 纪律一致）：
#   `lsof -nP -iTCP:<port> -sTCP:LISTEN` 只看监听者。
# ★ 且必须有**上限**（★ 修前是无界 while ⇒ ★ 极端情况无限循环）。
PORT=9222
PORT_MAX_TRIES=20
PORT_TRIES=0
while lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1; do
  PORT=$((PORT+1))
  PORT_TRIES=$((PORT_TRIES+1))
  if [ "$PORT_TRIES" -ge "$PORT_MAX_TRIES" ]; then
    echo "❌ 9222~${PORT} 连续 ${PORT_MAX_TRIES} 个端口都被 LISTEN 占用 ⇒ 放弃（★ 不再无界顺延）"
    exit 2
  fi
done
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

# ★ 日志与 profile 用**绝对路径**：★ 脚本随后会 cd 到仓根（★ 见下）
#   ⇒ ★ 相对路径（`maestro/reports/…`）会随 cwd 漂移 ⇒ ★ 日志落到
#   **调用者目录**下（★ 修前实测：`cd maestro && ./run-…` ⇒ 日志进 maestro/maestro/reports）。
REPORT_DIR="$PROJECT_ROOT/maestro/reports"
mkdir -p "$PROFILE" "$REPORT_DIR"
"$CHROME" --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage \
  --disable-extensions --remote-debugging-port=$PORT \
  --user-data-dir="$PROFILE" --window-size=1440,2200 \
  about:blank >"$REPORT_DIR/analytics-chrome.log" 2>&1 &
CHROME_PID=$!

for i in $(seq 1 30); do
  curl -s "http://127.0.0.1:$PORT/json/version" >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { echo "❌ Chrome 启动超时"; exit 2; }
  sleep 0.5
done
echo "✓ Chrome 就绪 (PID $CHROME_PID)"
echo

# ★ cwd 必须是**仓根**：★ 探针把截图/verdict 写到**相对路径**
#   `docs/audit/evidence/`（★ 相对 process.cwd()）⇒ ★ cwd = 仓根时
#   才落在 `<repo>/docs/audit/evidence/`。
#   ★ 修前是 `cd "$SCRIPT_DIR"`（= <repo>/maestro）⇒ ★ 落成 `maestro/docs/…`
#   ★ 而 `32fd95040` 只把**探针脚本路径**改成绝对路径、★ 没动 cwd
#   ⇒ ★ 「从仓根跑探针」的注释与实际行为**相反**（★ 声明与行为相反）。
#   ★ 本脚本用 `set -uo pipefail`（★ **无 -e**）⇒ ★ cd 失败不会终止
#   ⇒ ★ 必须显式判失败、★ 否则 cwd 仍是调用者目录（★ 又落回错位）。
cd "$PROJECT_ROOT" || { echo "❌ 进不去仓根: $PROJECT_ROOT"; exit 2; }
node "$PROJECT_ROOT/maestro/tools/assert-analytics-tenant.mjs" "$BASE_URL" "http://127.0.0.1:$PORT"
RC=$?

echo
[ $RC -eq 0 ] && echo "✅ 验证通过" || echo "❌ 验证失败 (rc=$RC)"
exit $RC
