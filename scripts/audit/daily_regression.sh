#!/usr/bin/env bash
# daily_regression.sh —— 每日回归机制（2026-11-11 协议转换审计轮 · 剩余任务②）
#
# ── 这个脚本解决什么 ────────────────────────────────────────────────────
# ★ 仓内**已经有**大量门（guards-fast / verify.sh），但它们都挂在 **push/PR** 上。
# ⇒ ⇒ **每日**跑到的那一类回归（时间相关的漂移、偶发失败、外部依赖变化、
#   数据随时间变化导致的断言失效）**没有任何机制在跑**。
# ⇒ ⇒ 本脚本是「每日回归」的**内容**（跑什么）；**节奏**（什么时候跑）由
#   codeup Flow 的**定时触发**提供（见 docs/audit/2026-11-11-每日回归机制.md）。
#
# ── 三条设计纪律 ────────────────────────────────────────────────────────
# ① **包集合机器派生** ★ ⇒ 「哪些包受 IR 改动影响」**不手抄** ⇒
#   ★★ 手抄清单 = 必然漂移 = 仓内 LESSONS 同款坑 ★★
#   ⇒ 派生口径 = 「传递依赖闭包里含 internal/ir 的包」⇒
#   ★★ 新增一个 import internal/ir 的包 ⇒ **自动**进每日回归 ⇒ 无需改任何清单 ★★
# ② **派生集合为空 ⇒ fail-closed** ★ ⇒ 「派生不出来」≠「没有影响面」
#   ⇒ ⇒ 直接 rc≠0 ⇒ ⇒ 不许静默跳过（★ 与「读不到锚点档 ⇒ fail-closed」同款 ★）
# ③ **阶段失败即 rc≠0** ★ ⇒ 且每阶段都打印**读数**（不是「跑过了」而是「多少」）
#
# ── 用法 ────────────────────────────────────────────────────────────────
#   bash scripts/audit/daily_regression.sh                 # 影响面 + 变异门 + 接线门
#   bash scripts/audit/daily_regression.sh --full          # 再加全量 go test ./...
#   bash scripts/audit/daily_regression.sh --emit-report   # 落一份报告到 docs/audit/
#
# 纪律：bash 3.2 可跑（macOS 自带 bash）★ 零外部依赖 ★ 读不到判据 ⇒ fail-closed。
set -uo pipefail

# ★ 2026-11-11（48h 审计第二轮约定）：接入仓内专用 Go 构建缓存 ⇒
#   本脚本是「跑 go test」的形态 ⇒ 每次跑都往用户全局缓存写一批永不再命中的
#   产物 ⇒ 必须指向专用目录（gocache_script_family_guard 门判据①）。
export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

FULL=0
EMIT_REPORT=0
for arg in "$@"; do
  case "$arg" in
    --full) FULL=1 ;;
    --emit-report) EMIT_REPORT=1 ;;
    *) echo "未知参数 $arg ⇒ ⇒ fail-closed（rc≠0）"; exit 2 ;;
  esac
done

MODULE="$(go list -m 2>/dev/null)"
if [[ -z "$MODULE" ]]; then
  echo "✘ 读不到 go module ⇒ ⇒ 【判据不存在】⇒ ⇒ fail-closed（rc≠0）"
  exit 2
fi

REPORT_DIR="$(mktemp -d /tmp/daily-regression.XXXXXX)"
REPORT="$REPORT_DIR/daily_regression.txt"
: > "$REPORT"

say() { printf '%s\n' "$*" | tee -a "$REPORT"; }
hr()  { say "------------------------------------------------------------"; }

FAILED=0
stage() {
  local name="$1"; shift
  hr
  say "▶ 阶段：$name"
  local start now
  start=$(date +%s)
  if "$@" >>"$REPORT" 2>&1; then
    now=$(date +%s)
    say "✅ $name 通过（$((now - start))s）"
  else
    now=$(date +%s)
    say "❌ $name 失败（$((now - start))s）"
    FAILED=1
  fi
}

say "=============================================================="
say " 每日回归 —— llm-gateway-go（协议转换审计轮）"
say " 时间：$(date '+%Y-%m-%d %H:%M:%S %z')"
say " HEAD：$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
say " 模块：$MODULE"
say "=============================================================="

# ── 披露：工作树是否干净（★ 归因辅助，不是判据）─────────────────────
# ★ 为什么需要这一段：★ 本脚本在**工作树**上跑 ⇒ ⇒ 别人的未提交产物也会被
#   扫到 ⇒ ⇒ 某个包红了，可能是**并行会话的在途文件**而不是本次改动
#   ⇒ ⇒ 所以把「跑的时候工作树是脏的」这件事**摆进报告** ⇒ ⇒ 读数才有归因依据。
# ★ 实测（2026-11-11）：★ 首次跑就抓到 security/sanitize 的红，根因是
#   ★ 并行会话未提交的 docker-compose.prompt-optimizer.yml 让审计 245 的
#   ★ 结论过期 ⇒ ★ 与本次改动无关 ⇒ ★★ 靠这一段才能一眼看出归因 ★★。
DIRTY="$(git status --short 2>/dev/null | head -20)"
if [[ -n "${DIRTY// /}" ]]; then
  say "⚠ 工作树**不干净**（以下为前 20 条，完整清单见 git status）⇒ ⇒ 本次读数含在途改动 ⇒ ⇒ 归因时必须把这一点算进去："
  printf '%s\n' "$DIRTY" | sed 's/^/  /' | tee -a "$REPORT"
