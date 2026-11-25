#!/usr/bin/env bash
# deploy_stop_budget_test.sh — 停机预算**两侧对拍门**（2026-11-17 cold
# shutdown-budget 轮，2026-11-25 从 stash 恢复并补齐缺失文件）。
#
# 对拍关系：
#   应用侧最坏预算 = cmd/gateway/shutdown_budget.go 的 shutdownWorstCaseBudget
#                   （注销 3s + probe drain 5s + srv.Shutdown 23s + worker 5s = 36s）
#   部署侧强杀线  = deploy/*.service 的 TimeoutStopSec（应 = 45s）
#                 ＋ scripts/deploy-seamless-docker.sh 的 DD_STOP_TIMEOUT（应 ≥ 36s）
#
# 判据：部署侧 ≥ 应用侧最坏预算。小于 ⇒ systemd/docker 在排空完成前
# SIGKILL ⇒ 在途 SSE / 流式请求被硬切 ⇒ nginx 502。
set -uo pipefail
FAIL=0

REPO="$(cd "$(dirname "$0")/.." && pwd)"

note() { printf '[stop-budget] %s\n' "$*"; }
bad()  { printf '[stop-budget] FAIL: %s\n' "$*"; FAIL=1; }

# ── 应用侧：从 shutdown_budget.go 提取 shutdownWorstCaseBudget ──────────
SB="$REPO/cmd/gateway/shutdown_budget.go"
if [[ ! -f "$SB" ]]; then
  bad "找不到 $SB —— 应用侧预算真源缺失"
  exit 1
fi

sum_seconds() {
  # 把形如 `3 * time.Second` 的表达式行求和（支持纯整数行）
  awk '
    /shutdownDeregisterBudget *=/ { gsub(/[^0-9]/,""); a=$0+0 }
    /shutdownProbeDrainBudget *=/ { gsub(/[^0-9]/,""); b=$0+0 }
    /shutdownHTTPDrainBudget *=/  { gsub(/[^0-9]/,""); c=$0+0 }
    /shutdownWorkerBudget *=/     { gsub(/[^0-9]/,""); d=$0+0 }
    END { print a+b+c+d }
  ' "$1"
}

WORST="$(sum_seconds "$SB")"
if [[ -z "$WORST" || "$WORST" -eq 0 ]]; then
  bad "无法从 shutdown_budget.go 解析 shutdownWorstCaseBudget（四段常量各占一行、带秒值）"
  exit 1
fi
note "应用侧最坏预算 = ${WORST}s"

# ── 部署侧：systemd 单元 TimeoutStopSec ─────────────────────────────────
for unit in "$REPO"/deploy/llm-gateway-go.service \
            "$REPO"/deploy/llm-gateway-go-canary@.service \
            "$REPO"/deploy/llmgo-245.service \
            "$REPO"/deploy/llmgo-245-canary@.service; do
  [[ -f "$unit" ]] || { bad "单元缺失：$unit"; continue; }
  val="$(grep -E '^TimeoutStopSec=[0-9]+s' "$unit" | head -1 | grep -oE '[0-9]+')"
  if [[ -z "$val" ]]; then
    bad "$unit 无 TimeoutStopSec（无界停机 ⇒ systemd 默认 90s，且注释自相矛盾）"
  elif (( val < WORST )); then
    bad "$unit TimeoutStopSec=${val}s < 应用最坏 ${WORST}s ⇒ 排空完成前 SIGKILL ⇒ 502"
  else
    note "$unit TimeoutStopSec=${val}s ≥ ${WORST}s ✓"
  fi
done

# ── 部署侧：蓝绿引擎 DD_STOP_TIMEOUT ────────────────────────────────────
ENG="$REPO/scripts/deploy-seamless-docker.sh"
if [[ ! -f "$ENG" ]]; then
  bad "找不到 $ENG"
else
  to="$(grep -E '^DD_STOP_TIMEOUT="\$\{DD_STOP_TIMEOUT:-[0-9]+\}"' "$ENG" | head -1 | grep -oE '[0-9]+\}"?$' | tr -d '}"')"
  if [[ -z "$to" ]]; then
    bad "$eng 无 DD_STOP_TIMEOUT 缺省值"
  elif (( to < WORST )); then
    bad "DD_STOP_TIMEOUT=${to}s < 应用最坏 ${WORST}s ⇒ docker 侧先 SIGKILL"
  else
    note "DD_STOP_TIMEOUT=${to}s ≥ ${WORST}s ✓"
  fi
  # 停旧槽必须走带预算的函数（锚在可执行语句上，不锚注释）
  if grep -qF 'dd_stop_container "$DD_ACTIVE_CONTAINER"' "$ENG" \
     && grep -qF 'docker stop --time "$DD_STOP_TIMEOUT"' "$ENG"; then
    note "停旧槽走 dd_stop_container（带 --time）✓"
  else
    bad "存在不带等待预算的裸 docker stop ⇒ 排空完成前 SIGKILL"
  fi
fi

if (( FAIL )); then
  printf '[stop-budget] RESULT: FAIL\n'
  exit 1
fi
printf '[stop-budget] RESULT: PASS（部署侧强杀线全部 ≥ 应用侧最坏预算 %ss）\n' "$WORST"
