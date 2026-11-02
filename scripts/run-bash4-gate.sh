#!/usr/bin/env bash
# scripts/run-bash4-gate.sh — 用一个 **bash ≥ 4** 的解释器跑一条门；找不到就**显式 SKIP**。
#
# ★ 2026-11-02 第二十四轮新增。
#
# # 为什么需要它
#
# 仓里有一条门 `tests/deploy_seamless_state_machine_test.sh`：它自己**不用**
# bash 4+ 特性，但它 source 的 `tests/lib/fake-remote-host.sh` 用了 `declare -A`
# ⇒ ⇒ **间接**需要 bash ≥ 4。
# ★ 而 **macOS 自带的 bash 是 3.2** ⇒ 直接跑会崩成 cryptic 的
#   `declare: -A: invalid option`。
#
# ★ 本脚本遵循本仓既有的「本次没跑」纪律（见 tests/deploy_switch_lock_parity_test.sh
#   与 scripts/deploy-test-gate.sh 的缺件处理）：
#   · 找得到 bash ≥ 4 ⇒ **真跑**（rc 透传）；
#   · 找不到 ⇒ **明确 SKIP**（打印 SKIP 行、rc=0）⇒ **不伪装成通过**。
#   ★ 绝不能「找不到就跑一下、失败就当过」——那正是「假绿」的来源。
#
# 用法：bash scripts/run-bash4-gate.sh tests/deploy_seamless_state_machine_test.sh
set -uo pipefail

if [[ $# -lt 1 ]]; then
  printf 'usage: %s <gate-script> [args...]\n' "$(basename "$0")" >&2
  exit 2
fi

gate="$1"
shift
if [[ ! -f "$gate" ]]; then
  printf 'FATAL: 门脚本不存在：%s\n' "$gate" >&2
  exit 2
fi

# 候选解释器：调用方的 BASH → PATH 里的 bash → 常见安装位置 → 本机 sdk 目录
candidates=(
  "${BASH:-}"
  "$(command -v bash 2>/dev/null || true)"
  /opt/homebrew/bin/bash
  /usr/local/bin/bash
  "${HOME}/sdk/bash5/bin/bash"
)

for c in "${candidates[@]}"; do
  [[ -n "$c" && -x "$c" ]] || continue
  if "$c" -c '(( BASH_VERSINFO[0] >= 4 ))' >/dev/null 2>&1; then
    exec "$c" "$gate" "$@"
  fi
done

printf 'SKIP: %s 需要 bash >= 4（macOS 自带的是 3.2）⇒ 本次**没跑**，不伪装成通过。\n' "$gate"
printf '      可安装 bash 4+（brew install bash）或直接用 ~/sdk/bash5/bin/bash 手动跑。\n'
exit 0