else
  say "ℹ 工作树干净"
fi
say ""

# ── 阶段①：派生「IR 影响面包集合」（机器派生，不手抄）────────────────
hr
say "▶ 阶段：派生 IR 影响面包集合（传递依赖闭包含 internal/ir）"
PKG_LIST="$(go list -f '{{.ImportPath}} {{join .Deps " "}}' ./... 2>/dev/null \
  | grep -w "$MODULE/internal/ir" \
  | awk '{print $1}' \
  | sed "s#^$MODULE#.#" \
  | sort -u)"
PKG_COUNT="$(printf '%s\n' "$PKG_LIST" | grep -c . || true)"
say "派生包数：$PKG_COUNT"
if [[ "$PKG_COUNT" -eq 0 ]]; then
  # ★ 纪律②：派生不出来 ≠ 没有影响面 ⇒ fail-closed。
  say "✘ 派生出的影响面包集合为空 ⇒ ⇒ 【判据不存在】⇒ ⇒ fail-closed（rc≠0）"
  say "   （若确无影响面，请改判据口径，而不是让本脚本静默通过）"
  FAILED=1
  PKG_LIST="."
fi
say "包集合："
printf '%s\n' "$PKG_LIST" | sed 's/^/  - /' | tee -a "$REPORT"

# ── 阶段②：IR 影响面套件 ─────────────────────────────────────────────
stage "IR 影响面套件（$PKG_COUNT 个包）" \
  go test $PKG_LIST -count=1 -timeout=900s

# ── 阶段③：IR 协议转换层自带套件 + 覆盖率门禁 ────────────────────────
hr
say "▶ 阶段：internal/ir 全套 + 覆盖率门禁（≥75%）"
COVER="$REPORT_DIR/coverage.ir.out"
if go test ./internal/ir/... -count=1 -timeout=600s \
     -coverprofile="$COVER" -covermode=atomic >>"$REPORT" 2>&1; then
  COV="$(go tool cover -func="$COVER" | awk '/^total:/ {gsub("%","",$3); print $3}')"
  say "IR 覆盖率：${COV}%"
  if awk -v c="$COV" 'BEGIN { exit !(c+0 >= 75) }'; then
    say "✅ 覆盖率门禁通过（${COV}% ≥ 75%）"
  else
    say "❌ 覆盖率门禁失败：${COV}% < 75%"
    FAILED=1
  fi
else
  say "❌ internal/ir 全套失败"
  FAILED=1
fi

# ── 阶段④：IR 变异门（守卫有牙的证明）───────────────────────────────
stage "IR 变异 harness — client error fix" \
  bash scripts/audit/ir-client-error-fix-mutation.sh
stage "IR 变异 harness — gemini only loss" \
  bash scripts/audit/ir-gemini-only-loss-mutation.sh
stage "IR 变异 harness — coerce numeric" \
  bash scripts/audit/ir-coerce-numeric-mutation.sh
stage "IR 变异 harness — toolcall canonical" \
  python3 scripts/audit/mutation-verify-2026-11-11.py --group toolcall-canonical
# ★ 2026-11-11（附件域审计轮）：★ 多模态附件的「上传缓存 + 转发」族（10 臂）★
#   ★ 它守的包（domains/attachments）**就在** IR 影响面闭包里 ⇒ ★
#   ★ ⇒ 每日回归本来就会跑它的测试 ⇒ ★⇒ 这里再跑它的**变异臂** ⇒
#   ★⇒ 「测试过了」与「测试有牙」两件事都在每日节奏里 ★。
stage "附件变异 harness — 多模态附件上传缓存与转发" \
  python3 scripts/audit/mutation-verify-2026-11-11.py --group attachments

# ── 阶段⑤：载体接线门（门必须在真正在跑的载体上）────────────────────
stage "CI 载体接线门（IR 变异 harness 的载体链路）" \
  bash tests/ci_carrier_ir_conversion_wiring_test.sh

# ── 阶段⑥（可选）：全量 go test ./... ─────────────────────────────────
if [[ "$FULL" -eq 1 ]]; then
  stage "全量 go test ./..." go test ./... -count=1 -timeout=1800s
fi

# ── 汇总 ───────────────────────────────────────────────────────────────
hr
if [[ "$FAILED" -eq 0 ]]; then
  say "✅ 每日回归全绿"
else
  say "❌ 每日回归有红项（详见下方阶段输出）"
fi
say " 报告：$REPORT"

if [[ "$EMIT_REPORT" -eq 1 ]]; then
  OUT="docs/audit/daily-regression-$(date '+%Y-%m-%d').txt"
  mkdir -p "$(dirname "$OUT")"
  cp "$REPORT" "$OUT"
  say " 已落盘：$OUT"
fi

[[ "$FAILED" -eq 0 ]] || exit 1
