#!/usr/bin/env bash
# =====================================================================
# tests/gate_scripts_caller_registry_test.sh — 「门脚本必须有调用方」门
#
# ★ 2026-11-02 第二十四轮。
#
# # 这道门为什么存在
#
# 本仓的纪律：**「门没有调用方 = 声明不是门」**（LESSONS §507；R69 / R89-*
# 反复被同一条纪律咬到）。⇒ 仓里每一个门脚本都**应当**有可执行调用方。
#
# ★ 本轮实测（先量具再读数）：
#   全仓门脚本（tests/*.sh + scripts/checks/*.sh + scripts/guards/*.sh）
#   **接线前 56** 个 ⇒ **32** 个有可执行调用方、**24** 个没有
#   （20 个只在文档里被提及 + **4** 个连文档都没有 ⇒ 完全隐形）
#   ⇒ 其中两条**已经坏掉或红着**：
#     · tests/session_storage_audit_gate_test.sh 依赖 ripgrep
#       （CI 精简镜像只有 Go，没有 ripgrep）⇒ **verify.sh 一直是红的**；
#     · tests/deploy_seamless_state_machine_test.sh 间接需要 bash ≥ 4
#       ⇒ macOS 自带 bash 3.2 下 cryptic 崩。
#   ⇒ 「没人跑」不是「没人关心」，是**它坏了也没人知道**。
#
# ★ **接线后**再测：57 个（+1 = 本门自己）⇒ **44** 个有可执行调用方、
#   **13** 个没有 ⇒ 这 13 个**全部登记在豁免表里** ⇒ **0 个隐形**
#   ⇒ 往后「新增一个门却没人跑」，由本门当场抓住。
#
# ⇒ 本门把「有没有调用方」变成**可检查的事实**：
#   · 每个门脚本，要么在豁免表里（tests/gate_scripts_caller_registry.tsv），
#     要么**必须**有可执行调用方；
#   · 豁免表里的条目**必须仍然没有**调用方（接上了就必须删掉 ⇒ 棘轮）；
#   · 豁免表里不许有幽灵条目，也不许有空原因。
#
# ★ 口径与 ci_carrier_truth_test.go 的 C3 同源：**注释里写不算调用方**
#   ⇒ 本门在判定前**剥离注释行**。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REGISTRY="$REPO_ROOT/tests/gate_scripts_caller_registry.tsv"

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
bad() { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL + 1)); }

[[ -f "$REGISTRY" ]] || { echo "missing $REGISTRY" >&2; exit 1; }

cd "$REPO_ROOT"

# ── 扫描面：事实源上的全部门脚本 ───────────────────────────────────
# ★ 分母锚在**磁盘**上（不是手写清单）⇒ 新增一个门脚本，这道门立刻看得见。
mapfile_gate_scripts() {
  find tests -maxdepth 1 -name '*.sh' -type f | sort
  find scripts/checks -maxdepth 1 -name '*.sh' -type f 2>/dev/null | sort
  find scripts/guards -maxdepth 1 -name '*.sh' -type f 2>/dev/null | sort
}

# ── 调用方集合：剥离注释行后出现过该文件名的文件 ──────────────────
# ★ 候选调用方文件：Makefile · 仓库根级 *.sh · CI 配置 · scripts/**.sh · tests/*.sh
caller_files() {
  {
    echo Makefile
    find . -maxdepth 1 -name '*.sh' -type f
    find .workflow -maxdepth 1 -name '*.yml' -type f 2>/dev/null
    find .github/workflows -maxdepth 1 -name '*.yml' -type f 2>/dev/null
    find scripts -name '*.sh' -type f
    find tests -maxdepth 1 -name '*.sh' -type f
  } | sort -u
}

# ── 调用方索引（**一次建索引**，不要每个脚本重扫一遍）─────────────────
#
# ★ 性能：第一版是「每个脚本 × 每个候选调用方文件」各起两次 grep
#   ⇒ 56 × 50 × 2 ≈ 5600 个进程 ⇒ 一次门要几十秒 ⇒ **放不进 CI 预算**。
# ⇒ 改成：**一次**扫完所有候选调用方文件 ⇒ 建「basename → 调用方文件」索引
#   ⇒ 之后的查询全部在索引里做 ⇒ 进程数降到 ~100。
#
# ★ 口径不变：只取**非注释行**（首个非空白字符是 # 的行不算）；
#   ★ 注释里写「见 xxx.sh」**不算**调用方（与 ci_carrier_truth_test.go 的 C3 同源）。
CALLER_INDEX=""
build_caller_index() {
  local outf
  outf="$(mktemp)"
  local f b
  while IFS= read -r f; do
    [[ -f "$f" ]] || continue
    grep -vE '^[[:space:]]*#' "$f" 2>/dev/null \
      | grep -oE '[A-Za-z0-9_.+-]+\.sh' \
      | sort -u \
      | while IFS= read -r b; do
          printf '%s\t%s\n' "$b" "$(printf '%s' "$f" | sed 's|^\./||')"
        done
  done < <(caller_files) > "$outf"
  CALLER_INDEX="$outf"
}

