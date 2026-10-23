#!/usr/bin/env bash
# deploy_legacy_restart_flag_test.sh —— --legacy-restart 旗标的防回归门（2026-10-24 批判审计新增）
#
# 背景：deploy-seamless.sh:404 曾有一行主流程级无条件 `LEGACY_RESTART=false`，
# 把参数解析（:88 `--legacy-restart) LEGACY_RESTART=true`）的结果覆盖回 false
# ⇒ 该应急旗标自引入起就是死的——2026-10-24 实测带旗标跑仍走蓝绿预热路径
# （日志 0 次 "legacy"），且此前没有任何测试覆盖它 ⇒ 死了没人知道。
#
# 本门钉三件事（源码形态契约，无需真跑部署）：
#   ① 参数解析存在：非注释行里 `--legacy-restart) LEGACY_RESTART=true` 恰好 1 处；
#   ② 消费点存在：`$LEGACY_RESTART == true` 分支至少 1 处；
#   ③ ★ 核心判据：非注释行里的 `LEGACY_RESTART=false` **只允许出现在参数解析区
#      的默认值行**（行号 < 100）——参数解析之后任何位置再对它赋 false 都会
#      静默杀死旗标（当年的 bug 形态）。
#
# ⚠ 本文件自身的坑（写第一版时踩到，留证）：中文全角括号紧跟 `$n` 时 bash 会把
#   `n）` 当变量名 ⇒ set -u 下报「未绑定的变量」——这正是 guards-shell
#   （check-shell-fullwidth-var.sh）在防的形态 ⇒ 变量一律 `${}` 界定。
set -u
SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/scripts/deploy-seamless.sh"
pass=0; fail=0
check() { # check <desc> <cond(0=ok)>
  if [ "$2" = "0" ]; then pass=$((pass+1)); else fail=$((fail+1)); echo "FAIL: ${1}"; fi
}

# ① 解析行恰好 1 处（只数非注释行：2026-10-24 修复的注释原文引用会造成第 2 处命中）
n=$(grep -v '^[[:space:]]*#' "$SCRIPT" | grep -c -F -- '--legacy-restart) LEGACY_RESTART=true')
[ "${n}" = "1" ]; check "参数解析行恰好 1 处（实际 ${n}）" $?

# ② 消费点至少 1 处
n=$(grep -c 'LEGACY_RESTART" == true\|\$LEGACY_RESTART == true' "$SCRIPT")
[ "${n}" -ge 1 ]; check "消费点至少 1 处（实际 ${n}）" $?

# ③ 核心：非注释行的 false 字面量只许出现在前 100 行（解析区默认值）
violations=$(grep -v '^[[:space:]]*#' "$SCRIPT" | grep -n 'LEGACY_RESTART=false' | awk -F: '$1 > 100' | wc -l | tr -d ' ')
[ "${violations}" = "0" ]; check "解析区之后无 LEGACY_RESTART=false 重置（当年死旗标形态，实际 ${violations} 处）" $?

echo "deploy legacy-restart flag: ${pass} 通过 / ${fail} 失败"
[ "${fail}" = "0" ]