# has_executable_caller <脚本相对路径>
# ★ 判定：索引里该 basename 是否出现在**别的**文件里。
has_executable_caller() {
  local rel="$1"
  local base
  base="$(basename "$rel")"
  local self
  self="$(printf '%s' "$rel" | sed 's|^\./||')"
  awk -F'\t' -v b="$base" -v self="$self" \
    '$1==b && $2!=self { found=1 } END { exit (found ? 0 : 1) }' "$CALLER_INDEX"
}

# 豁免表读取（path<TAB>reason）；跳过注释行与空行
registry_paths() {
  grep -vE '^[[:space:]]*(#|$)' "$REGISTRY" | cut -f1
}
registry_reason() {
  grep -vE '^[[:space:]]*(#|$)' "$REGISTRY" | awk -F'\t' -v p="$1" '$1==p{print $2; exit}'
}

build_caller_index
mapfile_gate_scripts > /tmp/.gate_scripts_list.$$
all_count=$(wc -l < /tmp/.gate_scripts_list.$$ | tr -d ' ')

# ★ 恒绿护栏：扫描面读空了 ⇒ 门会「一个都不检查」⇒ 必须报出来。
if [[ "$all_count" -lt 40 ]]; then
  bad "只扫到 ${all_count} 个门脚本（< 40）⇒ 扫描面失效（目录结构变了？），这道门会恒绿"
fi

# ── AC-1：每个门脚本要么在豁免表里，要么必须有可执行调用方 ──────────
while IFS= read -r rel; do
  [[ -n "$rel" ]] || continue
  if registry_paths | grep -qx "$rel"; then
    ok
  elif has_executable_caller "$rel"; then
    ok
  else
    bad "$rel 既没有可执行调用方，也没有登记进 tests/gate_scripts_caller_registry.tsv" \
        "⇒ 它只是**一段脚本**，不是门（LESSONS §507）"
  fi
done < /tmp/.gate_scripts_list.$$

# ── AC-2：豁免表里的条目必须**仍然没有**调用方（接上了就要删 ⇒ 棘轮）──
while IFS= read -r rel; do
  [[ -n "$rel" ]] || continue
  if has_executable_caller "$rel"; then
    bad "$rel 已登记在豁免表里，但它**已经有可执行调用方**了 ⇒ 请把它从豁免表里删掉（棘轮）"
  else
    ok
  fi
done < <(registry_paths)

# ── AC-3：豁免表里不许有幽灵条目（磁盘上不存在）────────────────────
while IFS= read -r rel; do
  [[ -n "$rel" ]] || continue
  if [[ -f "$rel" ]]; then
    ok
  else
    bad "豁免表里的 $rel 在磁盘上**不存在** ⇒ 幽灵条目（要么删掉，要么先把脚本补上）"
  fi
done < <(registry_paths)

# ── AC-4：豁免表里的每条都必须有非空原因 ───────────────────────────
while IFS= read -r rel; do
  [[ -n "$rel" ]] || continue
  reason="$(registry_reason "$rel")"
  if [[ -n "${reason// /}" ]]; then
    ok
  else
    bad "豁免表里的 $rel **没有写原因** ⇒ 「为什么它接不上线」必须落成文字，否则下一个人的记忆就是它的全部理由"
  fi
done < <(registry_paths)

# ── AC-5：豁免数有**上限**（≤ 15；接得上的门应当被接线）─────────────
exempt_count=$(registry_paths | grep -c . || true)
MAX_EXEMPT=15
if [[ "$exempt_count" -le "$MAX_EXEMPT" ]]; then
  ok
else
  bad "豁免表有 ${exempt_count} 条（> ${MAX_EXEMPT}）⇒ 豁免只许变少：接得上的门应当被接线，而不是被豁免"
fi

# ── AC-6：门脚本不得依赖 **ripgrep**（除非登记豁免并写明原因）─────────
# ★ 为什么这条单列（本轮实测的因果）：
#   仓里**曾经有 4 个**脚本用 `rg`，而它们**全部没有** Makefile/CI 调用方
#   ⇒ 不是巧合：★ ripgrep 是**开发机工具**，CI 精简镜像（codeup Flow 只装 Go）
#   里**没有** ⇒ 依赖它的门在 CI 上**必然红** ⇒ ⇒ 永远接不上线
#   ⇒ ⇒ 它「没接线」的**根因**是**环境依赖**，不是「没人重视」。
#   ⇒ 其中 tests/session_storage_audit_gate_test.sh 正是本轮抓到的那条：
#   它**从提交那天起就没跑过**，而 verify.sh 因为它**一直是红的**。
# ⇒ 判据：扫描面内的门脚本若在**非注释行**里用了 `rg`，必须登记豁免
#   （⇒ 也就是说：依赖 ripgrep 的门**不许**被接进 CI）。
while IFS= read -r rel; do
  [[ -n "$rel" ]] || continue
  if grep -vE '^[[:space:]]*#' "$rel" 2>/dev/null \
     | grep -qE '(^|[^a-zA-Z0-9_./-])rg[[:space:]]'; then
    if registry_paths | grep -qx "$rel"; then
      ok
    else
      bad "$rel 依赖 ripgrep（rg）⇒ CI 精简镜像里没有 ⇒ 它在 CI 上必然红" \
          "⇒ 要么改成可移植的 grep，要么登记进豁免表并写明原因"
    fi
  else
    ok
  fi
done < /tmp/.gate_scripts_list.$$

rm -f /tmp/.gate_scripts_list.$$ "$CALLER_INDEX"
echo "PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]]
